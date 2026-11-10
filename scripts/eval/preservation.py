"""브라우저 수용시험 보존 시험(docs/evaluation/browser-acceptance.md §6)의 집계.

계측기 근거 디렉터리의 engine-requests.jsonl과 가린 경로 기록 engine-paths.jsonl만 읽는다.
평가 정답(챌린지 목록)은 읽지 않는다(절대규칙 7). 결과에는 건수와 비율만 남기고, 규칙으로 못 가린 값이
섞일 수 있는 경로 패턴 목록은 내보내지 않는다.

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

from runner_evidence import FORWARDED_KINDS, PATH_LOG, EvidenceError, MeterFiles, _read_owned, _rows

SUMMARY_FORMAT = "abc2lab.preservation-summary/v1"
EXCLUSIONS_FORMAT = "abc2lab.preservation-exclusions/v1"
# cmd/eval-meter paths.go의 pathRow·pathMaskProfile과 같은 값이다.
PATH_ROW = frozenset({"seq", "method", "path_pattern", "masked_segments", "path_profile_ref"})
PATH_PROFILE = "abc2lab.path-mask/v2"
READ_METHODS = frozenset({"GET", "HEAD"})
# 정적 자원과 API를 따로 센다. 경로의 마지막 조각 확장자로만 나누며, 응답 형식은 보지 않는다.
# 계측기는 HMAC으로 바꾼 조각에도 이 확장자만 남긴다(cmd/eval-meter paths.go의 staticSuffixes와 같다).
STATIC_SUFFIXES = (".js", ".mjs", ".css", ".map", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".webp",
                   ".woff", ".woff2", ".ttf", ".otf")
_METHOD = re.compile(r"[A-Z]{1,16}\Z")


class PreservationError(RuntimeError):
    pass


@dataclass(frozen=True)
class Round:
    """회차 하나에서 예산 안에 대상까지 닿은(sent=yes) `(메서드, 경로 패턴)`."""

    patterns: frozenset[tuple[str, str]]
    sent: int
    static_sent: int
    uncertain: int
    masked_segments: int


def is_static(pattern: str) -> bool:
    return pattern.rsplit("/", 1)[-1].lower().endswith(STATIC_SUFFIXES)


def _path_rows(directory: Path) -> dict[int, dict]:
    rows = _rows(_read_owned(directory, PATH_LOG), PATH_ROW)
    for row in rows:
        pattern = row["path_pattern"]
        if (row["path_profile_ref"] != PATH_PROFILE or not isinstance(pattern, str) or not pattern
                or not isinstance(row["method"], str) or type(row["masked_segments"]) is not int
                or row["masked_segments"] < 0):
            raise PreservationError("path_row_invalid")
    return {row["seq"]: row for row in rows}


def load_round(directory: Path, budget: int) -> Round:
    """양쪽 모두 계측기 전달 순서로 예산 수까지만 센다(초과분 제외). 봉인 뒤 거부 행은 경로 행이 없다."""
    if type(budget) is not int or budget < 1:
        raise PreservationError("budget_invalid")
    try:
        engine = MeterFiles(directory).engine_rows()
        paths = _path_rows(directory)
    except EvidenceError as exc:
        raise PreservationError(str(exc)) from None
    forwarded = sorted((row for row in engine if row["kind"] in FORWARDED_KINDS), key=lambda row: row["seq"])
    if set(paths) != {row["seq"] for row in forwarded}:
        raise PreservationError("path_log_inconsistent")
    patterns: set[tuple[str, str]] = set()
    sent = static = uncertain = masked = 0
    for row in forwarded[:budget]:
        path = paths[row["seq"]]
        if path["method"] != row["method"]:
            raise PreservationError("path_log_inconsistent")
        if row["sent"] == "uncertain":
            uncertain += 1
        if row["sent"] != "yes":
            continue
        patterns.add((row["method"], path["path_pattern"]))
        sent += 1
        static += is_static(path["path_pattern"])
        masked += path["masked_segments"]
    return Round(frozenset(patterns), sent, static, uncertain, masked)


@dataclass(frozen=True)
class Exclusions:
    """승인 메서드 밖·위험 경로처럼 규칙으로 설계상 막히는 패턴. 수용시험 정책 파일과 함께 결과 전에 고정한다."""

    methods: frozenset[str]
    path_prefixes: tuple[str, ...]

    def excludes(self, key: tuple[str, str]) -> bool:
        method, pattern = key
        return method in self.methods or any(
            pattern == prefix or pattern.startswith(prefix.rstrip("/") + "/") for prefix in self.path_prefixes)


def load_exclusions(raw: object) -> Exclusions:
    if not isinstance(raw, dict) or set(raw) != {"format", "methods", "path_prefixes"} \
            or raw["format"] != EXCLUSIONS_FORMAT:
        raise PreservationError("exclusions_invalid")
    methods, prefixes = raw["methods"], raw["path_prefixes"]
    if not isinstance(methods, list) or not all(isinstance(m, str) and _METHOD.match(m) for m in methods):
        raise PreservationError("exclusions_invalid")
    if not isinstance(prefixes, list) or not all(isinstance(p, str) and p.startswith("/") for p in prefixes):
        raise PreservationError("exclusions_invalid")
    return Exclusions(frozenset(methods), tuple(prefixes))


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

    def keep(key: tuple[str, str]) -> bool:
        return not exclusions.excludes(key) and (not read_only or key[0] in READ_METHODS)

    base_all = {key for r in baseline for key in r.patterns}
    return {
        "format": SUMMARY_FORMAT,
        "path_profile_ref": PATH_PROFILE,
        "read_only": read_only,
        "pairs": len(baseline),
        "excluded_by_rules": sum(1 for key in base_all if exclusions.excludes(key)),
        **_compare(baseline, improved, keep),
        "by_class": {
            "api": _compare(baseline, improved, lambda key: keep(key) and not is_static(key[1])),
            "static": _compare(baseline, improved, lambda key: keep(key) and is_static(key[1])),
        },
        # 개선 후 회차에서 하위 자원이 쓴 예산을 드러낸다.
        "browser_cost": [{"sent": r.sent, "static_sent": r.static_sent,
                          "static_share": r.static_sent / r.sent if r.sent else None} for r in improved],
        "uncertain": {"baseline": [r.uncertain for r in baseline], "improved": [r.uncertain for r in improved]},
        "masked_segments": {"baseline": [r.masked_segments for r in baseline],
                            "improved": [r.masked_segments for r in improved]},
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="보존 시험 집계(브라우저 수용시험 §6)")
    parser.add_argument("--baseline", action="append", type=Path, required=True, help="원본 회차 계측기 디렉터리(짝 순서)")
    parser.add_argument("--improved", action="append", type=Path, required=True, help="개선 후 회차 계측기 디렉터리(짝 순서)")
    parser.add_argument("--budget", type=int, required=True, help="양쪽 회차의 요청 예산")
    parser.add_argument("--exclusions", type=Path, required=True, help="설계상 막히는 패턴 규칙 JSON")
    parser.add_argument("--read-only", action="store_true", help="GET·HEAD 패턴만 분모로 쓴다(R-R)")
    args = parser.parse_args(argv)
    try:
        try:
            exclusions = load_exclusions(json.loads(args.exclusions.read_text(encoding="utf-8")))
        except (OSError, UnicodeError, ValueError):
            raise PreservationError("exclusions_invalid") from None
        baseline = [load_round(path, args.budget) for path in args.baseline]
        improved = [load_round(path, args.budget) for path in args.improved]
        summary = summarize(baseline, improved, exclusions, args.read_only)
    except PreservationError as exc:
        print(f"preservation: {exc}", file=sys.stderr)
        return 2
    json.dump(summary, sys.stdout, ensure_ascii=False, indent=1)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
