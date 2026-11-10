"""브라우저 수용시험 보존 시험(docs/evaluation/browser-acceptance.md §6)의 집계.

계측기 근거 디렉터리의 engine-requests.jsonl, drain 표식, 가린 경로 기록 engine-paths.jsonl만 읽는다.
평가 정답(챌린지 목록)은 읽지 않는다(절대규칙 7). 결과에는 건수와 비율만 남기고 경로 패턴 목록은 내보내지 않는다.
계측기는 경로 조각을 키 HMAC 가명으로 남기므로, 설계상 막히는 경로 규칙도 같은 키로 가린 뒤 비교한다.
키는 근거 디렉터리 밖 파일로만 받고, 행의 키 참조와 맞는지 먼저 확인한다(다른 키면 규칙이 조용히 0건이 된다).

못 닿은 패턴의 이유(policy_blocked·budget_limited·not_attempted)는 E② 시도 기록이 있어야 나눌 수 있어서
이 단계에는 없다.
"""

from __future__ import annotations

import argparse
import json
import re
import statistics
import sys
from dataclasses import dataclass
from pathlib import Path

from path_mask import PATH_PROFILE, STATIC_SUFFIXES, PathKeyError, key_ref, mask_path, read_path_key
from runner_evidence import (
    DRAINED_MARKER, FORWARDED_KINDS, EvidenceError, MeterFiles, forwarded_count, sequence_complete,
)

SUMMARY_FORMAT = "abc2lab.preservation-summary/v2"
EXCLUSIONS_FORMAT = "abc2lab.preservation-exclusions/v1"
READ_METHODS = frozenset({"GET", "HEAD"})
_METHOD = re.compile(r"[A-Z]{1,16}\Z")
# 규칙 경로는 ASCII 안전 문자 조각만 받는다(path_mask의 소문자 변환이 Go와 같은 범위). `/` 하나는 전부를 빼서 받지 않는다.
_RULE_PATH = re.compile(r"(?:/[A-Za-z0-9._~-]+)+/?\Z")


class PreservationError(RuntimeError):
    pass


@dataclass(frozen=True)
class Round:
    """회차 하나의 예산 안 전달 행. 패턴은 그중 대상까지 닿은(sent=yes) `(메서드, 경로 패턴)`이다."""

    patterns: frozenset[tuple[str, str]]
    forwarded: int
    static_forwarded: int
    sent: int
    uncertain: int
    masked_segments: int


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
        total = forwarded_count(engine)
        drained = meter.marker(DRAINED_MARKER)
        # 최종 집계다. drain 표식이 없거나 기록이 덜 내려갔거나 순번·건수가 표식과 다르면(잘린 꼬리 포함) 받지 않는다.
        if (drained is None or drained.get("log_complete") is not True or drained.get("forwarded_requests") != total
                or not sequence_complete(engine)):
            raise PreservationError("meter_log_incomplete")
        paths = {row["seq"]: row for row in meter.path_rows(engine)}
    except EvidenceError as exc:
        raise PreservationError(str(exc)) from None
    if any(row["path_key_ref"] != expected_key_ref for row in paths.values()):
        raise PreservationError("path_key_mismatch")
    forwarded = sorted((row for row in engine if row["kind"] in FORWARDED_KINDS), key=lambda row: row["seq"])[:budget]
    patterns: set[tuple[str, str]] = set()
    static = sent = uncertain = masked = 0
    for row in forwarded:
        path = paths[row["seq"]]
        static += is_static(path["path_pattern"])
        if row["sent"] == "uncertain":
            uncertain += 1
        if row["sent"] != "yes":
            continue
        patterns.add((row["method"], path["path_pattern"]))
        sent += 1
        masked += path["masked_segments"]
    return Round(frozenset(patterns), len(forwarded), static, sent, uncertain, masked)


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


def summarize(baseline: list[Round], improved: list[Round], exclusions: Exclusions, read_only: bool) -> dict:
    """read_only면 GET·HEAD 패턴만 분모로 쓰는 읽기 보존율, 아니면 전체 보존율이다."""
    if not baseline or len(baseline) != len(improved):
        raise PreservationError("pairs_invalid")

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
        "by_class": {
            "api": _compare(baseline, improved, lambda key: keep(key) and not is_static(key[1])),
            "static": _compare(baseline, improved, lambda key: keep(key) and is_static(key[1])),
        },
        # 개선 후 회차에서 하위 자원이 쓴 예산을 드러낸다. 예산은 sent와 상관없이 전달 행마다 쓰인다.
        "browser_cost": [{"forwarded": r.forwarded, "static_forwarded": r.static_forwarded,
                          "static_share": r.static_forwarded / r.forwarded if r.forwarded else None}
                         for r in improved],
        "sent": {"baseline": [r.sent for r in baseline], "improved": [r.sent for r in improved]},
        "uncertain": {"baseline": [r.uncertain for r in baseline], "improved": [r.uncertain for r in improved]},
        "masked_segments": {"baseline": [r.masked_segments for r in baseline],
                            "improved": [r.masked_segments for r in improved]},
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="보존 시험 집계(브라우저 수용시험 §6)")
    parser.add_argument("--baseline", action="append", type=Path, required=True, help="원본 회차 계측기 디렉터리(짝 순서)")
    parser.add_argument("--improved", action="append", type=Path, required=True, help="개선 후 회차 계측기 디렉터리(짝 순서)")
    parser.add_argument("--budget", type=int, required=True, help="양쪽 회차의 요청 예산")
    parser.add_argument("--exclusions", type=Path, required=True, help="설계상 막히는 패턴 규칙 JSON(원문 경로)")
    parser.add_argument("--path-key-file", required=True, help="계측기 -path-key-file과 같은 키 파일(근거 디렉터리 밖)")
    parser.add_argument("--read-only", action="store_true", help="GET·HEAD 패턴만 분모로 쓴다(R-R)")
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
        summary = summarize(baseline, improved, exclusions, args.read_only)
    except PreservationError as exc:
        print(f"preservation: {exc}", file=sys.stderr)
        return 2
    json.dump(summary, sys.stdout, ensure_ascii=False, indent=1)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
