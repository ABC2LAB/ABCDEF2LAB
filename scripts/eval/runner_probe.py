"""부정 연결 점검(평가 프로토콜 §2): 엔진 netns 안에서 경로별 연결을 보고 기대와 대조한다.

목적지(대상·관측 front 주소와 포트, 엔진 망의 호스트 쪽 게이트웨이 주소, 관측 publish 포트)는 5b 스냅샷(docker inspect)에서
가져온다. 설정값을 목적지로 쓰면 실제로 떠 있는 것과 다른 곳을 점검할 수 있기 때문이다. 양성 대조(계측기·LLM)는
엔진이 실제로 쓰도록 선언된 경로(사전검사 origin·LLM 주소)다.

판정 규칙:
- 실패 기대 경로는 실패 종류까지 맞아야 통과다(다른 망 IP = 경로 없음, 이름 = NXDOMAIN, 게이트웨이 포트 = 닫힘).
- 같은 실행에서 양성 대조가 하나라도 실패하면 그 실행 전체가 무효다(망이 통째로 죽어도 "전부 실패"로 통과하지 않게).
- probe 자체 오류(시작 실패·계획 오류·출력 해석 실패)는 무효이지 통과가 아니다.
- 계측기 경로는 TCP 연결만 한다. HTTP 요청을 보내면 계측기 전달 건수(total_requests)에 섞인다.

성공·실패 기대값은 probe_expectations 한 곳에서만 LLM 백엔드로부터 도출한다(게이트웨이 도입 시 여기만 바꾼다).
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable
from urllib.parse import urlsplit

from runner_config import (
    ENGINE_SERVICE, LLM_GW_SERVICE, METER_SERVICE, OBS_SERVICE, TARGET_SERVICE, RunPlan,
)
from runner_docker import Docker, DockerError

# cmd/eval-meter/probe.go와 같은 상한·종류·출력 형식.
MAX_PROBE_CHECKS = 64
MAX_PROBE_OUTPUT_BYTES = 64 * 1024
TCP, DNS = "tcp", "dns"
POSITIVE, NEGATIVE = "positive", "negative"
PASS, VIOLATION, INVALID = "pass", "violation", "invalid"
_CONNECTED = frozenset({"connected"})
# 다른 망의 컨테이너 IP: Docker 격리 규칙이 버리면 timeout, ICMP 도달 불가면 unreachable. refused는 무언가에 닿았다는 뜻이라 아님.
_NO_PATH = frozenset({"timeout", "unreachable"})
# 엔진 망에 없는 서비스 이름: 주소를 못 받아야 한다. 내장 DNS가 모르는 이름은 NXDOMAIN이거나, 바깥 리졸버로
# 넘기려다 internal 망이라 실패해 SERVFAIL(dns_error)이 된다(11/03 스모크 실측: Docker 29.1.3·internal 엔진 망에서
# 6개 이름 모두 dns_error). 이름 해석 자체가 고장 난 경우는 이름으로 잇는 양성 대조(meter_tcp)가 무효로 막는다.
# timeout은 의미가 달라 계속 거부한다.
_NO_NAME = frozenset({"not_found", "dns_error"})
# 엔진 망의 호스트 쪽 게이트웨이 주소: 관측 포트는 127.0.0.1에만 publish → 그 주소엔 리슨 없음(RST),
# 호스트 방화벽 DROP(timeout), internal 망이라 호스트 쪽 경로가 없음(unreachable).
_CLOSED = frozenset({"refused", "timeout", "unreachable"})
# 엔진이 LLM upstream에 직접: internal 망이라 바깥 경로가 없다. 이름 기반이면 내장 DNS가 바깥 이름을 못 풀 수도 있다.
_NO_EXTERNAL = frozenset({"timeout", "unreachable", "not_found", "dns_error"})
_CODE = re.compile(r"[a-z0-9_]{1,64}\Z")
_DNS_NAME = re.compile(r"[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,252}[A-Za-z0-9])?\Z")
_IMAGE_ID = re.compile(r"sha256:[0-9a-f]{64}\Z")


class ProbeSetupError(ValueError):
    """스냅샷에서 목적지를 도출할 수 없음. 목적지가 비면 실패 기대 점검이 공허하게 통과하므로 무효로 다룬다."""


@dataclass(frozen=True)
class PathExpectation:
    id: str
    kind: str
    target: str
    role: str
    accepted: frozenset[str]


@dataclass(frozen=True)
class ProbeOutcome:
    status: str
    reasons: tuple[str, ...]
    rows: tuple[dict, ...]
    started_at: str = ""
    finished_at: str = ""
    # 러너가 띄운 probe 컨테이너 ID(--cidfile). 구간 감시가 이 ID의 생성·시작만 러너 것으로 허용한다.
    container_id: str | None = None


def _hostport(host: str, port: int) -> str:
    return f"[{host}]:{port}" if ":" in host else f"{host}:{port}"


def _tcp_ports(snapshot: dict, service: str) -> list[int]:
    exposed = snapshot["services"].get(service, {}).get("exposed", [])
    return sorted({int(key.split("/")[0]) for key in exposed if key.endswith("/tcp")})


def _addresses(snapshot: dict, service: str) -> list[str]:
    found = set()
    for network in snapshot["networks"].values():
        found.update(network["members"].get(service, []))
    return sorted(found)


def _target_ports(plan: RunPlan, snapshot: dict) -> list[int]:
    """EXPOSE(스냅샷) ∪ 계측기 upstream 포트. EXPOSE가 없는 앱도 목적지가 비지 않게 한다."""
    upstream = int(plan.compose_env["EVAL_TARGET_PORT"])  # 러너가 계측기 -upstream에 넘기는 값
    return sorted(set(_tcp_ports(snapshot, TARGET_SERVICE)) | {upstream})


def _dns_names(snapshot: dict, service: str) -> list[str]:
    """서비스 이름·compose 컨테이너 이름(project 접두어 포함)·짧은 컨테이너 ID. 모두 내장 DNS 별칭이 될 수 있다."""
    names = {service}
    for network in snapshot["networks"].values():
        name = network.get("names", {}).get(service)
        if name:
            names.add(name)
    container_id = snapshot["services"].get(service, {}).get("id", "")
    if container_id:
        names.add(container_id[:12])
    if not all(_DNS_NAME.fullmatch(name) for name in names):
        raise ProbeSetupError("dns_name_invalid")
    return sorted(names)


def _require(values: list, code: str) -> list:
    if not values:
        raise ProbeSetupError(code)
    return values


def probe_expectations(plan: RunPlan, snapshot: dict) -> tuple[PathExpectation, ...]:
    """경로별 기대(성공·실패와 허용 결과 코드). 성공 기대가 양성 대조다.

    엔진의 LLM 경로는 게이트웨이(양성)이고, LLM upstream에 직접 닿는 경로는 실패여야 한다(음성).
    """
    llm = urlsplit(plan.llm["base_url"])
    llm_port = llm.port or {"http": 80, "https": 443}[llm.scheme]
    if plan.llm["backend"] == "api":
        # TCP 연결만(TLS·요청 내용 없음). 이름 기반이라 DNS 단계에서 끝나도 경로 없음이다.
        upstream, upstream_accepted = _hostport(llm.hostname, llm_port), _NO_EXTERNAL
    elif plan.llm["backend"] == "local":
        # 고정 IP 기준. 엔진에는 extra_hosts가 없으므로(게이트웨이만 가짐) 이름으로는 처음부터 안 풀린다.
        upstream, upstream_accepted = _hostport(plan.llm["host_ip"], llm_port), _NO_PATH
    else:
        raise ProbeSetupError("llm_backend_unsupported")
    gateway_port = int(plan.compose_env["EVAL_LLM_GW_PORT"])
    rows = [
        PathExpectation("meter_tcp", TCP, _hostport(METER_SERVICE, plan.meter_port), POSITIVE, _CONNECTED),
        PathExpectation("llm_gateway_tcp", TCP, _hostport(LLM_GW_SERVICE, gateway_port), POSITIVE, _CONNECTED),
        PathExpectation(f"llm_upstream:{upstream}", TCP, upstream, NEGATIVE, upstream_accepted),
    ]
    target_ports = _target_ports(plan, snapshot)
    for address in _require(_addresses(snapshot, TARGET_SERVICE), "target_address_unknown"):
        for port in target_ports:
            rows.append(PathExpectation(f"target_ip:{_hostport(address, port)}", TCP, _hostport(address, port),
                                        NEGATIVE, _NO_PATH))
    obs_ports = _require(_tcp_ports(snapshot, OBS_SERVICE), "obs_port_unknown")
    for address in _require(_addresses(snapshot, OBS_SERVICE), "obs_address_unknown"):
        for port in obs_ports:
            rows.append(PathExpectation(f"obs_ip:{_hostport(address, port)}", TCP, _hostport(address, port),
                                        NEGATIVE, _NO_PATH))
    for service in (TARGET_SERVICE, OBS_SERVICE):
        for name in _dns_names(snapshot, service):
            rows.append(PathExpectation(f"dns:{name}", DNS, name, NEGATIVE, _NO_NAME))
    obs_host_ports = _require(sorted({b[2] for b in snapshot["services"].get(OBS_SERVICE, {}).get("bindings", [])}),
                              "obs_host_port_unknown")
    # 엔진이 붙은 망의 호스트 쪽 게이트웨이 주소. internal 망은 없을 수 있고, 그러면 호스트로 가는 주소 자체가 없다.
    gateways = sorted({gateway for network in snapshot["networks"].values()
                       if ENGINE_SERVICE in network["members"] for gateway in network["gateways"]})
    for gateway in gateways:
        for port in obs_host_ports:
            rows.append(PathExpectation(f"gateway_obs:{_hostport(gateway, port)}", TCP, _hostport(gateway, port),
                                        NEGATIVE, _CLOSED))
    if len(rows) > MAX_PROBE_CHECKS or len({row.id for row in rows}) != len(rows):
        raise ProbeSetupError("probe_plan_invalid")
    return tuple(rows)


def probe_plan(expectations: tuple[PathExpectation, ...], timeout_ms: int) -> bytes:
    checks = [{"id": e.id, "kind": e.kind, ("address" if e.kind == TCP else "name"): e.target} for e in expectations]
    return json.dumps({"timeout_ms": timeout_ms, "checks": checks}, separators=(",", ":")).encode("ascii")


def _parse_output(output: bytes, expectations: tuple[PathExpectation, ...]) -> dict[str, tuple[str, str]]:
    if not isinstance(output, bytes) or len(output) > MAX_PROBE_OUTPUT_BYTES:
        raise ValueError
    report = json.loads(output.decode("utf-8"))
    if not isinstance(report, dict) or set(report) != {"results"} or not isinstance(report["results"], list):
        raise ValueError
    kinds = {e.id: e.kind for e in expectations}
    results: dict[str, tuple[str, str]] = {}
    for item in report["results"]:
        if not isinstance(item, dict) or set(item) != {"id", "kind", "observed", "code"}:
            raise ValueError
        check_id, kind, observed, code = item["id"], item["kind"], item["observed"], item["code"]
        if (check_id not in kinds or check_id in results or kind != kinds[check_id]
                or observed not in ("success", "fail") or not isinstance(code, str) or not _CODE.fullmatch(code)):
            raise ValueError
        results[check_id] = (observed, code)
    if set(results) != set(kinds):
        raise ValueError
    return results


def _row(expectation: PathExpectation, observed: str | None, code: str | None, verdict: str) -> dict:
    return {"id": expectation.id, "kind": expectation.kind, "target": expectation.target, "role": expectation.role,
            "expected": "success" if expectation.role == POSITIVE else "fail",
            "accepted": sorted(expectation.accepted), "observed": observed, "code": code, "verdict": verdict}


def _invalid(expectations: tuple[PathExpectation, ...], reason: str) -> ProbeOutcome:
    return ProbeOutcome(INVALID, (reason,), tuple(_row(e, None, None, "not_observed") for e in expectations))


def evaluate_probe(expectations: tuple[PathExpectation, ...], output: bytes) -> ProbeOutcome:
    try:
        results = _parse_output(output, expectations)
    except (ValueError, UnicodeError, RecursionError):
        return _invalid(expectations, "probe_output_invalid")
    rows, reasons = [], set()
    for expectation in expectations:
        observed, code = results[expectation.id]
        if expectation.role == POSITIVE:
            verdict = "match" if observed == "success" and code in expectation.accepted else "positive_failed"
        elif observed == "success":
            verdict = "negative_succeeded"
        else:
            verdict = "match" if code in expectation.accepted else "failure_kind_mismatch"
        reasons.add({"match": "", "positive_failed": "positive_control_failed",
                     "negative_succeeded": "negative_path_reachable",
                     "failure_kind_mismatch": "failure_kind_mismatch"}[verdict])
        rows.append(_row(expectation, observed, code, verdict))
    reasons.discard("")
    if "positive_control_failed" in reasons:
        status = INVALID
    elif "negative_path_reachable" in reasons:
        status = VIOLATION
    elif reasons:
        status = INVALID
    else:
        status = PASS
    return ProbeOutcome(status, tuple(sorted(reasons)), tuple(rows))


def _stamp(now: Callable[[], datetime]) -> str:
    return now().astimezone(timezone.utc).isoformat()


def run_probe(docker: Docker, meter_image_id: str, snapshot: dict, plan: RunPlan, timeout_ms: int, uid: int,
              gid: int, cidfile: Path,
              now: Callable[[], datetime] = lambda: datetime.now(timezone.utc)) -> ProbeOutcome:
    """스냅샷의 엔진 컨테이너 netns에서 probe 1회. 도출·실행·해석 어디서 실패해도 무효로 돌려준다.

    이미지는 태그가 아니라 내용 ID(러너 설정의 계측기 이미지 ID)로만 받는다. 태그는 회차 중 다른 이미지를 가리킬 수 있다.
    모든 경로에 timeout_ms 하나를 쓴다. 양성 대조가 같은 값으로 성공해야 실패 기대의 timeout을 믿을 수 있다.
    """
    started = _stamp(now)
    if not isinstance(meter_image_id, str) or not _IMAGE_ID.fullmatch(meter_image_id):
        return ProbeOutcome(INVALID, ("probe_image_not_id",), (), started, _stamp(now))
    try:
        expectations = probe_expectations(plan, snapshot)
    except ProbeSetupError as exc:
        return ProbeOutcome(INVALID, (str(exc),), (), started, _stamp(now))
    engine = snapshot["services"].get(ENGINE_SERVICE, {}).get("id", "")
    container_id = None
    try:
        output, container_id = docker.probe(meter_image_id, engine, probe_plan(expectations, timeout_ms), uid, gid,
                                            cidfile)
    except DockerError:
        outcome = _invalid(expectations, "probe_run_failed")
    else:
        outcome = evaluate_probe(expectations, output)
    return ProbeOutcome(outcome.status, outcome.reasons, outcome.rows, started, _stamp(now), container_id)
