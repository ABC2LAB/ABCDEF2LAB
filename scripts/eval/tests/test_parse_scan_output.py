"""parse_scan_output — 판정 레코드 계약(docs/contracts/verdict-record.md) 파싱 테스트.

fixture는 전부 손으로 만든 합성 레코드이고, 실행 중 임시 디렉터리에만 쓴다 — 실제 스캔
출력·DB 덤프는 쓰지 않고 레포에 결과물처럼 보이는 JSON도 남기지 않는다(절대규칙 2).

실행: scripts/eval 에서  python -I -m unittest discover -s tests
"""

from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from aggregate import RepeatResult, ScanVerdict, count_basis  # noqa: E402
from collect import ScanOutputError, VerdictRecord, parse_scan_output  # noqa: E402
from ground_truth import core_gt  # noqa: E402
from scan_fixtures import record as _record  # noqa: E402

# 평가 쪽이 쥐는 상관(테스트용): resource_ref → GT 챌린지 키. 없으면 out-of-list.
_CORRELATION = {
    "basket/1": "basketAccessChallenge",
    "basket/2": "basketManipulateChallenge",
    "feedback/1": "forgedFeedbackChallenge",
    "review/1": "forgedReviewChallenge",
    "export/1": "dataExportChallenge",
}


def _correlate(record: VerdictRecord) -> str | None:
    return _CORRELATION.get(record.resource_ref)


class _FixtureDir(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)

    def write(self, payload: object, raw: str | None = None) -> str:
        path = os.path.join(self._tmp.name, "records.json")
        with open(path, "w", encoding="utf-8") as f:
            f.write(raw if raw is not None else json.dumps(payload))
        return path


class TestNormal(_FixtureDir):
    def test_all_contract_verdicts_map(self) -> None:
        """계약의 oracle_value 4종이 전부 매핑된다 — allowed는 NONE(판정 없음)과 구분된 ALLOWED."""
        path = self.write([
            _record("confirmed", "basket/1"),
            _record("suspected", "basket/2"),
            _record("undecidable", "feedback/1"),
            _record("allowed", "review/1"),
        ])
        verdicts, out_of_list, *_ = parse_scan_output(path, _correlate)
        self.assertEqual(verdicts, {
            "basketAccessChallenge": ScanVerdict.CONFIRMED,
            "basketManipulateChallenge": ScanVerdict.SUSPECTED,
            "forgedFeedbackChallenge": ScanVerdict.UNDECIDABLE,
            "forgedReviewChallenge": ScanVerdict.ALLOWED,
        })
        self.assertEqual(out_of_list, ())

    def test_records_envelope_accepted(self) -> None:
        path = self.write({"records": [_record("confirmed", "basket/1")]})
        verdicts, *_ = parse_scan_output(path, _correlate)
        self.assertEqual(verdicts, {"basketAccessChallenge": ScanVerdict.CONFIRMED})

    def test_out_of_list_deduped(self) -> None:
        """correlate가 None이면 GT 밖 — 같은 자원은 한 번만 센다."""
        path = self.write([
            _record("confirmed", "admin/secret-page"),
            _record("suspected", "admin/secret-page"),
            _record("confirmed", "basket/1"),
        ])
        verdicts, out_of_list, *_ = parse_scan_output(path, _correlate)
        self.assertEqual(out_of_list, ("admin/secret-page",))
        self.assertEqual(verdicts, {"basketAccessChallenge": ScanVerdict.CONFIRMED})

    def test_strongest_verdict_wins_per_challenge(self) -> None:
        """여러 레코드면 강한 판정 우선 — 약한 판정이 덮어써 사라지지 않는다."""
        path = self.write([
            _record("undecidable", "basket/1"),
            _record("confirmed", "basket/1"),
            _record("allowed", "basket/2"),
            _record("undecidable", "basket/2"),
        ])
        verdicts, *_ = parse_scan_output(path, _correlate)
        self.assertEqual(verdicts["basketAccessChallenge"], ScanVerdict.CONFIRMED)
        self.assertEqual(verdicts["basketManipulateChallenge"], ScanVerdict.UNDECIDABLE)


class TestConflicts(_FixtureDir):
    def test_candidate_with_allowed_is_conflict(self) -> None:
        """위반_후보(confirmed·suspected)와 위반_미관찰(allowed)이 한 챌린지에 같이 오면 모순."""
        path = self.write([
            _record("confirmed", "basket/1"),
            _record("allowed", "basket/1"),
            _record("allowed", "basket/2"),
            _record("suspected", "basket/2"),
            _record("confirmed", "export/1"),
        ])
        result = parse_scan_output(path, _correlate)
        self.assertEqual(result.conflicts, ("basketAccessChallenge", "basketManipulateChallenge"))
        # 모순을 센다고 판정이 바뀌지 않는다 — 판정은 tie-break 순위 그대로.
        self.assertEqual(result.verdicts["basketAccessChallenge"], ScanVerdict.CONFIRMED)
        self.assertEqual(result.verdicts["basketManipulateChallenge"], ScanVerdict.SUSPECTED)

    def test_undecidable_mix_and_candidates_only_not_conflict(self) -> None:
        """판정_불가는 어느 쪽 주장도 아니고, 후보끼리 섞인 것도 모순이 아니다."""
        path = self.write([
            _record("undecidable", "basket/1"),
            _record("allowed", "basket/1"),
            _record("confirmed", "basket/2"),
            _record("suspected", "basket/2"),
            _record("undecidable", "basket/2"),
        ])
        self.assertEqual(parse_scan_output(path, _correlate).conflicts, ())

    def test_out_of_list_not_counted_as_conflict(self) -> None:
        path = self.write([
            _record("confirmed", "admin/secret-page"),
            _record("allowed", "admin/secret-page"),
        ])
        self.assertEqual(parse_scan_output(path, _correlate).conflicts, ())


class TestNoSilentReclassification(_FixtureDir):
    def test_undecidable_and_allowed_not_turned_into_fp_or_clean(self) -> None:
        """판단불가·allowed는 판단불가로 — 오탐/확정/미탐으로 바뀌지 않는다(§4-6).

        solved=True여도 undecidable·allowed는 UNDECIDED(확정 크레딧도, 미탐 처리도 없음).
        """
        path = self.write([
            _record("undecidable", "basket/1"),
            _record("allowed", "basket/2"),
        ])
        verdicts, out_of_list, *_ = parse_scan_output(path, _correlate)
        repeat = RepeatResult(
            condition="test",
            elapsed_seconds=1.0,
            total_requests=1,
            verdicts=verdicts,
            solved={"basketAccessChallenge": True, "basketManipulateChallenge": True},
            out_of_list_keys=out_of_list,
        )
        counts = count_basis(repeat, core_gt())
        self.assertEqual(counts.false_positive, 0)
        self.assertEqual(counts.confirmed, 0)
        # undecidable·allowed 2개 + 레코드 없는 나머지 7개(미해결·커버리지 출처 없음, §4-4) = 전부 판단불가.
        self.assertEqual(counts.undecided, len(core_gt()))
        self.assertEqual(counts.missed, 0)


class TestMalformed(_FixtureDir):
    def assert_error(self, payload: object, *fragments: str, raw: str | None = None) -> str:
        path = self.write(payload, raw=raw)
        with self.assertRaises(ScanOutputError) as ctx:
            parse_scan_output(path, _correlate)
        msg = str(ctx.exception)
        for frag in fragments:
            self.assertIn(frag, msg)
        return msg

    def test_broken_json(self) -> None:
        self.assert_error(None, "JSON 파싱 실패", raw='[{"oracle_value": "confirmed",')

    def test_top_level_not_array(self) -> None:
        self.assert_error({"oracle_value": "confirmed"}, "레코드 배열이 아님")

    def test_record_not_object(self) -> None:
        self.assert_error(["confirmed"], "객체가 아님")

    def test_missing_oracle_value(self) -> None:
        rec = _record("confirmed", "basket/1")
        del rec["oracle_value"]
        self.assert_error([rec], "oracle_value 부재/부정")

    def test_invalid_verdict_value(self) -> None:
        self.assert_error([_record("maybe", "basket/1", vuln_verdict="maybe")], "oracle_value 부재/부정", "'maybe'")

    def test_missing_vuln_verdict(self) -> None:
        rec = _record("confirmed", "basket/1")
        del rec["vuln_verdict"]
        self.assert_error([rec], "vuln_verdict 필드 부재")

    def test_allowed_with_nonnull_vuln_verdict(self) -> None:
        self.assert_error([_record("allowed", "basket/1", vuln_verdict="confirmed")], "allowed인데 vuln_verdict가 null 아님")

    def test_vuln_verdict_mismatch(self) -> None:
        self.assert_error([_record("confirmed", "basket/1", vuln_verdict=None)], "≠ oracle_value")

    def test_missing_resource_ref(self) -> None:
        rec = _record("confirmed", "basket/1")
        del rec["resource_ref"]
        self.assert_error([rec], "resource_ref 부재/빈값")

    def test_unknown_field(self) -> None:
        self.assert_error([_record("confirmed", "basket/1", challenge_key="basketAccessChallenge")], "계약에 없는 필드: 'challenge_key'")

    def test_errors_aggregated_not_first_only(self) -> None:
        """여러 레코드 오류를 전부 모은다 — 좋은 레코드만 조용히 반환하지 않는다."""
        bad_value = _record("maybe", "basket/1", vuln_verdict="maybe")
        no_ref = _record("confirmed", "basket/2")
        del no_ref["resource_ref"]
        msg = self.assert_error([_record("confirmed", "export/1"), bad_value, no_ref], "[1]", "[2]")
        self.assertIn("레코드 3개 중", msg)
        self.assertNotIn("[0]", msg)

    def test_bad_evidence_refs(self) -> None:
        self.assert_error([_record("confirmed", "basket/1", traffic_evidence_refs="t1")], "id 배열")
        for bad in (True, "", 1.5, None, {"id": 1}):
            self.assert_error([_record("confirmed", "basket/1", traffic_evidence_refs=["t1", bad])], "잘못된 traffic ref")


class TestEvidenceRefs(_FixtureDir):
    def test_refs_normalized_to_str_and_deduped(self) -> None:
        path = self.write([_record("confirmed", "basket/1", traffic_evidence_refs=[7, "7", "t2"])])
        rec, = parse_scan_output(path, _correlate).confirmed_records["basketAccessChallenge"]
        self.assertEqual(rec.traffic_evidence_refs, ("7", "t2"))

    def test_absent_or_null_refs_are_empty(self) -> None:
        path = self.write([
            _record("confirmed", "basket/1"),
            _record("confirmed", "basket/1", traffic_evidence_refs=None),
        ])
        records = parse_scan_output(path, _correlate).confirmed_records["basketAccessChallenge"]
        self.assertEqual([r.traffic_evidence_refs for r in records], [(), ()])

    def test_confirmed_records_collects_only_mapped_confirmed(self) -> None:
        # 증거 판정은 confirmed 레코드에만 — suspected·allowed·out-of-list는 안 모은다.
        path = self.write([
            _record("confirmed", "basket/1", traffic_evidence_refs=["a"]),
            _record("confirmed", "basket/1", traffic_evidence_refs=["b"]),
            _record("suspected", "basket/1"),
            _record("allowed", "basket/2"),
            _record("confirmed", "admin/secret-page"),
        ])
        confirmed = parse_scan_output(path, _correlate).confirmed_records
        self.assertEqual(list(confirmed), ["basketAccessChallenge"])
        self.assertEqual([r.traffic_evidence_refs for r in confirmed["basketAccessChallenge"]], [("a",), ("b",)])


class TestRedaction(_FixtureDir):
    def test_leaked_token_rejected_without_echoing_value(self) -> None:
        secret = "eyJhbGciOiJIUzI1NiJ9.leaked"
        path = self.write([_record("confirmed", "basket/1", token=secret)])
        with self.assertRaises(ScanOutputError) as ctx:
            parse_scan_output(path, _correlate)
        msg = str(ctx.exception)
        self.assertIn("민감 필드 'token'", msg)
        self.assertNotIn(secret, msg)  # 에러 메시지로도 값이 새지 않는다

    def test_leaked_cookie_case_insensitive(self) -> None:
        path = self.write([_record("confirmed", "basket/1", Cookie="session=abc")])
        with self.assertRaises(ScanOutputError) as ctx:
            parse_scan_output(path, _correlate)
        self.assertIn("민감 필드 'Cookie'", str(ctx.exception))
        self.assertNotIn("session=abc", str(ctx.exception))

    def test_redacted_and_null_sensitive_fields_kept(self) -> None:
        """[REDACTED]/null이면 마스킹 유지된 것 — 정상 파싱."""
        path = self.write([
            _record("confirmed", "basket/1", authorization="[REDACTED]", cookie=None),
        ])
        verdicts, *_ = parse_scan_output(path, _correlate)
        self.assertEqual(verdicts, {"basketAccessChallenge": ScanVerdict.CONFIRMED})


if __name__ == "__main__":
    unittest.main()
