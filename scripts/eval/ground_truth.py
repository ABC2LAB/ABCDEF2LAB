"""평가 정답(GT) 단일 원본 — OWASP Juice Shop v15.0.0 인가 챌린지.

docs/evaluation/evaluation-protocol.md §3의 표를 코드로 미러한다. 조건이 바뀌면
프로토콜 문서를 먼저 고치고 여기를 맞춘다(문서가 정본).

이 목록은 **평가에만** 쓴다 — 스캐너·탐색·판정·시나리오 입력에 넣지 않는다
(CLAUDE.md 절대규칙 7). 그래서 집계 쪽(aggregate)만 import하고, 엔진 경로에서는
import하지 않는다.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum


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


# 표 1 — Broken Access Control category 전체 10개 (§3). 모두 in_bac_category=True.
# ○ 7개가 코어 GT, ✗ 3개(csrf/easterEgg/ssrf)는 범위 밖(인가 아님)으로 명시 제외.
_BAC_CATEGORY = (
    Challenge("adminSectionChallenge", "Admin Section", Axis.VERTICAL, True, True),
    Challenge("basketAccessChallenge", "View Basket", Axis.HORIZONTAL, True, True),
    Challenge("feedbackChallenge", "Five-Star Feedback", Axis.VERTICAL, True, True),
    Challenge("basketManipulateChallenge", "Manipulate Basket", Axis.HORIZONTAL, True, True),
    Challenge("changeProductChallenge", "Product Tampering", Axis.VERTICAL, True, True),
    Challenge("csrfChallenge", "CSRF", Axis.OUT_OF_SCOPE, False, True),
    Challenge("forgedFeedbackChallenge", "Forged Feedback", Axis.HORIZONTAL, True, True),
    Challenge("forgedReviewChallenge", "Forged Review", Axis.HORIZONTAL, True, True),
    Challenge("easterEggLevelOneChallenge", "Easter Egg", Axis.OUT_OF_SCOPE, False, True),
    Challenge("ssrfChallenge", "SSRF", Axis.OUT_OF_SCOPE, False, True),
)

# 표 2 — 다른 category의 인가 클래스 (코어 GT 추가). BAC category에는 들지 않는다.
_OTHER_CATEGORY_AUTHZ = (
    Challenge("registerAdminChallenge", "Admin Registration", Axis.VERTICAL, True, False),
    Challenge("dataExportChallenge", "GDPR Data Theft", Axis.HORIZONTAL, True, False),
)

# 별도 축(후속) — 코어 GT 아님. 단일계정 업무로직/크립토라 현재 탐지 기계 대상 아님.
# 코어 미탐 계산에서 제외하되, 투명성을 위해 목록으로 남긴다(§3 별도 축).
_BUSINESS_FLOW = (
    Challenge("negativeOrderChallenge", "Payback Time", Axis.BUSINESS_FLOW, False, False),
    Challenge("freeDeluxeChallenge", "Deluxe Fraud", Axis.BUSINESS_FLOW, False, False),
    Challenge("deprecatedInterfaceChallenge", "Deprecated Interface", Axis.BUSINESS_FLOW, False, False),
)

# 전체 등록 챌린지(평가에서 참조하는 것만). challenges.yml 전체 102개 중 발췌.
ALL_CHALLENGES: tuple[Challenge, ...] = _BAC_CATEGORY + _OTHER_CATEGORY_AUTHZ + _BUSINESS_FLOW


def core_gt() -> tuple[Challenge, ...]:
    """코어 GT = 인가 9개 (수평 5 · 수직 4). 미탐/확정의 기준 분모."""
    return tuple(c for c in ALL_CHALLENGES if c.in_core_gt)


def bac_category() -> tuple[Challenge, ...]:
    """BAC category 전체 10개. cherry-pick 방지용 부차 지표 분모."""
    return tuple(c for c in ALL_CHALLENGES if c.in_bac_category)


def by_key(key: str) -> Challenge | None:
    """key로 챌린지 조회. 모르는 key면 None(= out-of-list 후보)."""
    for c in ALL_CHALLENGES:
        if c.key == key:
            return c
    return None


def axis_counts(challenges: tuple[Challenge, ...]) -> dict[Axis, int]:
    """축별 개수. 코어 GT가 수평5·수직4인지 검증·표기에 쓴다."""
    counts: dict[Axis, int] = {}
    for c in challenges:
        counts[c.axis] = counts.get(c.axis, 0) + 1
    return counts
