"""측정 매트릭스 집계 — 순수 계산부.

반복 1회(RepeatResult)를 §4 판정 기준으로 분류하고, 조건별 N회를 §5 매트릭스
(평균 ± 표본 표준편차(SD))로 집계한다. 네트워크·파일·시계 접근 없음 — collect.py가 I/O로
RepeatResult를 만들어 여기로 넘긴다. 순수하게 둬서 테이블 테스트가 가능하다.

분류는 사전 해결과 독립 러너의 엔진 귀속을 먼저 확인한 뒤, 판정·증거·커버리지·
상관 가능 여부를 적용한다. pre를 확인하지 못했거나 pre=true면 판단불가다.
pre=false→post=true여도 엔진 귀속을 확인하지 못하면 판단불가다.
목록에 없는 finding은 즉시 오탐이 아니라 out-of-list로 따로 센다(§4-3).
"""

from __future__ import annotations

import statistics
from dataclasses import dataclass, field
from enum import Enum

from ground_truth import Challenge


class ScanVerdict(Enum):
    """엔진이 한 챌린지에 대해 내놓은 판정. E③ 오라클 출력에서 매핑된다.

    NONE = 그 챌린지에 대한 finding이 아예 없음(보고 누락 포함).
    ALLOWED = 위반 미관찰(검증된 정책상 허용). NONE과 섞지 않는다 — 코어 GT는 전부 실제
    취약이라 GT 챌린지에 ALLOWED는 KG·정책 오류 신호다(프로토콜 §4-6).
    """

    CONFIRMED = "confirmed"
    SUSPECTED = "suspected"
    UNDECIDABLE = "undecidable"
    ALLOWED = "allowed"
    NONE = "none"


class Bucket(Enum):
    """§5 매트릭스의 네 칸. out-of-list는 칸이 아니라 별도 카운터."""

    CONFIRMED = "확정"
    FALSE_POSITIVE = "오탐"
    MISSED = "미탐"
    UNDECIDED = "판단불가"


def classify(
    verdict: ScanVerdict, solved: bool, *, evidence_ok: bool, covered: bool, correlatable: bool,
    pre_solved: bool | None = None, engine_attributed: bool = False,
) -> Bucket:
    """한 GT 챌린지 → 매트릭스 칸. 프로토콜 §4 판정표.

    - pre 미확인 또는 pre=true → 판단불가 (사전 해결을 성과로 세지 않음)
    - pre=false·post=true지만 엔진 귀속 미확인 → 판단불가
    - confirmed + 해결 + 증거 충족   → 확정
    - confirmed + 해결 + 증거 미충족 → 판단불가 (확인 못 한 증거로 확정 주지 않음, §4-2)
    - confirmed + 미해결            → 오탐 (증거 유무와 무관, §4-2)
    - suspected/undecidable        → 판단불가 (confirmed 주장이 아님)
    - allowed                      → 판단불가 (GT에 ALLOWED = KG·정책 오류 신호, §4-6)
    - 판정 없음 + 해결 + 상관 가능            → 미탐 (해결시키고 보고하지 않음)
    - 판정 없음 + 미해결 + 커버리지 + 상관 가능 → 미탐
    - 판정 없음 + 미해결 + 커버리지 없음       → 판단불가 (§4-4)
    - 판정 없음 + 상관 불가                   → 판단불가 (판정 없음 자체를 모름)
    """
    if pre_solved is not False:
        return Bucket.UNDECIDED
    if solved and not engine_attributed:
        return Bucket.UNDECIDED
    if verdict is ScanVerdict.CONFIRMED:
        if not solved:
            return Bucket.FALSE_POSITIVE
        return Bucket.CONFIRMED if evidence_ok else Bucket.UNDECIDED
    if verdict in (ScanVerdict.SUSPECTED, ScanVerdict.UNDECIDABLE, ScanVerdict.ALLOWED):
        return Bucket.UNDECIDED
    # 판정 없음의 미탐 두 행은 각각 상관 가능할 때만 — 상관 불가면 '판정 없음' 자체를 모른다.
    if solved:
        return Bucket.MISSED if correlatable else Bucket.UNDECIDED
    return Bucket.MISSED if covered and correlatable else Bucket.UNDECIDED


@dataclass(frozen=True)
class RepeatResult:
    """조건 1개 × 반복 1회의 원자료. collect.py가 채운다.

    verdicts/solved/evidence_ok의 키는 challenge key. 기준 분모(core/bac)에 있는 키가 여기
    없으면 판정 NONE·미해결·증거 미충족으로 본다. pre_solved 누락은 확인 불가로 판단불가다.
    out_of_list_keys는 GT 목록에 없는 finding.
    elapsed_seconds·total_requests는 러너가 측정 못 하면 None — 표에 "—"(값 지어내지 않음).
    """

    condition: str  # 예: "원본@35b", "우리@35b"
    elapsed_seconds: float | None  # 확정까지 시간 (초), 미측정 None
    total_requests: int | None  # 총 요청 수 = 실제 전송 HTTP만(§5), 미측정 None
    verdicts: dict[str, ScanVerdict] = field(default_factory=dict)
    solved: dict[str, bool] = field(default_factory=dict)
    out_of_list_keys: tuple[str, ...] = ()
    evidence_ok: dict[str, bool] = field(default_factory=dict)  # confirmed 챌린지의 증거 3조건 충족
    covered: frozenset[str] | None = None  # 커버리지 기록이 있는 챌린지. None = 출처 없음
    correlatable: frozenset[str] | None = None  # GT 상관 type이 있는 챌린지. None = 정보 없음(상관 불가로 봄)
    pre_solved: dict[str, bool] = field(default_factory=dict)  # 누락은 pre 확인 불가
    engine_solved: frozenset[str] = frozenset()  # 독립 러너의 직접 해결 이벤트로 확인한 항목
    blocked_attempts: int | None = None  # E² 차단 시도 수 — 총 요청 수와 별도 지표(§5), 미측정 None


@dataclass(frozen=True)
class Counts:
    """한 반복을 한 기준(core/bac)으로 분류한 칸별 개수."""

    confirmed: int = 0
    false_positive: int = 0
    missed: int = 0
    undecided: int = 0
    out_of_list: int = 0


def count_basis(repeat: RepeatResult, basis: tuple[Challenge, ...]) -> Counts:
    """반복 1회를 주어진 기준 분모(core_gt 또는 bac_category)로 분류·집계."""
    tally = {b: 0 for b in Bucket}
    for ch in basis:
        bucket = classify(
            repeat.verdicts.get(ch.key, ScanVerdict.NONE),
            repeat.solved.get(ch.key, False),
            evidence_ok=repeat.evidence_ok.get(ch.key, False),
            covered=repeat.covered is not None and ch.key in repeat.covered,
            correlatable=repeat.correlatable is not None and ch.key in repeat.correlatable,
            pre_solved=repeat.pre_solved.get(ch.key),
            engine_attributed=ch.key in repeat.engine_solved,
        )
        tally[bucket] += 1
    return Counts(
        confirmed=tally[Bucket.CONFIRMED],
        false_positive=tally[Bucket.FALSE_POSITIVE],
        missed=tally[Bucket.MISSED],
        undecided=tally[Bucket.UNDECIDED],
        out_of_list=len(repeat.out_of_list_keys),
    )


@dataclass(frozen=True)
class Stat:
    """한 지표의 반복 간 평균과 표준편차. n<2면 stdev=None(1회로 결론 금지, §5)."""

    mean: float
    stdev: float | None
    n: int

    def render(self) -> str:
        if self.n == 0:
            return "—"
        if self.stdev is None:
            return f"{self.mean:g} (n=1)"
        return f"{self.mean:g} ± {self.stdev:g}"


def _stat(values: list[float]) -> Stat:
    if not values:
        return Stat(0.0, None, 0)
    stdev = statistics.stdev(values) if len(values) >= 2 else None
    return Stat(statistics.mean(values), stdev, len(values))


def _optional_stat(values: list[float | int | None]) -> Stat:
    """측정이 빠진 회차가 하나라도 있으면 "—"(n=0). 일부 회차만으로 평균을 내 누락을 가리지 않는다."""
    if any(v is None for v in values):
        return Stat(0.0, None, 0)
    return _stat([float(v) for v in values])


@dataclass(frozen=True)
class ConditionAggregate:
    """한 조건의 N회 반복을 한 기준으로 집계한 결과(매트릭스 한 행)."""

    condition: str
    elapsed_seconds: Stat
    total_requests: Stat
    blocked_attempts: Stat
    confirmed: Stat
    false_positive: Stat
    missed: Stat
    undecided: Stat
    out_of_list: Stat
    # 대상 요청을 하나도 보내지 못한 회차 수와 요청 수를 잰 회차 수. 무효는 아니지만 요청 수 감소로 읽으면 안 된다.
    zero_request_runs: int = 0
    measured_request_runs: int = 0


def aggregate_condition(
    condition: str, repeats: list[RepeatResult], basis: tuple[Challenge, ...]
) -> ConditionAggregate:
    """한 조건의 반복들을 한 기준으로 집계. 각 지표를 평균 ± 표준편차로."""
    counts = [count_basis(r, basis) for r in repeats]
    return ConditionAggregate(
        condition=condition,
        elapsed_seconds=_optional_stat([r.elapsed_seconds for r in repeats]),
        total_requests=_optional_stat([r.total_requests for r in repeats]),
        blocked_attempts=_optional_stat([r.blocked_attempts for r in repeats]),
        confirmed=_stat([float(c.confirmed) for c in counts]),
        false_positive=_stat([float(c.false_positive) for c in counts]),
        missed=_stat([float(c.missed) for c in counts]),
        undecided=_stat([float(c.undecided) for c in counts]),
        out_of_list=_stat([float(c.out_of_list) for c in counts]),
        zero_request_runs=sum(1 for r in repeats if r.total_requests == 0),
        measured_request_runs=sum(1 for r in repeats if r.total_requests is not None),
    )


def render_matrix(rows: list[ConditionAggregate], basis_name: str) -> str:
    """§5 매트릭스를 Markdown 표로. 값은 평균 ± 표본 표준편차(SD), 반복 수 명시."""
    header = (
        f"### 측정 매트릭스 — {basis_name} 기준\n\n"
        "| 조건 | 확정까지 시간 | 총 요청 수 | 차단 시도 수 | 확정 수 | 오탐 수 | 미탐 수 | 판단불가 수 | out-of-list |\n"
        "|---|---|---|---|---|---|---|---|---|\n"
    )
    lines = []
    for r in rows:
        lines.append(
            f"| {r.condition} | {r.elapsed_seconds.render()} | {r.total_requests.render()} "
            f"| {r.blocked_attempts.render()} "
            f"| {r.confirmed.render()} | {r.false_positive.render()} | {r.missed.render()} "
            f"| {r.undecided.render()} | {r.out_of_list.render()} |"
        )
    table = header + "\n".join(lines) + "\n"
    zero = [f"{r.condition} {r.zero_request_runs}/{r.measured_request_runs}" for r in rows if r.zero_request_runs]
    if zero:
        table += (f"\n대상 요청 0 회차: {', '.join(zero)}. 대상 요청이 없던 회차이므로 "
                  "요청 수·시간 감소를 효율 개선으로 읽지 않는다.\n")
    return table
