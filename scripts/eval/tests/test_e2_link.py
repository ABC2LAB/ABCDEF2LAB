"""러너–제품 E② 연결의 회차 값 묶기. 가짜 함수만 쓰며 제품 E²로 돌린 것이 아니다."""

from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from e2_collect import OUTCOME_PRODUCT_FAILURE, CloseAck  # noqa: E402
from e2_control import CloseResult  # noqa: E402
from e2_link import E2Link, E2LinkPaths, evaluation_declaration  # noqa: E402
from e2_prepare import CONFIG_NAME, E2PrepareError  # noqa: E402

FINGERPRINT = "f" * 64
ACK = CloseAck("close-1", 6, "9" * 64, "8" * 64)


def product(**changes):
    return {"policy_path": "", "policy_fingerprint_sha256": FINGERPRINT, "header_mode": "evaluation_caller_passthrough", "ready_timeout_seconds": 30,
            "close_ack_timeout_seconds": 5, "close_timeout_seconds": 7, "poll_seconds": 1, "exchange_timeout_seconds": 3, "verify_tool": "/bin/tool",
            "verify_timeout_seconds": 9} | changes


class Case(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        base = Path(temp.name)
        self.paths = E2LinkPaths(*(base / name for name in ("config", "evidence", "control", "copy")))
        for path in (self.paths.config, self.paths.evidence, self.paths.control, self.paths.copy):
            path.mkdir(mode=0o700)
        self.policy = base / "policy.json"
        self.policy.write_bytes(b"{}\n")

    def link(self, **changes):
        return E2Link(product(policy_path=str(self.policy), **changes), self.paths, "eval-run-1", "model-1", "api", 8080)

    def closed(self, execution_ref="execution-1", state="complete", ack=ACK):
        return CloseResult(state, None, True, execution_ref, "8" * 64, ack)


class TestLink(Case):
    def test_evaluation_declaration_only_for_the_passthrough_mode(self):
        self.assertEqual(evaluation_declaration("evaluation_caller_passthrough", "local"), {"llm_backend": "local", "target_kind": "public_practice"})
        self.assertIsNone(evaluation_declaration("preserve", "local"))

    def test_preparing_writes_the_config_with_the_run_values(self):
        link = self.link()
        config = json.loads((self.paths.config / CONFIG_NAME).read_bytes())
        self.assertEqual((config["eval_run_id"], config["model_tag"], config["reservation_ref"], config["listen"], config["evaluation"]),
                         ("eval-run-1", "model-1", link.prepared.reservation_ref, ":8080", {"llm_backend": "api", "target_kind": "public_practice"}))

    def test_bad_values_stop_at_preparing(self):
        with self.assertRaises(E2PrepareError):
            E2Link(product(policy_path=str(self.policy / "absent")), self.paths, "eval-run-1", "model-1", "api", 8080)

    def test_channel_uses_the_run_and_timeouts(self):
        link = self.link()
        self.assertEqual((link.channel.eval_run_id, link.channel.reservation_ref, link.channel.timeout_seconds, link.channel.directory),
                         ("eval-run-1", link.prepared.reservation_ref, 3, self.paths.control))
        self.assertEqual(link.waiting.poll_seconds, 1)

    def test_service_ready_compares_with_what_was_written_and_frozen(self):
        link = self.link()
        with mock.patch("e2_link.wait_service_ready", return_value={"state": "service_ready"}) as wait:
            self.assertEqual(link.service_ready(), {"state": "service_ready"})
        channel, expected, waiting, timeout = wait.call_args.args
        self.assertIs(channel, link.channel)
        self.assertIs(waiting, link.waiting)
        self.assertEqual((expected.config_sha256, expected.header_mode, expected.policy_fingerprint_sha256, timeout),
                         (link.prepared.config_sha256, "evaluation_caller_passthrough", FINGERPRINT, 30))
        self.assertEqual(expected.evaluation, {"llm_backend": "api", "target_kind": "public_practice"})

    def test_bind_goes_to_the_channel(self):
        link = self.link()
        with mock.patch.object(link.channel, "bind") as bind:
            link.bind("target-1")
        bind.assert_called_once_with("target-1")

    def test_close_uses_a_new_reference_and_the_configured_timeouts(self):
        link = self.link()
        with mock.patch("e2_link.close_execution") as close:
            link.close()
            link.close()
        first, second = close.call_args_list
        self.assertTrue(first.args[1].startswith("close-"))
        self.assertNotEqual(first.args[1], second.args[1])
        self.assertEqual((first.args[0], first.args[2], first.args[3], first.args[4]), (link.channel, link.waiting, 5, 7))


class TestCollect(Case):
    def test_collect_binds_every_value_to_this_run(self):
        link = self.link()
        with mock.patch("e2_link.collect_e2_export", return_value="result") as collect, mock.patch("e2_link.subprocess_verifier", return_value="tool") as verifier:
            self.assertEqual(link.collect(self.closed(), "17", "target-1"), "result")
        verifier.assert_called_once_with("/bin/tool", 9)
        source, copy, expected, ack, key, run = collect.call_args.args
        self.assertEqual((source, copy, ack, key, run), (self.paths.evidence / "execution-1", self.paths.copy, ACK, str(link.prepared.key_path), "tool"))
        self.assertEqual((expected.eval_run_id, expected.model_tag, expected.reset_generation, expected.reservation_ref, expected.engine_task_id,
                          expected.policy_fingerprint_sha256, expected.header_mode, expected.llm_backend),
                         ("eval-run-1", "model-1", "target-1", link.prepared.reservation_ref, "17", FINGERPRINT, "evaluation_caller_passthrough", "api"))

    def test_execution_reference_cannot_leave_the_evidence_directory(self):
        link = self.link()
        for bad in ("../x", "a/b", "/abs", "", ".hidden", None, 7, "x" * 129):
            with self.subTest(bad=bad), mock.patch("e2_link.collect_e2_export") as collect:
                result = link.collect(self.closed(bad), "17", "target-1")
                collect.assert_not_called()
                self.assertEqual((result.outcome, result.reason, result.export, result.checkpoint), (OUTCOME_PRODUCT_FAILURE, "execution_ref_invalid", None, None))

    def test_longest_directory_name_is_taken(self):
        link = self.link()
        with mock.patch("e2_link.collect_e2_export", return_value="result"), mock.patch("e2_link.subprocess_verifier"):
            self.assertEqual(link.collect(self.closed("x" * 128), "17", "target-1"), "result")


if __name__ == "__main__":
    unittest.main()
