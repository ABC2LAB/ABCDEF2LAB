"""탐색 분리 배치(개선 후 O) 회차의 실제 포트(runner_split_run.SplitRunPorts 구현, 순서 4-7c-3).

Docker는 기존 러너 부품(runner_docker.Docker)을, 제품은 제품 API 구동기(runner_product.ProductDriver)를 쓴다. 판정 규칙은
runner_split_run·runner_window·runner_split_probe에 있고, 여기는 연결만 한다. 볼륨 이름은 compose 규칙대로 `<project>_<키>`다.
- 시험 DNS 제어 토큰: `target-tls` 볼륨 루트 `control-token`(fixture-init이 1000:1000 0600으로 만든다).
- 경계 원자료: `explore-evidence` 볼륨 `run/`(경계 uid 1001, 계약 §4.1 결정 B).
- 제품 승인 장부·호출 기록: `product-state` 볼륨 `config/.openrouter-<sha256(approval_id)>.{ledger,calls}`(제품 uid 1000,
  manifest `/p/config/openrouter-model.json` 옆).
도우미는 망 없이·읽기 전용으로 붙고(runner_docker.copy_volume_files), 꺼낸 원자료는 회차 디렉터리(러너 uid 0700)에 둔다.
"""

from __future__ import annotations

import dataclasses
import hashlib
import os
import platform
import subprocess
import threading
import time
from pathlib import Path
from urllib.parse import urlsplit

from runner import _read_text, read_host_spec, snapshot_state_sha256
from runner_checks import (Binding, ExpectedTopology, Listener, NetworkExpectation, check_namespace_sharing,
                           check_other_publishes, check_topology, network_snapshot, network_subnets, parse_container,
                           parse_network, planned_subnets, read_host_listeners)
from runner_config import OBSERVER_OPAQUE, RunDirectory, RunnerConfig
from runner_evidence import EvidenceError, GatewayFiles, read_private_json
from runner_llm import PRODUCT_VERIFY_ROLE
from meter_describe import MAX_DESCRIBE_BYTES, describe_args, parse as describe_parse

METER_DESCRIBE_SECONDS = 30.0
from runner_docker import Docker, client_environment
from runner_engine import AdminSecret
from runner_product import ModelKey, ProductDriver
from run_record import read_runner_key, sign_run_record
from runner_split import read_workflow_defaults, split_conditions, target_registration
from runner_split_config import SplitConfig, SplitPlan, split_compose_args
from runner_split_inputs import SplitInputs, remove_split_inputs
from runner_split_probe import SplitProbeOutcome, run_split_probes, split_ports
from runner_window import BackgroundWatch, HostWatch, healthcheck_actions
from snapshot_collector import SnapshotEndpoint, collect_snapshot
from snapshot_writer import SnapshotContext
from tool_surface import ours_side

TOKEN_FILE = "control-token"
TOKEN_UID = 1000
BOUNDARY_UID = 1001
PRODUCT_UID = 1000
BOUNDARY_FILES = ("boundary-records.jsonl", "boundary-sealed.json")
MAX_TOKEN_BYTES = 4096
BOUNDARY_DIRECTORY = "boundary"
# 제품 블록 원본 SHA 근거(결정안 §3.3, 대조하지 않음): product-state 볼륨 config/의 설정 넷과 회차 실행 조건.
PRODUCT_CONFIG_FILES = (("installation", "installation.json"), ("policy", "policy.json"), ("api_launch", "api-launch.json"),
                        ("target_policy", "target-policy.json"))
RUNTIME_CONDITIONS_FILE = "runtime-conditions.json"
MAX_PRODUCT_FILE_BYTES = 1 << 20


UNUSED_PROFILE = "eval-split-unused"


class SplitReadyError(RuntimeError):
    """분리 배치가 준비되지 않음. 고정 code만 담는다."""


def ready_expectations(rendered: dict) -> dict[str, str]:
    """렌더 결과에서 서비스마다 준비 조건: 다른 서비스가 완료를 기다리는 일회성은 `completed`(종료 0), healthcheck가
    있으면 `healthy`, 그 밖은 `running`. 이 배치에 띄우지 않는 서비스(eval-split-unused 프로필)는 뺀다."""
    services = {name: service for name, service in rendered["services"].items()
                if UNUSED_PROFILE not in (service.get("profiles") or [])}
    one_shot = {dependency for service in services.values() for dependency, condition in (service.get("depends_on") or {}).items()
                if (condition or {}).get("condition") == "service_completed_successfully"}
    healthy = set(healthcheck_actions({"services": services}))
    return {name: "completed" if name in one_shot else "healthy" if name in healthy else "running" for name in services}


def ready_state(expected: dict[str, str], states: list[dict]) -> bool:
    """모두 준비됐으면 True, 아직이면 False. 되돌릴 수 없는 실패(일회성이 0 아닌 종료, 상주 서비스가 끝남, unhealthy,
    모르는 서비스, 서비스가 빠짐)는 고정 code로 멈춘다."""
    seen = {}
    for state in states:
        service = state.get("service")
        if service not in expected or service in seen:
            raise SplitReadyError("split_unexpected_container")
        seen[service] = state
    ready = True
    for service, want in expected.items():
        state = seen.get(service)
        if state is None:
            ready = False
            continue
        status, health = state.get("status"), state.get("health")
        if want == "completed":
            if status == "exited":
                if state.get("exit_code") != 0:
                    raise SplitReadyError(f"split_init_failed:{service}")
            else:
                ready = False
            continue
        if status in ("exited", "dead"):
            raise SplitReadyError(f"split_service_exited:{service}")
        if want == "healthy":
            if health == "unhealthy":
                raise SplitReadyError(f"split_service_unhealthy:{service}")
            ready = ready and status == "running" and health == "healthy"
        else:
            ready = ready and status == "running"
    return ready


def split_expected_topology(rendered: dict) -> tuple[ExpectedTopology, frozenset[str], dict[str, str]]:
    """렌더 결과에서 분리 배치의 기대 배치. (망·publish·고정 주소, 완료돼야 하는 일회성 서비스, 선언된 netns 공유)."""
    expected = ready_expectations(rendered)
    services = {name: rendered["services"][name] for name in expected}
    one_shot = frozenset(name for name, want in expected.items() if want == "completed")
    shared = {}
    for name, service in services.items():
        mode = service.get("network_mode") or ""
        if mode.startswith("service:"):
            shared[name] = mode[len("service:"):]
    networks = {}
    pinned: dict[str, dict[str, str]] = {}
    for key, network in (rendered.get("networks") or {}).items():
        users = {name for name, service in services.items() if key in (service.get("networks") or {})}
        if not users:
            continue  # 이 배치의 서비스가 쓰지 않는 망은 compose가 만들지 않는다
        # 끝난 일회성 컨테이너는 망 구성원 목록(docker network inspect)에 없다. 그런 망은 구성원 없음이 기대값이다.
        members = set()
        for name in users - one_shot:
            members.add(name)
            attached = services[name]["networks"][key]
            address = attached.get("ipv4_address") if isinstance(attached, dict) else None
            if address:
                pinned.setdefault(key, {})[name] = address
        networks[key] = NetworkExpectation(bool((network or {}).get("internal")), frozenset(members))
    publishes = {}
    for name, service in services.items():
        if name in one_shot:
            continue
        bindings = set()
        for port in service.get("ports") or []:
            bindings.add(Binding(f"{port['target']}/{port.get('protocol', 'tcp')}", port.get("host_ip", ""), int(port["published"])))
        publishes[name] = frozenset(bindings)
    return ExpectedTopology(networks=networks, publishes=publishes, pinned=pinned), one_shot, shared


def check_split_layout(rendered: dict, project: str, networks: list, containers: list, running: list,
                       expected_images: dict[str, str]) -> list[str]:
    """분리 배치 pre/post 배치 검사. B의 check_topology·check_namespace_sharing·check_other_publishes를 그대로 쓰고, 분리
    배치 예외는 둘뿐이다: 완료된 일회성 서비스(종료 0)는 실행 서비스 대조에서 빼고, 렌더에 선언된 `service:X` netns 공유는
    그 서비스의 실제 컨테이너 ID를 가리킬 때만 받는다. 이미지는 서비스마다 렌더 이미지의 내용 ID와 같아야 한다."""
    topology, one_shot, shared = split_expected_topology(rendered)
    violations = set()
    live = []
    for container in containers:
        if container.project == project and container.service in one_shot:
            if container.running:
                violations.add(f"service.{container.service}: ONE_SHOT_RUNNING")
            continue
        live.append(container)
    ids = {c.service: c.id for c in live if c.project == project}
    declared = {c.id for c in live if c.project == project and c.service in shared
                and c.network_mode == "container:" + ids.get(shared[c.service], "-")}
    # 선언된 공유(api·workbench → preparation)는 망 구성원이 아니라 check_topology의 망 대조와 맞는다. 예외는 그 컨테이너의
    # 망 namespace 축 하나뿐이다. 같은 컨테이너의 PID namespace 공유는 그대로 검사한다(#668 리뷰: 컨테이너째 빼면 선언된 netns
    # 공유 + 선언 안 된 pidns 공유가 통과했다).
    checked = [dataclasses.replace(c, network_mode="") if c.id in declared else c for c in running]
    violations.update(check_topology(topology, project, networks, live))
    violations.update(check_namespace_sharing(live, checked))
    violations.update(check_other_publishes(project, running))
    for container in live:
        if container.project != project:
            continue
        if container.service in shared and container.id not in declared:
            violations.add(f"service.{container.service}: SHARED_NAMESPACE_MISMATCH")
        want = expected_images.get(container.service)
        if want is None or container.image != want:
            violations.add(f"service.{container.service}: IMAGE_MISMATCH")
    return sorted(violations)


def product_record_names(approval_id: str) -> tuple[str, str]:
    """제품 승인 장부·호출 기록 파일 이름(llmpolicy openrouter_file_linux.go와 같은 규칙)."""
    approval = hashlib.sha256(approval_id.encode("utf-8")).hexdigest()
    return f".openrouter-{approval}.ledger", f".openrouter-{approval}.calls"


class DockerSplitPorts:
    def __init__(self, *, config: RunnerConfig, split: SplitConfig, plan: SplitPlan, directory: RunDirectory, docker: Docker,
                 driver: ProductDriver, inputs: SplitInputs, rendered: dict, manifest: dict, model_key: ModelKey,
                 exploration: dict, llm_env: dict | None = None, workflow_defaults: tuple[dict, str] | None = None):
        self.config, self.split, self.plan, self.directory = config, split, plan, directory
        self.docker, self.driver, self.inputs, self.rendered = docker, driver, inputs, rendered
        self.manifest, self.model_key, self.exploration = manifest, model_key, exploration
        # generation-verify 조회 설정(B 게이트웨이와 같은 compose 값, runner_split_main.llm_lookup_env)
        self.llm_env = dict(llm_env or {})
        self.base_args = split_compose_args(plan)
        self.registration: dict | None = None
        # 설치 입력을 만들 때 실제로 읽은 워크플로 기본값과 그 바이트 SHA(#710 리뷰: 회차 끝에 다시 읽으면 그 사이 바뀐 원본의
        # SHA가 서명된다). 서명 출처를 보장하는 결속이라 꼭 넘겨야 한다. 없으면 split_conditions가 다시 읽지 않고 오류를 낸다.
        self.workflow_defaults = workflow_defaults
        self.timeout = config.run["snapshot_timeout_seconds"]

    # ── 회차 시작 전·띄우기 ──────────────────────────────────────────────────────────────────────────────────
    def preexisting(self) -> bool:
        return bool(self.docker.project_network_ids(self.plan.project) or self.docker.project_container_ids(self.plan.project))

    def image_ids(self) -> dict:
        return {role: self.docker.image_id(image.ref) for role, image in self.plan.images.items()}

    def meter_describe(self) -> object:
        """계측기 이미지 `-role describe`(망 없음). 옛 이미지는 이 역할이 없어 None(점검이 거부)."""
        result = self.docker.run_bounded(describe_args(self.plan.images["meter"].id), METER_DESCRIBE_SECONDS,
                                         MAX_DESCRIBE_BYTES)
        return describe_parse(result.stdout) if result.returncode == 0 else None

    def host_spec(self) -> dict:
        return read_host_spec(_read_text, self.docker.daemon().version, os.cpu_count(), platform.release())

    def planned_subnets(self) -> list[str]:
        return planned_subnets(self.rendered)

    def host_subnets(self) -> list[str]:
        return sorted({subnet for network in self.docker.inspect_networks(self.docker.all_network_ids())
                       for subnet in network_subnets(network)})

    def up(self) -> None:
        """띄우고(대기 없이) 렌더 결과의 준비 조건을 폴링한다(compose `--wait`는 제품 경계의 healthcheck NONE에서 실패)."""
        self.docker.compose_up_detached(self.base_args)
        expected = ready_expectations(self.rendered)
        deadline = time.monotonic() + self.config.run["compose_wait_seconds"]
        while True:
            states = self.docker.container_states(self.docker.project_container_ids(self.plan.project))
            if ready_state(expected, states):
                return
            if time.monotonic() >= deadline:
                raise SplitReadyError("split_ready_timeout")
            time.sleep(self.config.run["budget_poll_seconds"])

    def down(self) -> None:
        self.docker.compose_down(self.base_args, self.config.run["down_timeout_seconds"])

    def remove_inputs(self) -> None:
        remove_split_inputs(self.inputs.directory, missing_ok=True)

    # ── 관측·점검 ────────────────────────────────────────────────────────────────────────────────────────────
    def snapshot(self, phase: str) -> dict:
        daemon = self.docker.daemon()
        networks = [parse_network(n) for n in self.docker.inspect_networks(self.docker.project_network_ids(self.plan.project))]
        containers = [parse_container(c) for c in self.docker.inspect_containers(self.docker.project_container_ids(self.plan.project))]
        return network_snapshot(daemon, self.plan.project, networks, containers)

    def check(self, phase: str) -> dict:
        """pre/post 배치 검사(#668 리뷰). 회차 망·컨테이너와 호스트의 실행 중 컨테이너 전체를 읽어 대조한다."""
        networks = [parse_network(n) for n in self.docker.inspect_networks(self.docker.project_network_ids(self.plan.project))]
        containers = [parse_container(c) for c in self.docker.inspect_containers(self.docker.project_container_ids(self.plan.project))]
        running = [parse_container(c) for c in self.docker.inspect_containers(self.docker.running_container_ids())]
        images = {name: self.docker.image_id(service["image"]) for name, service in self.rendered["services"].items()
                  if name in ready_expectations(self.rendered)}
        violations = check_split_layout(self.rendered, self.plan.project, networks, containers, running, images)
        return {"phase": phase, "violations": violations, "containers": len(containers), "running": len(running)}

    def probes(self, phase: str, snapshot: dict) -> SplitProbeOutcome:
        # 단계마다 새 cidfile 디렉터리다(docker는 이미 있는 cidfile이면 실행을 거부한다).
        cid_dir = self.directory.run / f"{phase}-probe-cid"
        os.mkdir(cid_dir, 0o700)
        upstream = urlsplit(self.manifest["base_url"])
        external = f"{upstream.hostname}:{upstream.port or 443}"
        return run_split_probes(self.docker, self.config.images["meter"].id, snapshot,
                                split_ports(self.rendered, self.inputs.boundary, self.exploration), external,
                                self.config.probe_timeout_ms, os.geteuid(), os.getegid(), cid_dir)

    def scoreboard(self, phase: str, snapshot: dict) -> dict:
        target = snapshot["services"]["target"]["id"]
        context = SnapshotContext(run_id=self.plan.run_id, model_tag=self.manifest["model"], reset_generation=target,
                                  instance_id=target, execution_id=snapshot["services"]["preparation"]["id"])
        endpoint = SnapshotEndpoint(f"http://127.0.0.1:{self.config.ports['obs_host']}", self.config.observer_path,
                                    self.config.run["snapshot_timeout_seconds"], self.config.run["snapshot_max_bytes"])
        opaque = self.config.observer_format == OBSERVER_OPAQUE
        collected = collect_snapshot(endpoint, self.directory.run, phase, context, opaque=opaque)
        normalized = read_private_json(self.directory.run / phase, "normalized.json")
        if not isinstance(normalized, dict) or not isinstance(normalized.get("solved"), dict):
            raise EvidenceError("snapshot_normalized_invalid")
        return {"raw": collected.artifacts.raw, "normalized": collected.artifacts.normalized,
                "requested_at": collected.requested_at, "received_at": collected.received_at, "solved": normalized["solved"],
                "state_sha256": snapshot_state_sha256(self.directory.run / phase, opaque, normalized["solved"])}

    def start_events(self, since: str):
        return self.docker.event_stream(since)

    def start_host_watch(self) -> BackgroundWatch:
        allowed = frozenset(Listener(entry.address, entry.port) for entry in self.config.host_listen_allowlist)
        watch = HostWatch(lambda: read_host_listeners(_read_text), allowed, time.monotonic)
        return BackgroundWatch(watch, self.config.run["host_watch_interval_seconds"]).start()

    def background(self, work) -> None:
        threading.Thread(target=work, name="product-end-read", daemon=True).start()

    # ── 도우미 복사 ──────────────────────────────────────────────────────────────────────────────────────────
    def _copy(self, volume_key: str, directory: str, names: tuple[str, ...], role: str, uid: int, max_bytes: int,
              subpath: tuple[str, ...] = ()) -> dict:
        image = self.plan.images["fixture" if role == "dns-token" else "product"].id
        files, _ = self.docker.copy_volume_files(image, f"{self.plan.project}_{volume_key}", directory, names, role, uid, uid,
                                                 self.directory.run / f"{role}.cid", max_bytes, self.timeout, subpath=subpath)
        return files

    def product_files(self) -> dict:
        """제품 블록 설치 파일 넷의 원본 SHA(결정안 §3.3). 내용은 메모리에서 지문만 뜨고 남기지 않는다(설치 입력에 비밀
        핀이 있다). 제품을 멈춘 뒤에 부른다."""
        config = self._copy("product-state", "config", tuple(name for _, name in PRODUCT_CONFIG_FILES), "product-files",
                            PRODUCT_UID, MAX_PRODUCT_FILE_BYTES)
        return {key: hashlib.sha256(config[name]).hexdigest() for key, name in PRODUCT_CONFIG_FILES}

    def runtime_conditions(self, draft_id: str) -> bytes:
        """회차 실행 조건 원본 바이트(runs/<초안 ID>/state/). 초안(diagnosisprep Job) ID가 실행 디렉터리 이름이고, E② 상태
        디렉터리가 그 아래 state다(url_preparation_linux.go). 진단 실행이 붙은 회차에만 있다(e2_http_factory.go).
        결속·형식 검사는 회차 흐름이 한다(runner_split_run.checked_runtime_conditions)."""
        return self._copy("product-state", "runs", (RUNTIME_CONDITIONS_FILE,), "product-run-conditions", PRODUCT_UID,
                          MAX_PRODUCT_FILE_BYTES, subpath=(draft_id, "state"))[RUNTIME_CONDITIONS_FILE]

    def control_policy(self, task_id: str) -> dict:
        return self.driver.control_policy(task_id)

    def split_conditions(self) -> dict:
        """이 회차의 설치·등록 조건(runner_split.split_conditions). 워크플로 기본값과 SHA는 설치 입력을 만들 때 읽은 값이다."""
        if self.workflow_defaults is None:
            raise ValueError("split ports: workflow defaults not bound")  # 회차 끝에 다시 읽어 다른 원본을 서명하지 않는다
        defaults, sha = self.workflow_defaults
        return split_conditions(workflow_defaults=defaults, workflow_sha256=sha, install=self.inputs.install_public,
                                registration=self.registration)

    def boundary_directory(self) -> Path:
        return self.directory.run / BOUNDARY_DIRECTORY

    def sign(self, payload: dict) -> dict:
        # 키는 서명 직전에만 읽는다(B DockerPorts.sign과 같다). 경로는 러너 설정(회차 디렉터리 밖)이다.
        return sign_run_record(payload, read_runner_key(str(self.config.runner_key_path)))

    def tool_surface(self) -> dict:
        """O 도구 면(결정안 §4): 제품 이미지 중 E② 샌드박스가 보는 CLI, 탐색 브라우저 이미지의 MCP 원 목록·허용 목록 적용 뒤
        목록. 1회용 컨테이너(망 없음)로 회차 망을 띄우기 전에 잰다."""
        return ours_side(self.plan.images["product"].id, self.plan.images["explore_browser"].id, self.docker.run_bounded,
                         subprocess.Popen, client_environment(os.environ, {}))

    def dns_token(self) -> str:
        data = self._copy("target-tls", ".", (TOKEN_FILE,), "dns-token", TOKEN_UID, MAX_TOKEN_BYTES)[TOKEN_FILE]
        return data.decode("ascii").strip()

    def copy_boundary_evidence(self) -> None:
        files = self._copy("explore-evidence", "run", BOUNDARY_FILES, "boundary-evidence", BOUNDARY_UID,
                           self.split.evidence_max_bytes)
        target = self.directory.run / BOUNDARY_DIRECTORY
        os.mkdir(target, 0o700)
        for name, data in files.items():
            fd = os.open(target / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
            with os.fdopen(fd, "wb") as stream:
                stream.write(data)

    def copy_product_records(self) -> tuple[bytes, bytes]:
        ledger, calls = product_record_names(self.manifest["approval_id"])
        # 상한: 승인 요청 수 × 호출 기록 줄 상한(llmpolicy와 같다). 장부 줄은 그보다 짧다.
        limit = max(1, int(self.manifest.get("max_requests", 1))) * 512 * 2
        files = self._copy("product-state", "config", (ledger, calls), "product-records", PRODUCT_UID, limit)
        return files[ledger], files[calls]

    def generation_verify(self, calls: bytes, sampling: dict | None) -> None:
        """계측기 이미지의 generation-verify 역할(회차 망 밖)로 제품 호출 기록을 사후 검증한다. 표식은 회차 llm 디렉터리에
        쓴다. 키는 제품에 넘긴 것과 같은 값이고 docker 호출 환경으로만 간다."""
        self.docker.generation_verify(self.plan.images["meter"].id, calls, key=self.model_key.reveal(), llm_env=self.llm_env,
                                      sampling=sampling, out_dir=self.directory.llm, uid=os.geteuid(), gid=os.getegid(),
                                      cidfile=self.directory.run / "generation-verify.cid",
                                      timeout=self.config.run["llm_verify_timeout_seconds"])

    def llm_verified(self) -> dict | None:
        # generation-verify 역할이 쓴 표식만 받는다(게이트웨이 표식과 모양이 같아 역할로 가른다, cmd/eval-meter/genverify.go).
        return GatewayFiles(self.directory.llm).verified(role=PRODUCT_VERIFY_ROLE)

    # ── 제품 구동 ────────────────────────────────────────────────────────────────────────────────────────────
    def prepare_round(self, dns_token: str) -> dict:
        d, split = self.driver, self.split
        d.login(self.inputs.admin)
        d.set_model_key(split.profile_id, self.manifest["model"], self.model_key)
        workflow = self.inputs.install_public["target_policy"]["workflow"]
        profile = d.catalog_profile(split.profile_id, workflow)
        # 등록 요청 수는 화면처럼 카탈로그의 회차 요청 상한이다(카탈로그를 읽은 뒤에야 안다).
        registration = target_registration(target_name=split.target_name, meter_port=self.plan.meter_port,
                                           requests=profile["workflow_request_limit"], valid_hours=split.registration_valid_hours,
                                           wide_writes=split.wide_writes)
        pending = d.register(registration)
        d.publish_challenge(dns_token, pending)
        target = d.approve(pending, registration)
        draft = d.create_draft(registration["url"], split.profile_id, target["id"])
        self.registration = registration
        return {"target": target, "catalog_profile": profile, "draft": draft, "registration": registration}

    def start_round(self, prepared: dict) -> dict:
        draft = prepared["draft"]
        self.driver.start(draft)
        running = self.driver.wait_task(draft["id"], self.split.task_wait_seconds, self.split.poll_interval_seconds)
        return {"draft_id": draft["id"], "task_id": running["task_id"], "state": running["state"]}

    def product_status(self, draft_id: str) -> dict:
        return self.driver.status(draft_id)

    def stop_round(self, draft_id: str) -> dict:
        return self.driver.stop(draft_id)

    def wait_closed(self, draft_id: str, deadline_seconds: float) -> dict:
        return self.driver.wait_closed(draft_id, deadline_seconds, self.split.poll_interval_seconds)

    def run_record(self, task_id: str) -> dict:
        return self.driver.run_record(task_id)

    # ── 봉인·정지 ───────────────────────────────────────────────────────────────────────────────────────────
    def seal(self, meter_container_id: str) -> None:
        self.docker.signal(meter_container_id, "USR1")

    def stop_boundary(self, boundary_container_id: str) -> None:
        self.docker.stop_container(boundary_container_id, self.split.boundary_stop_seconds)
