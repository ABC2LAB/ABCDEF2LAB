"""E② 시도 경로 투영 검증(e2_attempt_paths.py). 합성 입력만 쓴다."""

from __future__ import annotations

import hashlib
import json
import re
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
sys.path.insert(0, str(Path(__file__).resolve().parent))

from e2_attempt_paths import (ATTEMPT_PATHS_DONE_FILE, ATTEMPT_PATHS_FILE, ATTEMPT_PATHS_FORMAT, AttemptPathsError,  # noqa: E402
                              parse_attempt_paths)
from path_mask import PATH_PROFILE, key_ref, mask_path  # noqa: E402
from test_e2_export import encode, normal_rows  # noqa: E402

ROOT = Path(__file__).resolve().parents[3]
KEY = bytes(range(32))
REF = key_ref(KEY)


def path_rows(attempts_rows, change=None):
    rows = []
    for row in attempts_rows:
        if row["stage"] != "result":
            continue
        escaped = "" if row["decision"] == "unsupported" else "/api/Users/" + str(row["sequence"])
        pattern, masked = mask_path(escaped, KEY) if escaped else (None, 0)
        rows.append({"sequence": row["sequence"], "decision": row["decision"], "reason": row["reason"], "method": row.get("method", ""),
                     "path_pattern": pattern, "masked_segments": masked, "path_profile_ref": PATH_PROFILE, "path_key_ref": REF})
    if change:
        change(rows)
    return b"".join(json.dumps(row).encode() + b"\n" for row in rows)


def done_for(data, **changes):
    marker = {"format": ATTEMPT_PATHS_FORMAT, "rows": data.count(b"\n"), "sha256": hashlib.sha256(data).hexdigest()} | changes
    return json.dumps(marker).encode() + b"\n"


class TestParse(unittest.TestCase):
    def setUp(self):
        self.audit_rows = normal_rows()
        self.attempts = encode(self.audit_rows)
        self.rows = path_rows(self.audit_rows)

    def reject(self, code, rows=None, done=None, attempts=None, ref=REF):
        rows = self.rows if rows is None else rows
        done = done_for(rows) if done is None else done
        with self.assertRaises(AttemptPathsError) as caught:
            parse_attempt_paths(rows, done, self.attempts if attempts is None else attempts, ref)
        self.assertEqual(str(caught.exception), code)

    def test_valid_projection_matches_the_audit_results(self):
        parsed = parse_attempt_paths(self.rows, done_for(self.rows), self.attempts, REF)
        self.assertEqual([(r["sequence"], r["decision"]) for r in parsed], [(1, "allowed"), (2, "denied"), (3, "unsupported")])
        self.assertIsNone(parsed[2]["path_pattern"])

    def test_missing_marker_or_rows_are_not_used(self):
        # 마감에 실패한 기록은 행이 다 있어도 표식이 없다.
        with self.assertRaises(AttemptPathsError) as caught:
            parse_attempt_paths(self.rows, None, self.attempts, REF)
        self.assertEqual(str(caught.exception), "attempt_paths_missing")
        with self.assertRaises(AttemptPathsError):
            parse_attempt_paths(None, done_for(self.rows), self.attempts, REF)

    def test_marker_must_match_the_bytes(self):
        self.reject("attempt_paths_digest_mismatch", done=done_for(self.rows, sha256="0" * 64))
        self.reject("attempt_paths_row_count_mismatch", done=done_for(self.rows, rows=2))
        for bad in (done_for(self.rows, format="abc2lab.e2-attempt-paths/v2"), done_for(self.rows, rows=-1), done_for(self.rows, rows=True),
                    done_for(self.rows, sha256="A" * 64), done_for(self.rows, extra=1), done_for(self.rows)[:-1],
                    done_for(self.rows) + b"\n", b"[]\n", b'{"format": 1, "format": 2}\n'):
            with self.subTest(bad=bad):
                self.reject("attempt_paths_done_invalid", done=bad)

    def test_truncated_tail_is_rejected(self):
        cut = self.rows[:-1]
        self.reject("attempt_paths_truncated", rows=cut, done=done_for(cut))

    def test_rows_are_closed_and_bound_to_the_same_masking(self):
        cases = {
            "extra field": lambda rows: rows[0].update(raw_path="/api"),
            "missing field": lambda rows: rows[0].pop("method"),
            "bad decision": lambda rows: rows[0].update(decision="allow"),
            "list decision": lambda rows: rows[0].update(decision=[]),
            "object decision": lambda rows: rows[0].update(decision={}),
            "list reason": lambda rows: rows[0].update(reason=["x"]),
            "list pattern": lambda rows: rows[0].update(path_pattern=["/x"]),
            "empty reason": lambda rows: rows[0].update(reason=""),
            "relative pattern": lambda rows: rows[0].update(path_pattern="api/x"),
            "null with masked": lambda rows: rows[2].update(masked_segments=1),
            "bool masked": lambda rows: rows[0].update(masked_segments=True),
            "bool sequence": lambda rows: rows[0].update(sequence=True),
            "other profile": lambda rows: rows[0].update(path_profile_ref="abc2lab.path-mask/v1"),
        }
        for name, change in cases.items():
            with self.subTest(name):
                rows = path_rows(self.audit_rows, change)
                self.reject("attempt_paths_row_invalid", rows=rows)
        rows = path_rows(self.audit_rows, lambda rows: rows[1].update(path_key_ref="f" * 16))
        self.reject("attempt_paths_key_mismatch", rows=rows)
        self.reject("attempt_paths_key_ref_invalid", ref="F" * 16)

    def test_rows_match_the_audit_results_one_to_one(self):
        cases = {
            "missing row": lambda rows: rows.pop(1),
            "extra row": lambda rows: rows.append(dict(rows[0], sequence=9)),
            "decision differs": lambda rows: rows[1].update(decision="allowed"),
        }
        for name, change in cases.items():
            with self.subTest(name):
                self.reject("attempt_paths_audit_mismatch", rows=path_rows(self.audit_rows, change))
        repeated = path_rows(self.audit_rows, lambda rows: rows.append(dict(rows[0])))
        self.reject("attempt_paths_sequence_repeated", rows=repeated)

    def test_no_raw_path_in_the_parsed_rows(self):
        parsed = parse_attempt_paths(self.rows, done_for(self.rows), self.attempts, REF)
        self.assertNotIn("Users", json.dumps(parsed))


class TestGoContract(unittest.TestCase):
    def test_names_and_fields_match_the_writer(self):
        go = (ROOT / "server/e2_attempt_paths.go").read_text(encoding="utf-8")
        for name, value in (("E2AttemptPathsFile", ATTEMPT_PATHS_FILE), ("E2AttemptPathsDoneFile", ATTEMPT_PATHS_DONE_FILE),
                            ("E2AttemptPathsFormat", ATTEMPT_PATHS_FORMAT)):
            self.assertRegex(go, r"%s\s*=\s*\"%s\"" % (name, re.escape(value)))
        tags = set(re.findall(r'`json:"([a-z_0-9]+)"`', go))
        self.assertEqual(tags, {"sequence", "decision", "reason", "method", "path_pattern", "masked_segments", "path_profile_ref",
                                "path_key_ref", "format", "rows", "sha256"})


if __name__ == "__main__":
    unittest.main()
