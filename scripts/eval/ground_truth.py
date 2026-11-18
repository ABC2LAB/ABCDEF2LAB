"""평가 정답(GT) 단일 원본 — OWASP Juice Shop v15.0.0 인가 챌린지.

평가 정답 파일(evaluation-gt/juice-shop-v15/challenges.json, 사람용 표는 같은 디렉터리 challenges.md)을 읽는다.
선정 기준은 평가 프로토콜 §3이다. 목록을 바꾸면 두 파일을 같이 고친다(시험이 대조한다).

이 목록은 **평가에만** 쓴다 — 스캐너·탐색·판정·시나리오 입력에 넣지 않는다
(CLAUDE.md 절대규칙 7). 그래서 집계 쪽(aggregate)만 import하고, 엔진 경로에서는
import하지 않는다.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from enum import Enum
from pathlib import Path


class Axis(Enum):
    """Authorization Matrix 축. BF(업무흐름)는 코어 밖 후속 축."""

    HORIZONTAL = "H"  # 동일 권한에서 타 사용자 객체 접근/변조 (IDOR/BOLA)
    VERTICAL = "V"  # 상위 권한·관리자 전용 기능 접근 (권한상승/BFLA)
    BUSINESS_FLOW = "BF"  # 단일계정 업무로직·크립토 — 현재 탐지 기계 대상 아님
    OUT_OF_SCOPE = "-"  # category상 포함이나 인가 클래스가 아님 (CSRF/SSRF/숨김파일)


@dataclass(frozen=True)
class Challenge:
    """챌린지 하나. key는 Juice Shop challenges.yml / `/api/Challenges`의 key와 동일."""

    key: str
    name: str
    axis: Axis
    in_core_gt: bool  # 코어 GT(인가 9개)에 드는가
    in_bac_category: bool  # Broken Access Control category 전체(10) 부차지표에 드는가


# 데이터는 평가 정답 파일(evaluation-gt/juice-shop-v15/challenges.json)에 있다. 저장소 루트 `.ignore`로 세션·에이전트의
# 검색에서 빠지는 경로라, 챌린지 키·이름을 이 코드에 적지 않는다(2026-11-18, 정답표 노출 방지).
GT_FILE = Path(__file__).resolve().parents[2] / "evaluation-gt" / "juice-shop-v15" / "challenges.json"
GT_FORMAT = "abc2lab.eval-gt/v1"
_ITEM_FIELDS = frozenset({"key", "name", "axis", "group", "in_core_gt", "in_bac_category"})
_GROUPS = ("bac_category", "other_category_authz", "business_flow")


class GroundTruthError(ValueError):
    """평가 정답 파일이 형식에 맞지 않는다. 내용(키·이름)은 오류에 넣지 않는다."""


def _unique_object(pairs: list) -> dict:
    """같은 이름의 칸이 두 번 나오면 거부한다. json 기본 동작은 앞 값을 버려 분모가 조용히 바뀐다(#712 리뷰)."""
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate field")
        value[key] = item
    return value


def _load(path: Path = GT_FILE) -> tuple[Challenge, ...]:
    try:
        data = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=_unique_object)
    except (OSError, UnicodeError, ValueError, RecursionError):
        raise GroundTruthError("ground_truth_unreadable") from None
    items = data.get("challenges") if isinstance(data, dict) and data.get("format") == GT_FORMAT else None
    if not isinstance(items, list) or not items:
        raise GroundTruthError("ground_truth_invalid")
    out, keys, last_group = [], set(), 0
    for item in items:
        if (not isinstance(item, dict) or set(item) != _ITEM_FIELDS or not isinstance(item["key"], str) or not item["key"]
                or not isinstance(item["name"], str) or item["group"] not in _GROUPS
                or type(item["in_core_gt"]) is not bool or type(item["in_bac_category"]) is not bool
                or item["key"] in keys):
            raise GroundTruthError("ground_truth_invalid")
        group = _GROUPS.index(item["group"])
        # 묶음 순서(표 1 → 표 2 → 별도 축)와 묶음별 규칙: 표 1만 BAC category, 별도 축은 코어 GT 아님
        if (group < last_group or item["in_bac_category"] != (item["group"] == "bac_category")
                or (item["group"] == "business_flow" and item["in_core_gt"])):
            raise GroundTruthError("ground_truth_invalid")
        try:
            axis = Axis(item["axis"])
        except ValueError:
            raise GroundTruthError("ground_truth_invalid") from None
        keys.add(item["key"])
        last_group = group
        out.append(Challenge(key=item["key"], name=item["name"], axis=axis, in_core_gt=item["in_core_gt"],
                             in_bac_category=item["in_bac_category"]))
    return tuple(out)


# 전체 등록 챌린지(평가에서 참조하는 것만). challenges.yml 전체 102개 중 발췌.
# 정답 목록은 처음 쓰일 때 읽는다(#712 리뷰). 시험은 그 전에 합성 목록으로 바꿔 실제 정답 경로를 열지 않는다.
_cache: tuple[Challenge, ...] | None = None


def _challenges() -> tuple[Challenge, ...]:
    global _cache
    if _cache is None:
        _cache = _load(GT_FILE)
    return _cache


def __getattr__(name: str):
    # ALL_CHALLENGES는 모듈 속성으로 읽을 때마다 지금 쓰는 목록을 돌려준다(처음 읽을 때 불러온다).
    if name == "ALL_CHALLENGES":
        return _challenges()
    raise AttributeError(name)


def use_ground_truth_file(path: Path) -> None:
    """시험 전용: 정답 목록을 다른 파일(합성 목록)로 바꾼다(2026-11-18, 정답표 노출 방지). 시험은 실제 정답 대신 같은 묶음·축
    구성의 합성 목록으로 돌고, 실제 정답이 꼭 필요한 시험만 evaluation-gt/ 아래에서 실제 목록으로 돈다. 함수들은 호출 때 이
    전역을 읽으므로 바꾼 뒤의 호출부터 적용된다."""
    global _cache
    _cache = _load(Path(path))


def core_gt() -> tuple[Challenge, ...]:
    """코어 GT = 인가 9개 (수평 5 · 수직 4). 미탐/확정의 기준 분모."""
    return tuple(c for c in _challenges() if c.in_core_gt)


def bac_category() -> tuple[Challenge, ...]:
    """BAC category 전체 10개. cherry-pick 방지용 부차 지표 분모."""
    return tuple(c for c in _challenges() if c.in_bac_category)


def by_key(key: str) -> Challenge | None:
    """key로 챌린지 조회. 모르는 key면 None(= out-of-list 후보)."""
    for c in _challenges():
        if c.key == key:
            return c
    return None


def axis_counts(challenges: tuple[Challenge, ...]) -> dict[Axis, int]:
    """축별 개수. 코어 GT가 수평5·수직4인지 검증·표기에 쓴다."""
    counts: dict[Axis, int] = {}
    for c in challenges:
        counts[c.axis] = counts.get(c.axis, 0) + 1
    return counts
