"""E③ 실제 검증 시나리오 집계(e3_vampi_tally.py). 합성 결과만 쓴다."""

from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from e3_vampi_tally import (  # noqa: E402
    EXIT_FALSE_CONFIRMED, EXIT_MISMATCH, EXIT_OK, TallyError, exit_code, false_confirmed, load_manifest, main, tally,
)

MANIFEST = {
    "version": "e3-acceptance-scenarios/v1",
    "declaration": "declaration.json",
    "scenarios": [
        {"id": "S01", "name": "confirmed", "fake": True, "real": "blocked_id_source",
         "expect": {"status": "completed", "reason": "", "verdicts": {"book": {"value": "confirmed", "reasons": []}}}},
        {"id": "S03", "name": "declaration_missing", "fake": True, "real": "runnable",
         "expect": {"status": "disabled", "reason": "declaration_missing", "requests_zero": True, "verdicts": {}}},
        {"id": "S05", "name": "identity_same_subject", "fake": True, "real": "runnable",
         "expect": {"status": "completed", "reason": "",
                    "verdicts": {"book": {"value": "undecidable", "reasons": ["product_subjects_identical"]}}}},
        {"id": "S13", "name": "journal_mismatch", "fake": True, "real": "fake_only",
         "expect": {"status": "confirmation_integrity_failed", "reason": "journal_event_mismatch", "verdicts": {}}},
    ],
}


def verdict(typ, value, product=(), oracle=(), refs=()):
    return {"resource_type": typ, "oracle_value": value, "product_reason_codes": list(product),
            "oracle_reason_codes": list(oracle), "candidate_refs": list(refs)}


def result(status, reason="", verdicts=(), candidates=(), requests=0):
    return {"version": "e3-confirmation-run/v1", "status": status, "reason": reason, "verdicts": list(verdicts),
            "candidates": list(candidates), "requests": requests}


GOOD = {
    "S01": result("completed", verdicts=[verdict("book", "confirmed", refs=["cand-1"])],
                  candidates=[{"candidate_ref": "cand-1", "level": "confirmed"}], requests=5),
    "S03": result("disabled", "declaration_missing"),
    "S05": result("completed", verdicts=[verdict("book", "undecidable", product=["product_subjects_identical"])], requests=2),
    "S13": result("confirmation_integrity_failed", "journal_event_mismatch", requests=3),
}


class TallyTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.manifest_path = self.root / "scenarios.json"
        self.manifest_path.write_text(json.dumps(MANIFEST))
        self.results = self.root / "results"

    def tearDown(self):
        self.tmp.cleanup()

    def write(self, results: dict):
        for sid, r in results.items():
            d = self.results / sid
            d.mkdir(parents=True, exist_ok=True)
            (d / "e3-confirmation.json").write_text(r if isinstance(r, str) else json.dumps(r))

    def run_tally(self, mode="fake"):
        return tally(load_manifest(self.manifest_path), self.results, mode)

    def test_all_pass(self):
        self.write(GOOD)
        s = self.run_tally()
        self.assertEqual(s["totals"]["pass"], 4)
        self.assertEqual(s["totals"]["false_confirmed"], 0)
        self.assertEqual(exit_code(s), EXIT_OK)

    def test_false_confirmed_verdict(self):
        bad = dict(GOOD)
        bad["S05"] = result("completed", verdicts=[verdict("book", "confirmed")], requests=5)
        self.write(bad)
        s = self.run_tally()
        row = next(r for r in s["scenarios"] if r["id"] == "S05")
        self.assertEqual(row["false_confirmed"], ["verdict:book"])
        self.assertEqual(row["outcome"], "fail")
        self.assertEqual(exit_code(s), EXIT_FALSE_CONFIRMED)

    def test_false_confirmed_unexpected_type_and_orphan_candidate(self):
        self.assertEqual(false_confirmed(result("completed", verdicts=[verdict("note", "confirmed")]), {}), ["verdict:note"])
        # 확정 판정이 가리키지 않는 확정 후보는 거짓이다(E③ 계약 §9).
        orphan = result("completed", verdicts=[verdict("book", "confirmed", refs=["cand-1"])],
                        candidates=[{"candidate_ref": "cand-2", "level": "confirmed"}])
        self.assertEqual(false_confirmed(orphan, {"book": {"value": "confirmed"}}), ["candidate:cand-2"])
        disabled = result("disabled", "declaration_missing", candidates=[{"candidate_ref": "cand-1", "level": "confirmed"}])
        self.assertEqual(false_confirmed(disabled, {}), ["candidate:cand-1"])

    def test_mismatch_and_missing(self):
        bad = dict(GOOD)
        bad["S03"] = result("disabled", "test_accounts_unavailable")
        del bad["S13"]
        self.write(bad)
        s = self.run_tally()
        rows = {r["id"]: r for r in s["scenarios"]}
        self.assertEqual(rows["S03"]["outcome"], "fail")
        self.assertEqual(rows["S13"]["outcome"], "missing")
        self.assertEqual(exit_code(s), EXIT_MISMATCH)

    def test_requests_zero_and_missing_reason(self):
        bad = dict(GOOD)
        bad["S03"] = result("disabled", "declaration_missing", requests=1)
        bad["S05"] = result("completed", verdicts=[verdict("book", "undecidable", product=["product_identity_unverified"])])
        self.write(bad)
        rows = {r["id"]: r for r in self.run_tally()["scenarios"]}
        self.assertIn("requests 1 != 0", rows["S03"]["problems"])
        self.assertEqual(rows["S05"]["outcome"], "fail")

    def test_unexpected_verdict_rows(self):
        bad = dict(GOOD)
        bad["S03"] = result("disabled", "declaration_missing", verdicts=[verdict("book", "undecidable")])
        self.write(bad)
        rows = {r["id"]: r for r in self.run_tally()["scenarios"]}
        self.assertIn("기대하지 않은 판정 행", rows["S03"]["problems"])

    def test_unreadable_results_are_not_pass(self):
        bad = dict(GOOD)
        bad["S03"] = "{not json"
        bad["S05"] = json.dumps(result("completed")).replace('"version": "e3-confirmation-run/v1"', '"version": "v0"')
        bad["S13"] = '{"version":"e3-confirmation-run/v1","status":"x","status":"y","verdicts":[]}'
        self.write(bad)
        rows = {r["id"]: r for r in self.run_tally()["scenarios"]}
        self.assertEqual([rows[i]["outcome"] for i in ("S03", "S05", "S13")], ["missing"] * 3)

    def test_real_mode(self):
        # fake_only는 건너뛰고, blocked_*는 결과가 있어도 거짓 CONFIRMED만 센다.
        bad = dict(GOOD)
        del bad["S13"]
        bad["S01"] = result("completed", verdicts=[verdict("book", "undecidable", product=["product_create_failed"])])
        self.write(bad)
        s = self.run_tally("real")
        rows = {r["id"]: r for r in s["scenarios"]}
        self.assertEqual(rows["S13"]["outcome"], "skipped")
        self.assertEqual(rows["S01"]["outcome"], "blocked")
        self.assertEqual(exit_code(s), EXIT_OK)
        bad["S01"] = result("completed", verdicts=[verdict("note", "confirmed")])
        self.write(bad)
        s = self.run_tally("real")
        self.assertEqual(s["totals"]["false_confirmed"], 1)
        self.assertEqual(exit_code(s), EXIT_FALSE_CONFIRMED)

    def test_manifest_checks(self):
        for edit in (lambda m: m.update(version="v0"),
                     lambda m: m["scenarios"].append(dict(m["scenarios"][0])),
                     lambda m: m["scenarios"][0].update(real="sometimes"),
                     lambda m: m["scenarios"][0]["expect"].pop("reason")):
            m = json.loads(json.dumps(MANIFEST))
            edit(m)
            self.manifest_path.write_text(json.dumps(m))
            with self.assertRaises(TallyError):
                load_manifest(self.manifest_path)

    def test_main_exit_codes(self):
        self.write(GOOD)
        args = ["--manifest", str(self.manifest_path), "--results", str(self.results), "--mode", "fake"]
        self.assertEqual(main(args), EXIT_OK)
        self.manifest_path.write_text("{}")
        self.assertEqual(main(args), EXIT_MISMATCH)


if __name__ == "__main__":
    unittest.main()
