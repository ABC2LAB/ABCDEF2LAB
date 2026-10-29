"""측정 원자료 수집 — I/O 어댑터.

RepeatResult를 만들기 위한 바깥 세계와의 접점만 모은다. 순수 계산은 aggregate.py에
있다. 여기는 테스트 대상이 아니라 '경계'라서 얇게 두고, 의존하는 포맷이 아직
정해지지 않은 곳은 명시적 TODO로 막는다(조용한 빈 성공 금지, CLAUDE.md 절대규칙 5).

의존성: 표준 라이브러리만(urllib). 새 패키지는 승인 후.
"""

from __future__ import annotations

import json
import os
import urllib.request
from collections.abc import Callable
from dataclasses import dataclass
from typing import NamedTuple
from urllib.parse import urlparse

from aggregate import ScanVerdict
from scoreboard import normalize_challenges

# 랩 전용 허용 호스트. 대상 origin은 .env로 고정하고, 그 호스트가 이 집합에 들 때만
# 요청한다(CLAUDE.md 절대규칙 1: 설정의 대상 origin만, localhost 랩). 외부 대상 없음.
_ALLOWED_HOSTS = frozenset({"127.0.0.1", "localhost", "::1"})


@dataclass(frozen=True)
class EvalConfig:
    """평가 환경 설정. 값은 .env에서 온다 — 코드 하드코딩 금지(프로토콜 §2·§6)."""

    target_origin: str  # 예: http://127.0.0.1:3000 (EVAL_TARGET_ORIGIN)
    llm_base_url: str | None  # ARTEX_LLM_BASE_URL (Ollama)
    model_tag: str | None  # baseline·개선 후 동일해야 하는 정확한 태그 (§6)


def load_config() -> EvalConfig:
    """환경변수에서 평가 설정을 읽는다. .env는 compose/셸이 로드한 것을 전제."""
    origin = os.environ.get("EVAL_TARGET_ORIGIN")
    if not origin:
        raise RuntimeError(
            "EVAL_TARGET_ORIGIN 미설정 — .env에 랩 대상 origin을 고정하라 (예 http://127.0.0.1:3000)"
        )
    _reject_non_lab_origin(origin)
    return EvalConfig(
        target_origin=origin.rstrip("/"),
        llm_base_url=os.environ.get("ARTEX_LLM_BASE_URL"),
        model_tag=os.environ.get("EVAL_MODEL_TAG"),
    )


def _reject_non_lab_origin(origin: str) -> None:
    """origin이 랩(localhost) 밖이면 거부. 리다이렉트 후 URL도 호출부에서 재검사."""
    host = urlparse(origin).hostname
    if host not in _ALLOWED_HOSTS:
        raise RuntimeError(f"대상 origin이 랩 호스트가 아님: {host!r} — 외부 대상 금지(절대규칙 1)")


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """리다이렉트를 따라가지 않는다 — 대상 origin 밖으로 새지 않게(절대규칙 1)."""

    def redirect_request(self, *args, **kwargs):  # noqa: D401, ANN002, ANN003
        raise RuntimeError("대상 origin이 리다이렉트를 시도함 — 랩 밖 가능성, 차단")


def read_solved_state(config: EvalConfig, timeout_seconds: float = 10.0) -> dict[str, bool]:
    """Juice Shop `/api/Challenges`에서 챌린지별 실제 해결 여부를 읽는다.

    집계의 독립 정답(solved)이다. 엔진 출력과 분리된 경로로, 엔진 주장의 검증에만 쓴다.
    행 파싱은 저장 스냅샷 정규화와 같은 규칙(scoreboard.normalize_challenges)을 쓴다 —
    key 없는 행·불리언 아닌 solved는 건너뛰지 않고 SnapshotError.
    """
    _reject_non_lab_origin(config.target_origin)
    url = f"{config.target_origin}/api/Challenges"
    opener = urllib.request.build_opener(_NoRedirect)
    req = urllib.request.Request(url, headers={"Accept": "application/json"})
    with opener.open(req, timeout=timeout_seconds) as resp:
        final_host = urlparse(resp.geturl()).hostname
        if final_host not in _ALLOWED_HOSTS:
            raise RuntimeError(f"응답 최종 호스트가 랩 밖: {final_host!r}")
        payload = json.load(resp)
    return normalize_challenges(payload)


# 판정 레코드 계약(docs/contracts/verdict-record.md §2)이 정의한 필드 전체. 이 집합 밖
# 키는 계약 드리프트 신호라 조용히 넘기지 않고 에러로 센다(절대규칙 5).
_KNOWN_FIELDS = frozenset({
    # §2(a) 연결·식별
    "id", "finding_id", "case_ref", "axis",
    # §2(b) 판정
    "oracle_value", "vuln_verdict", "access_policy_result",
    "policy_check", "identity_check", "data_check", "reason_codes",
    # §2(c) 비교 신호
    "same_resource", "owner_normal_data", "cross_normal_data",
    "nontrivial_data_match", "reflection_excluded", "template_excluded",
    "canary_fields",
    # §2(d) 통제변수·근거 ref
    "expected_access", "sharing", "delegation", "policy_version",
    "policy_valid_from", "policy_valid_until", "hint_matches_canonical",
    "observed_at", "reset_generation", "owner_ref", "evaluated_ref",
    "owner_status", "evaluated_status", "traffic_evidence_refs",
    "vuln_class", "resource_ref",
    # §2(e) 운영
    "model_tag", "created_at",
})

# 계약 §2(b) oracle_value → 집계용 ScanVerdict. 값 목록은 계약에 정의된 그대로다.
# allowed(위반 미관찰)는 NONE(판정 없음)과 섞지 않는다 — 집계에서 판단불가(프로토콜 §4-6).
_ORACLE_VALUE_TO_SCAN = {
    "confirmed": ScanVerdict.CONFIRMED,
    "suspected": ScanVerdict.SUSPECTED,
    "undecidable": ScanVerdict.UNDECIDABLE,
    "allowed": ScanVerdict.ALLOWED,
}

# tie-break 순위: confirmed > suspected > undecidable > allowed/none. 엔진이 한 번이라도
# 확정했으면 확정이고, 약한 쪽으로 덮어써 판단불가가 미탐처럼 사라지는 걸 막는다.
_PRECEDENCE = {
    ScanVerdict.CONFIRMED: 3,
    ScanVerdict.SUSPECTED: 2,
    ScanVerdict.UNDECIDABLE: 1,
    ScanVerdict.ALLOWED: 0,
    ScanVerdict.NONE: 0,
}

# 모순 판정용 — 계약 §2(b) access_policy_result 축의 '위반_후보'(confirmed·suspected).
# 같은 챌린지에 위반_후보와 '위반_미관찰'(allowed)이 같이 오면 서로 반대 주장이다.
# undecidable(판정_불가)은 어느 쪽 주장도 아니라 모순에 안 든다.
_VIOLATION_CANDIDATE = frozenset({ScanVerdict.CONFIRMED, ScanVerdict.SUSPECTED})

# 레코드에 값이 실려선 안 되는 민감 필드명(절대규칙 2, 계약 §3). 어댑터가 흘리면
# null/[REDACTED]가 아닌 한 에러로 막아 마스킹이 유지되는지 확인한다.
_SENSITIVE_FIELDS = frozenset({
    "authorization", "cookie", "set-cookie", "token", "password", "secret",
})
_REDACTED = "[REDACTED]"


class ScanOutputError(ValueError):
    """판정 레코드 파싱 실패. 메시지에 레코드별 사유를 전부 모아 담는다."""


class ScanParseResult(NamedTuple):
    """parse_scan_output 결과.

    conflicts = 위반_후보(confirmed·suspected)와 위반_미관찰(allowed) 레코드가 같이 온 챌린지
    키(정렬). 판정은 tie-break 순위로 정하되 모순은 숨기지 않고 따로 남긴다 — KG·정책
    불일치나 reset 세대 혼입 신호일 수 있다.
    """

    verdicts: dict[str, ScanVerdict]
    out_of_list: tuple[str, ...]
    conflicts: tuple[str, ...]


@dataclass(frozen=True)
class VerdictRecord:
    """판정 레코드 1건 중 집계·상관에 쓰는 부분(계약 §2).

    원문·비밀은 담지 않는다 — 판정·분류·논리 참조만(절대규칙 2). GT 챌린지 키도 없다:
    상관은 parse_scan_output의 correlate가 평가 쪽에서 한다(절대규칙 7, 계약 §4).
    """

    oracle_value: str
    vuln_verdict: str | None
    vuln_class: str
    resource_ref: str
    axis: str | None
    scan_verdict: ScanVerdict


def _load_json_records(path: str) -> list[object]:
    """물리적 출처 어댑터 — 지금은 JSON 파일 하나뿐.

    최상위는 레코드 배열 또는 {"records": [...]}. DB 쿼리 등 다른 출처가 정해지면 같은
    '레코드 dict 리스트'를 돌려주는 형제 어댑터를 추가한다. 아래 검증·상관은 논리적
    레코드만 받으므로 그대로 재사용된다(물리 형식은 이동찬 어댑터와 미정).
    """
    with open(path, encoding="utf-8") as f:
        try:
            payload = json.load(f)
        except json.JSONDecodeError as e:
            raise ScanOutputError(f"판정 레코드 JSON 파싱 실패: {e}") from e
    if isinstance(payload, dict) and "records" in payload:
        payload = payload["records"]
    if not isinstance(payload, list):
        raise ScanOutputError(f"최상위가 레코드 배열이 아님: {type(payload).__name__}")
    return payload


def _validate_record(index: int, raw: object) -> tuple[VerdictRecord | None, list[str]]:
    """레코드 1건을 계약대로 검증. 실패 사유는 전부 모아 돌려준다(첫 오류에서 멈추지 않음)."""
    if not isinstance(raw, dict):
        return None, [f"[{index}] 레코드가 객체가 아님: {type(raw).__name__}"]

    errors: list[str] = []
    for key, value in raw.items():
        if key.lower() in _SENSITIVE_FIELDS:
            if value not in (None, _REDACTED):
                errors.append(f"[{index}] 민감 필드 '{key}'에 값이 실림 — null/{_REDACTED}여야 함(절대규칙 2)")
        elif key not in _KNOWN_FIELDS:
            errors.append(f"[{index}] 계약에 없는 필드: '{key}'")

    oracle_value = raw.get("oracle_value")
    if oracle_value not in _ORACLE_VALUE_TO_SCAN:
        errors.append(
            f"[{index}] oracle_value 부재/부정: {oracle_value!r} (계약: {sorted(_ORACLE_VALUE_TO_SCAN)})"
        )

    # 계약 §2(b)·e3-oracle §8-①: allowed면 vuln_verdict=null, 그 외엔 oracle_value와 같아야.
    if "vuln_verdict" not in raw:
        errors.append(f"[{index}] vuln_verdict 필드 부재(allowed면 null로 명시)")
    elif oracle_value in _ORACLE_VALUE_TO_SCAN:
        vuln_verdict = raw["vuln_verdict"]
        if oracle_value == "allowed" and vuln_verdict is not None:
            errors.append(f"[{index}] allowed인데 vuln_verdict가 null 아님: {vuln_verdict!r}")
        elif oracle_value != "allowed" and vuln_verdict != oracle_value:
            errors.append(f"[{index}] vuln_verdict {vuln_verdict!r} ≠ oracle_value {oracle_value!r}")

    for name in ("vuln_class", "resource_ref"):
        value = raw.get(name)
        if not isinstance(value, str) or not value:
            errors.append(f"[{index}] {name} 부재/빈값: {value!r}")

    if errors:
        return None, errors
    axis = raw.get("axis")
    return VerdictRecord(
        oracle_value=oracle_value,
        vuln_verdict=raw["vuln_verdict"],
        vuln_class=raw["vuln_class"],
        resource_ref=raw["resource_ref"],
        axis=axis if isinstance(axis, str) else None,
        scan_verdict=_ORACLE_VALUE_TO_SCAN[oracle_value],
    ), []


def parse_scan_output(
    path: str, correlate: Callable[[VerdictRecord], str | None]
) -> ScanParseResult:
    """판정 레코드(docs/contracts/verdict-record.md) → (챌린지별 판정, out-of-list 키들, 모순 챌린지).

    correlate는 레코드의 자연 증거(vuln_class·resource_ref)를 GT 챌린지 키로 잇는다.
    None이면 GT 목록 밖 = out-of-list. 상관은 평가가 쥐고 주입한다 — 레코드엔 챌린지
    키가 없다(절대규칙 7). 대상 앱 엔드포인트를 여기 하드코딩하지 않는다.

    fail-closed(절대규칙 5): 깨진 레코드·부정 verdict·계약 밖 필드·민감값 누출은 조용히
    버리지 않고 전부 모아 ScanOutputError로 올린다 — 일부만 빠진 매트릭스를 정상처럼
    내지 않는다. undecidable은 UNDECIDABLE, allowed는 ALLOWED로만 가고 '오탐'이나
    '취약점 없음'으로 바뀌지 않는다(분류는 aggregate.classify가 solved와 함께).

    TODO(윤재상): baseline(원본 ARTEX) 출력은 이 레코드를 내지 않는다 — 별도 어댑터로
    같은 VerdictRecord를 만들지, baseline은 finding→NONE/CONFIRMED만 매핑할지 정렬 필요.
    """
    raw_records = _load_json_records(path)
    errors: list[str] = []
    verdicts: dict[str, ScanVerdict] = {}
    out_of_list: list[str] = []
    candidate_keys: set[str] = set()
    allowed_keys: set[str] = set()

    for i, raw in enumerate(raw_records):
        record, record_errors = _validate_record(i, raw)
        if record is None:
            errors.extend(record_errors)
            continue
        key = correlate(record)
        if key is None:
            if record.resource_ref not in out_of_list:
                out_of_list.append(record.resource_ref)
            continue
        # 한 챌린지에 레코드가 여럿이면 가장 강한 판정을 쓴다:
        # confirmed > suspected > undecidable > allowed/none
        prev = verdicts.get(key)
        if prev is None or _PRECEDENCE[record.scan_verdict] > _PRECEDENCE[prev]:
            verdicts[key] = record.scan_verdict
        if record.scan_verdict in _VIOLATION_CANDIDATE:
            candidate_keys.add(key)
        elif record.oracle_value == "allowed":
            allowed_keys.add(key)

    if errors:
        raise ScanOutputError(
            f"판정 레코드 {len(errors)}건 오류 (레코드 {len(raw_records)}개 중):\n  "
            + "\n  ".join(errors)
        )
    return ScanParseResult(
        verdicts=verdicts,
        out_of_list=tuple(out_of_list),
        conflicts=tuple(sorted(candidate_keys & allowed_keys)),
    )
