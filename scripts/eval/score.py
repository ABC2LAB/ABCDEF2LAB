"""저장 raw 채점기 — 회차 manifest → §4 분류 → §5 매트릭스(코어 GT 9 / BAC 10).

baseline(원본)과 개선 후(우리)를 **같은 스크립트**로 매긴다. 엔진마다 다른 건 '판정
레코드 → 챌린지별 판정' 어댑터 하나뿐이고(`_VERDICT_ADAPTERS`), 그 뒤 채점
(assemble_repeat → aggregate)은 엔진을 보지 않는다.

baseline 어댑터는 아직 없다(A5 — 원본 ARTEX finding의 GT 상관 기준 미정). baseline
회차가 들어오면 0점으로 채점하지 않고 에러로 멈춘다 — 빈 판정은 baseline을 전부
미탐으로 만들어 우리 쪽에 유리하게 기운다.

GT(챌린지 목록)는 이 디렉터리 안에서만 쓴다(절대규칙 7). 하나라도 실패하면 표를 내지
않는다 — 일부 회차만 빠진 매트릭스를 정상처럼 보이지 않게(절대규칙 5).

실행: python3 -I scripts/eval/score.py <manifest.json> [--correlation <GT 상관 JSON>]
"""

from __future__ import annotations

import argparse
import os
import sys
from collections.abc import Callable

# python3 -I(-P 포함)는 스크립트 디렉터리를 sys.path에 올리지 않는다. 같은 디렉터리의
# 평가 모듈(우리 코드)만 명시적으로 올린다 — cwd·사용자 site-packages는 계속 차단된다.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from aggregate import RepeatResult, aggregate_condition, render_matrix  # noqa: E402
from collect import ScanOutputError, ScanParseResult, VerdictRecord, parse_scan_output  # noqa: E402
from correlation import DEFAULT_PATH, CorrelationError, load_correlation  # noqa: E402
from evidence import TrafficIndex, challenge_has_evidence  # noqa: E402
from ground_truth import bac_category, core_gt  # noqa: E402
from manifest import Engine, ManifestError, RunManifest, load_manifests  # noqa: E402
from scoreboard import SnapshotError, parse_scoreboard_snapshot, require_basis_keys  # noqa: E402

Correlate = Callable[[VerdictRecord], str | None]


class BaselineNotImplementedError(NotImplementedError):
    """baseline(원본 ARTEX) 어댑터 미구현(A5 보류). 0점으로 대신 채점하지 않는다."""


class ComparabilityError(ValueError):
    """회차들을 한 비교에 묶을 수 없음(통제변수·리셋·중복). 사유를 전부 모아 담는다."""


def _ours_verdicts(manifest: RunManifest, correlate: Correlate) -> ScanParseResult:
    return parse_scan_output(manifest.records_path, correlate)


def _baseline_verdicts(manifest: RunManifest, correlate: Correlate) -> ScanParseResult:
    raise BaselineNotImplementedError(
        f"미구현(A5 보류): baseline 회차 '{manifest.condition}'#{manifest.repeat_index} — "
        "원본 ARTEX finding의 GT 상관 기준이 정해지지 않아 채점하지 않는다(0점 처리 금지)"
    )


# 엔진별로 다른 유일한 곳 — 레코드 → 챌린지별 판정. 이 뒤 채점은 엔진을 보지 않는다.
_VERDICT_ADAPTERS: dict[Engine, Callable[[RunManifest, Correlate], ScanParseResult]] = {
    Engine.OURS: _ours_verdicts,
    Engine.BASELINE: _baseline_verdicts,
}


def confirmed_evidence(
    parsed: ScanParseResult, traffic: TrafficIndex | None, correlate: Correlate
) -> dict[str, bool]:
    """챌린지별 증거 3조건 충족(§4-2) — confirmed 레코드 중 충족 레코드가 하나 이상인가."""
    return {
        key: challenge_has_evidence(records, key, traffic, correlate)
        for key, records in parsed.confirmed_records.items()
    }


def assemble_repeat(
    manifest: RunManifest,
    parsed: ScanParseResult,
    solved: dict[str, bool],
    evidence_ok: dict[str, bool],
    covered: frozenset[str] | None,
) -> RepeatResult:
    """정규화된 판정·해결상태·증거·커버리지 → RepeatResult. 엔진을 보지 않는다."""
    return RepeatResult(
        condition=manifest.condition,
        elapsed_seconds=manifest.elapsed_seconds,
        total_requests=manifest.total_requests,
        blocked_attempts=manifest.blocked_attempts,
        verdicts=dict(parsed.verdicts),
        solved=dict(solved),
        out_of_list_keys=parsed.out_of_list,
        evidence_ok=dict(evidence_ok),
        covered=covered,
    )


def score_repeat(
    manifest: RunManifest,
    correlate: Correlate,
    *,
    traffic: TrafficIndex | None = None,
    covered: frozenset[str] | None = None,
) -> RepeatResult:
    """회차 1개를 채점 입력으로. 스냅샷에 채점 분모(코어 ∪ BAC)의 key가 빠지면 에러(§2).

    traffic 인덱스·커버리지는 아직 로더가 없어 CLI는 None으로 부른다(TODO(이동찬) traffic
    export·type 명명, TODO(이경준) 커버리지 지도). None이면 증거 (b)(c)를 확인할 수 없어
    confirmed+해결은 판단불가, 판정 없음+미해결도 판단불가다 — 설계상 정직한 결과.
    """
    parsed = _VERDICT_ADAPTERS[manifest.engine](manifest, correlate)
    solved = parse_scoreboard_snapshot(manifest.snapshot_path)
    require_basis_keys(solved, core_gt() + bac_category())
    evidence_ok = confirmed_evidence(parsed, traffic, correlate)
    return assemble_repeat(manifest, parsed, solved, evidence_ok, covered)


def check_comparable(manifests: list[RunManifest]) -> None:
    """한 비교에 묶을 회차 검사: 모델 태그 하나(§6), reset 세대는 회차마다 다름(§4-5),
    (조건, 반복) 중복 없음, 한 조건 안에선 엔진 하나."""
    errors: list[str] = []
    tags = sorted({m.model_tag for m in manifests})
    if len(tags) > 1:
        errors.append(f"모델 태그가 여럿: {tags} — 통제변수는 단일 태그(§6)")

    by_generation: dict[str, list[str]] = {}
    seen_repeats: set[tuple[str, int]] = set()
    engines: dict[str, set[str]] = {}
    for m in manifests:
        run = f"{m.condition}#{m.repeat_index}"
        by_generation.setdefault(m.reset_generation, []).append(run)
        if (m.condition, m.repeat_index) in seen_repeats:
            errors.append(f"회차 중복: {run}")
        seen_repeats.add((m.condition, m.repeat_index))
        engines.setdefault(m.condition, set()).add(m.engine.value)

    for generation, runs in sorted(by_generation.items()):
        if len(runs) > 1:
            errors.append(f"reset 세대 '{generation}'를 회차 {runs}가 공유 — 회차마다 리셋(§4-5)")
    for condition, names in sorted(engines.items()):
        if len(names) > 1:
            errors.append(f"조건 '{condition}'에 엔진이 섞임: {sorted(names)}")
    if errors:
        raise ComparabilityError(f"비교 불가 {len(errors)}건:\n  " + "\n  ".join(errors))


def build_report(manifests: list[RunManifest], correlate: Correlate) -> str:
    """전 회차를 채점해 코어 GT·BAC 두 표를 만든다. 행 순서는 manifest에 처음 나온 순서."""
    check_comparable(manifests)
    by_condition: dict[str, list[RepeatResult]] = {}
    for m in manifests:
        by_condition.setdefault(m.condition, []).append(score_repeat(m, correlate))

    sections = [f"모델 태그: {manifests[0].model_tag}\n"]
    for basis_name, basis in (("코어 GT(인가 9)", core_gt()), ("BAC category(10)", bac_category())):
        rows = [aggregate_condition(c, repeats, basis) for c, repeats in by_condition.items()]
        sections.append(render_matrix(rows, basis_name))
    return "\n".join(sections)


# 표를 내지 않고 멈추는 오류들. 그 밖의 예외는 버그라 그대로 올린다.
_STOP_ERRORS = (
    ManifestError,
    SnapshotError,
    ScanOutputError,
    CorrelationError,
    ComparabilityError,
    BaselineNotImplementedError,
    OSError,
)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="저장 raw → 평가 프로토콜 §4 분류 → §5 매트릭스")
    parser.add_argument("manifest", help="회차 manifest JSON")
    parser.add_argument("--correlation", default=DEFAULT_PATH, help="평가 전용 GT 상관 JSON")
    args = parser.parse_args(argv)
    try:
        table = load_correlation(args.correlation)
        report = build_report(load_manifests(args.manifest), table.correlate)
    except _STOP_ERRORS as e:
        print(f"채점 중단: {e}", file=sys.stderr)
        return 2
    if table.unmapped:
        # type이 빈 챌린지는 어떤 레코드와도 상관되지 않는다 — 판정이 그 챌린지에 안 붙는다.
        print(f"경고: GT 상관 type 없는 챌린지 {len(table.unmapped)}개: {list(table.unmapped)}", file=sys.stderr)
    sys.stdout.write(report)
    return 0


if __name__ == "__main__":
    sys.exit(main())
