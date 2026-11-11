"""보존 시험 집계(preservation.py) 시험."""

from __future__ import annotations

import io
import json
import os
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from path_mask import key_ref, mask_path  # noqa: E402
from preservation import (  # noqa: E402
    EXCLUSIONS_FORMAT, PATH_PROFILE, SUMMARY_FORMAT, AuthHeaders, Exclusions, PreservationError, Round, is_static,
    load_exclusions, load_round, main, summarize,
)

KEY = bytes(range(32))
REF = key_ref(KEY)
NONE = Exclusions(frozenset(), ())


def masked(path):
    return mask_path(path, KEY)[0]


def engine_row(seq, method="GET", kind="request", sent="yes"):
    response = "none" if sent == "no" else "complete"
    return {"seq": seq, "kind": kind, "method": method, "status": 200, "started_at": "a", "completed_at": "b",
            "response_bytes": 1, "outcome": "completed", "sent": sent, "response": response,
            "attempt_id_state": "absent", "attempt_id": ""}


def path_row(seq, path, method="GET", ref=REF, auth=None):
    """auth가 (Authorization, Cookie) 쌍이면 인증 헤더 칸이 있는 계측기 행, None이면 그 칸이 생기기 전 행이다."""
    pattern, count = mask_path(path, KEY)
    row = {"seq": seq, "method": method, "path_pattern": pattern, "masked_segments": count,
           "path_profile_ref": PATH_PROFILE, "path_key_ref": ref}
    if auth is not None:
        row.update(has_authorization=auth[0], has_cookie=auth[1])
    return row


def rnd(*keys, forwarded=None, static=0):
    n = len(keys) if forwarded is None else forwarded
    return Round(frozenset(keys), n, static, len(keys), 0, 0)


def rules(methods=(), prefixes=()):
    return load_exclusions({"format": EXCLUSIONS_FORMAT, "methods": list(methods), "path_prefixes": list(prefixes)},
                           KEY)


@unittest.skipUnless(hasattr(os, "O_NOFOLLOW") and hasattr(os, "geteuid"), "POSIX 전용")
class LoadRoundTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)

    def meter(self, engine, paths, drained=True, engine_tail="", sealed=True):
        directory = Path(self.tmp.name, str(len(os.listdir(self.tmp.name))))
        directory.mkdir(mode=0o700)
        os.chmod(directory, 0o700)
        (directory / "engine-requests.jsonl").write_text(
            "".join(json.dumps(row) + "\n" for row in engine) + engine_tail, encoding="utf-8")
        if paths is not None:
            (directory / "engine-paths.jsonl").write_text("".join(json.dumps(row) + "\n" for row in paths),
                                                          encoding="utf-8")
        if sealed:
            (directory / "engine-sealed.json").write_text(json.dumps({"role": "engine"}), encoding="utf-8")
        if drained:
            forwarded = sum(1 for row in engine if row["kind"] != "rejected")
            marker = {"role": "engine", "forwarded_requests": forwarded, "log_complete": True, "inflight_at_drain": 0,
                      "forced_cancelled_requests": 0}
            if isinstance(drained, dict):
                marker.update(drained)
            (directory / "engine-drained.json").write_text(json.dumps(marker), encoding="utf-8")
        return directory

    def check(self, directory, code, budget=10):
        with self.assertRaises(PreservationError) as caught:
            load_round(directory, budget, REF)
        self.assertEqual(str(caught.exception), code)

    def test_counts_rows_inside_the_budget(self):
        engine = [engine_row(1), engine_row(2, sent="uncertain"), engine_row(3, "POST"), engine_row(4, sent="no"),
                  engine_row(5), engine_row(6, kind="rejected", sent="no"), engine_row(7)]
        paths = [path_row(1, "/api/Users/7"), path_row(2, "/a"), path_row(3, "/api/Feedbacks", "POST"),
                 path_row(4, "/b.css"), path_row(5, "/main.js"), path_row(7, "/over/budget")]
        got = load_round(self.meter(engine, paths), 5, REF)
        self.assertEqual(got.patterns, {("GET", masked("/api/Users/7")), ("POST", masked("/api/Feedbacks")),
                                        ("GET", masked("/main.js"))})
        # 예산은 sent와 상관없이 전달 행마다 쓰인다. 정적 자원은 예산 안 전달 행(sent=no 포함)으로 센다.
        self.assertEqual((got.forwarded, got.static_forwarded, got.sent, got.uncertain, got.masked_segments),
                         (5, 2, 3, 1, 1))
        self.assertTrue(got.reached_budget)
        self.assertFalse(load_round(self.meter(engine, paths), 7, REF).reached_budget)

    def test_auth_headers_are_counted_inside_the_budget(self):
        # 예산 안 전달 행이면 sent와 상관없이 센다. 봉인 뒤 거부(경로 행 없음)와 예산 밖 행은 세지 않는다.
        engine = [engine_row(1), engine_row(2), engine_row(3, sent="no"), engine_row(4), engine_row(5),
                  engine_row(6, kind="rejected", sent="no")]
        paths = [path_row(1, "/a", auth=(True, False)), path_row(2, "/b", auth=(False, True)),
                 path_row(3, "/c", auth=(True, True)), path_row(4, "/d", auth=(False, False)),
                 path_row(5, "/e", auth=(True, True))]
        self.assertEqual(load_round(self.meter(engine, paths), 4, REF).auth_headers, AuthHeaders(2, 2, 3))
        self.assertEqual(load_round(self.meter(engine, paths), 10, REF).auth_headers, AuthHeaders(3, 3, 4))

    def test_rows_before_the_auth_columns_are_not_run(self):
        got = load_round(self.meter([engine_row(1)], [path_row(1, "/a")]), 10, REF)
        self.assertIsNone(got.auth_headers)
        # 전달이 없는 회차는 계측기 판과 상관없이 붙은 요청도 없다.
        self.assertEqual(load_round(self.meter([], []), 10, REF).auth_headers, AuthHeaders(0, 0, 0))

    def test_too_long_paths_are_counted(self):
        long_path = "/a" * 33
        got = load_round(self.meter([engine_row(1), engine_row(2)], [path_row(1, long_path), path_row(2, "/b")]), 10, REF)
        self.assertEqual((got.too_long, ("GET", "/{too_long}") in got.patterns), (1, True))

    def test_rejects_unfinished_or_missing_logs(self):
        # 계측기 디렉터리 대신 빈 디렉터리를 넘긴 경우.
        empty = Path(self.tmp.name, "empty")
        empty.mkdir(mode=0o700)
        os.chmod(empty, 0o700)
        self.check(empty, "meter_log_incomplete")
        self.check(self.meter([engine_row(1)], [path_row(1, "/a")], drained=False), "meter_log_incomplete")
        self.check(self.meter([engine_row(1)], [path_row(1, "/a")], drained={"log_complete": False}),
                   "meter_log_incomplete")
        # 요청 기록 꼬리가 잘리면 drain 표식의 건수와 다르다.
        self.check(self.meter([engine_row(1)], [path_row(1, "/a")], drained={"forwarded_requests": 2},
                              engine_tail=json.dumps(engine_row(2))), "meter_log_incomplete")
        self.check(self.meter([engine_row(2)], [path_row(2, "/a")]), "meter_log_incomplete")
        # 러너가 무효로 하는 기록도 받지 않는다(같은 함수를 쓴다).
        for marker in ({"inflight_at_drain": 5}, {"forwarded_requests": 1.0}, {"forwarded_requests": True},
                       {"forced_cancelled_requests": 1}):
            with self.subTest(marker=marker):
                self.check(self.meter([engine_row(1)], [path_row(1, "/a")], drained=marker), "meter_log_incomplete")
        self.check(self.meter([engine_row(1)], [path_row(1, "/a")], sealed=False), "meter_log_incomplete")
        self.check(self.meter([engine_row(1)], None), "meter_path_log_missing")

    def test_unknown_kind_is_rejected_not_skipped(self):
        self.check(self.meter([engine_row(1), engine_row(2, kind="other")], [path_row(1, "/a")]), "meter_log_invalid")

    def test_path_log_must_match_forwarded_rows(self):
        for engine, paths in (
            ([engine_row(1), engine_row(2)], [path_row(1, "/a")]),
            ([engine_row(1)], [path_row(1, "/a"), path_row(2, "/b")]),
            ([engine_row(1, "POST")], [path_row(1, "/a", "GET")]),
            ([engine_row(1)], []),
        ):
            self.check(self.meter(engine, paths), "meter_path_log_invalid")

    def test_key_must_be_the_meter_key(self):
        self.check(self.meter([engine_row(1)], [path_row(1, "/a", ref="0" * 16)]), "path_key_mismatch")

    def test_budget_must_be_positive(self):
        directory = self.meter([engine_row(1)], [path_row(1, "/a")])
        for budget in (0, -1, True, 1.5):
            self.check(directory, "budget_invalid", budget)

    def test_cli_prints_summary(self):
        base = self.meter([engine_row(1), engine_row(2), engine_row(3)],
                          [path_row(1, "/a", auth=(False, True)), path_row(2, "/b", auth=(False, False)),
                           path_row(3, "/rest/admin/users", auth=(True, True))])
        improved = self.meter([engine_row(1)], [path_row(1, "/a")])
        rule_file = Path(self.tmp.name, "rules.json")
        rule_file.write_text(json.dumps({"format": EXCLUSIONS_FORMAT, "methods": ["DELETE"],
                                         "path_prefixes": ["/rest/admin"]}), encoding="utf-8")
        key_file = Path(self.tmp.name, "path.key")
        key_file.write_bytes(KEY)
        os.chmod(key_file, 0o600)
        args = ["--baseline", str(base), "--improved", str(improved), "--exclusions", str(rule_file),
                "--path-key-file", str(key_file)]
        out = io.StringIO()
        with redirect_stdout(out):
            code = main(args + ["--budget", "10"])
        self.assertEqual(code, 0)
        summary = json.loads(out.getvalue())
        self.assertEqual((summary["denominator"], summary["union_ratio"], summary["excluded_by_rules"]), (2, 0.5, 1))
        self.assertEqual(summary["format"], SUMMARY_FORMAT)
        self.assertEqual(summary["auth_headers"], {"baseline": [{"authorization": 1, "cookie": 2, "any": 2}],
                                                   "improved": [None]})
        # 패턴·규칙 원문·가명 어느 것도 결과에 싣지 않는다.
        for text in ("/a", "admin", masked("/a"), masked("/rest/admin")):
            self.assertNotIn(text, out.getvalue())
        for extra, expected in ((["--budget", "0"], "budget_invalid"),):
            err = io.StringIO()
            with redirect_stderr(err):
                self.assertEqual(main(args + extra), 2)
            self.assertIn(expected, err.getvalue())
        os.chmod(key_file, 0o644)
        err = io.StringIO()
        with redirect_stderr(err):
            self.assertEqual(main(args + ["--budget", "10"]), 2)
        self.assertIn("path_key_untrusted", err.getvalue())


class SummarizeTests(unittest.TestCase):
    def test_union_and_pair_ratios(self):
        a, b, c, d = ("GET", "/a"), ("GET", "/b"), ("GET", "/c"), ("GET", "/d")
        baseline = [rnd(a, b), rnd(a, c, d)]
        improved = [rnd(a), rnd(c, d, ("GET", "/new"))]
        got = summarize(baseline, improved, NONE, False)
        self.assertEqual(got["denominator"], 4)
        self.assertEqual(got["union_ratio"], 3 / 4)
        self.assertEqual(got["pair_ratios"], [1 / 2, 2 / 3])
        self.assertAlmostEqual(got["pair_mean"], (1 / 2 + 2 / 3) / 2)
        self.assertIsNotNone(got["pair_sd"])
        self.assertEqual(got["improved_only"], 1)

    def test_exclusions_match_masked_meter_patterns(self):
        # 계측기가 실제로 남기는 v2 패턴으로 비교한다. 규칙도 같은 키로 가린다.
        rule = rules(["DELETE"], ["/rest/admin"])
        self.assertNotIn("admin", "".join(rule.path_prefixes))
        baseline = [rnd(("DELETE", masked("/x")), ("GET", masked("/rest/admin")), ("GET", masked("/rest/admin/users")),
                        ("GET", masked("/rest/administer")), ("GET", masked("/rest/admin.js")), ("GET", masked("/a")))]
        improved = [rnd(("GET", masked("/a")), ("DELETE", masked("/y")), ("GET", masked("/rest/admin/new")))]
        got = summarize(baseline, improved, rule, False)
        self.assertEqual((got["excluded_by_rules"], got["denominator"], got["union_ratio"]), (3, 3, 1 / 3))
        # 설계상 막히는 패턴은 개선 후에만 닿아도 세지 않는다.
        self.assertEqual(got["improved_only"], 0)

    def test_value_like_rule_segments_match_every_value(self):
        rule = rules(prefixes=["/api/Users/1"])
        self.assertTrue(rule.excludes(("GET", masked("/api/Users/42/basket"))))
        self.assertFalse(rule.excludes(("GET", masked("/api/Users"))))

    def test_rules_are_checked_and_need_the_key(self):
        good = {"format": EXCLUSIONS_FORMAT, "methods": ["DELETE"], "path_prefixes": ["/admin"]}
        self.assertEqual(load_exclusions(good, KEY).methods, {"DELETE"})
        for bad in ([], dict(good, format="x"), dict(good, extra=1), dict(good, methods="DELETE"),
                    dict(good, methods=["delete"]), dict(good, path_prefixes=["admin"]), dict(good, path_prefixes=[1]),
                    dict(good, path_prefixes=["/"]), dict(good, path_prefixes=["/a%2Fb"]),
                    dict(good, path_prefixes=["/a//b"]), dict(good, path_prefixes=["/é"]),
                    dict(good, path_prefixes=["/rest/../admin"]), dict(good, path_prefixes=["/."]),
                    dict(good, path_prefixes=["/a/./b"]), dict(good, path_prefixes=["/a/.."])):
            with self.subTest(bad=bad), self.assertRaises(PreservationError):
                load_exclusions(bad, KEY)
        with self.assertRaises(PreservationError):
            load_exclusions(good, KEY[:8])
        # 점으로 시작할 뿐인 조각은 받는다.
        self.assertEqual(len(load_exclusions(dict(good, path_prefixes=["/.well-known", "/a..b/"]), KEY).path_prefixes), 2)

    def test_both_sides_report_budget_use(self):
        baseline = [rnd(("GET", "/a")), Round(frozenset({("GET", "/a")}), 80, 0, 70, 0, 0, reached_budget=False)]
        improved = [rnd(("GET", "/a"), forwarded=500), Round(frozenset({("GET", "/a")}), 500, 0, 500, 0, 0, too_long=2)]
        got = summarize(baseline, improved, NONE, False)
        self.assertEqual(got["forwarded"], {"baseline": [1, 80], "improved": [500, 500]})
        self.assertEqual(got["pairs_under_budget"], [2])
        self.assertEqual(got["too_long"], {"baseline": [0, 0], "improved": [0, 2]})

    def test_excluded_count_follows_the_read_only_domain(self):
        baseline = [rnd(("GET", "/a"), ("DELETE", "/x"), ("DELETE", "/y"))]
        got = summarize(baseline, [rnd(("GET", "/a"))], Exclusions(frozenset({"DELETE"}), ()), True)
        self.assertEqual((got["denominator"], got["excluded_by_rules"]), (1, 0))
        got = summarize(baseline, [rnd(("GET", "/a"))], Exclusions(frozenset({"DELETE"}), ()), False)
        self.assertEqual((got["denominator"], got["excluded_by_rules"]), (1, 2))

    def test_read_only_uses_get_and_head(self):
        baseline = [rnd(("GET", "/a"), ("HEAD", "/a"), ("POST", "/a"), ("PUT", "/b"))]
        got = summarize(baseline, [rnd(("GET", "/a"), ("HEAD", "/a"))], NONE, True)
        self.assertEqual((got["denominator"], got["union_ratio"]), (2, 1.0))
        self.assertEqual(summarize(baseline, [rnd(("GET", "/a"), ("HEAD", "/a"))], NONE, False)["union_ratio"],
                         1 / 2)

    def test_static_and_api_are_split(self):
        bundle = masked("/main.3f2a9b8c7d6e5f4a3b2c1d0e9f8a7b6c.js")
        self.assertEqual(bundle, "/{id}.js")
        self.assertTrue(is_static(bundle) and is_static(masked("/Logo.PNG")) and not is_static(masked("/api/x")))
        baseline = [rnd(("GET", masked("/main.js")), ("GET", masked("/api/x")), ("GET", masked("/Logo.PNG")),
                        ("GET", bundle))]
        got = summarize(baseline, [rnd(("GET", masked("/api/x")))], NONE, False)
        self.assertEqual(got["by_class"]["api"]["union_ratio"], 1.0)
        self.assertEqual((got["by_class"]["static"]["denominator"], got["by_class"]["static"]["union_ratio"]),
                         (3, 0.0))

    def test_empty_denominator_pairs_are_counted_not_averaged(self):
        baseline = [rnd(("DELETE", "/x")), rnd(("GET", "/a"))]
        rule = Exclusions(frozenset({"DELETE"}), ())
        got = summarize(baseline, [rnd(), rnd(("GET", "/a"))], rule, False)
        self.assertEqual((got["pair_ratios"], got["pairs_without_denominator"], got["pair_sd"]), ([1.0], 1, None))
        self.assertIsNone(summarize([rnd()], [rnd()], NONE, False)["union_ratio"])

    def test_browser_cost_uses_the_forwarded_budget(self):
        # 예산 100, sent=no 20건, 정적 자원 40건이면 정적 비율은 0.4다(sent=yes 80건 기준 0.5가 아니다).
        improved = [Round(frozenset(), 100, 40, 80, 2, 3)]
        got = summarize([rnd(("GET", "/a"))], improved, NONE, False)
        self.assertEqual(got["browser_cost"], [{"forwarded": 100, "static_forwarded": 40, "static_share": 0.4}])
        self.assertEqual((got["sent"]["improved"], got["uncertain"]["improved"], got["masked_segments"]["improved"]),
                         ([80], [2], [3]))
        self.assertIsNone(summarize([rnd()], [Round(frozenset(), 0, 0, 0, 0, 0)], NONE, False)["browser_cost"][0]
                          ["static_share"])

    def test_auth_headers_are_reported_per_round(self):
        counted = Round(frozenset(), 3, 0, 3, 0, 0, auth_headers=AuthHeaders(1, 2, 2))
        got = summarize([counted, rnd()], [rnd(), counted], NONE, False)
        self.assertEqual(got["auth_headers"], {"baseline": [{"authorization": 1, "cookie": 2, "any": 2}, None],
                                               "improved": [None, {"authorization": 1, "cookie": 2, "any": 2}]})

    def test_baseline_reference_uses_every_ordered_pair(self):
        a, b, c = ("GET", "/a"), ("GET", "/b"), ("GET", "/c")
        got = summarize([rnd(a, b), rnd(a), rnd(a, c)], [rnd(), rnd(), rnd()], NONE, False)["baseline_reference"]
        # (0,1) 1/2 · (0,2) 1/2 · (1,0) 1 · (1,2) 1 · (2,0) 1/2 · (2,1) 1/2. 개선 후 회차는 값에 들어가지 않는다.
        self.assertEqual(got["pair_ratios"], [0.5, 0.5, 1.0, 1.0, 0.5, 0.5])
        self.assertAlmostEqual(got["pair_mean"], 4 / 6)
        self.assertIsNotNone(got["pair_sd"])
        self.assertEqual((got["reason"], got["pairs_without_denominator"]), (None, 0))

    def test_baseline_reference_follows_domain_and_exclusions(self):
        get, post = ("GET", "/a"), ("POST", "/b")
        baseline = [rnd(get, post), rnd(get)]
        self.assertEqual(summarize(baseline, [rnd(), rnd()], NONE, False)["baseline_reference"]["pair_ratios"],
                         [0.5, 1.0])
        self.assertEqual(summarize(baseline, [rnd(), rnd()], NONE, True)["baseline_reference"]["pair_ratios"],
                         [1.0, 1.0])
        rule = Exclusions(frozenset({"POST"}), ())
        self.assertEqual(summarize(baseline, [rnd(), rnd()], rule, False)["baseline_reference"]["pair_ratios"],
                         [1.0, 1.0])

    def test_baseline_reference_needs_two_rounds(self):
        got = summarize([rnd(("GET", "/a"))], [rnd(("GET", "/a"))], NONE, False)["baseline_reference"]
        self.assertEqual(got, {"reason": "baseline_rounds_lt_2", "pair_ratios": [], "pair_mean": None,
                               "pair_sd": None, "pairs_without_denominator": 0})

    def test_baseline_reference_counts_empty_denominators(self):
        got = summarize([rnd(), rnd(("GET", "/a"))], [rnd(), rnd()], NONE, False)["baseline_reference"]
        self.assertEqual((got["pair_ratios"], got["pairs_without_denominator"], got["pair_sd"]), ([0.0], 1, None))

    def test_pairs_must_line_up(self):
        for baseline, improved in (([], []), ([rnd()], []), ([rnd()], [rnd(), rnd()])):
            with self.assertRaises(PreservationError):
                summarize(baseline, improved, NONE, False)


if __name__ == "__main__":
    unittest.main()
