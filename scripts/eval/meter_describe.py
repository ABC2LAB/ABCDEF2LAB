"""회차 전 계측기·게이트웨이 이미지 점검(유료 호출 전, 2026-11-17 사용자 결정).

파일럿 B 1회차가 #638 이전 계측기 이미지로 돌아, 모델 비용을 쓴 뒤 게이트웨이 표식 칸이 모자라 무효(`llm_marker_invalid`)가
됐다. 그래서 러너는 compose를 띄우기 전에 같은 이미지를 망 없이 `-role describe`로 한 번 실행해 두 가지를 본다.

- 표식 구성: 계측기가 쓰는 표식 칸(Go 구조체의 json 칸)이 이 러너가 엄격하게 검사하는 칸과 같은가.
- 기대 빌드: 계측기를 만든 소스 커밋이 사전검사 선언의 개선 후 커밋과 같은가(공식 절차는 계측기를 그 커밋에서 빌드한다).

하나라도 다르면 회차를 시작하지 않는다(모델을 한 번도 부르지 않는다).
"""

from __future__ import annotations

import json
import re

from runner_evidence import _ENGINE_ROW, PATH_INGRESS_FIELD
from runner_llm import _MARKER_FIELDS

FORMAT = "abc2lab.eval-meter-describe/v1"
MAX_DESCRIBE_BYTES = 64 << 10
_SHA = re.compile(r"[0-9a-f]{40}")
# 러너가 읽는 표식과 그 기대 칸(이 러너 코드의 정의에서 바로 가져온다).
EXPECTED = {
    "llm-verified": {"required": sorted(_MARKER_FIELDS), "optional": []},
    "engine-row": {"required": sorted(_ENGINE_ROW), "optional": [PATH_INGRESS_FIELD]},
}


def describe_args(image_id: str) -> list[str]:
    """망·쓰기·권한 없이 계측기 이미지를 한 번 실행한다(이미지 ENTRYPOINT가 eval-meter)."""
    return ["run", "--rm", "--pull", "never", "--network", "none", "--read-only", "--cap-drop", "ALL",
            "--security-opt", "no-new-privileges:true", image_id, "-role", "describe"]


def parse(raw: bytes) -> object:
    if len(raw) > MAX_DESCRIBE_BYTES:
        return None
    try:
        return json.loads(raw.decode("utf-8"))
    except (UnicodeError, ValueError):
        return None


_FIELD = re.compile(r"[a-z][a-z0-9_]{0,63}")


def _field_names(value: object) -> list[str] | None:
    """표식 칸 이름 목록(문자열 배열)이면 정렬한 목록, 아니면 None. 객체를 넣으면 sorted가 키만 보고, 숫자·글자가 섞이면
    비교가 예외를 낸다(#684 리뷰) — 모양부터 확인한다. 중복은 기대 목록(중복 없음)과 비교에서 걸린다."""
    if not isinstance(value, list) or any(not isinstance(name, str) or not _FIELD.fullmatch(name) for name in value):
        return None
    return sorted(value)


def check(describe: object, expected_source_sha: str) -> list[str]:
    """다르면 사유(정렬), 같으면 빈 목록. 옛 이미지는 describe 역할이 없어 출력이 없으므로 meter_describe_unavailable이다."""
    if not isinstance(describe, dict) or describe.get("format") != FORMAT:
        return ["meter_describe_unavailable"]
    reasons = set()
    markers = describe.get("markers")
    for name, want in EXPECTED.items():
        got = markers.get(name) if isinstance(markers, dict) else None
        if (not isinstance(got, dict) or set(got) != {"required", "optional"}
                or _field_names(got["required"]) != want["required"] or _field_names(got["optional"]) != sorted(want["optional"])):
            reasons.add(f"meter_marker_fields_mismatch:{name}")
    build = describe.get("build") if isinstance(describe.get("build"), dict) else {}
    revision = build.get("source_revision") or build.get("vcs_revision") or ""
    if not isinstance(revision, str) or not _SHA.fullmatch(revision):
        reasons.add("meter_build_unknown")
    elif revision != expected_source_sha:
        reasons.add("meter_build_mismatch")
    vcs = build.get("vcs_revision")
    if vcs and (vcs != revision or build.get("vcs_modified") is not False):
        reasons.add("meter_build_mismatch")  # VCS 정보가 있으면 명시한 커밋과 같고 깨끗한 트리여야 한다
    return sorted(reasons)


def summary(describe: object, reasons: list[str], expected_source_sha: str) -> dict:
    """회차 근거로 남길 요약(칸 이름·커밋만)."""
    build = describe.get("build") if isinstance(describe, dict) and isinstance(describe.get("build"), dict) else {}
    return {"format": "abc2lab.meter-preflight/v1", "expected_source_sha": expected_source_sha,
            "source_revision": build.get("source_revision"), "vcs_revision": build.get("vcs_revision"),
            "vcs_modified": build.get("vcs_modified"), "reasons": reasons}
