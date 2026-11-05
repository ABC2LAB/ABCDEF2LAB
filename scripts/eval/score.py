"""저장 raw 채점기 — 회차 manifest → §4 분류 → §5 매트릭스(코어 GT 9 / BAC 10).

baseline(원본)과 개선 후(우리)를 **같은 스크립트**로 매긴다. 엔진마다 다른 건 '판정
레코드 → 챌린지별 판정' 어댑터 하나뿐이고(`_VERDICT_ADAPTERS`), 그 뒤 채점
(assemble_repeat → aggregate)은 엔진을 보지 않는다.

baseline 어댑터는 아직 없다(A5 — 원본 ARTEX finding의 GT 상관 기준 미정). baseline
회차가 들어오면 0점으로 채점하지 않고 에러로 멈춘다 — 빈 판정은 baseline을 전부
미탐으로 만들어 우리 쪽에 유리하게 기운다.

GT(챌린지 목록)는 이 디렉터리 안에서만 쓴다(절대규칙 7). 하나라도 실패하면 표를 내지
않는다 — 일부 회차만 빠진 매트릭스를 정상처럼 보이지 않게(절대규칙 5).

실행: python3 -I scripts/eval/score.py <manifest.json> --runner-key-file <키 파일>
      [--correlation <GT 상관 JSON>]
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
from collect import ScanOutputError, ScanParseResult, VerdictRecord, parse_scan_payload  # noqa: E402
from correlation import DEFAULT_PATH, CorrelationError, load_correlation  # noqa: E402
from evidence import TrafficIndex, challenge_has_evidence, dangling_refs  # noqa: E402
from ground_truth import bac_category, core_gt  # noqa: E402
from manifest import Engine, ManifestError, RunManifest, load_manifests  # noqa: E402
from scoreboard import SnapshotError  # noqa: E402

from run_record import V1, V11, RunInputs, RunRecordError, load_run_inputs, read_runner_key  # noqa: E402

Correlate = Callable[[VerdictRecord], str | None]


class BaselineNotImplementedError(NotImplementedError):
    """baseline(원본 ARTEX) 어댑터 미구현(A5 보류). 0점으로 대신 채점하지 않는다."""


class ComparabilityError(ValueError):
    """회차들을 한 비교에 묶을 수 없음(통제변수·리셋·중복). 사유를 전부 모아 담는다."""


class ScoredRepeat(NamedTuple):
    """회차 1개의 채점 입력과 경고. 경고는 표면화만 한다 — 판정은 이미 result에 정해져 있다."""

    result: RepeatResult
    warnings: tuple[str, ...]
    inputs: RunInputs


class Report(NamedTuple):
    """CLI 출력. text는 stdout(파싱·재검증 대상이라 표만), warnings는 stderr."""

    text: str
    warnings: tuple[str, ...]


def _ours_verdicts(manifest: RunManifest, correlate: Correlate, payload: object) -> ScanParseResult:
    return parse_scan_payload(payload, correlate, manifest=manifest)


def _baseline_verdicts(manifest: RunManifest, correlate: Correlate, payload: object) -> ScanParseResult:
    raise BaselineNotImplementedError(
        f"미구현(A5 보류): baseline 회차 '{manifest.condition}'#{manifest.repeat_index} — "
        "원본 ARTEX finding의 GT 상관 기준이 정해지지 않아 채점하지 않는다(0점 처리 금지)"
    )


# 엔진별로 다른 유일한 곳 — 레코드 → 챌린지별 판정. 이 뒤 채점은 엔진을 보지 않는다.
_VERDICT_ADAPTERS: dict[Engine, Callable[[RunManifest, Correlate, object], ScanParseResult]] = {
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
    *, pre_solved: dict[str, bool] | None = None, engine_solved: frozenset[str] = frozenset(),
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
        pre_solved=dict(pre_solved or {}),
        engine_solved=engine_solved,
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
    runner_key: bytes | None = None,
    correlatable: frozenset[str] | None = None,
) -> ScoredRepeat:
    """서명된 러너 기록으로 회차 입력을 검증한 뒤 채점한다. 임의 traffic/coverage 주입은 없다."""
    inputs = load_run_inputs(manifest, runner_key, correlate)
    run = f"{manifest.condition}#{manifest.repeat_index}"
    if inputs.judgments_collected:
        parsed = _VERDICT_ADAPTERS[manifest.engine](manifest, correlate, inputs.records)
    else:
        parsed = _uncollected_verdicts()
    evidence_ok = confirmed_evidence(parsed, inputs.traffic, correlate)
    result = assemble_repeat(manifest, parsed, inputs.post, evidence_ok, inputs.covered, correlatable,
                             pre_solved=inputs.pre, engine_solved=inputs.engine_solved)
    warnings = list(repeat_warnings(manifest, parsed, inputs.traffic))
    if not inputs.judgments_collected:
        warnings.append(f"경고[판정 미수집] {run}: v1.1 records=null — GT 판정 전부 판단불가(미탐·오탐·확정 0), "
                        "해결 귀속만 보고(프로토콜 §4-7)")
    if inputs.version == V11 and inputs.proof_id is None:
        warnings.append(f"경고[격리 증명 없음] {run}: 새 해결의 엔진 귀속 미확인(run-record-v1.1 §4)")
    basis = {c.key for c in core_gt() + bac_category()}
    for key in sorted(basis):
        if inputs.pre[key]:
            warnings.append(f"경고[사전 해결] {manifest.condition}#{manifest.repeat_index}: '{key}' — 판단불가")
        elif inputs.post[key] and key not in inputs.engine_solved:
            warnings.append(f"경고[해결 귀속 미확인] {manifest.condition}#{manifest.repeat_index}: '{key}' — 판단불가")
    return ScoredRepeat(result, tuple(warnings), inputs)


def _uncollected_verdicts() -> ScanParseResult:
    """판정 미수집: GT 전 항목을 판단불가 판정으로. 판정표에서 해결·커버리지와 무관하게 판단불가가 된다."""
    keys = sorted({c.key for c in core_gt() + bac_category()})
    return ScanParseResult({key: ScanVerdict.UNDECIDABLE for key in keys}, (), (), {})


def score_repeat(
    manifest: RunManifest, correlate: Correlate, *, runner_key: bytes | None = None,
    correlatable: frozenset[str] | None = None,
) -> RepeatResult:
    """score_run에서 채점 입력만. 경고가 필요 없는 호출용."""
    return score_run(manifest, correlate, runner_key=runner_key, correlatable=correlatable).result


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
    manifests: list[RunManifest], correlate: Correlate, correlatable: frozenset[str],
    *, runner_key: bytes | None = None,
) -> Report:
    """전 회차를 채점해 코어 GT·BAC 두 표와 경고를 만든다. 행·경고 순서는 manifest 순서."""
    check_comparable(manifests)
    by_condition: dict[str, list[RepeatResult]] = {}
    warnings: list[str] = []
    reference = None
    seen_executions: set[str] = set()
    seen_proofs: set[str] = set()
    versions: set[str] = set()
    drain_policies: set[tuple[int, int]] = set()
    run_conditions: dict[str, set[str]] = {"엔진 설정": set(), "적용 샘플링": set(), "머신 사양": set(), "머신": set(),
                                           "LLM 스트리밍": set()}
    for m in manifests:
        scored = score_run(m, correlate, correlatable=correlatable, runner_key=runner_key)
        inputs = scored.inputs
        basis = {c.key for c in core_gt() + bac_category()}
        conditions = (inputs.fixture_fingerprint, inputs.environment_fingerprint,
                      tuple((key, inputs.pre[key]) for key in sorted(basis)))
        if reference is not None and conditions != reference:
            raise ComparabilityError("fixture/환경/pre 해결 상태 불일치 — 동일 조건 비교 중단")
        reference = conditions
        if inputs.execution_id in seen_executions:
            raise ComparabilityError("execution_id 중복 — 러너 기록 재사용")
        seen_executions.add(inputs.execution_id)
        if inputs.proof_id is not None:
            if inputs.proof_id in seen_proofs:
                raise ComparabilityError("격리 증명 proof_id 재사용")
            seen_proofs.add(inputs.proof_id)
        versions.add(inputs.version)
        if len(versions) > 1:
            raise ComparabilityError("러너 기록 v1/v1.1 혼합 — 같은 귀속 방식끼리만 비교(프로토콜 §4)")
        # 봉인 뒤 유예·정착 대기는 post 관측 시점과 취소 여부를 바꾼다. 환경 지문으로 고정되지만 서명 근거도 같아야
        # 한다(#153 리뷰). 끊은 건수·적용 대기는 결과라 비교하지 않는다.
        if inputs.drain_policy is not None:
            drain_policies.add(inputs.drain_policy)
            if len(drain_policies) > 1:
                raise ComparabilityError(f"drain 유예·정착 대기 정책 불일치 {sorted(drain_policies)} — "
                                         "같은 정책끼리만 비교(프로토콜 §2)")
        # 엔진 실행 설정·게이트웨이가 실제 넣은 샘플링·머신이 회차마다 같아야 같은 조건이다(프로토콜 §5.1·§6).
        if inputs.run_conditions is not None:
            for (label, seen), value in zip(run_conditions.items(), inputs.run_conditions):
                seen.add(value)
                if len(seen) > 1:
                    raise ComparabilityError(f"회차 조건 불일치({label}) — 같은 실행 설정·샘플링·머신끼리만 비교"
                                             "(프로토콜 §5.1·§6)")
        by_condition.setdefault(m.condition, []).append(scored.result)
        warnings.extend(scored.warnings)

    if versions == {V1}:
        warnings.append("경고[공식 비교 아님] 러너 기록 v1(직접 관측 이벤트) — 합성 시험·과거 형식 회귀 검증용, "
                        "공식 비교표는 v1.1 isolated_window만(프로토콜 §4)")
    sections = [f"모델 태그: {manifests[0].model_tag}\n"]
    for basis_name, basis in (("코어 GT(인가 9)", core_gt()), ("BAC category(10)", bac_category())):
        rows = [aggregate_condition(c, repeats, basis) for c, repeats in by_condition.items()]
        sections.append(render_matrix(rows, basis_name))
    return Report("\n".join(sections), tuple(warnings))


# 표를 내지 않고 멈추는 오류들. 그 밖의 예외는 버그라 그대로 올린다.
_STOP_ERRORS = (
    ManifestError,
    RunRecordError,
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
    parser.add_argument("--runner-key-file", required=True, help="엔진 밖의 신뢰 러너 HMAC 키 파일(값 출력 금지)")
    parser.add_argument("--correlation", default=DEFAULT_PATH, help="평가 전용 GT 상관 JSON")
    args = parser.parse_args(argv)
    try:
        table = load_correlation(args.correlation)
        report = build_report(load_manifests(args.manifest), table.correlate, table.operative,
                              runner_key=read_runner_key(args.runner_key_file))
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
