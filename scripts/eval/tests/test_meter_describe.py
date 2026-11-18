"""회차 전 계측기 이미지 점검(meter_describe) 시험. 표식 칸·기대 빌드, 실제 eval-meter -role describe 출력과 대조."""

from __future__ import annotations

import copy
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from meter_describe import EXPECTED, FORMAT, check, describe_args, parse, summary  # noqa: E402

ROOT = Path(__file__).resolve().parents[3]
SHA = "0123456789abcdef0123456789abcdef01234567"


def described(**build):
    markers = {name: {"required": list(fields["required"]), "optional": list(fields["optional"])} for name, fields in EXPECTED.items()}
    return {"format": FORMAT, "markers": markers,
            "build": {"source_revision": SHA, "vcs_revision": "", "vcs_modified": None, "go_version": "go1.26.3", **build}}


class TestCheck(unittest.TestCase):
    def test_matching_image_passes(self):
        self.assertEqual(check(described(), SHA), [])
        self.assertEqual(check(described(vcs_revision=SHA, vcs_modified=False), SHA), [])

    def test_old_image_without_describe_is_refused(self):
        # 파일럿 B 1회차의 #638 이전 이미지는 describe 역할이 없다(실행 실패 → None).
        for raw in (None, {}, {"format": "other"}, "x"):
            with self.subTest(raw=raw):
                self.assertEqual(check(raw, SHA), ["meter_describe_unavailable"])

    def test_marker_fields_must_match_what_the_runner_checks(self):
        old = described()
        old["markers"]["llm-verified"]["required"].remove("rate_limited")  # #638 이전 표식
        self.assertEqual(check(old, SHA), ["meter_marker_fields_mismatch:llm-verified"])
        extra = described()
        extra["markers"]["engine-row"]["required"].append("new_field")
        self.assertEqual(check(extra, SHA), ["meter_marker_fields_mismatch:engine-row"])
        optional = described()
        optional["markers"]["engine-row"]["optional"] = []
        self.assertEqual(check(optional, SHA), ["meter_marker_fields_mismatch:engine-row"])
        missing = described()
        del missing["markers"]["llm-verified"]
        self.assertEqual(check(missing, SHA), ["meter_marker_fields_mismatch:llm-verified"])

    def test_build_must_be_the_declared_commit(self):
        self.assertEqual(check(described(source_revision=""), SHA), ["meter_build_unknown"])
        self.assertEqual(check(described(source_revision="short"), SHA), ["meter_build_unknown"])
        self.assertEqual(check(described(), "f" * 40), ["meter_build_mismatch"])
        self.assertEqual(check(described(source_revision="", vcs_revision=SHA, vcs_modified=False), SHA), [])  # 본 클론 빌드
        self.assertEqual(check(described(vcs_revision=SHA, vcs_modified=True), SHA), ["meter_build_mismatch"])  # 더러운 트리
        self.assertEqual(check(described(vcs_revision="f" * 40, vcs_modified=False), SHA), ["meter_build_mismatch"])

    def test_parse_and_args(self):
        self.assertEqual(parse(json.dumps(described()).encode()), described())
        self.assertIsNone(parse(b"x" * (65 << 10)))
        self.assertIsNone(parse(b"not json"))
        args = describe_args("sha256:" + "a" * 64)
        self.assertEqual(args[-3:], ["sha256:" + "a" * 64, "-role", "describe"])
        for flag in (["--network", "none"], ["--read-only"], ["--cap-drop", "ALL"]):
            self.assertTrue(any(args[i:i + len(flag)] == flag for i in range(len(args))), flag)
        self.assertEqual(summary(described(), [], SHA)["source_revision"], SHA)


@unittest.skipUnless(shutil.which("go"), "go 없음")
class TestRealMeter(unittest.TestCase):
    """실제 eval-meter를 빌드해 -role describe 출력이 이 러너의 기대 칸과 같은지 본다(코드가 어긋나면 깨진다)."""

    def test_built_meter_matches_the_runner(self):
        with tempfile.TemporaryDirectory() as tmp:
            binary = Path(tmp) / "eval-meter"
            # -buildvcs=false: 본 클론·CI 체크아웃에서는 Go가 그 트리의 커밋을 넣어 시험용 커밋과 어긋난다.
            subprocess.run(["go", "build", "-trimpath", "-buildvcs=false", "-ldflags", f"-X main.sourceRevision={SHA}", "-o", str(binary),
                            "./cmd/eval-meter"], cwd=ROOT, check=True, capture_output=True, timeout=600,
                           env={**os.environ, "CGO_ENABLED": "0"})
            out = subprocess.run([str(binary), "-role", "describe"], capture_output=True, check=True, timeout=30).stdout
        got = parse(out)
        self.assertEqual(check(got, SHA), [])
        self.assertEqual(got["build"]["source_revision"], SHA)


if __name__ == "__main__":
    unittest.main()
