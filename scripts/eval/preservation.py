"""브라우저 수용시험 보존 시험(docs/evaluation/browser-acceptance.md §6)의 집계.

계측기 근거 디렉터리의 engine-requests.jsonl, drain 표식, 가린 경로 기록 engine-paths.jsonl만 읽는다.
평가 정답(챌린지 목록)은 읽지 않는다(절대규칙 7). 결과에는 건수와 비율만 남기고 경로 패턴 목록은 내보내지 않는다.
계측기는 경로 조각을 키 HMAC 가명으로 남기므로, 설계상 막히는 경로 규칙도 같은 키로 가린 뒤 비교한다.
키는 근거 디렉터리 밖 파일로만 받고, 행의 키 참조와 맞는지 먼저 확인한다(다른 키면 규칙이 조용히 0건이 된다).

원본끼리 보존율(`baseline_reference`)은 원본이 회차마다 흔들리는 정도를 보여 주는 참고값이고 판정에 쓰지 않는다.
못 닿은 패턴의 이유(policy_blocked·budget_limited·not_attempted)는 같은 키로 가린 차단 기록(탐색 경계·E②)이
있어야 나눌 수 있어서 이 단계에는 없다.
"""

from __future__ import annotations

import argparse
import json
import re
import statistics
import sys
from dataclasses import asdict, dataclass
from pathlib import Path

# -I에서도 이 저장소의 평가 모듈만 가져오고 사용자 site-packages는 쓰지 않는다.
sys.path.insert(0, str(Path(__file__).resolve().parent))

from preservation_record import (PreservationRecordError, load_record, validate_series, assessment)
from run_record import RunRecordError, read_runner_key
from path_mask import PATH_PROFILE, STATIC_SUFFIXES, PathKeyError, key_ref, mask_path, read_path_key
from repeat_denials import summarize_round
from runner_evidence import (
    DRAINED_MARKER, FORWARDED_KINDS, INGRESSES, PATH_AUTH_FIELDS, PATH_INGRESS_FIELD, SEALED_MARKER, EvidenceError, MeterFiles,
    drain_complete, final_log_consistent,
)

SUMMARY_FORMAT = "abc2lab.preservation-summary/v6"
EXCLUSIONS_FORMAT = "abc2lab.preservation-exclusions/v1"
READ_METHODS = frozenset({"GET", "HEAD"})
_METHOD = re.compile(r"[A-Z]{1,16}\Z")
# 규칙 경로는 ASCII 안전 문자 조각만 받는다(path_mask의 소문자 변환이 Go와 같은 범위). `/` 하나는 전부를 빼서 받지 않는다.
# `.`·`..` 조각은 계측기 경로에 남지 않아 어떤 패턴과도 맞지 않으므로(오타가 조용히 0건이 된다) 받지 않는다.
_RULE_PATH = re.compile(r"(?:/(?!\.\.?(?:/|\Z))[A-Za-z0-9._~-]+)+/?\Z")
# 계측기는 32조각을 넘는 경로를 이 패턴 하나로 남긴다. 접두사 규칙에 걸리지 않고 서로 다른 경로가 합쳐지므로 수를 따로 싣는다.
TOO_LONG_PATTERN = "/{too_long}"


class PreservationError(RuntimeError):
    pass


@dataclass(frozen=True)
class AuthHeaders:
    """예산 안 전달 행 중 인증 헤더가 붙은 수. 값은 계측기도 남기지 않으므로 유무만 센다."""

    authorization: int
    cookie: int
    any: int


@dataclass(frozen=True)
class Round:
    """회차 하나의 예산 안 전달 행. 패턴은 그중 대상까지 닿은(sent=yes) `(메서드, 경로 패턴)`이다."""

    patterns: frozenset[tuple[str, str]]
    forwarded: int
    static_forwarded: int
    sent: int
    uncertain: int
    masked_segments: int
    # 예산까지 전달했는지. 아니면 시간 등 다른 한도로 먼저 멈춘 회차다(멈춘 이유는 러너 결과에 있다).
    reached_budget: bool = True
    too_long: int = 0
    # 예산 안 전달 행 중 Authorization·Cookie 헤더가 붙은 수. 계측기가 그 칸을 남기기 전 회차는 None(NOT_RUN)이다.
    # 인증을 시도했다는 관찰일 뿐 로그인 성공 근거가 아니다.
    auth_headers: AuthHeaders | None = None
    # 예산 안 전달 행의 계측기 입구별 수(main: 원본 엔진 직접·E② 진단 입구, explore: 탐색 경계 입구).
    # 계측기가 입구 칸을 남기기 전 회차는 None(NOT_RUN)이다.
    ingress: tuple[tuple[str, int], ...] | None = None


def is_static(pattern: str) -> bool:
    """마지막 조각의 확장자로만 나눈다. 계측기는 `{h:…}`·`{id}`로 가린 조각에도 정적 확장자를 남긴다."""
    return pattern.rsplit("/", 1)[-1].lower().endswith(STATIC_SUFFIXES)


def load_round(directory: Path, budget: int, expected_key_ref: str) -> Round:
    """drain까지 끝난 계측기 기록만 받는다. 양쪽 모두 전달 순서로 예산 수까지만 센다(초과분 제외)."""
    if type(budget) is not int or budget < 1:
        raise PreservationError("budget_invalid")
    meter = MeterFiles(directory)
    try:
        engine = meter.engine_rows()
        drained = meter.marker(DRAINED_MARKER)
        # 최종 집계다. 러너가 무효로 하는 기록(drain 미완료, 봉인 표식 없음, 순번·건수가 표식과 다름, 잘린 꼬리)은
        # 러너와 같은 함수로 거른다.
        if (drained is None or not drain_complete(drained)
                or not final_log_consistent(engine, meter.marker(SEALED_MARKER), drained)):
            raise PreservationError("meter_log_incomplete")
        paths = {row["seq"]: row for row in meter.path_rows(engine)}
    except EvidenceError as exc:
        raise PreservationError(str(exc)) from None
    if any(row["path_key_ref"] != expected_key_ref for row in paths.values()):
        raise PreservationError("path_key_mismatch")
    ordered = sorted((row for row in engine if row["kind"] in FORWARDED_KINDS), key=lambda row: row["seq"])
    forwarded = ordered[:budget]
    patterns: set[tuple[str, str]] = set()
    static = sent = uncertain = masked = too_long = 0
    # path_rows가 한 기록 안의 행 모양을 하나로 맞춰 두므로, 칸이 없는 행이 하나라도 있으면 그 회차 전체에 없다.
    has_auth_fields = all(PATH_AUTH_FIELDS <= set(row) for row in paths.values())
    has_ingress = all(PATH_INGRESS_FIELD in row for row in paths.values())
    ingress = dict.fromkeys(sorted(INGRESSES), 0)
    authorization = cookie = either = 0
    for row in forwarded:
        path = paths[row["seq"]]
        if has_ingress:
            ingress[path[PATH_INGRESS_FIELD]] += 1
        if has_auth_fields:
            authorization += path["has_authorization"]
            cookie += path["has_cookie"]
            either += path["has_authorization"] or path["has_cookie"]
        static += is_static(path["path_pattern"])
        if row["sent"] == "uncertain":
            uncertain += 1
        if row["sent"] != "yes":
            continue
        patterns.add((row["method"], path["path_pattern"]))
        sent += 1
        masked += path["masked_segments"]
        too_long += path["path_pattern"] == TOO_LONG_PATTERN
    return Round(frozenset(patterns), len(forwarded), static, sent, uncertain, masked,
                 reached_budget=len(ordered) >= budget, too_long=too_long,
                 auth_headers=AuthHeaders(authorization, cookie, either) if has_auth_fields else None,
                 ingress=tuple(ingress.items()) if has_ingress else None)


@dataclass(frozen=True)
class Exclusions:
    """승인 메서드 밖·위험 경로처럼 규칙으로 설계상 막히는 패턴. 수용시험 정책 파일과 함께 결과 전에 고정한다.

    path_prefixes는 계측기와 같은 키로 가린 형태다. 값처럼 보이는 규칙 조각은 `{id}`가 되어 모든 값에 맞는다.
    """

    methods: frozenset[str]
    path_prefixes: tuple[str, ...]

    def excludes(self, key: tuple[str, str]) -> bool:
        method, pattern = key
        return method in self.methods or any(
            pattern == prefix or pattern.startswith(prefix.rstrip("/") + "/") for prefix in self.path_prefixes)


def load_exclusions(raw: object, key: bytes) -> Exclusions:
    """규칙 파일의 원문 경로를 계측기와 같은 가림으로 바꾼다. 원문 규칙은 결과에 싣지 않는다."""
    if not isinstance(raw, dict) or set(raw) != {"format", "methods", "path_prefixes"} \
            or raw["format"] != EXCLUSIONS_FORMAT:
        raise PreservationError("exclusions_invalid")
    methods, prefixes = raw["methods"], raw["path_prefixes"]
    if not isinstance(methods, list) or not all(isinstance(m, str) and _METHOD.match(m) for m in methods):
        raise PreservationError("exclusions_invalid")
    if not isinstance(prefixes, list) or not all(isinstance(p, str) and _RULE_PATH.match(p) for p in prefixes):
        raise PreservationError("exclusions_invalid")
    try:
        masked = tuple(mask_path(prefix, key)[0] for prefix in prefixes)
    except PathKeyError as exc:
        raise PreservationError(str(exc)) from None
    return Exclusions(frozenset(methods), masked)


def _ratio(denominator: set, reached: set) -> float | None:
    return len(denominator & reached) / len(denominator) if denominator else None


def _compare(baseline: list[Round], improved: list[Round], keep) -> dict:
    base_union = {key for r in baseline for key in r.patterns if keep(key)}
    improved_union = {key for r in improved for key in r.patterns if keep(key)}
    pair_ratios = []
    empty_pairs = 0
    # 회차별 값은 §5.1 순서의 같은 짝끼리 비교한다. 분모가 빈 짝은 비율에 넣지 않고 수만 남긴다.
    for b, o in zip(baseline, improved):
        ratio = _ratio({key for key in b.patterns if keep(key)}, set(o.patterns))
        if ratio is None:
            empty_pairs += 1
        else:
            pair_ratios.append(ratio)
    return {
        "denominator": len(base_union),
        "union_ratio": _ratio(base_union, improved_union),
        "pair_ratios": pair_ratios,
        "pair_mean": statistics.fmean(pair_ratios) if pair_ratios else None,
        "pair_sd": statistics.stdev(pair_ratios) if len(pair_ratios) >= 2 else None,
        "pairs_without_denominator": empty_pairs,
        "improved_only": len(improved_union - base_union),
    }


def _baseline_reference(baseline: list[Round], keep) -> dict:
    """원본 회차끼리의 보존율. 같은 원본이 회차마다 얼마나 흔들리는지 보여 주는 참고값이고 판정에는 쓰지 않는다.

    순서쌍 (Bi, Bj), i≠j 전부를 개선 후 짝과 같은 분모 범위·제외 규칙으로 |Bi∩Bj| / |Bi|로 센다.
    방향마다 분모가 달라 값이 달라지므로 한쪽 방향만 고르지 않는다.
    """
    if len(baseline) < 2:
        return {"reason": "baseline_rounds_lt_2", "pair_ratios": [], "pair_mean": None, "pair_sd": None,
                "pairs_without_denominator": 0}
    ratios = []
    empty = 0
    for i, first in enumerate(baseline):
        denominator = {key for key in first.patterns if keep(key)}
        for j, second in enumerate(baseline):
            if i == j:
                continue
            ratio = _ratio(denominator, set(second.patterns))
            if ratio is None:
                empty += 1
            else:
                ratios.append(ratio)
    return {"reason": None, "pair_ratios": ratios, "pair_mean": statistics.fmean(ratios) if ratios else None,
            "pair_sd": statistics.stdev(ratios) if len(ratios) >= 2 else None, "pairs_without_denominator": empty}


def _auth_headers(round_: Round) -> dict | None:
    return None if round_.auth_headers is None else asdict(round_.auth_headers)


def summarize(baseline: list[Round], improved: list[Round], exclusions: Exclusions, read_only: bool,
              repeat_denials: list[dict] | None = None) -> dict:
    """read_only면 GET·HEAD 패턴만 분모로 쓰는 읽기 보존율, 아니면 전체 보존율이다.

    repeat_denials는 개선 후 회차마다의 진단 요청 반복 차단 수(repeat_denials.summarize_round)다. 없으면 회차마다 NOT_RUN이다.
    """
    if not baseline or len(baseline) != len(improved):
        raise PreservationError("pairs_invalid")
    if repeat_denials is None:
        repeat_denials = [summarize_round(None, "") for _ in improved]
    if len(repeat_denials) != len(improved):
        raise PreservationError("e2_copies_invalid")

    def in_domain(key: tuple[str, str]) -> bool:
        return not read_only or key[0] in READ_METHODS

    def keep(key: tuple[str, str]) -> bool:
        return in_domain(key) and not exclusions.excludes(key)

    # 분모에서 실제로 뺀 수만 센다. 읽기 보존율이면 처음부터 분모 밖인 쓰기 패턴은 세지 않는다.
    base_domain = {key for r in baseline for key in r.patterns if in_domain(key)}
    return {
        "format": SUMMARY_FORMAT,
        "path_profile_ref": PATH_PROFILE,
        "read_only": read_only,
        "pairs": len(baseline),
        "excluded_by_rules": sum(1 for key in base_domain if exclusions.excludes(key)),
        **_compare(baseline, improved, keep),
        "baseline_reference": _baseline_reference(baseline, keep),
        "by_class": {
            "api": _compare(baseline, improved, lambda key: keep(key) and not is_static(key[1])),
            "static": _compare(baseline, improved, lambda key: keep(key) and is_static(key[1])),
        },
        # 개선 후 회차에서 하위 자원이 쓴 예산을 드러낸다. 예산은 sent와 상관없이 전달 행마다 쓰인다.
        "browser_cost": [{"forwarded": r.forwarded, "static_forwarded": r.static_forwarded,
                          "static_share": r.static_forwarded / r.forwarded if r.forwarded else None}
                         for r in improved],
        "sent": {"baseline": [r.sent for r in baseline], "improved": [r.sent for r in improved]},
        # 양쪽 모두 예산 수까지만 센다(§6). 예산 전에 멈춘 회차가 있는 짝은 순번으로 드러낸다.
        "forwarded": {"baseline": [r.forwarded for r in baseline], "improved": [r.forwarded for r in improved]},
        "pairs_under_budget": [i for i, (b, o) in enumerate(zip(baseline, improved), start=1)
                               if not (b.reached_budget and o.reached_budget)],
        # 32조각을 넘어 하나로 합쳐진 경로(sent=yes 행 수). 제외 규칙에 걸리지 않고 분모에 한 패턴으로 남는다.
        "too_long": {"baseline": [r.too_long for r in baseline], "improved": [r.too_long for r in improved]},
        "uncertain": {"baseline": [r.uncertain for r in baseline], "improved": [r.uncertain for r in improved]},
        "masked_segments": {"baseline": [r.masked_segments for r in baseline],
                            "improved": [r.masked_segments for r in improved]},
        # 회차별 예산 안 전달 행의 계측기 입구별 수. null은 계측기가 입구 칸을 남기기 전 회차(NOT_RUN)다.
        "ingress": {"baseline": [None if r.ingress is None else dict(r.ingress) for r in baseline],
                    "improved": [None if r.ingress is None else dict(r.ingress) for r in improved]},
        # 개선 후 회차별 진단 요청 반복 차단 수(E②까지 온 거부만). 원본에는 E②가 없다.
        "repeat_denials": {"improved": repeat_denials},
        # 회차별 인증 헤더가 붙은 예산 안 전달 수. null은 계측기가 그 칸을 남기기 전 회차(NOT_RUN)다.
        "auth_headers": {"baseline": [_auth_headers(r) for r in baseline],
                         "improved": [_auth_headers(r) for r in improved]},
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="보존 시험 집계(브라우저 수용시험 §6)")
    parser.add_argument("--baseline", action="append", type=Path, required=True, help="원본 회차 계측기 디렉터리(짝 순서)")
    parser.add_argument("--improved", action="append", type=Path, required=True, help="개선 후 회차 계측기 디렉터리(짝 순서)")
    parser.add_argument("--budget", type=int, required=True, help="양쪽 회차의 요청 예산")
    parser.add_argument("--exclusions", type=Path, required=True, help="설계상 막히는 패턴 규칙 JSON(원문 경로)")
    parser.add_argument("--path-key-file", required=True, help="계측기 -path-key-file과 같은 키 파일(근거 디렉터리 밖)")
    parser.add_argument("--read-only", action="store_true", help="GET·HEAD 패턴만 분모로 쓴다(R-R)")
    parser.add_argument("--improved-e2", action="append", type=Path,
                        help="개선 후 회차의 E② 수집 사본(회차 디렉터리의 e2-copy, --improved와 같은 순서). 주면 반복 차단 수를 센다")
    parser.add_argument("--baseline-record-dir", action="append", type=Path,
                        help="원본 서명 sidecar 디렉터리(각 --baseline과 같은 순서)")
    parser.add_argument("--improved-record-dir", action="append", type=Path,
                        help="개선 후 서명 sidecar 디렉터리(각 --improved와 같은 순서)")
    parser.add_argument("--runner-key-file", help="sidecar를 검증할 보호 러너 키(자료 디렉터리 밖)")
    parser.add_argument("--profile", choices=("R-P", "R-F", "R-R"), help="서명 자료의 기존 수용시험 기준")
    args = parser.parse_args(argv)
    try:
        try:
            key = read_path_key(args.path_key_file)
        except PathKeyError as exc:
            raise PreservationError(str(exc)) from None
        try:
            raw = json.loads(args.exclusions.read_text(encoding="utf-8"))
        except (OSError, UnicodeError, ValueError):
            raise PreservationError("exclusions_invalid") from None
        exclusions = load_exclusions(raw, key)
        ref = key_ref(key)
        baseline = [load_round(path, args.budget, ref) for path in args.baseline]
        improved = [load_round(path, args.budget, ref) for path in args.improved]
        repeats = None if args.improved_e2 is None else [summarize_round(path, ref) for path in args.improved_e2]
        summary = summarize(baseline, improved, exclusions, args.read_only, repeats)
        signed = (args.baseline_record_dir, args.improved_record_dir, args.runner_key_file, args.profile)
        if any(v is not None for v in signed):
            if not all(v is not None for v in signed):
                raise PreservationError("signed_round_arguments_incomplete")
            if len(args.baseline_record_dir) != len(args.baseline) or len(args.improved_record_dir) != len(args.improved):
                raise PreservationError("signed_round_count_mismatch")
            runner_key = read_runner_key(args.runner_key_file)
            # 두 보호 키는 원자료·sidecar와 함께 배포하지 않는다.
            roots = args.baseline + args.improved + args.baseline_record_dir + args.improved_record_dir
            for key_file in (Path(args.runner_key_file), Path(args.path_key_file)):
                if any(key_file.resolve().is_relative_to(root.resolve()) for root in roots):
                    raise PreservationError("key_inside_evidence_directory")
            b = [load_record(directory, MeterFiles(meter), runner_key)
                 for directory, meter in zip(args.baseline_record_dir, args.baseline)]
            o = [load_record(directory, MeterFiles(meter), runner_key)
                 for directory, meter in zip(args.improved_record_dir, args.improved)]
            if args.profile == "R-P" and args.budget != 100:
                raise PreservationError("preservation_profile_budget_mismatch")
            if args.read_only != (args.profile == "R-R"):
                raise PreservationError("preservation_profile_domain_mismatch")
            verified = validate_series(b, o, request_budget=args.budget, read_only=args.read_only)
            summary.update(format="abc2lab.preservation-summary/v7", signed_evidence=verified,
                           assessment=assessment(summary, args.profile, verified))
    except (PreservationError, PreservationRecordError, RunRecordError, EvidenceError, OSError) as exc:
        reason = "preservation_files_unavailable" if isinstance(exc, OSError) else str(exc)
        print(f"preservation: {reason}", file=sys.stderr)
        return 2
    json.dump(summary, sys.stdout, ensure_ascii=False, indent=1)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
