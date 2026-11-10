"""보존 시험 집계(preservation.py) 시험."""

from __future__ import annotations

import io
import json
import os
import re
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from preservation import (  # noqa: E402
    EXCLUSIONS_FORMAT, PATH_PROFILE, Exclusions, PreservationError, Round, load_exclusions, load_round, main,
    summarize,
)

NONE = Exclusions(frozenset(), ())


def engine_row(seq, method="GET", kind="request", sent="yes"):
    response = "none" if sent == "no" else "complete"
    return {"seq": seq, "kind": kind, "method": method, "status": 200, "started_at": "a", "completed_at": "b",
            "response_bytes": 1, "outcome": "completed", "sent": sent, "response": response,
            "attempt_id_state": "absent", "attempt_id": ""}


def path_row(seq, pattern, method="GET", masked=0, profile=PATH_PROFILE):
    return {"seq": seq, "method": method, "path_pattern": pattern, "masked_segments": masked,
            "path_profile_ref": profile}


def rnd(*keys, sent=None, static=0):
    return Round(frozenset(keys), len(keys) if sent is None else sent, static, 0, 0)


@unittest.skipUnless(hasattr(os, "O_NOFOLLOW") and hasattr(os, "geteuid"), "POSIX 전용")
class LoadRoundTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)

    def meter(self, engine, paths):
        directory = Path(self.tmp.name, str(len(os.listdir(self.tmp.name))))
        directory.mkdir(mode=0o700)
        os.chmod(directory, 0o700)
        for name, rows in (("engine-requests.jsonl", engine), ("engine-paths.jsonl", paths)):
            (directory / name).write_text("".join(json.dumps(row) + "\n" for row in rows), encoding="utf-8")
        return directory

    def test_counts_only_sent_rows_inside_the_budget(self):
        engine = [engine_row(1), engine_row(2, sent="uncertain"), engine_row(3, "POST"), engine_row(4, sent="no"),
                  engine_row(5), engine_row(6, kind="rejected", sent="no"), engine_row(7)]
        paths = [path_row(1, "/api/Users/{id}", masked=1), path_row(2, "/a"), path_row(3, "/api/Feedbacks", "POST"),
                 path_row(4, "/b"), path_row(5, "/main.js"), path_row(7, "/over/budget")]
        got = load_round(self.meter(engine, paths), 5)
        self.assertEqual(got.patterns, {("GET", "/api/Users/{id}"), ("POST", "/api/Feedbacks"), ("GET", "/main.js")})
        self.assertEqual((got.sent, got.static_sent, got.uncertain, got.masked_segments), (3, 1, 1, 1))

    def test_path_log_must_match_forwarded_rows(self):
        for engine, paths in (
            ([engine_row(1), engine_row(2)], [path_row(1, "/a")]),
            ([engine_row(1)], [path_row(1, "/a"), path_row(2, "/b")]),
            ([engine_row(1, kind="rejected", sent="no")], [path_row(1, "/a")]),
            ([engine_row(1, "POST")], [path_row(1, "/a", "GET")]),
        ):
            with self.assertRaises(PreservationError) as caught:
                load_round(self.meter(engine, paths), 10)
            self.assertEqual(str(caught.exception), "path_log_inconsistent")

    def test_path_rows_are_checked(self):
        for bad in (path_row(1, "/a", profile="other"), path_row(1, ""), path_row(1, "/a", masked=-1),
                    dict(path_row(1, "/a"), masked_segments="1"), dict(path_row(1, "/a"), extra=1)):
            with self.assertRaises(PreservationError):
                load_round(self.meter([engine_row(1)], [bad]), 10)

    def test_missing_path_log_is_inconsistent(self):
        directory = self.meter([engine_row(1)], [])
        (directory / "engine-paths.jsonl").unlink()
        with self.assertRaises(PreservationError):
            load_round(directory, 10)

    def test_budget_must_be_positive(self):
        directory = self.meter([engine_row(1)], [path_row(1, "/a")])
        for budget in (0, -1, True, 1.5):
            with self.assertRaises(PreservationError):
                load_round(directory, budget)

    def test_cli_prints_summary(self):
        base = self.meter([engine_row(1), engine_row(2)], [path_row(1, "/a"), path_row(2, "/b")])
        improved = self.meter([engine_row(1)], [path_row(1, "/a")])
        rules = Path(self.tmp.name, "rules.json")
        rules.write_text(json.dumps({"format": EXCLUSIONS_FORMAT, "methods": ["DELETE"], "path_prefixes": []}),
                         encoding="utf-8")
        out = io.StringIO()
        with redirect_stdout(out):
            code = main(["--baseline", str(base), "--improved", str(improved), "--budget", "10",
                         "--exclusions", str(rules)])
        self.assertEqual(code, 0)
        summary = json.loads(out.getvalue())
        self.assertEqual((summary["denominator"], summary["union_ratio"]), (2, 0.5))
        self.assertNotIn("/a", out.getvalue())
        err = io.StringIO()
        with redirect_stderr(err):
            self.assertEqual(main(["--baseline", str(base), "--improved", str(improved), "--budget", "0",
                                   "--exclusions", str(rules)]), 2)
        self.assertIn("budget_invalid", err.getvalue())


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

    def test_exclusions_leave_the_denominator(self):
        rules = load_exclusions({"format": EXCLUSIONS_FORMAT, "methods": ["DELETE"], "path_prefixes": ["/admin"]})
        baseline = [rnd(("DELETE", "/x"), ("GET", "/admin"), ("GET", "/admin/users"), ("GET", "/administer"),
                        ("GET", "/a"))]
        got = summarize(baseline, [rnd(("GET", "/a"))], rules, False)
        self.assertEqual((got["excluded_by_rules"], got["denominator"], got["union_ratio"]), (3, 2, 1 / 2))

    def test_read_only_uses_get_and_head(self):
        baseline = [rnd(("GET", "/a"), ("HEAD", "/a"), ("POST", "/a"), ("PUT", "/b"))]
        got = summarize(baseline, [rnd(("GET", "/a"), ("HEAD", "/a"))], NONE, True)
        self.assertEqual((got["denominator"], got["union_ratio"]), (2, 1.0))
        self.assertEqual(summarize(baseline, [rnd(("GET", "/a"), ("HEAD", "/a"))], NONE, False)["union_ratio"],
                         1 / 2)

    def test_static_and_api_are_split(self):
        baseline = [rnd(("GET", "/main.js"), ("GET", "/api/x"), ("GET", "/Logo.PNG"))]
        got = summarize(baseline, [rnd(("GET", "/api/x"))], NONE, False)
        self.assertEqual(got["by_class"]["api"]["union_ratio"], 1.0)
        self.assertEqual((got["by_class"]["static"]["denominator"], got["by_class"]["static"]["union_ratio"]),
                         (2, 0.0))

    def test_empty_denominator_pairs_are_counted_not_averaged(self):
        baseline = [rnd(("DELETE", "/x")), rnd(("GET", "/a"))]
        rules = Exclusions(frozenset({"DELETE"}), ())
        got = summarize(baseline, [rnd(), rnd(("GET", "/a"))], rules, False)
        self.assertEqual((got["pair_ratios"], got["pairs_without_denominator"], got["pair_sd"]), ([1.0], 1, None))
        self.assertIsNone(summarize([rnd()], [rnd()], NONE, False)["union_ratio"])

    def test_browser_cost_and_counts(self):
        got = summarize([rnd(("GET", "/a"))], [Round(frozenset(), 10, 4, 2, 3)], NONE, False)
        self.assertEqual(got["browser_cost"], [{"sent": 10, "static_sent": 4, "static_share": 0.4}])
        self.assertEqual((got["uncertain"]["improved"], got["masked_segments"]["improved"]), ([2], [3]))
        self.assertIsNone(summarize([rnd()], [Round(frozenset(), 0, 0, 0, 0)], NONE, False)["browser_cost"][0]
                          ["static_share"])

    def test_pairs_must_line_up(self):
        for baseline, improved in (([], []), ([rnd()], []), ([rnd()], [rnd(), rnd()])):
            with self.assertRaises(PreservationError):
                summarize(baseline, improved, NONE, False)


class ExclusionsTests(unittest.TestCase):
    def test_rejects_malformed_rules(self):
        good = {"format": EXCLUSIONS_FORMAT, "methods": ["DELETE"], "path_prefixes": ["/admin"]}
        self.assertEqual(load_exclusions(good).methods, {"DELETE"})
        for bad in ([], dict(good, format="x"), dict(good, extra=1), dict(good, methods="DELETE"),
                    dict(good, methods=["delete"]), dict(good, path_prefixes=["admin"]),
                    dict(good, path_prefixes=[1])):
            with self.assertRaises(PreservationError):
                load_exclusions(bad)

    def test_path_profile_matches_the_meter(self):
        go = (Path(__file__).resolve().parents[3] / "cmd/eval-meter/paths.go").read_text(encoding="utf-8")
        self.assertRegex(go, r'pathMaskProfile\s*=\s*"%s"' % re.escape(PATH_PROFILE))
        for field in ("seq", "method", "path_pattern", "masked_segments", "path_profile_ref"):
            self.assertIn('`json:"%s"`' % field, go)


if __name__ == "__main__":
    unittest.main()
