"""제품 E② 회차 준비(제어 채널 제안 §4). 임시 디렉터리만 쓰며 제품 E²로 읽어 본 것이 아니다."""

from __future__ import annotations

import hashlib
import json
import os
import re
import stat
import sys
import tempfile
import traceback
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from e2_prepare import (AUDIT_KEY_BYTES, CONFIG_NAME, KEY_NAME, MAX_POLICY_BYTES, POLICY_NAME, E2PrepareError, E2RunValues,  # noqa: E402
                        new_reference, prepare_e2_config)

ROOT = Path(__file__).resolve().parents[3]
OVERLAY = ROOT / "deploy/eval/compose.e2-product.yaml"
EVALUATION = {"llm_backend": "api", "target_kind": "public_practice"}
VALUES = E2RunValues(eval_run_id="eval-run-1", model_tag="qwen/qwen3.5-35b-a3b", header_mode="evaluation_caller_passthrough",
                     evaluation=EVALUATION, port=8080)
POLICY = b'{"approval_mode": "preauthorized_scope", "rules": ["synthetic"]}\n'


class Case(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.temp = Path(temp.name)
        self.directory = self.temp / "config"
        self.directory.mkdir(mode=0o700)
        self.policy = self.temp / "policy-source.json"
        self.policy.write_bytes(POLICY)

    def prepare(self, values=VALUES, policy=None, directory=None):
        return prepare_e2_config(directory or self.directory, values, policy or self.policy)

    def rejected(self, code, **kwargs):
        with self.assertRaises(E2PrepareError) as caught:
            self.prepare(**kwargs)
        self.assertEqual(caught.exception.code, code)
        self.assertIsNone(caught.exception.__cause__)
        self.assertTrue(caught.exception.__suppress_context__)
        return caught.exception

    def nothing_written(self):
        self.assertEqual(os.listdir(self.directory), [])


class TestPrepare(Case):
    def test_config_has_the_closed_fields_and_digest_of_its_exact_bytes(self):
        prepared = self.prepare()
        data = (self.directory / CONFIG_NAME).read_bytes()
        self.assertEqual(prepared.config_sha256, hashlib.sha256(data).hexdigest())
        config = json.loads(data)
        self.assertEqual(config, {"format": "abc2lab.e2-runner-config/v1", "eval_run_id": "eval-run-1", "reservation_ref": prepared.reservation_ref,
                                  "model_tag": "qwen/qwen3.5-35b-a3b", "header_mode": "evaluation_caller_passthrough", "evaluation": EVALUATION,
                                  "policy_file": "policy.json", "audit_key_file": "audit.key", "listen": ":8080",
                                  "upstream": "http://eval-meter-upstream:8080", "evidence_dir": "/evidence", "control_dir": "/run/e2-control"})
        self.assertTrue(prepared.reservation_ref.startswith("reservation-"))

    def test_policy_copy_gains_only_this_runs_key_pin(self):
        prepared = self.prepare()
        written = (self.directory / POLICY_NAME).read_bytes()
        document = json.loads(written)
        pin = document.pop("audit_key")
        self.assertEqual(document, json.loads(POLICY))
        self.assertEqual(pin, {"file": KEY_NAME, "sha256": hashlib.sha256(prepared.key_path.read_bytes()).hexdigest()})
        self.assertEqual(prepared.policy_sha256, hashlib.sha256(written).hexdigest())

    def test_policy_pin_changes_with_the_key(self):
        other = self.temp / "other-pin"
        other.mkdir(mode=0o700)
        self.prepare()
        self.prepare(directory=other)
        first, second = (json.loads((d / POLICY_NAME).read_bytes())["audit_key"]["sha256"] for d in (self.directory, other))
        self.assertNotEqual(first, second)

    def test_size_limit_matches_the_go_loader(self):
        source = (ROOT / "e2config/config.go").read_text(encoding="utf-8")
        match = re.search(r"const MaxPolicyBytes = (\d+) << (\d+)", source)
        self.assertIsNotNone(match)
        self.assertEqual(MAX_POLICY_BYTES, int(match.group(1)) << int(match.group(2)))

    def test_pin_cannot_push_the_policy_over_the_limit(self):
        head, tail = b'{"approval_mode": "preauthorized_scope", "x": "', b'"}'
        source = self.temp / "at-limit.json"
        source.write_bytes(head + b"a" * (MAX_POLICY_BYTES - len(head) - len(tail)) + tail)
        self.rejected("policy_too_large", policy=source)
        self.nothing_written()

    def test_files_are_private(self):
        self.prepare()
        self.assertEqual(sorted(os.listdir(self.directory)), sorted([CONFIG_NAME, KEY_NAME, POLICY_NAME]))
        for name in (CONFIG_NAME, KEY_NAME, POLICY_NAME):
            self.assertEqual(stat.S_IMODE(os.stat(self.directory / name).st_mode), 0o600, name)

    def test_key_is_new_each_run_and_never_leaves_its_file(self):
        first = self.prepare()
        other = self.temp / "other"
        other.mkdir(mode=0o700)
        second = self.prepare(directory=other)
        key = first.key_path.read_bytes()
        self.assertEqual(first.key_path, self.directory / KEY_NAME)
        self.assertEqual(len(key), AUDIT_KEY_BYTES)
        self.assertNotEqual(key, second.key_path.read_bytes())
        self.assertNotEqual(first.reservation_ref, second.reservation_ref)
        config = (self.directory / CONFIG_NAME).read_bytes()
        self.assertNotIn(key, config)
        self.assertNotIn(key.hex().encode(), config)
        self.assertNotIn(key.hex(), repr(first))

    def test_other_modes_have_no_evaluation(self):
        self.prepare(values=E2RunValues("eval-run-1", "model-1", "preserve", None, 8080))
        self.assertIsNone(json.loads((self.directory / CONFIG_NAME).read_bytes())["evaluation"])

    def test_port_boundaries(self):
        for port in (1, 65535):
            directory = self.temp / f"port-{port}"
            directory.mkdir(mode=0o700)
            self.prepare(values=E2RunValues("eval-run-1", "model-1", "preserve", None, port), directory=directory)
            self.assertEqual(json.loads((directory / CONFIG_NAME).read_bytes())["listen"], f":{port}")

    def test_preparing_twice_does_not_overwrite(self):
        self.prepare()
        key = (self.directory / KEY_NAME).read_bytes()
        self.rejected("already_prepared")
        self.assertEqual((self.directory / KEY_NAME).read_bytes(), key)

    def test_references_are_opaque_and_unique(self):
        self.assertTrue(new_reference("close").startswith("close-"))
        self.assertNotEqual(new_reference("close"), new_reference("close"))
        self.assertEqual(len(new_reference("close")), len("close-") + 32)


class TestRejected(Case):
    def test_bad_values_write_nothing(self):
        cases = (({"eval_run_id": ""}, "run_values_invalid"), ({"eval_run_id": "http://x"}, "run_values_invalid"), ({"model_tag": 7}, "run_values_invalid"),
                 ({"model_tag": "a b"}, "run_values_invalid"), ({"header_mode": "other"}, "header_mode_invalid"), ({"header_mode": []}, "header_mode_invalid"),
                 ({"port": 0}, "port_invalid"), ({"port": 65536}, "port_invalid"), ({"port": "8080"}, "port_invalid"), ({"port": True}, "port_invalid"),
                 ({"evaluation": None}, "evaluation_invalid"), ({"evaluation": {}}, "evaluation_invalid"), ({"evaluation": "api"}, "evaluation_invalid"), ({"evaluation": 7}, "evaluation_invalid"),
                 ({"evaluation": EVALUATION | {"run_ref": "x"}}, "evaluation_invalid"), ({"evaluation": EVALUATION | {"llm_backend": "other"}}, "evaluation_invalid"),
                 ({"evaluation": EVALUATION | {"llm_backend": []}}, "evaluation_invalid"), ({"evaluation": EVALUATION | {"target_kind": "customer"}}, "evaluation_invalid"),
                 ({"header_mode": "preserve"}, "evaluation_unexpected"), ({"header_mode": "preserve", "evaluation": {}}, "evaluation_unexpected"))
        for changes, code in cases:
            with self.subTest(changes=changes):
                self.rejected(code, values=E2RunValues(**{**VALUES.__dict__, **changes}))
                self.nothing_written()

    def test_policy_source_must_be_one_small_regular_file(self):
        link = self.temp / "link.json"
        link.symlink_to(self.policy)
        self.rejected("policy_unavailable", policy=link)
        self.rejected("policy_unavailable", policy=self.temp / "absent.json")
        self.rejected("policy_not_regular", policy=self.temp)
        fifo = self.temp / "fifo"
        os.mkfifo(fifo)
        self.rejected("policy_not_regular", policy=fifo)
        large = self.temp / "large.json"
        large.write_bytes(b" " * (MAX_POLICY_BYTES + 1))
        self.rejected("policy_too_large", policy=large)
        self.nothing_written()

    def test_policy_must_preauthorize_requests(self):
        for name, data, code in (("confirm", b'{"approval_mode": "confirm_every_request"}', "policy_approval_mode"),
                                 ("missing", b'{"rules": []}', "policy_approval_mode"),
                                 ("not-string", b'{"approval_mode": 1}', "policy_approval_mode"),
                                 ("array", b'["preauthorized_scope"]', "policy_approval_mode"),
                                 ("not-json", b"not json", "policy_invalid"),
                                 ("not-utf8", b"\xff", "policy_invalid"),
                                 ("deep", b"[" * 100000, "policy_invalid"),
                                 ("preset", b'{"approval_mode": "preauthorized_scope", "audit_key": {}}', "policy_audit_key_preset"),
                                 ("duplicate", b'{"approval_mode": "preauthorized_scope", "approval_mode": "preauthorized_scope"}', "policy_invalid"),
                                 ("nan", b'{"approval_mode": "preauthorized_scope", "x": NaN}', "policy_invalid"),
                                 ("surrogate", b'{"approval_mode": "preauthorized_scope", "x": "\\ud800"}', "policy_invalid")):
            with self.subTest(name):
                source = self.temp / f"{name}.json"
                source.write_bytes(data)
                self.rejected(code, policy=source)

    def test_largest_policy_is_accepted(self):
        exact = self.temp / "exact.json"
        exact.write_bytes(POLICY + b" " * (MAX_POLICY_BYTES - len(POLICY)))
        self.prepare(policy=exact)
        self.assertLessEqual(len((self.directory / POLICY_NAME).read_bytes()), MAX_POLICY_BYTES)

    def test_directory_must_be_private_and_not_a_link(self):
        link = self.temp / "link-dir"
        link.symlink_to(self.directory)
        self.rejected("directory_unavailable", directory=link)
        self.rejected("directory_unavailable", directory=self.temp / "absent")
        os.chmod(self.directory, 0o750)
        self.rejected("directory_not_private")
        os.chmod(self.directory, 0o700)
        with mock.patch("e2_prepare.os.geteuid", return_value=os.geteuid() + 1):
            self.rejected("directory_not_private")
        self.nothing_written()

    def test_write_and_stat_failures_have_fixed_codes(self):
        # 파일 쓰기와 마지막 디렉터리 동기화를 따로 본다.
        with mock.patch("e2_prepare.os.fdopen", side_effect=OSError("synthetic")):
            self.rejected("write_failed")
        for name in os.listdir(self.directory):
            (self.directory / name).unlink()
        with mock.patch("e2_prepare.os.fsync", side_effect=[None, None, None, OSError("synthetic")]):
            self.rejected("write_failed")
        for name in os.listdir(self.directory):
            (self.directory / name).unlink()
        with mock.patch("e2_prepare.os.fstat", side_effect=OSError("synthetic")):
            exc = self.rejected("policy_unavailable")
        self.assertNotIn("synthetic", "".join(traceback.format_exception(exc)))

    def test_descriptors_are_released(self):
        before = len(os.listdir("/proc/self/fd"))
        self.rejected("policy_not_regular", policy=self.temp)
        os.chmod(self.directory, 0o750)
        self.rejected("directory_not_private")
        os.chmod(self.directory, 0o700)
        with mock.patch("e2_prepare.os.fstat", side_effect=[os.stat(self.policy), OSError("synthetic")]):
            self.rejected("directory_unavailable")
        self.prepare()
        self.rejected("already_prepared")
        self.assertEqual(len(os.listdir("/proc/self/fd")), before)


class TestOverlay(unittest.TestCase):
    def setUp(self):
        self.text = "\n".join(line for line in OVERLAY.read_text(encoding="utf-8").splitlines() if not line.lstrip().startswith("#"))

    def test_mounts_and_hardening(self):
        for expected in ("${EVAL_E2_CONFIG_DIR:?}:/run/e2-config:ro", "${EVAL_E2_EVIDENCE_DIR:?}:/evidence", "${EVAL_E2_CONTROL_DIR:?}:/run/e2-control",
                         "${EVAL_E2_STATE_DIR:?}:/run/e2-state", "ipv4_address: ${EVAL_E2_METER_IP:?}", "- subnet: ${EVAL_E2_SUBNET:?}",
                         "image: ${EVAL_E2_IMAGE:?}", 'user: "${EVAL_RUNNER_UID:?}:${EVAL_RUNNER_GID:?}"', "read_only: true", "cap_drop: [ALL]",
                         "no-new-privileges:true", "-config=/run/e2-config/" + CONFIG_NAME):
            self.assertIn(expected, self.text)

    def test_no_signing_key_socket_or_publish(self):
        for forbidden in ("docker.sock", "runner.key", "ports:", "RUNNER_KEY", "EVAL_METER_IMAGE", "-role=e2-stub"):
            self.assertNotIn(forbidden, self.text)

    def test_network_placement_matches_the_stub(self):
        stub = (ROOT / "deploy/eval/compose.e2-stub.yaml").read_text(encoding="utf-8")
        for part in ("aliases: [eval-meter]", "aliases: [eval-meter-upstream]", "networks: !override", "internal: true", "e2_net: {}", "target_net: {}"):
            self.assertIn(part, self.text)
            self.assertIn(part, stub)

    def test_paths_match_the_config_file(self):
        self.assertIn(":/evidence", self.text)
        self.assertIn(":/run/e2-control", self.text)


if __name__ == "__main__":
    unittest.main()
