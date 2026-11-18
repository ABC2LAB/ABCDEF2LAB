"""보존 sidecar v3 칸 공용 부품(preservation_v3_fields) 시험."""

from __future__ import annotations

import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import preservation_v3_fields as v3f  # noqa: E402


class TestFields(unittest.TestCase):
    def test_driver_digest_follows_file_bytes_and_names(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "a.py").write_bytes(b"one")
            first = v3f.driver_sha256(("a.py",), root)
            (root / "a.py").write_bytes(b"two")
            self.assertNotEqual(first, v3f.driver_sha256(("a.py",), root))
            (root / "b.py").write_bytes(b"two")
            self.assertNotEqual(v3f.driver_sha256(("a.py",), root), v3f.driver_sha256(("b.py",), root))
        self.assertTrue(all((v3f.EVAL_DIR / name).is_file() for name in v3f.B_DRIVER_FILES + v3f.O_DRIVER_FILES))

    def test_end_signal_shape(self):
        terminal = {"status": "done", "at": "t", "elapsed_seconds": 12.5, "closing_reason": "budget_limited"}
        self.assertEqual(v3f.end_signal(terminal, product=False), {"status": "done", "elapsed_seconds": 12.5, "closing_reason": None})
        self.assertEqual(v3f.end_signal(terminal, product=True)["closing_reason"], "budget_limited")
        self.assertIsNone(v3f.end_signal(None, product=True))

    def test_surface_fields(self):
        surface = {"cli": {"sha256": "c" * 64}, "mcp": {"package_version": "0.0.84", "raw": {"sha256": "d" * 64},
                                                       "exposed": {"sha256": "e" * 64}}}
        self.assertEqual(v3f.surface_fields(surface), {"mcp_version": "0.0.84", "mcp_raw_tools_sha256": "d" * 64,
                                                       "cli_surface_sha256": "c" * 64, "mcp_exposed_tools_sha256": "e" * 64})

    def test_settings_digest_uses_only_named_fields(self):
        base = {"poll_seconds": 5, "other": 1}
        self.assertEqual(v3f.settings_sha256(base, ("poll_seconds",)), v3f.settings_sha256(dict(base, other=2), ("poll_seconds",)))
        with self.assertRaises(KeyError):
            v3f.settings_sha256(base, ("missing",))


class TestDockerToolSurface(unittest.TestCase):
    def test_baseline_surface_uses_the_engine_image_and_seed_disabled_exposure(self):
        import runner
        ports = runner.DockerPorts.__new__(runner.DockerPorts)
        ports.plan = mock.Mock()
        ports.plan.engine_image.id = "sha256:" + "a" * 64
        ports.docker = mock.Mock()
        listed = {"server_version": "s", "tools": [{"name": "browser_click"}, {"name": "browser_run_code_unsafe"}]}
        with mock.patch.object(runner, "image_path", return_value="/usr/bin") as path, \
                mock.patch.object(runner, "collect_cli", return_value={"sha256": "c" * 64, "count": 1}) as cli, \
                mock.patch.object(runner, "mcp_version", return_value="0.0.84"), \
                mock.patch.object(runner, "mcp_tools", return_value=listed) as tools:
            got = ports.tool_surface()
        self.assertEqual(cli.call_args.args[:3], ("sha256:" + "a" * 64, "/usr/bin", None))  # B: PATH 전체, 샌드박스 없음
        self.assertEqual(tools.call_args.args[:2], ("sha256:" + "a" * 64, ("--headless",)))
        self.assertEqual((got["side"], got["mcp"]["exposed"]["names"], got["mcp"]["exposed_basis"]),
                         ("baseline", [], "original_seed_disabled"))
        self.assertEqual(got["mcp"]["raw"]["names"], ["browser_click", "browser_run_code_unsafe"])
        path.assert_called_once()


if __name__ == "__main__":
    unittest.main()
