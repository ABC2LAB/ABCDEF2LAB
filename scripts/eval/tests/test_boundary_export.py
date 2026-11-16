"""탐색 경계 원자료 검증(boundary_export)의 합성 시험. 대상·모델·네트워크를 쓰지 않는다."""
from __future__ import annotations

import json
import re
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import boundary_export as be
import boundary_fixture as bf
from path_mask import key_ref, mask_path
from runner_evidence import EvidenceError, PATH_PROFILE

ROOT = Path(__file__).resolve().parents[3]
# 시험 전용 키다. 어떤 실제 키도 fixture에 넣지 않는다.
KEY = bytes(range(32))
OTHER_KEY = bytes(range(1, 33))


def meter_row(seq, ingress, kind="request"):
    sent, response = ("no", "none") if kind == "rejected" else ("yes", "complete")
    return {"seq": seq, "kind": kind, "method": "GET", "status": 200, "started_at": "a", "completed_at": "b", "response_bytes": 0,
            "outcome": "completed", "sent": sent, "response": response, "attempt_id_state": "absent", "attempt_id": "", "ingress": ingress}


def meter_path(seq, raw, ingress, key=KEY, method="GET"):
    pattern, count = mask_path(raw, key)
    return {"seq": seq, "method": method, "path_pattern": pattern, "masked_segments": count, "path_profile_ref": PATH_PROFILE,
            "path_key_ref": key_ref(key), "has_authorization": False, "has_cookie": False, "ingress": ingress}


class BoundaryExportFixture(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.n = 0
        # 계측기: main 입구 1(진단), explore 입구 2(경계가 전달한 /b·/c).
        self.meter_rows = [meter_row(1, "main"), meter_row(2, "explore"), meter_row(3, "explore")]
        self.meter_paths = [meter_path(1, "/a", "main"), meter_path(2, "/b", "explore"), meter_path(3, "/c", "explore")]

    def rows(self):
        return [bf.approved(1, "/b", KEY), bf.approved(2, "/c", KEY, ingress="direct"), bf.denied(3, "out_of_scope_path"),
                bf.denied(4, "internal_address", dest="internal", ingress="browser"), bf.external(5)]

    def write(self, rows=None, **kw):
        self.n += 1
        return bf.write(self.root / f"b{self.n}", self.rows() if rows is None else rows, KEY, **kw)

    def verify(self, directory, *, meter_rows=None, meter_paths=None, task_id=bf.TASK_ID, ref=None):
        return be.verify(directory, self.meter_rows if meter_rows is None else meter_rows,
                         self.meter_paths if meter_paths is None else meter_paths,
                         path_key_ref=key_ref(KEY) if ref is None else ref, task_id=task_id)

    def assertCode(self, code, fn, *args, **kw):
        with self.assertRaises(be.BoundaryExportError) as ctx:
            fn(*args, **kw)
        self.assertEqual(str(ctx.exception), code)


class VerifiedRound(BoundaryExportFixture):
    def test_counts_come_from_rows_and_denials_stay_separate(self):
        block = self.verify(self.write())
        self.assertEqual(block["status"], "verified")
        c = block["counters"]
        self.assertEqual(c["forwarded"], {"approved": {"browser": 1, "direct": 1}, "external": {"browser": 1, "direct": 0}})
        self.assertEqual(c["denied"]["total"], 2)
        self.assertEqual({k: v for k, v in c["denied"]["by_reason"].items() if v}, {"out_of_scope_path": 1, "internal_address": 1})
        self.assertEqual(set(c["denied"]["by_reason"]), be.REASONS)
        self.assertEqual(c["denied"]["by_ingress"], {"browser": 1, "direct": 1})
        self.assertEqual((c["external_hosts"], c["forwarded_no_response"], c["rows_after_close"]), (1, 0, 0))
        self.assertEqual(block["reconciliation"], {"meter_explore_forwarded": 2, "meter_explore_rejected": 0, "boundary_only_forwarded": 0})
        self.assertEqual(block["closed_reason"], "completed")
        be.validate_block(block)
        # E② 진단 차단·반복 차단 칸은 이 요약에 없다(합치지 않는다).
        self.assertNotIn("repeat", json.dumps(block))
        self.assertNotIn("e2", json.dumps(c))

    def test_marker_counters_are_not_trusted(self):
        # 표식에는 행 수·hash만 있고 차단 수는 없다. 행을 바꾸면 hash가 어긋난다.
        directory = self.write()
        # 길이·행 수가 같은 변조다(바이트 수 검사로 잡히지 않고 hash로만 잡힌다).
        raw = (directory / be.ROWS).read_bytes().replace(b'"internal_address"', b'"metadata_address"')
        (directory / be.ROWS).write_bytes(raw)
        self.assertCode("boundary_export_invalid", self.verify, directory)

    def test_external_hosts_are_distinct_names(self):
        rows = self.rows() + [bf.external(6), bf.external(7, host="fonts.example")]
        self.assertEqual(self.verify(self.write(rows))["counters"]["external_hosts"], 2)

    def test_rows_after_close_are_counted(self):
        block = self.verify(self.write(rows_at_close=3))
        self.assertEqual(block["counters"]["rows_after_close"], 2)


class MeterReconciliation(BoundaryExportFixture):
    """수용시험 P03: (메서드, 가린 경로)별 건수 대응."""

    def test_meter_only_exploration_row_means_boundary_bypass(self):
        rows = self.meter_rows + [meter_row(4, "explore")]
        paths = self.meter_paths + [meter_path(4, "/bypass", "explore")]
        self.assertCode("boundary_meter_mismatch", self.verify, self.write(), meter_rows=rows, meter_paths=paths)

    def test_same_count_different_pattern_is_mismatch(self):
        paths = self.meter_paths[:2] + [meter_path(3, "/other", "explore")]
        self.assertCode("boundary_meter_mismatch", self.verify, self.write(), meter_paths=paths)

    def test_boundary_only_responded_row_needs_meter_rejection(self):
        rows = self.rows() + [bf.approved(6, "/late", KEY)]
        self.assertCode("boundary_meter_mismatch", self.verify, self.write(rows))
        block = self.verify(self.write(rows), meter_rows=self.meter_rows + [meter_row(4, "explore", "rejected")])
        self.assertEqual(block["reconciliation"]["boundary_only_forwarded"], 1)
        self.assertEqual(block["reconciliation"]["meter_explore_rejected"], 1)

    def test_boundary_only_no_response_row_is_allowed(self):
        rows = self.rows() + [bf.approved(6, "/hung", KEY, status=0)]
        block = self.verify(self.write(rows))
        self.assertEqual((block["counters"]["forwarded_no_response"], block["reconciliation"]["boundary_only_forwarded"]), (1, 1))

    def test_no_response_row_cannot_hide_an_unexplained_responded_row(self):
        # /b에 응답 없는 행이 하나 더 있어도, 계측기에 없는 응답 받은 /x는 봉인 뒤 거부로만 설명된다.
        rows = self.rows() + [bf.approved(6, "/b", KEY, status=0), bf.approved(7, "/x", KEY)]
        paths = self.meter_paths + [meter_path(4, "/b", "explore")]
        meter = self.meter_rows + [meter_row(4, "explore")]
        self.assertCode("boundary_meter_mismatch", self.verify, self.write(rows), meter_rows=meter, meter_paths=paths)

    def test_total_boundary_only_bound(self):
        rows = self.rows() + [bf.approved(6, "/x", KEY), bf.approved(7, "/y", KEY)]
        meter = self.meter_rows + [meter_row(4, "explore", "rejected")]
        self.assertCode("boundary_meter_mismatch", self.verify, self.write(rows), meter_rows=meter)

    def test_main_ingress_rows_do_not_enter_reconciliation(self):
        meter = self.meter_rows + [meter_row(4, "main")]
        paths = self.meter_paths + [meter_path(4, "/diag", "main")]
        self.assertEqual(self.verify(self.write(), meter_rows=meter, meter_paths=paths)["status"], "verified")


class StatusValues(BoundaryExportFixture):
    def test_session_not_opened_is_null_not_zero(self):
        block = self.verify(self.write([], session=False), meter_rows=[meter_row(1, "main")], meter_paths=[meter_path(1, "/a", "main")])
        self.assertEqual((block["status"], block["counters"], block["reconciliation"], block["binding_sha256"]), ("not_opened", None, None, None))
        be.validate_block(block)
        # 세션이 없는데 탐색 입구 트래픽이 있으면 경계 미경유다.
        self.assertCode("boundary_meter_mismatch", self.verify, self.write([], session=False))
        self.assertCode("boundary_binding_mismatch", self.verify, self.write(session=False))

    def test_truncated_round_has_no_counts(self):
        block = self.verify(self.write(marker_changes={"truncated": True, "rows_not_written": 3}))
        self.assertEqual((block["status"], block["counters"], block["reconciliation"]), ("truncated", None, None))

    def test_missing_and_incomplete(self):
        self.assertCode("boundary_export_incomplete", self.verify, self.write(marker_changes=bf.MISSING))
        self.assertCode("boundary_export_missing", self.verify, bf.write(self.root / "none", None, KEY, marker_changes=bf.MISSING))
        self.assertCode("boundary_export_missing", self.verify, bf.write(self.root / "marker-only", None, KEY))
        self.assertCode("boundary_sink_failed", self.verify, self.write(marker_changes={"log_complete": False}))
        self.assertCode("boundary_export_incomplete", self.verify, self.write(marker_changes={"sealed_by": "failure"}))
        self.assertCode("boundary_export_incomplete", self.verify, self.write(marker_changes={"inflight_at_seal": 1}))

    def test_key_and_binding(self):
        self.assertCode("boundary_path_key_mismatch", self.verify, self.write(), ref=key_ref(OTHER_KEY))
        # 계측기·러너 키는 같고 표식만 다른 키를 말하면 그것만으로 거부한다.
        self.assertCode("boundary_path_key_mismatch", self.verify, self.write(marker_changes={"path_key_ref": key_ref(OTHER_KEY)}))
        paths = self.meter_paths[:2] + [meter_path(3, "/c", "explore", key=OTHER_KEY)]
        self.assertCode("boundary_path_key_mismatch", self.verify, self.write(), meter_paths=paths)
        self.assertCode("boundary_binding_mismatch", self.verify, self.write(), task_id="8")
        # 재검증(task_id 없음)은 결속 hash로 같음을 본다.
        self.assertEqual(self.verify(self.write(), task_id=None)["binding_sha256"], self.verify(self.write())["binding_sha256"])
        other = self.verify(self.write(session_changes={"run_ref": "run-2"}))
        self.assertNotEqual(other["binding_sha256"], self.verify(self.write())["binding_sha256"])

    def test_private_directory_required(self):
        directory = self.write()
        directory.chmod(0o750)
        with self.assertRaises(EvidenceError):
            self.verify(directory)


class StrictStructure(BoundaryExportFixture):
    def test_row_violations(self):
        base = self.rows()
        # (바꿀 행 위치, 바꾼 행). 다른 행은 그대로라 그 칸 하나의 검사만 걸린다.
        bad = {
            "unknown_reason": (2, dict(base[2], reason="because")),
            "forwarded_with_reason": (0, dict(base[0], reason="risky_path")),
            "denied_with_status": (2, dict(base[2], status=200)),
            "denied_with_bytes": (2, dict(base[2], bytes=5)),
            "approved_without_path": (0, {k: v for k, v in base[0].items() if k != "path_pattern"}),
            "approved_with_host": (0, dict(base[0], external_host="cdn.example")),
            "external_without_host": (4, {k: v for k, v in base[4].items() if k != "external_host"}),
            "external_with_path": (4, dict(base[4], path_pattern="/x")),
            "forwarded_internal": (0, dict(base[0], dest_class="internal")),
            "other_session": (0, dict(base[0], session_ref="session-2")),
            "extra_field": (0, dict(base[0], query="a=1")),
            "bad_ingress": (0, dict(base[0], ingress="other")),
            "bad_status": (0, dict(base[0], status=42)),
            "bad_time": (0, dict(base[0], at="yesterday")),
        }
        for name, (index, row) in bad.items():
            with self.subTest(name=name):
                rows = list(base)
                rows[index] = row
                self.assertCode("boundary_export_invalid", self.verify, self.write(rows))
        self.assertCode("boundary_export_invalid", self.verify, self.write([base[0], dict(base[1], seq=1)] + base[2:]))

    def test_sequence_gap_is_row_loss(self):
        # 중간 차단 행(seq 3)만 빼고 표식의 행 수·바이트·hash를 그 파일에 맞춘 원자료다. 차단 수를 줄인 채 통과하면 안 된다.
        rows = [r for r in self.rows() if r["seq"] != 3]
        self.assertCode("boundary_export_incomplete", self.verify, self.write(rows))
        # 끝 행이나 첫 행을 빼도 같다(1부터 연속이어야 한다).
        self.assertCode("boundary_export_incomplete", self.verify, self.write(self.rows()[1:]))
        self.assertCode("boundary_export_incomplete", self.verify, self.write([dict(r, seq=r["seq"] + 1) for r in self.rows()]))
        # 동시 완료로 물리적 순서만 바뀐 행은 그대로 받는다.
        self.assertEqual(self.verify(self.write(list(reversed(self.rows()))))["status"], "verified")
        # 잘린 원자료는 뒤 행이 빠지는 것이 정상이라 순번을 묻지 않는다.
        block = self.verify(self.write(self.rows()[:2], marker_changes={"truncated": True, "rows_not_written": 3}))
        self.assertEqual(block["status"], "truncated")

    def test_closed_vocabulary_fields_reject_other_json_types(self):
        # 어휘 칸에 배열·객체·null·숫자가 오면 TypeError로 새지 않고 고정 오류 코드다.
        base = self.rows()
        for value in ([], {}, None, 1):
            for name, write in (("sealed_by", lambda v: self.write(marker_changes={"sealed_by": v})),
                                ("closed_reason", lambda v: self.write(session_changes={"closed_reason": v})),
                                ("dest_class", lambda v: self.write([dict(base[0], dest_class=v)] + base[1:])),
                                ("ingress", lambda v: self.write([dict(base[0], ingress=v)] + base[1:])),
                                ("decision", lambda v: self.write([dict(base[0], decision=v)] + base[1:])),
                                ("reason", lambda v: self.write(base[:2] + [dict(base[2], reason=v)] + base[3:]))):
                with self.subTest(field=name, value=value):
                    self.assertCode("boundary_export_invalid", self.verify, write(value))

    def test_byte_level_violations(self):
        good = b"".join(json.dumps(r).encode() + b"\n" for r in self.rows())
        for name, raw in {"duplicate_key": good.replace(b'"seq": 1,', b'"seq": 1, "seq": 9,'),
                          "nan": good.replace(b'"bytes": 10', b'"bytes": NaN', 1),
                          "no_final_newline": good[:-1]}.items():
            with self.subTest(name=name):
                self.assertCode("boundary_export_invalid", self.verify, self.write(raw_rows=raw))

    def test_marker_violations(self):
        for name, changes, session in (("rows_count", {"rows": 9}, None), ("bytes", {"bytes": 1}, None),
                                       ("truncation_flag", {"truncated": True}, None), ("over_cap", {"max_bytes": 10}, None),
                                       ("format", {"format": "abc2lab.explore-boundary-export/v2"}, None),
                                       ("profile", {"path_profile": "abc2lab.path-mask/v1"}, None),
                                       ("extra", {"note": "x"}, None), ("open_session", {}, {"closed_at": None}),
                                       ("close_reason", {}, {"closed_reason": "whatever"}), ("task", {}, {"task_id": "07"}),
                                       ("rows_at_close", {}, {"rows_at_close": 99})):
            with self.subTest(name=name):
                self.assertCode("boundary_export_invalid", self.verify, self.write(marker_changes=changes, session_changes=session))

    def test_block_shape(self):
        block = self.verify(self.write())
        for name, changed in (("status", block | {"status": "PASS"}), ("counts_on_truncated", block | {"status": "truncated", "reconciliation": None}),
                              ("reconciliation_on_truncated", block | {"status": "truncated", "counters": None}),
                              ("files", block | {"files": {}}), ("binding", block | {"binding_sha256": None})):
            with self.subTest(name=name):
                self.assertCode("boundary_export_invalid", be.validate_block, changed)


class GoContractDrift(unittest.TestCase):
    """Go 경계가 쓰는 칸·어휘와 같은지 소스에서 확인한다(값을 바꾸면 계약 문서도 같이 고친다)."""

    def go(self, path):
        return (ROOT / path).read_text(encoding="utf-8")

    def tags(self, source, struct):
        body = re.search(r"type " + struct + r" struct \{(.*?)\n\}", source, re.S).group(1)
        return {m.group(1) for m in re.finditer(r'json:"([a-z0-9_]+)', body)}

    def test_reasons_and_close_reasons(self):
        reasons = set(re.findall(r'Reason[A-Za-z]+\s*=\s*"([a-z_]+)"', self.go("exploreboundary/policy.go") + self.go("exploreboundary/session.go")))
        self.assertEqual(reasons, set(be.REASONS))
        close = set(re.findall(r'\s[A-Z][A-Za-z]+\s+Reason = "([a-z_]+)"', self.go("exploration/session.go")))
        self.assertEqual(close, set(be.CLOSE_REASONS))

    def test_field_names(self):
        evidence = self.go("exploreboundary/evidence.go")
        self.assertEqual(self.tags(evidence, "EvidenceMarker"), set(be._MARKER_FIELDS))
        self.assertEqual(self.tags(evidence, "EvidenceSession"), set(be._SESSION_FIELDS))
        self.assertEqual(self.tags(self.go("exploreboundary/session.go"), "Record"), set(be._ROW_REQUIRED | be._ROW_OPTIONAL))
        for name, value in (("EvidenceFormat", be.FORMAT), ("EvidenceRowsName", be.ROWS), ("EvidenceMarkerName", be.MARKER)):
            self.assertRegex(evidence, name + r'\s*=\s*"' + re.escape(value) + '"')
        for value in be.SEALED_BY:
            self.assertIn(f'"{value}"', evidence)


if __name__ == "__main__":
    unittest.main()
