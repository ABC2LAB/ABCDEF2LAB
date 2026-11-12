"""제품 E② 결과 수집·감사 대조(E②–러너 연결 제안 ER04·ER15의 러너 쪽). 합성 입력과 가짜 검증 명령만 쓴다."""

from __future__ import annotations

import hashlib
import json
import os
import re
import signal
import sys
import tempfile
import traceback
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
sys.path.insert(0, str(Path(__file__).resolve().parent))

from e2_collect import (CONFIG_VIOLATIONS, MAX_CHECKPOINT_BYTES, MAX_JOURNAL_BYTES, MAX_MANIFEST_BYTES, OUTCOME_COMPLETE,  # noqa: E402
                        OUTCOME_PRODUCT_FAILURE, CloseAck, E2CollectionInvalid, collect_e2_export, subprocess_verifier)
from e2_export import E2Expectation  # noqa: E402
from test_e2_export import A1, EXPECTED, SCOPE, encode, go_block, manifest_for, normal_rows  # noqa: E402

ROOT = Path(__file__).resolve().parents[3]
MAC = "a" * 64
JOURNAL_ID = "b" * 32
SECRET_LIKE = "synthetic-value-41c2"
GO_SHAPES = {"e2proxy/audit.go": {"type JournalCheckpoint struct": "e064bb13b152ee418dfedd1a4db691c6d4fbf0a690fb17e0fab6086030f4469d", "type JournalSummary struct": "e4fea42c1d57a82f71c4afac9cb2d3befa20fc8542318efd516f06c28fbdb158",
                                  "type journalRecord struct": "1d1fa82eff65d1491edfd620d1588f0f15f999e52a82514bba5f3d7ee315c1ae", "type journalLine struct": "e76cf9c872c60b1f714c41de0acb319889933870d43c1157c413c3a738c470ac"}}


def checkpoint_for(rows=6, **changes):
    return {"Version": "e2-http-v2", "JournalID": JOURNAL_ID, "Sequence": rows, "MAC": MAC, "Scope": dict(SCOPE)} | changes


def journal_for(rows, version="e2-http-v2"):
    lines = [{"Kind": "header", "Header": {"Version": version, "JournalID": JOURNAL_ID, "CreatedAt": "2026-11-07T00:00:00Z"}, "MAC": MAC}]
    lines += [{"Kind": "event", "Record": {"Index": row["index"], "Previous": MAC, "Event": row}, "MAC": MAC} for row in rows]
    return b"".join(json.dumps(line).encode() + b"\n" for line in lines)


class Fixture(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        base = Path(self.temp.name)
        self.source = base / "export"
        self.copy = base / "copy"
        for path in (self.source, self.copy):
            path.mkdir(mode=0o700)
        self.rows = normal_rows()
        self.attempts = encode(self.rows)
        self.checkpoint = json.dumps(checkpoint_for()).encode()
        self.manifest = manifest_for(self.attempts, 6, 6, 3, audit_checkpoint_sha256=hashlib.sha256(self.checkpoint).hexdigest())
        self.ack_sequence = 6
        self.calls = []
        self.verifier_result = (0, json.dumps({"Checkpoint": checkpoint_for(), "Events": 6}).encode(), b"")
        self.publish()

    def write(self, name, data, mode=0o600):
        path = self.source / name
        if path.is_symlink() or path.exists():
            path.unlink()
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, mode)
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
        os.chmod(path, mode)

    def publish(self):
        self.write("manifest.json", json.dumps(self.manifest).encode())
        self.write("attempts.jsonl", self.attempts)
        self.write("e2.audit", journal_for(self.rows))
        self.write("e2.checkpoint", self.checkpoint)

    def verifier(self, args):
        self.calls.append(args)
        return self.verifier_result

    def current_ack(self):
        """게시된 manifest를 고정한 정상 ACK."""
        try:
            digest = hashlib.sha256((self.source / "manifest.json").read_bytes()).hexdigest()
        except OSError:
            digest = "0" * 64  # 게시 실패 시험은 ACK까지 가지 않는다.
        return CloseAck("close-1", self.ack_sequence, MAC, digest)

    def collect(self, ack="default", expected=EXPECTED):
        return collect_e2_export(self.source, self.copy, expected, self.current_ack() if ack == "default" else ack, "/private/run.key", self.verifier)

    def assert_product_failure(self, reason, **kwargs):
        result = self.collect(**kwargs)
        self.assertEqual((result.outcome, result.reason), (OUTCOME_PRODUCT_FAILURE, reason))
        self.assertIsNone(result.checkpoint)
        return result

    def assert_invalid(self, code, **kwargs):
        with self.assertRaises(E2CollectionInvalid) as caught:
            self.collect(**kwargs)
        self.assertEqual(caught.exception.code, code)
        return caught.exception


class TestComplete(Fixture):
    def test_stored_v1_run_is_verified_with_v1_rules(self):
        # C16 전에 저장한 회차는 checkpoint 판(v1)을 검증기에 그대로 넘긴다.
        self.rows = [row | {"version": "e2-http-v1"} for row in normal_rows()]
        self.attempts = encode(self.rows)
        self.checkpoint = json.dumps(checkpoint_for(Version="e2-http-v1")).encode()
        self.manifest = manifest_for(self.attempts, 6, 6, 3, audit_checkpoint_sha256=hashlib.sha256(self.checkpoint).hexdigest())
        self.verifier_result = (0, json.dumps({"Checkpoint": checkpoint_for(Version="e2-http-v1"), "Events": 6}).encode(), b"")
        self.write("manifest.json", json.dumps(self.manifest).encode())
        self.write("attempts.jsonl", self.attempts)
        self.write("e2.audit", journal_for(self.rows, "e2-http-v1"))
        self.write("e2.checkpoint", self.checkpoint)
        result = self.collect()
        self.assertEqual((result.outcome, result.reason), (OUTCOME_COMPLETE, ""))
        self.assertEqual(self.calls[0][:2], ["--journal-format", "e2-http-v1"])

    def test_unknown_checkpoint_version_is_a_product_failure(self):
        self.checkpoint = json.dumps(checkpoint_for(Version="e2-http-v3")).encode()
        self.manifest = manifest_for(self.attempts, 6, 6, 3, audit_checkpoint_sha256=hashlib.sha256(self.checkpoint).hexdigest())
        self.publish()
        self.assert_product_failure("checkpoint_invalid")
        self.assertEqual(self.calls, [])

    def test_complete_export_is_verified_on_the_private_copy(self):
        result = self.collect()
        self.assertEqual((result.outcome, result.reason), (OUTCOME_COMPLETE, ""))
        self.assertEqual(result.checkpoint, {"sequence": 6, "mac": MAC})
        self.assertEqual(len(result.export.decisions), 3)
        self.assertEqual(self.calls, [["--journal-format", "e2-http-v2", "--journal", str(self.copy / "journal.jsonl"),
                                       "--checkpoint", str(self.copy / "checkpoint.json"), "--key-file", "/private/run.key"]])
        self.assertEqual(sorted(result.sha256), ["attempts.jsonl", "checkpoint.json", "journal.jsonl", "manifest.json"])
        for name, digest in result.sha256.items():
            self.assertEqual(hashlib.sha256((self.copy / name).read_bytes()).hexdigest(), digest)
            self.assertEqual(os.stat(self.copy / name).st_mode & 0o777, 0o600)

    def test_checks_use_the_bytes_read_once_not_a_later_source_change(self):
        def swap_then_pass(args):
            # 검증 중 원본이 바뀌어도 사본과 읽어 둔 바이트로 대조한다.
            self.write("e2.audit", b"changed\n")
            return self.verifier_result
        result = collect_e2_export(self.source, self.copy, EXPECTED, self.current_ack(), "/private/run.key", swap_then_pass)
        self.assertEqual(result.outcome, OUTCOME_COMPLETE)
        self.assertEqual((self.copy / "journal.jsonl").read_bytes(), journal_for(self.rows))

    def test_empty_journal_has_an_empty_head_scope(self):
        self.rows, self.attempts, self.ack_sequence = [], b"", 0
        self.checkpoint = json.dumps(checkpoint_for(0, Scope={key: "" for key in SCOPE})).encode()
        self.manifest = manifest_for(b"", audit_checkpoint_sha256=hashlib.sha256(self.checkpoint).hexdigest())
        self.verifier_result = (0, json.dumps({"Checkpoint": json.loads(self.checkpoint), "Events": 0}).encode(), b"")
        self.publish()
        self.assertEqual(self.collect().outcome, OUTCOME_COMPLETE)

    def test_evidence_has_counts_and_digests_only(self):
        evidence = self.collect().evidence()
        self.assertEqual((evidence["format"], evidence["outcome"], evidence["export"]["attempts"]), ("abc2lab-eval-e2-collection-v1", "complete", 3))
        self.assertEqual(evidence["checkpoint"], {"sequence": 6, "mac": MAC})
        self.assertNotIn(A1, json.dumps(evidence))
        self.assertNotIn("/private/run.key", json.dumps(evidence))


class TestProductFailure(Fixture):
    """E②가 못 내거나 잘못 낸 결과는 회차를 무효로 만들지 않는다. 완료로 채우지도 않는다."""

    def test_nothing_published(self):
        (self.source / "manifest.json").unlink()
        self.assertIsNone(self.assert_product_failure("manifest_missing").export)
        self.assertEqual(self.calls, [])

    def test_export_directory_missing(self):
        for name in os.listdir(self.source):
            (self.source / name).unlink()
        self.source.rmdir()
        result = self.assert_product_failure("export_missing")
        self.assertEqual((result.export, dict(result.sha256)), (None, {}))

    def test_each_named_file_missing(self):
        for name, reason in (("attempts.jsonl", "attempts_missing"), ("e2.audit", "journal_missing"), ("e2.checkpoint", "checkpoint_missing")):
            with self.subTest(name=name):
                self.tearDown_copy()
                self.publish()
                (self.source / name).unlink()
                self.assert_product_failure(reason)

    def tearDown_copy(self):
        for name in os.listdir(self.copy):
            (self.copy / name).unlink()

    def test_incomplete_export_keeps_counts_and_makes_no_claim(self):
        self.manifest["state"] = "incomplete"
        self.publish()
        result = self.assert_product_failure("export_incomplete")
        self.assertEqual(result.export.summary()["attempts"], 3)
        self.assertEqual(self.calls, [])
        self.assertEqual(result.evidence()["checkpoint"], None)

    def test_close_ack_lost_or_different_is_not_complete(self):
        self.assert_product_failure("close_ack_missing", ack=None)
        self.assertEqual(self.calls, [])
        digest = self.current_ack().manifest_sha256
        for ack, reason in ((CloseAck("close-2", 6, MAC, digest), "close_request_mismatch"), (CloseAck("close-1", 5, MAC, digest), "close_ack_mismatch"),
                            (CloseAck("close-1", 6, "c" * 64, digest), "close_ack_mismatch"), (CloseAck("close-1", 6, MAC, "d" * 64), "close_manifest_mismatch")):
            with self.subTest(ack=ack):
                self.tearDown_copy()
                self.assert_product_failure(reason, ack=ack)
        self.assertEqual(self.calls, [])

    def test_broken_manifest_and_rows(self):
        self.write("manifest.json", b"not json")
        self.assert_product_failure("json_invalid")
        self.tearDown_copy()
        self.write("manifest.json", json.dumps(self.manifest | {"extra": 1}).encode())
        self.assert_product_failure("manifest_fields")
        self.tearDown_copy()
        self.write("manifest.json", json.dumps(self.manifest).encode())
        self.write("attempts.jsonl", self.attempts + b"\n")
        self.assertIsNone(self.assert_product_failure("attempts_digest_mismatch").export)

    def test_checkpoint_problems(self):
        for changes, reason in (({"Version": "other"}, "checkpoint_invalid"), ({"JournalID": "x"}, "checkpoint_invalid"), ({"JournalID": 7}, "checkpoint_invalid"),
                                ({"MAC": "x"}, "checkpoint_invalid"), ({"MAC": None}, "checkpoint_invalid"), ({"Sequence": "6"}, "checkpoint_invalid"),
                                ({"extra": 1}, "checkpoint_invalid"), ({"Sequence": 5}, "checkpoint_sequence_mismatch"),
                                ({"Scope": SCOPE | {"RunRef": "other"}}, "checkpoint_scope_mismatch")):
            with self.subTest(changes=changes):
                self.tearDown_copy()
                self.checkpoint = json.dumps(checkpoint_for() | changes).encode()
                self.manifest["audit_checkpoint_sha256"] = hashlib.sha256(self.checkpoint).hexdigest()
                self.publish()
                self.assert_product_failure(reason)
        self.tearDown_copy()
        self.checkpoint = b"not json"
        self.manifest["audit_checkpoint_sha256"] = hashlib.sha256(self.checkpoint).hexdigest()
        self.publish()
        self.assert_product_failure("checkpoint_invalid")
        self.tearDown_copy()
        self.manifest["audit_checkpoint_sha256"] = "d" * 64
        self.publish()
        self.assert_product_failure("checkpoint_digest_mismatch")
        self.assertEqual(self.calls, [])

    def test_audit_integrity_failure_is_the_products(self):
        self.verifier_result = (1, b"", b"audit_integrity_failed\n")
        self.assert_product_failure("audit_integrity_failed")

    def test_rows_must_equal_the_verified_journal(self):
        changed = [dict(row) for row in self.rows]
        changed[3]["reason"] = "budget_exhausted"
        for journal in (journal_for(changed), journal_for(self.rows[:-1]), b"", b"not json\n",
                        journal_for(self.rows).split(b"\n", 1)[1],
                        journal_for(self.rows).replace(b'"Kind": "event"', b'"Kind": "other"'),
                        journal_for(self.rows).replace(b'"Record": {', b'"Record": 7, "Other": {')):
            with self.subTest(journal=journal[:30]):
                self.tearDown_copy()
                self.publish()
                self.write("e2.audit", journal)
                self.assert_product_failure("projection_mismatch")

    def test_unsafe_or_oversized_files(self):
        target = self.source / "elsewhere"
        target.write_bytes(self.attempts)
        os.chmod(target, 0o600)
        (self.source / "attempts.jsonl").unlink()
        (self.source / "attempts.jsonl").symlink_to(target)
        self.assert_product_failure("export_untrusted")
        self.tearDown_copy()
        (self.source / "attempts.jsonl").unlink()
        (self.source / "attempts.jsonl").mkdir()
        self.assert_product_failure("export_untrusted")
        (self.source / "attempts.jsonl").rmdir()
        self.tearDown_copy()
        self.publish()
        os.chmod(self.source / "attempts.jsonl", 0o640)
        self.assert_product_failure("export_not_private")
        self.tearDown_copy()
        self.write("attempts.jsonl", self.attempts)
        self.write("manifest.json", b" " * (MAX_MANIFEST_BYTES + 1))
        self.assert_product_failure("export_too_large")

    def test_size_limit_boundary(self):
        data = json.dumps(self.manifest).encode()
        self.write("manifest.json", data + b" " * (MAX_MANIFEST_BYTES - len(data)))
        self.assertEqual(self.collect().outcome, OUTCOME_COMPLETE)
        self.tearDown_copy()
        self.write("manifest.json", data + b" " * (MAX_MANIFEST_BYTES + 1 - len(data)))
        self.assert_product_failure("export_too_large")

    def test_fifo_does_not_block_the_runner(self):
        def timed_out(_signum, _frame):
            raise AssertionError("FIFO에서 멈춤")
        (self.source / "attempts.jsonl").unlink()
        os.mkfifo(self.source / "attempts.jsonl", 0o600)
        previous = signal.signal(signal.SIGALRM, timed_out)
        self.addCleanup(signal.signal, signal.SIGALRM, previous)
        self.addCleanup(signal.alarm, 0)
        signal.alarm(10)
        self.assert_product_failure("export_untrusted")

    def test_symlink_loop_in_the_export_path(self):
        loop = Path(self.temp.name) / "loop"
        loop.symlink_to(loop)
        result = collect_e2_export(loop / "x", self.copy, EXPECTED, self.current_ack(), "k", self.verifier)
        self.assertEqual((result.outcome, result.reason), (OUTCOME_PRODUCT_FAILURE, "export_untrusted"))

    def test_export_directory_shape(self):
        os.chmod(self.source, 0o750)
        self.assert_product_failure("export_not_private")
        os.chmod(self.source, 0o700)
        other = Path(self.temp.name) / "link"
        other.symlink_to(self.source)
        with_link = collect_e2_export(other, self.copy, EXPECTED, self.current_ack(), "k", self.verifier)
        self.assertEqual((with_link.outcome, with_link.reason), (OUTCOME_PRODUCT_FAILURE, "export_untrusted"))
        plain = Path(self.temp.name) / "plain"
        plain.write_bytes(b"")
        as_file = collect_e2_export(plain, self.copy, EXPECTED, self.current_ack(), "k", self.verifier)
        self.assertEqual((as_file.outcome, as_file.reason), (OUTCOME_PRODUCT_FAILURE, "export_untrusted"))

    def test_evidence_records_the_failure(self):
        (self.source / "manifest.json").unlink()
        evidence = self.collect().evidence()
        self.assertEqual((evidence["outcome"], evidence["reason"], evidence["export"], evidence["checkpoint"]),
                         ("product_failure", "manifest_missing", None, None))


class TestTechnicallyInvalid(Fixture):
    """러너가 묶은 조건과 다르거나 러너 쪽 경로가 고장 나면 회차를 쓰지 않는다."""

    def test_run_binding_and_fingerprint_mismatch(self):
        for name, code in (("eval_run_id", "eval_run_id_mismatch"), ("model_tag", "model_tag_mismatch"), ("reset_generation", "reset_generation_mismatch"),
                           ("reservation_ref", "reservation_ref_mismatch"), ("engine_task_id", "engine_task_id_mismatch"),
                           ("header_mode", "header_mode_mismatch"), ("policy_fingerprint_sha256", "policy_fingerprint_mismatch")):
            with self.subTest(name=name):
                for copied in os.listdir(self.copy):
                    (self.copy / copied).unlink()
                self.write("manifest.json", json.dumps(self.manifest | {name: "other"}).encode())
                self.assert_invalid(code)
        self.assertEqual(self.calls, [])

    def test_identifier_axes_and_declaration_mismatch(self):
        evaluation = self.manifest["evaluation"]
        for changes, code in (({"scope": SCOPE | {"TaskRef": "18"}}, "task_ref_mismatch"),
                              ({"run_ref": "eval-run-1", "scope": SCOPE | {"RunRef": "eval-run-1"}, "evaluation": evaluation | {"run_ref": "eval-run-1"}},
                               "run_ref_is_eval_run"),
                              ({"evaluation": None}, "evaluation_invalid"), ({"evaluation": evaluation | {"run_ref": "other"}}, "evaluation_run_ref_mismatch"),
                              ({"evaluation": evaluation | {"llm_backend": "local"}}, "evaluation_backend_mismatch"),
                              ({"evaluation": evaluation | {"target_kind": "customer"}}, "evaluation_target_mismatch")):
            with self.subTest(code=code):
                for copied in os.listdir(self.copy):
                    (self.copy / copied).unlink()
                self.write("manifest.json", json.dumps(self.manifest | changes).encode())
                self.assert_invalid(code)
        for copied in os.listdir(self.copy):
            (self.copy / copied).unlink()
        plain = E2Expectation(**{**EXPECTED.__dict__, "header_mode": "preserve"})
        self.write("manifest.json", json.dumps(self.manifest | {"header_mode": "preserve", "evaluation": {}}).encode())
        self.assert_invalid("evaluation_unexpected", expected=plain)
        for copied in os.listdir(self.copy):
            (self.copy / copied).unlink()
        unusable = E2Expectation(**{**EXPECTED.__dict__, "llm_backend": "other"})
        self.assert_invalid("expectation_invalid", expected=unusable)

    def test_owner_mismatch_is_a_placement_violation(self):
        real = os.geteuid()
        # 호출 순서: 사본 디렉터리, 결과 디렉터리, 결과 파일.
        with mock.patch("e2_collect.os.geteuid", side_effect=[real + 1]):
            self.assert_invalid("copy_dir_not_private")
        with mock.patch("e2_collect.os.geteuid", side_effect=[real, real + 1]):
            self.assert_invalid("export_owner_mismatch")
        with mock.patch("e2_collect.os.geteuid", side_effect=[real, real, real + 1]):
            self.assert_invalid("export_owner_mismatch")

    def test_copy_directory_cannot_be_a_symlink(self):
        link = Path(self.temp.name) / "copy-link"
        link.symlink_to(self.copy)
        with self.assertRaises(E2CollectionInvalid) as caught:
            collect_e2_export(self.source, link, EXPECTED, self.current_ack(), "k", self.verifier)
        self.assertEqual(caught.exception.code, "copy_dir_unavailable")

    def test_read_error_is_a_collection_failure(self):
        real_fdopen = os.fdopen

        def failing(fd, mode):
            stream = real_fdopen(fd, mode)
            wrapper = mock.MagicMock()
            wrapper.__enter__.return_value.read.side_effect = OSError("synthetic")
            wrapper.__exit__.side_effect = lambda *_: stream.close()
            return wrapper
        with mock.patch("e2_collect.os.fdopen", side_effect=failing):
            self.assert_invalid("export_unavailable")

    def test_every_config_code_is_one_the_parser_can_raise(self):
        source = (ROOT / "scripts/eval/e2_export.py").read_text(encoding="utf-8")
        raised = set(re.findall(r'_fail\("([A-Z_]+)"\)', source))
        raised |= {name.upper() + "_MISMATCH" for name in ("eval_run_id", "model_tag", "reset_generation", "reservation_ref", "engine_task_id", "header_mode")}
        self.assertLessEqual(set(CONFIG_VIOLATIONS), raised)

    def test_format_errors_are_not_config_violations(self):
        for code in ("MANIFEST_FIELDS", "MANIFEST_FORMAT", "JSON_INVALID", "DUPLICATE_KEY", "ATTEMPTS_DIGEST_MISMATCH", "ROW_INDEX_GAP", "STATE_INVALID"):
            self.assertNotIn(code, CONFIG_VIOLATIONS)

    def test_verifier_trouble_is_the_runners(self):
        for result, code in (((1, b"", b"audit_input_invalid\n"), "audit_verifier_failed"), ((2, b"", b"usage"), "audit_verifier_failed"),
                             ((0, b"not json", b""), "audit_verifier_output_invalid"), ((0, b"{}", b""), "audit_verifier_output_invalid"),
                             ((0, json.dumps({"Checkpoint": checkpoint_for(), "Events": 5}).encode(), b""), "audit_verifier_output_invalid"),
                             ((0, json.dumps({"Checkpoint": checkpoint_for(MAC="c" * 64), "Events": 6}).encode(), b""), "audit_verifier_output_invalid")):
            with self.subTest(result=result[0:2]):
                for copied in os.listdir(self.copy):
                    (self.copy / copied).unlink()
                self.verifier_result = result
                self.assert_invalid(code)

    def test_copy_directory_must_be_private_and_fresh(self):
        os.chmod(self.copy, 0o750)
        self.assert_invalid("copy_dir_not_private")
        os.chmod(self.copy, 0o700)
        (self.copy / "manifest.json").write_bytes(b"old")
        self.assert_invalid("copy_write_failed")
        (self.copy / "manifest.json").unlink()
        missing = Path(self.temp.name) / "absent"
        with self.assertRaises(E2CollectionInvalid) as caught:
            collect_e2_export(self.source, missing, EXPECTED, self.current_ack(), "k", self.verifier)
        self.assertEqual(caught.exception.code, "copy_dir_unavailable")

    def test_unreadable_export_is_a_collection_failure(self):
        if os.geteuid() == 0:
            self.skipTest("root는 권한 오류를 재현할 수 없음")
        os.chmod(self.source / "manifest.json", 0o000)
        self.assert_invalid("export_unavailable")
        os.chmod(self.source / "manifest.json", 0o600)
        os.chmod(self.source, 0o000)
        self.addCleanup(os.chmod, self.source, 0o700)
        self.assert_invalid("export_unavailable")

    def test_subprocess_verifier_passes_no_environment_and_reports_exec_failure(self):
        script = Path(self.temp.name) / "tool.sh"
        script.write_text('#!/bin/sh\nprintf "%s" "$(env | grep -c SECRET_NAME)|$1|$(/usr/bin/readlink /proc/$$/fd/0)" ; exit 3\n')
        os.chmod(script, 0o700)
        os.environ["SECRET_NAME"] = "x"
        self.addCleanup(os.environ.pop, "SECRET_NAME")
        code, stdout, _ = subprocess_verifier(str(script), 5)(["arg"])
        self.assertEqual((code, stdout), (3, b"0|arg|/dev/null"))
        with self.assertRaises(E2CollectionInvalid) as caught:
            subprocess_verifier(str(Path(self.temp.name) / "absent"), 5)(["arg"])
        self.assertEqual(caught.exception.code, "audit_verifier_unavailable")
        script.write_text('#!/bin/sh\nexec sleep 5\n')
        with self.assertRaises(E2CollectionInvalid) as caught:
            subprocess_verifier(str(script), 0.05)(["arg"])
        self.assertEqual(caught.exception.code, "audit_verifier_unavailable")

    def test_failures_do_not_carry_values_or_chains(self):
        self.write("manifest.json", json.dumps(self.manifest | {"model_tag": SECRET_LIKE}).encode())
        exc = self.assert_invalid("model_tag_mismatch")
        self.assertNotIn(SECRET_LIKE, "".join(traceback.format_exception(exc)))
        self.assertIsNone(exc.__cause__)
        self.assertTrue(exc.__suppress_context__)


class TestGoDefinitions(unittest.TestCase):
    def test_followed_go_blocks_are_unchanged(self):
        for path, shapes in GO_SHAPES.items():
            text = (ROOT / path).read_text(encoding="utf-8")
            for start, expected in shapes.items():
                with self.subTest(start=start):
                    self.assertEqual(hashlib.sha256(go_block(text, start).encode()).hexdigest(), expected)

    def test_limits_and_codes_match_the_go_source(self):
        audit = (ROOT / "e2proxy/audit.go").read_text(encoding="utf-8")
        self.assertRegex(audit, r"journalMaxBytes\s*=\s*256 << 20")
        self.assertEqual(MAX_JOURNAL_BYTES, 256 << 20)
        self.assertEqual(MAX_CHECKPOINT_BYTES, 4096)
        self.assertEqual(MAX_MANIFEST_BYTES, 64 * 1024)
        tool = "".join(path.read_text(encoding="utf-8") for path in sorted((ROOT / "cmd/e2-audit-verify").glob("*.go")) if not path.name.endswith("_test.go"))
        self.assertIn('"audit_integrity_failed"', tool)
        self.assertRegex(tool, r"e2proxyJournalFormat\s*=\s*e2proxy\.JournalVersion")
        self.assertRegex(tool, r"e2proxyJournalFormatV1\s*=\s*e2proxy\.VersionV1")
        types = (ROOT / "e2proxy/types.go").read_text(encoding="utf-8")
        self.assertRegex(audit, r"JournalVersion\s*=\s*Version\b")
        self.assertRegex(types, r'const Version = "e2-http-v2"')
        self.assertRegex(types, r'const VersionV1 = "e2-http-v1"')
        self.assertRegex(tool, r"maxCheckpointBytes\s*=\s*4096")


if __name__ == "__main__":
    unittest.main()


class TestAttemptPaths(Fixture):
    """수용시험 회차(경로 키를 넘긴 회차)의 시도 경로 기록·완료 표식 수집. 공식 회차에 있으면 무효다."""

    def collect_paths(self, attempt_paths):
        for copied in self.copy.iterdir():
            copied.unlink()
        return collect_e2_export(self.source, self.copy, EXPECTED, self.current_ack(), "/private/run.key", self.verifier,
                                 attempt_paths=attempt_paths)

    def test_acceptance_round_copies_the_projection_into_the_signed_digests(self):
        self.write("e2-attempt-paths.jsonl", b'{"sequence":1}\n')
        self.write("e2-attempt-paths.done.json", b'{"format":"x"}\n')
        result = self.collect_paths(True)
        self.assertEqual(result.outcome, OUTCOME_COMPLETE)
        for name in ("e2-attempt-paths.jsonl", "e2-attempt-paths.done.json"):
            self.assertEqual(result.sha256[name], hashlib.sha256((self.copy / name).read_bytes()).hexdigest())
            self.assertIn(name, result.evidence()["sha256"])

    def test_acceptance_round_without_projection_stays_complete(self):
        # 마감 실패로 표식이 없거나 기록이 없으면 옮기지 않는다. E② 결과 판정은 그대로다.
        self.write("e2-attempt-paths.jsonl", b'{"sequence":1}\n')
        result = self.collect_paths(True)
        self.assertEqual(result.outcome, OUTCOME_COMPLETE)
        self.assertIn("e2-attempt-paths.jsonl", result.sha256)
        self.assertNotIn("e2-attempt-paths.done.json", result.sha256)
        os.unlink(self.source / "e2-attempt-paths.jsonl")
        result = self.collect_paths(True)
        self.assertEqual(result.outcome, OUTCOME_COMPLETE)

    def test_official_round_with_projection_is_invalid(self):
        for name in ("e2-attempt-paths.jsonl", "e2-attempt-paths.done.json"):
            with self.subTest(name):
                for other in ("e2-attempt-paths.jsonl", "e2-attempt-paths.done.json"):
                    if (self.source / other).exists():
                        (self.source / other).unlink()
                self.write(name, b"{}\n")
                with self.assertRaises(E2CollectionInvalid) as caught:
                    self.collect_paths(False)
                self.assertEqual(caught.exception.code, "attempt_paths_unexpected")

    def test_official_round_is_invalid_even_when_the_export_failed(self):
        # 결과가 불완전하거나 manifest가 없어 제품 실패로 끝나는 회차에도 공식 회차 금지가 먼저 걸린다.
        for name in ("e2-attempt-paths.jsonl", "e2-attempt-paths.done.json"):
            for failure in ("incomplete", "manifest_missing"):
                with self.subTest(name=name, failure=failure):
                    for leftover in os.listdir(self.source):
                        (self.source / leftover).unlink()
                    self.manifest["state"] = "incomplete" if failure == "incomplete" else "complete"
                    self.publish()
                    if failure == "manifest_missing":
                        (self.source / "manifest.json").unlink()
                    self.write(name, b"{}\n")
                    with self.assertRaises(E2CollectionInvalid) as caught:
                        self.collect_paths(False)
                    self.assertEqual(caught.exception.code, "attempt_paths_unexpected")
                    # 수용시험 회차라면 같은 실패는 제품 실패로 남고 경로 파일은 옮기지 않는다.
                    result = self.collect_paths(True)
                    self.assertEqual(result.outcome, OUTCOME_PRODUCT_FAILURE)
                    self.assertNotIn(name, result.sha256)

    def test_linked_projection_is_not_followed(self):
        target = Path(self.temp.name) / "elsewhere"
        target.write_bytes(b"{}\n")
        os.symlink(target, self.source / "e2-attempt-paths.jsonl")
        result = self.collect_paths(True)
        self.assertEqual(result.outcome, OUTCOME_COMPLETE)
        self.assertNotIn("e2-attempt-paths.jsonl", result.sha256)
        with self.assertRaises(E2CollectionInvalid):
            self.collect_paths(False)
