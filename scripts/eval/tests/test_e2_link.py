"""러너–제품 E② 연결의 회차 값 묶기. 가짜 함수만 쓰며 제품 E²로 돌린 것이 아니다."""

from __future__ import annotations

import hashlib
import json
import os
import sys
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from e2_collect import OUTCOME_PRODUCT_FAILURE, CloseAck  # noqa: E402
from e2_control import CloseResult  # noqa: E402
from e2_link import E2Link, E2LinkPaths, evaluation_declaration  # noqa: E402
from e2_prepare import CONFIG_NAME, POLICY_NAME, E2PrepareError  # noqa: E402

FINGERPRINT = "f" * 64
NOW = datetime(2026, 11, 24, 3, 4, 5, 678000, timezone.utc)
ACK = CloseAck("close-1", 6, "9" * 64, "8" * 64)


def product(**changes):
    return {"policy_path": "", "policy_fingerprint_sha256": FINGERPRINT, "header_mode": "evaluation_caller_passthrough", "ready_timeout_seconds": 30,
            "close_ack_timeout_seconds": 5, "close_timeout_seconds": 7, "poll_seconds": 1, "exchange_timeout_seconds": 3, "verify_tool": "/bin/tool",
            "verify_timeout_seconds": 9, "policy_tool": "", "policy_tool_timeout_seconds": 11, "helper": "/usr/local/bin/helper", "helper_sha256": "a" * 64,
            "subnet": "172.30.9.0/24", "meter_ip": "172.30.9.10", "window_seconds": 7200, "approval_valid_until": "2099-01-01T00:00:00Z"} | changes


class Case(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        base = Path(temp.name)
        self.paths = E2LinkPaths(*(base / name for name in ("config", "evidence", "control", "copy")))
        for path in (self.paths.config, self.paths.evidence, self.paths.control, self.paths.copy):
            path.mkdir(mode=0o700)
        self.policy = base / "policy.json"
        self.policy.write_bytes(json.dumps({"approval_mode": "preauthorized_scope", "rules": [{"ref": "rule-1"}, {"ref": "rule-2"}],
                                            "evaluation": {"llm_backend": "api", "target_kind": "public_practice"}}).encode())
        self.tool = base / "fake-ca-tool"
        self.tool.write_text('#!/bin/sh\numask 077\nprintf cert > "$3"\nprintf key > "$5"\n')
        self.tool.chmod(0o700)

    def link(self, **changes):
        return E2Link(product(policy_path=str(self.policy), policy_tool=str(self.tool), **changes), self.paths, "eval-run-1", "model-1", "api", 8080, now=lambda: NOW)

    def closed(self, execution_ref="execution-1", state="complete", ack=ACK):
        return CloseResult(state, None, True, execution_ref, "8" * 64, ack)


class TestRunValues(Case):
    def policy_written(self):
        self.link()
        return json.loads((self.paths.config / POLICY_NAME).read_bytes())

    def test_template_receives_the_values_of_this_run(self):
        policy = self.policy_written()
        self.assertEqual((policy["valid_from"], policy["valid_until"], policy["state_dir"], policy["helper"], policy["helper_sha256"]),
                         ("2026-11-24T03:04:05Z", "2026-11-24T05:04:05Z", "/run/e2-state", "/usr/local/bin/helper", "a" * 64))
        self.assertEqual([rule["connect_ip"] for rule in policy["rules"]], ["172.30.9.10", "172.30.9.10"])
        self.assertEqual(policy["ca_cert"], {"file": "ca.pem", "sha256": hashlib.sha256(b"cert").hexdigest()})
        self.assertEqual(policy["ca_key"], {"file": "ca.key", "sha256": hashlib.sha256(b"key").hexdigest()})

    def test_ca_lives_as_long_as_the_approval(self):
        seen = self.tool.with_name("ca-args")
        self.tool.write_text(f'#!/bin/sh\numask 077\nprintf "%s\\n" "$@" > "{seen}"\nprintf cert > "$3"\nprintf key > "$5"\n')
        self.link()
        args = seen.read_text().splitlines()
        self.assertEqual(args[args.index("--expires") + 1], "2026-11-24T05:04:05Z")

    def test_engine_run_reference_is_new_and_differs_from_the_evaluation_id(self):
        policy = self.policy_written()
        self.assertRegex(policy["run_ref"], r"^e2run-[0-9a-f]{32}$")
        self.assertNotEqual(policy["run_ref"], "eval-run-1")
        self.assertEqual(policy["evaluation"]["run_ref"], policy["run_ref"])

    def test_two_runs_do_not_share_a_run_reference(self):
        first = self.policy_written()["run_ref"]
        for name in os.listdir(self.paths.config):
            os.unlink(self.paths.config / name)
        self.assertNotEqual(self.policy_written()["run_ref"], first)

    def test_window_past_the_approval_stops_before_the_ca_exists(self):
        with self.assertRaises(E2PrepareError) as caught:
            self.link(approval_valid_until="2026-11-24T05:04:04Z")
        self.assertEqual(caught.exception.code, "approval_window_exceeded")
        self.assertEqual(os.listdir(self.paths.config), [])

    def test_window_ending_exactly_at_the_approval_is_allowed(self):
        self.link(approval_valid_until="2026-11-24T05:04:05Z")

    def test_failing_ca_tool_stops_before_the_policy(self):
        self.tool.write_text("#!/bin/sh\nexit 3\n")
        with self.assertRaises(E2PrepareError) as caught:
            self.link()
        self.assertEqual(caught.exception.code, "ca_tool_failed")
        self.assertNotIn(POLICY_NAME, os.listdir(self.paths.config))


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
            E2Link(product(policy_path=str(self.policy / "absent"), policy_tool=str(self.tool)), self.paths, "eval-run-1", "model-1", "api", 8080)

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
