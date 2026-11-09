"""의미 템플릿에 회차 값을 채우는 생산자(docs/contracts/e2-evaluation-policy-format.md). 임시 디렉터리와 가짜 도구만 쓴다."""

from __future__ import annotations

import contextlib
import hashlib
import io
import json
import os
import stat
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from e2_policy_fingerprint import expected_fingerprint, main as fingerprint_main  # noqa: E402
from e2_prepare import (E2PrepareError, E2RunResources, E2RunValues, approval_window, issue_run_ca, prepare_e2_config,  # noqa: E402
                        render_policy)

RESOURCES = E2RunResources("run-1", "2026-11-24T00:00:00Z", "2026-11-24T02:00:00Z", "/run/e2-state", "/usr/local/bin/helper", "a" * 64, "b" * 64, "c" * 64, "172.30.9.10")
TEMPLATE = {"approval_mode": "preauthorized_scope", "max_requests": 20, "limits": {"max_calls": 5}, "evaluation": {"llm_backend": "api", "target_kind": "public_practice"},
            "rules": [{"ref": "rule-1", "origin": "http://eval-meter:8080"}, {"ref": "rule-2", "origin": "http://eval-meter:8080"}]}


class Base(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.base = Path(temp.name)
        self.prep = self.base / "prep"
        self.prep.mkdir(mode=0o700)

    def code(self, call, *args, **kwargs) -> str:
        with self.assertRaises(E2PrepareError) as caught:
            call(*args, **kwargs)
        return caught.exception.code

    def template(self, document=None) -> Path:
        path = self.base / "template.json"
        path.write_text(json.dumps(TEMPLATE if document is None else document), encoding="utf-8")
        return path

    def tool(self, body: str) -> Path:
        path = self.base / "tool"
        path.write_text("#!/bin/sh\numask 077\n" + body + "\n", encoding="utf-8")
        path.chmod(0o700)
        return path


class TestRender(Base):
    def rendered(self, document=None) -> dict:
        return json.loads(render_policy(self.template(document), RESOURCES))

    def test_fills_the_run_values(self):
        policy = self.rendered()
        self.assertEqual({name: policy[name] for name in ("run_ref", "valid_from", "valid_until", "state_dir", "helper", "helper_sha256")},
                         {"run_ref": "run-1", "valid_from": "2026-11-24T00:00:00Z", "valid_until": "2026-11-24T02:00:00Z", "state_dir": "/run/e2-state",
                          "helper": "/usr/local/bin/helper", "helper_sha256": "a" * 64})
        self.assertEqual((policy["ca_cert"], policy["ca_key"]), ({"file": "ca.pem", "sha256": "b" * 64}, {"file": "ca.key", "sha256": "c" * 64}))

    def test_every_rule_gets_the_pinned_address_and_keeps_its_fields(self):
        self.assertEqual(self.rendered()["rules"], [{"ref": "rule-1", "origin": "http://eval-meter:8080", "connect_ip": "172.30.9.10"},
                                                    {"ref": "rule-2", "origin": "http://eval-meter:8080", "connect_ip": "172.30.9.10"}])

    def test_reviewed_fields_are_not_changed(self):
        policy = self.rendered()
        self.assertEqual((policy["approval_mode"], policy["max_requests"], policy["limits"]), ("preauthorized_scope", 20, {"max_calls": 5}))

    def test_evaluation_gets_the_same_run_reference(self):
        self.assertEqual(self.rendered()["evaluation"], {"llm_backend": "api", "target_kind": "public_practice", "run_ref": "run-1"})

    def test_null_evaluation_stays_null(self):
        self.assertIsNone(self.rendered(TEMPLATE | {"evaluation": None})["evaluation"])

    def test_template_cannot_already_hold_a_run_value(self):
        for name in ("run_ref", "valid_from", "valid_until", "state_dir", "helper", "helper_sha256", "ca_cert", "ca_key"):
            with self.subTest(name):
                self.assertEqual(self.code(render_policy, self.template(TEMPLATE | {name: "x"}), RESOURCES), "template_has_run_value")

    def test_template_cannot_pin_a_rule_address(self):
        rules = [{"ref": "rule-1"}, {"ref": "rule-2", "connect_ip": "10.0.0.1"}]
        self.assertEqual(self.code(render_policy, self.template(TEMPLATE | {"rules": rules}), RESOURCES), "template_has_run_value")

    def test_template_cannot_hold_the_evaluation_run_reference(self):
        document = TEMPLATE | {"evaluation": {"llm_backend": "api", "target_kind": "public_practice", "run_ref": "other"}}
        self.assertEqual(self.code(render_policy, self.template(document), RESOURCES), "template_has_run_value")

    def test_template_cannot_hold_an_audit_key(self):
        self.assertEqual(self.code(render_policy, self.template(TEMPLATE | {"audit_key": {}}), RESOURCES), "policy_audit_key_preset")

    def test_rules_must_be_a_non_empty_list_of_objects(self):
        for rules in ([], None, 5, "rule", ["rule"], {"ref": "rule-1"}):
            with self.subTest(repr(rules)):
                self.assertEqual(self.code(render_policy, self.template(TEMPLATE | {"rules": rules}), RESOURCES), "template_rules_invalid")

    def test_missing_rules_are_rejected(self):
        document = {name: value for name, value in TEMPLATE.items() if name != "rules"}
        self.assertEqual(self.code(render_policy, self.template(document), RESOURCES), "template_rules_invalid")

    def test_template_must_be_preauthorized_scope(self):
        self.assertEqual(self.code(render_policy, self.template(TEMPLATE | {"approval_mode": "confirm_every_request"}), RESOURCES), "policy_approval_mode")

    def test_rendered_policy_over_the_limit_is_rejected(self):
        document = TEMPLATE | {"padding": "x" * (256 * 1024 - 200)}
        self.assertEqual(self.code(render_policy, self.template(document), RESOURCES), "policy_too_large")

    def test_prepare_accepts_the_rendered_bytes(self):
        rendered = render_policy(self.template(), RESOURCES)
        prepare_e2_config(self.prep, E2RunValues("eval-1", "model-1", "preserve", None, 8080), rendered)
        written = json.loads((self.prep / "policy.json").read_bytes())
        self.assertEqual(written["run_ref"], "run-1")
        self.assertEqual(sorted(written["audit_key"]), ["file", "sha256"])


class TestWindow(unittest.TestCase):
    NOW = datetime(2026, 11, 24, 3, 4, 5, 999999, timezone.utc)

    def test_starts_at_the_current_second_and_lasts_the_declared_time(self):
        self.assertEqual(approval_window(self.NOW, 7200, "2099-01-01T00:00:00Z"), ("2026-11-24T03:04:05Z", "2026-11-24T05:04:05Z"))

    def test_other_timezones_are_converted_to_utc(self):
        now = self.NOW.astimezone(timezone(timedelta(hours=9)))
        self.assertEqual(approval_window(now, 60, "2099-01-01T00:00:00Z"), ("2026-11-24T03:04:05Z", "2026-11-24T03:05:05Z"))

    def test_limit_boundary(self):
        self.assertEqual(approval_window(self.NOW, 60, "2026-11-24T03:05:05Z")[1], "2026-11-24T03:05:05Z")
        with self.assertRaises(E2PrepareError) as caught:
            approval_window(self.NOW, 60, "2026-11-24T03:05:04Z")
        self.assertEqual(caught.exception.code, "approval_window_exceeded")

    def test_unreadable_limit_is_rejected(self):
        for limit in ("soon", "2026-11-24", None):
            with self.subTest(repr(limit)):
                with self.assertRaises(E2PrepareError) as caught:
                    approval_window(self.NOW, 60, limit)
                self.assertEqual(caught.exception.code, "approval_limit_invalid")


class TestIssueCA(Base):
    WRITES = 'printf cert > "$3"\nprintf key > "$5"'

    def issue(self, tool, timeout=5):
        return issue_run_ca(tool, self.prep, "run-1", "2026-11-24T02:00:00Z", timeout)

    def test_returns_the_digests_of_the_written_files(self):
        self.assertEqual(self.issue(self.tool(self.WRITES)), (hashlib.sha256(b"cert").hexdigest(), hashlib.sha256(b"key").hexdigest()))

    def test_calls_the_tool_with_the_contract_arguments(self):
        seen = self.base / "args"
        self.issue(self.tool(f'printf "%s\\n" "$@" > "{seen}"\n' + self.WRITES))
        self.assertEqual(seen.read_text().splitlines(), ["ca", "--cert-file", str(self.prep / "ca.pem"), "--key-file", str(self.prep / "ca.key"),
                                                         "--run-ref", "run-1", "--expires", "2026-11-24T02:00:00Z"])

    def test_tool_gets_no_inherited_environment(self):
        os.environ["E2_PRODUCER_TEST_SECRET"] = "leak"
        self.addCleanup(os.environ.pop, "E2_PRODUCER_TEST_SECRET")
        seen = self.base / "env"
        self.issue(self.tool(f'printf "%s" "$E2_PRODUCER_TEST_SECRET" > "{seen}"\n' + self.WRITES))
        self.assertEqual(seen.read_text(), "")

    def test_failing_missing_or_slow_tool_is_one_failure(self):
        for name, tool, timeout in (("exit", self.tool("exit 3"), 5), ("missing", self.base / "absent", 5)):
            with self.subTest(name):
                self.assertEqual(self.code(self.issue, tool, timeout), "ca_tool_failed")
        self.assertEqual(self.code(self.issue, self.tool("sleep 5"), 1), "ca_tool_failed")

    def test_written_files_must_be_private_regular_single_links(self):
        cases = {"group-readable": self.WRITES + '\nchmod 640 "$5"',
                 "missing-key": 'printf cert > "$3"',
                 "empty-key": 'printf cert > "$3"\n: > "$5"',
                 "symlink": 'printf cert > "$3"\nprintf key > "$3.real"\nln -s "$3.real" "$5"',
                 "hardlink": 'printf cert > "$3"\nprintf key > "$5"\nln "$5" "$5.other"'}
        for name, body in cases.items():
            with self.subTest(name):
                for entry in os.listdir(self.prep):
                    os.unlink(self.prep / entry)
                self.assertEqual(self.code(self.issue, self.tool(body)), "ca_file_invalid")

    def test_oversized_file_is_rejected(self):
        self.assertEqual(self.code(self.issue, self.tool('printf cert > "$3"\ndd if=/dev/zero of="$5" bs=1024 count=1025 2>/dev/null')), "ca_file_invalid")

    def test_one_mebibyte_file_is_accepted(self):
        self.issue(self.tool('printf cert > "$3"\ndd if=/dev/zero of="$5" bs=1024 count=1024 2>/dev/null'))

    def test_private_directory_is_required(self):
        os.chmod(self.prep, 0o750)
        self.addCleanup(os.chmod, self.prep, 0o700)
        self.assertEqual(self.code(self.issue, self.tool(self.WRITES)), "directory_not_private")
        self.assertEqual(stat.S_IMODE(os.stat(self.prep).st_mode), 0o750)


class TestExpectedFingerprint(Base):
    DIGEST = "d" * 64
    NOW = datetime(2026, 11, 24, 3, 4, 5, tzinfo=timezone.utc)

    def fingerprint(self, tool, template=None):
        return expected_fingerprint(template or self.template(), tool, 5, now=self.NOW)

    def test_prints_the_digest_from_the_tool(self):
        self.assertEqual(self.fingerprint(self.tool(f'printf "%s\\n" {self.DIGEST}')), self.DIGEST)

    def test_tool_receives_a_complete_policy_of_placeholders(self):
        seen = self.base / "policy"
        self.fingerprint(self.tool(f'cat "$3" > "{seen}"\nprintf "%s\\n" {self.DIGEST}'))
        policy = json.loads(seen.read_text())
        self.assertEqual((policy["run_ref"], policy["evaluation"]["run_ref"], policy["helper"], policy["valid_from"], policy["valid_until"]),
                         ("placeholder-run", "placeholder-run", "/placeholder/helper", "2026-11-24T03:04:05Z", "2026-11-24T04:04:05Z"))
        self.assertEqual({rule["connect_ip"] for rule in policy["rules"]}, {"192.0.2.1"})

    def test_calls_the_fingerprint_subcommand_on_a_base_policy(self):
        seen = self.base / "args"
        self.fingerprint(self.tool(f'printf "%s\\n" "$1" "$2" "$#" > "{seen}"\nprintf "%s\\n" {self.DIGEST}'))
        self.assertEqual(seen.read_text().splitlines(), ["fingerprint", "--policy-file", "3"])

    def test_bad_tool_output_is_not_a_fingerprint(self):
        for name, body in (("short", 'printf "%s\\n" abc'), ("no-newline", f"printf %s {self.DIGEST}"), ("extra", f'printf "%s\\n%s\\n" {self.DIGEST} {self.DIGEST}'),
                           ("upper", f'printf "%s\\n" {"D" * 64}')):
            with self.subTest(name):
                self.assertEqual(self.code(self.fingerprint, self.tool(body)), "fingerprint_output_invalid")

    def test_failing_tool_is_reported(self):
        self.assertEqual(self.code(self.fingerprint, self.tool("exit 1")), "fingerprint_tool_failed")

    def test_cli_exit_codes(self):
        good = self.tool(f'printf "%s\\n" {self.DIGEST}')
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            self.assertEqual(fingerprint_main(["--template", str(self.template()), "--tool", str(good), "--timeout-seconds", "5"]), 0)
            bad = self.template(TEMPLATE | {"run_ref": "x"})
            self.assertEqual(fingerprint_main(["--template", str(bad), "--tool", str(good), "--timeout-seconds", "5"]), 1)
        self.assertEqual((out.getvalue(), err.getvalue()), (self.DIGEST + "\n", "e2_prepare: template_has_run_value\n"))


if __name__ == "__main__":
    unittest.main()
