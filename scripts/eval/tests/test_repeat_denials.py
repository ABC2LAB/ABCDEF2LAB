"""진단 요청 반복 차단 수(repeat_denials.py). 합성 E② 수집 사본만 쓴다."""

from __future__ import annotations

import hashlib
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
sys.path.insert(0, str(Path(__file__).resolve().parent))

from e2_attempt_paths import ATTEMPT_PATHS_DONE_FILE, ATTEMPT_PATHS_FILE, ATTEMPT_PATHS_FORMAT  # noqa: E402
from path_mask import PATH_PROFILE, key_ref, mask_path  # noqa: E402
from repeat_denials import NOT_RUN, OK, summarize_round  # noqa: E402
from test_e2_export import SENT, attempt, closed, control, encode, manifest_for  # noqa: E402

KEY = bytes(range(32))
REF = key_ref(KEY)
# (순번, 결정, 사유, 메서드, 규칙 참조, 원문 경로 또는 None)
ATTEMPTS = [
    (1, "allowed", "complete", "GET", "rule-1", "/allowed/1"),
    (2, "denied", "policy_denied", "GET", "", "/outside/a"),
    (3, "denied", "policy_denied", "GET", "", "/outside/a"),
    (4, "denied", "policy_denied", "POST", "", "/outside/a"),
    (5, "unsupported", "unsupported", "", "", None),
    (6, "denied", "policy_denied", "GET", "", "/outside/b"),
]


def write(directory: Path, name: str, data: bytes) -> None:
    fd = os.open(directory / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "wb") as stream:
        stream.write(data)


def audit_rows(attempts=ATTEMPTS):
    """감사 내보내기 계약(e2_export)에 맞는 행. 허용은 의도·결과 두 행, 거부·미지원은 결과 한 행이다."""
    rows, index = [control(1)], 2
    for seq, decision, reason, method, rule, _ in attempts:
        attempt_id = format(seq, "032x")
        extra = {"method": method}
        if rule and decision != "allowed":
            extra["rule_ref"] = rule
        if decision == "allowed":
            rows.append(attempt(index, "intent", attempt_id, seq, decision, reason, **extra))
            index += 1
            rows.append(attempt(index, "result", attempt_id, seq, decision, reason, **extra, **SENT))
        else:
            rows.append(attempt(index, "result", attempt_id, seq, decision, reason, **extra))
        index += 1
    rows.append(closed(index))
    return rows


def copy_dir(base: Path, attempts=ATTEMPTS, paths=True, done=True, ref=REF, audit=None, manifest_changes=None) -> Path:
    directory = base / f"copy-{len(os.listdir(base))}"
    directory.mkdir(mode=0o700)
    rows = audit_rows(attempts) if audit is None else audit
    data = encode(rows) if isinstance(rows, list) else rows
    last_sequence = max((seq for seq, *_ in attempts), default=0)
    manifest = manifest_for(data, data.count(b"\n"), data.count(b"\n"), last_sequence) | (manifest_changes or {})
    write(directory, "attempts.jsonl", data)
    write(directory, "manifest.json", json.dumps(manifest).encode())
    if paths:
        lines = []
        for seq, decision, reason, method, _, raw in attempts:
            pattern, masked = mask_path(raw, KEY) if raw else (None, 0)
            lines.append({"sequence": seq, "decision": decision, "reason": reason, "method": method, "path_pattern": pattern,
                          "masked_segments": masked, "path_profile_ref": PATH_PROFILE, "path_key_ref": ref})
        projection = b"".join(json.dumps(line).encode() + b"\n" for line in lines)
        write(directory, ATTEMPT_PATHS_FILE, projection)
        if done:
            marker = {"format": ATTEMPT_PATHS_FORMAT, "rows": len(lines), "sha256": hashlib.sha256(projection).hexdigest()}
            write(directory, ATTEMPT_PATHS_DONE_FILE, json.dumps(marker).encode() + b"\n")
    return directory


class TestRepeatDenials(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.base = Path(temp.name)

    def test_counts_repeated_refusals_by_masked_path(self):
        got = summarize_round(copy_dir(self.base), REF)
        # (policy_denied, GET, /outside/a)만 두 번이다. POST는 다른 묶음, 경로 없는 미지원은 경로 미확보다.
        self.assertEqual(got["paths"], {"status": OK, "reason": None, "refusals": 5, "repeated_groups": 1, "repeated_refusals": 2,
                                        "path_unavailable": 1})
        # 보조 지표는 경로를 안 봐서 /outside/a·/outside/b의 GET 거부가 한 묶음(3회)이 된다.
        self.assertEqual(got["rule_refs"], {"status": OK, "reason": None, "refusals": 5, "repeated_groups": 1, "repeated_refusals": 3})
        dumped = json.dumps(got)
        for raw in ("outside", "{h:", "rule-1"):
            self.assertNotIn(raw, dumped)

    def test_allowed_requests_are_not_counted(self):
        attempts = [(1, "allowed", "complete", "GET", "rule-1", "/allowed/1"), (2, "allowed", "complete", "GET", "rule-1", "/allowed/1")]
        got = summarize_round(copy_dir(self.base, attempts), REF)
        self.assertEqual((got["paths"]["refusals"], got["paths"]["repeated_groups"], got["rule_refs"]["refusals"]), (0, 0, 0))

    def test_projection_problems_leave_only_the_coarse_metric(self):
        cases = {"attempt_paths_missing": dict(paths=False), "attempt_paths_missing ": dict(done=False),
                 "attempt_paths_key_mismatch": dict(ref="f" * 16)}
        for reason, kwargs in cases.items():
            with self.subTest(reason):
                got = summarize_round(copy_dir(self.base, **kwargs), REF)
                self.assertEqual((got["paths"]["status"], got["paths"]["reason"]), (NOT_RUN, reason.strip()))
                self.assertIsNone(got["paths"]["path_unavailable"])
                self.assertEqual(got["rule_refs"]["status"], OK)

    def test_missing_copy_or_audit_is_not_run(self):
        got = summarize_round(None, REF)
        self.assertEqual((got["paths"]["status"], got["rule_refs"]["reason"]), (NOT_RUN, "e2_copy_not_given"))
        empty = self.base / "empty"
        empty.mkdir(mode=0o700)
        got = summarize_round(empty, REF)
        self.assertEqual((got["paths"]["reason"], got["rule_refs"]["reason"]), ("e2_attempts_missing", "e2_attempts_missing"))

    def test_links_and_bad_audit_are_not_read(self):
        directory = copy_dir(self.base)
        os.unlink(directory / "attempts.jsonl")
        target = self.base / "elsewhere.jsonl"
        target.write_bytes(b"{}\n")
        os.symlink(target, directory / "attempts.jsonl")
        self.assertEqual(summarize_round(directory, REF)["rule_refs"]["reason"], "e2_copy_unreadable")
        def changed(index, **values):
            rows = audit_rows()
            rows[index] = rows[index] | values
            return rows
        bad_audits = {
            "not json": b"not json\n", "array": b"[]\n", "empty object": b"{}\n",
            "list decision": changed(3, decision=[]), "object reason": changed(3, reason={}), "list rule": changed(3, rule_ref=["r"]),
            "bad stage": changed(3, stage="resul"), "upper decision": changed(3, decision="DENIED"),
            "missing field": [{k: v for k, v in row.items() if k != "not_sent"} if i == 3 else row for i, row in enumerate(audit_rows())],
        }
        for name, bad in bad_audits.items():
            with self.subTest(name):
                got = summarize_round(copy_dir(self.base, audit=bad), REF)
                self.assertEqual((got["rule_refs"]["status"], got["rule_refs"]["reason"], got["paths"]["status"]),
                                 (NOT_RUN, "e2_attempts_invalid", NOT_RUN))
                self.assertIsNone(got["rule_refs"]["refusals"])
        # 결과가 complete가 아닌 내보내기는 감사가 다 있는지 몰라 세지 않는다.
        got = summarize_round(copy_dir(self.base, manifest_changes={"state": "incomplete"}), REF)
        self.assertEqual((got["rule_refs"]["reason"], got["paths"]["reason"]), ("e2_export_incomplete", "e2_export_incomplete"))
        missing = copy_dir(self.base)
        os.unlink(missing / "manifest.json")
        self.assertEqual(summarize_round(missing, REF)["rule_refs"]["reason"], "e2_attempts_missing")


if __name__ == "__main__":
    unittest.main()
