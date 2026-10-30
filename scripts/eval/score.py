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
from typing import NamedTuple

# python3 -I(-P 포함)는 스크립트 디렉터리를 sys.path에 올리지 않는다. 같은 디렉터리의
# 평가 모듈(우리 코드)만 명시적으로 올린다 — cwd·사용자 site-packages는 계속 차단된다.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from aggregate import RepeatResult, ScanVerdict, aggregate_condition, render_matrix  # noqa: E402
from collect import ScanOutputError, ScanParseResult, VerdictRecord, parse_scan_output  # noqa: E402
from correlation import DEFAULT_PATH, CorrelationError, load_correlation  # noqa: E402
from evidence import TrafficIndex, challenge_has_evidence, dangling_refs  # noqa: E402
from ground_truth import bac_category, core_gt  # noqa: E402
from manifest import Engine, ManifestError, RunManifest, load_manifests  # noqa: E402
from scoreboard import SnapshotError, parse_scoreboard_snapshot, require_basis_keys  # noqa: E402

Correlate = Callable[[VerdictRecord], str | None]


class BaselineNotImplementedError(NotImplementedError):
    """baseline(원본 ARTEX) 어댑터 미구현(A5 보류). 0점으로 대신 채점하지 않는다."""


class ComparabilityError(ValueError):
    """회차들을 한 비교에 묶을 수 없음(통제변수·리셋·중복). 사유를 전부 모아 담는다."""


class ScoredRepeat(NamedTuple):
    """회차 1개의 채점 입력과 경고. 경고는 표면화만 한다 — 판정은 이미 result에 정해져 있다."""

    result: RepeatResult
    warnings: tuple[str, ...]


class Report(NamedTuple):
    """CLI 출력. text는 stdout(파싱·재검증 대상이라 표만), warnings는 stderr."""

    text: str
    warnings: tuple[str, ...]


def _ours_verdicts(manifest: RunManifest, correlate: Correlate) -> ScanParseResult:
    return parse_scan_output(manifest.records_path, correlate, manifest=manifest)


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
    correlatable: frozenset[str] | None,
) -> RepeatResult:
    """정규화된 판정·해결상태·증거·커버리지·상관 가능 여부 → RepeatResult. 엔진을 보지 않는다."""
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
        correlatable=correlatable,
    )


def repeat_warnings(
    manifest: RunManifest, parsed: ScanParseResult, traffic: TrafficIndex | None
) -> tuple[str, ...]:
    """판정을 바꾸지 않고 드러내기만 할 이상 신호. 각 경고에 회차·챌린지(·레코드)를 적는다.

    - GT 챌린지 판정이 ALLOWED뿐 → KG·정책 오류 신호(§4-6). 판정은 classify가 판단불가로.
    - correlate 모순 → 위반_후보와 ALLOWED가 한 챌린지에 함께. 판정은 tie-break 그대로.
    - 끊긴 ref → traffic 인덱스는 있는데 confirmed 레코드의 ref가 거기 없음(§4-2). 인덱스
      자체가 없으면(None) 끊겼는지 알 수 없어 경고하지 않는다 — 그건 판단불가로 이미 반영된다.
    """
    run = f"{manifest.condition}#{manifest.repeat_index}"
    scored = {c.key for c in core_gt() + bac_category()}
    warnings: list[str] = []
    for key in sorted(k for k, v in parsed.verdicts.items() if v is ScanVerdict.ALLOWED and k in scored):
        warnings.append(
            f"경고[ALLOWED 이상치] {run}: GT 챌린지 '{key}' 판정이 ALLOWED뿐 — "
            "KG·정책 오류 신호, 판단불가로 집계(§4-6)"
        )
    for key in parsed.conflicts:
        warnings.append(
            f"경고[모순] {run}: '{key}'에 위반_후보(confirmed·suspected)와 ALLOWED 레코드가 함께 있음 — "
            "판정은 tie-break대로, KG·정책 불일치나 reset 세대 혼입 확인"
        )
    for key, records in sorted(parsed.confirmed_records.items()):
        for record in records:
            missing = dangling_refs([record], traffic)
            if missing:
                warnings.append(
                    f"경고[끊긴 ref] {run}: '{key}' confirmed 레코드 {record.resource_ref}의 "
                    f"traffic ref {list(missing)}가 traffic 기록에 없음 — 증거로 세지 않음(§4-2)"
                )
    return tuple(warnings)


def score_run(
    manifest: RunManifest,
    correlate: Correlate,
    *,
    traffic: TrafficIndex | None = None,
    covered: frozenset[str] | None = None,
    correlatable: frozenset[str] | None = None,
) -> ScoredRepeat:
    """회차 1개를 채점 입력 + 경고로. 스냅샷에 채점 분모(코어 ∪ BAC)의 key가 빠지면 에러(§2).

    traffic 인덱스·커버리지는 아직 로더가 없어 CLI는 None으로 부른다(TODO(이동찬) traffic
    export·type 명명, TODO(이경준) 커버리지 지도). None이면 증거 (b)(c)를 확인할 수 없어
    confirmed+해결은 판단불가, 판정 없음+미해결도 판단불가다 — 설계상 정직한 결과.
    correlatable은 GT 상관 표의 type 있는 챌린지(CLI는 표에서 넘긴다). None이면 어느 챌린지도
    상관 가능으로 보지 않아 '판정 없음 + 해결'도 판단불가다.
    """
    parsed = _VERDICT_ADAPTERS[manifest.engine](manifest, correlate)
    solved = parse_scoreboard_snapshot(manifest.snapshot_path, manifest)
    require_basis_keys(solved, core_gt() + bac_category())
    evidence_ok = confirmed_evidence(parsed, traffic, correlate)
    result = assemble_repeat(manifest, parsed, solved, evidence_ok, covered, correlatable)
    return ScoredRepeat(result, repeat_warnings(manifest, parsed, traffic))


def score_repeat(
    manifest: RunManifest,
    correlate: Correlate,
    *,
    traffic: TrafficIndex | None = None,
    covered: frozenset[str] | None = None,
    correlatable: frozenset[str] | None = None,
) -> RepeatResult:
    """score_run에서 채점 입력만. 경고가 필요 없는 호출용."""
    return score_run(manifest, correlate, traffic=traffic, covered=covered, correlatable=correlatable).result


def check_comparable(manifests: list[RunManifest]) -> None:
    """한 비교에 묶을 회차 검사: 모델 태그 하나(§6), reset 세대는 회차마다 다름(§4-5),
    (조건, 반복) 중복 없음, 한 조건 안에선 엔진 하나."""
    errors: list[str] = []
    tags = sorted({m.model_tag for m in manifests})
    if len(tags) > 1:
        errors.append(f"모델 태그가 여럿: {tags} — 통제변수는 단일 태그(§6)")

    by_generation: dict[str, list[str]] = {}
    by_run_id: dict[str, list[str]] = {}
    seen_repeats: set[tuple[str, int]] = set()
    engines: dict[str, set[str]] = {}
    for m in manifests:
        run = f"{m.condition}#{m.repeat_index}"
        by_generation.setdefault(m.reset_generation, []).append(run)
        by_run_id.setdefault(m.run_id, []).append(run)
        if (m.condition, m.repeat_index) in seen_repeats:
            errors.append(f"회차 중복: {run}")
        seen_repeats.add((m.condition, m.repeat_index))
        engines.setdefault(m.condition, set()).add(m.engine.value)

    for generation, runs in sorted(by_generation.items()):
        if len(runs) > 1:
            errors.append(f"reset 세대 '{generation}'를 회차 {runs}가 공유 — 회차마다 리셋(§4-5)")
    for run_id, runs in sorted(by_run_id.items()):
        if len(runs) > 1:
            errors.append(f"run_id '{run_id}'를 회차 {runs}가 공유")
    for condition, names in sorted(engines.items()):
        if len(names) > 1:
            errors.append(f"조건 '{condition}'에 엔진이 섞임: {sorted(names)}")
    if errors:
        raise ComparabilityError(f"비교 불가 {len(errors)}건:\n  " + "\n  ".join(errors))


def build_report(
    manifests: list[RunManifest], correlate: Correlate, correlatable: frozenset[str]
) -> Report:
    """전 회차를 채점해 코어 GT·BAC 두 표와 경고를 만든다. 행·경고 순서는 manifest 순서."""
    check_comparable(manifests)
    by_condition: dict[str, list[RepeatResult]] = {}
    warnings: list[str] = []
    for m in manifests:
        scored = score_run(m, correlate, correlatable=correlatable)
        by_condition.setdefault(m.condition, []).append(scored.result)
        warnings.extend(scored.warnings)

    sections = [f"모델 태그: {manifests[0].model_tag}\n"]
    for basis_name, basis in (("코어 GT(인가 9)", core_gt()), ("BAC category(10)", bac_category())):
        rows = [aggregate_condition(c, repeats, basis) for c, repeats in by_condition.items()]
        sections.append(render_matrix(rows, basis_name))
    return Report("\n".join(sections), tuple(warnings))


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
        report = build_report(load_manifests(args.manifest), table.correlate, table.operative)
    except _STOP_ERRORS as e:
        print(f"채점 중단: {e}", file=sys.stderr)
        return 2
    if table.unmapped:
        # type이 빈 챌린지는 어떤 레코드와도 상관되지 않는다 — 판정이 그 챌린지에 안 붙는다.
        print(f"경고: GT 상관 type 없는 챌린지 {len(table.unmapped)}개: {list(table.unmapped)}", file=sys.stderr)
    # 경고는 전부 stderr — stdout은 표만 둔다(파싱·재검증 대상).
    for warning in report.warnings:
        print(warning, file=sys.stderr)
    sys.stdout.write(report.text)
    return 0


if __name__ == "__main__":
    sys.exit(main())
