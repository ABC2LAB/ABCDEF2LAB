"""회차 전 계측기 이미지 점검(meter_describe) 시험. 표식 칸·기대 빌드, 실제 eval-meter -role describe 출력과 대조."""

from __future__ import annotations

import copy
import json
import os
import re
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


def _go_toolchain_reason() -> str | None:
    """이 환경의 Go가 go.mod 요구 판 이상이면 None, 아니면 건너뛰는 이유. 평가 CI job에는 setup-go가 없고
    GOTOOLCHAIN=local이라 판이 보장되지 않는다(#684 리뷰). 같은 대조는 Go job의 TestDescribeMatchesTheRunnerCheck가 한다."""
    go = shutil.which("go")
    if go is None:
        return "go 없음"
    wanted = re.search(r"^go (\d+(?:\.\d+)*)$", (ROOT / "go.mod").read_text(encoding="utf-8"), re.M)
    try:
        have = subprocess.run([go, "env", "GOVERSION"], capture_output=True, text=True, timeout=60).stdout.strip()
    except (OSError, subprocess.SubprocessError):
        return "go env 실패"
    version = lambda text: tuple(int(part) for part in re.findall(r"\d+", text)[:3])
    if not wanted or not have.startswith("go") or version(have) < version(wanted.group(1)):
        return f"Go 도구체인 {have or '알 수 없음'} < go.mod {wanted.group(1) if wanted else '?'}"
    return None


class TestToolchainGate(unittest.TestCase):
    def test_low_or_missing_toolchain_is_a_reason_to_skip(self):
        from unittest import mock
        import test_meter_describe as module

        def fake(version):
            return mock.patch.object(module.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, version + "\n", ""))
        with mock.patch.object(module.shutil, "which", return_value="/usr/bin/go"):
            with fake("go1.20.3"):
                self.assertIn("go1.20.3", module._go_toolchain_reason())
            with fake("go1.26.3"), mock.patch.object(Path, "read_text", return_value="module x\n\ngo 1.26.3\n"):
                self.assertIsNone(module._go_toolchain_reason())
            with fake("go1.27.0"), mock.patch.object(Path, "read_text", return_value="module x\n\ngo 1.26.3\n"):
                self.assertIsNone(module._go_toolchain_reason())
            with fake(""):
                self.assertIsNotNone(module._go_toolchain_reason())
        with mock.patch.object(module.shutil, "which", return_value=None):
            self.assertEqual(module._go_toolchain_reason(), "go 없음")


class TestRealMeter(unittest.TestCase):
    """실제 eval-meter를 빌드해 -role describe 출력이 이 러너의 기대 칸과 같은지 본다(코드가 어긋나면 깨진다)."""

    def test_built_meter_matches_the_runner(self):
        reason = _go_toolchain_reason()
        if reason is not None:
            self.skipTest(reason)
        with tempfile.TemporaryDirectory() as tmp:
            binary = Path(tmp) / "eval-meter"
            # -buildvcs=false: 본 클론·CI 체크아웃에서는 Go가 그 트리의 커밋을 넣어 시험용 커밋과 어긋난다.
            built = subprocess.run(["go", "build", "-trimpath", "-buildvcs=false", "-ldflags", f"-X main.sourceRevision={SHA}", "-o",
                                    str(binary), "./cmd/eval-meter"], cwd=ROOT, capture_output=True, text=True, timeout=600,
                                   env={**os.environ, "CGO_ENABLED": "0"})
            self.assertEqual(built.returncode, 0, f"go build 실패: {built.stderr[-2000:]}")  # 컴파일러 오류를 남긴다
            out = subprocess.run([str(binary), "-role", "describe"], capture_output=True, check=True, timeout=30).stdout
        got = parse(out)
        self.assertEqual(check(got, SHA), [])
        self.assertEqual(got["build"]["source_revision"], SHA)


if __name__ == "__main__":
    unittest.main()
