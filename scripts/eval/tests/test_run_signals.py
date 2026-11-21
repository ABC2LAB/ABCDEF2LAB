"""멈춤 신호 처리(run_signals, official_session.wait_process) 시험. 공식 세션을 뒤에서 띄웠을 때 시한을 넘긴 러너가 SIGINT를 받아
정리하는지(2026-11-21 기존 세션 하네스에서 무시 상태가 자식까지 물려가 상한 멈춤이 안 먹은 일), SIGINT가 안 먹으면 SIGTERM →
SIGKILL로 넘어가는지 본다."""

from __future__ import annotations

import os
import signal
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

EVAL = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(EVAL))

import official_session  # noqa: E402
import runner  # noqa: E402
import run_signals  # noqa: E402

# 자식(러너 자리): 시작 때 SIGINT 처리 상태를 적고, KeyboardInterrupt(SIGINT·SIGTERM)를 받으면 정리했다고 적는다.
CHILD = """
import signal, sys, time
out, mode = sys.argv[1], sys.argv[2]
if mode == "ignore-int":
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    signal.signal(signal.SIGTERM, lambda *a: (open(out, "a").write("term\\n"), sys.exit(143)))
elif mode == "ignore-all":
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
open(out, "a").write("start ignored=%s\\n" % (signal.getsignal(signal.SIGINT) == signal.SIG_IGN))
try:
    time.sleep(60)
except KeyboardInterrupt:
    open(out, "a").write("interrupted\\n")
    sys.exit(130)
"""

# 공식 세션 자리: restore면 main처럼 신호를 되돌린 뒤 자식을 띄우고 시한(1초) 뒤 끊는다.
PARENT = """
import subprocess, sys, time
sys.path.insert(0, sys.argv[1])
import official_session, run_signals
if sys.argv[4] == "restore":
    run_signals.interrupt_on_stop_signals()
p = subprocess.Popen([sys.executable, "-I", "-B", sys.argv[2], sys.argv[3], "plain"], start_new_session=True)
time.sleep(0.5)
print(official_session.wait_process(p, 1, 5), p.returncode)
"""


class TestBackgroundLaunch(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        (self.root / "child.py").write_text(CHILD, encoding="utf-8")
        (self.root / "parent.py").write_text(PARENT, encoding="utf-8")

    def tearDown(self):
        self._tmp.cleanup()

    def background(self, mode: str, out: Path) -> str:
        """비대화형 bash에서 `&`로 띄운다(SIGINT가 무시 상태로 넘어가는 실제 경로)."""
        command = f'{sys.executable} -I -B "{self.root}/parent.py" "{EVAL}" "{self.root}/child.py" "{out}" {mode} & wait'
        return subprocess.run(["bash", "-c", command], capture_output=True, text=True, timeout=60).stdout.strip()

    def test_without_restore_the_background_child_misses_sigint(self):
        # 대조: 되돌리지 않으면 자식이 무시 상태를 물려받아 정리하지 못하고 끝난다(이 시험이 위험을 실제로 재현하는지)
        out = self.root / "plain.txt"
        self.background("keep", out)
        self.assertEqual(out.read_text().splitlines(), ["start ignored=True"])

    def test_session_restores_sigint_so_the_background_child_cleans_up(self):
        out = self.root / "restored.txt"
        printed = self.background("restore", out)
        self.assertEqual(out.read_text().splitlines(), ["start ignored=False", "interrupted"])
        self.assertTrue(printed.endswith(" 130"), printed)


class TestEscalation(unittest.TestCase):
    def run_child(self, mode: str) -> tuple[subprocess.Popen, list[str]]:
        with tempfile.TemporaryDirectory() as root:
            child, out = Path(root, "child.py"), Path(root, "out.txt")
            child.write_text(CHILD, encoding="utf-8")
            proc = subprocess.Popen([sys.executable, "-I", "-B", str(child), str(out), mode], start_new_session=True)
            for _ in range(50):  # 자식이 신호 처리를 다 건 뒤에 끊는다
                if out.exists():
                    break
                subprocess.run(["sleep", "0.1"], check=False)
            self.assertIsNone(official_session.wait_process(proc, 0.1, 1))
            return proc, out.read_text().splitlines()

    def test_sigterm_follows_an_ignored_sigint(self):
        proc, lines = self.run_child("ignore-int")
        self.assertEqual((lines[-1], proc.returncode), ("term", 143))

    def test_sigkill_ends_a_child_that_ignores_both(self):
        proc, lines = self.run_child("ignore-all")
        self.assertEqual((lines, proc.returncode), (["start ignored=True"], -signal.SIGKILL))


class TestHandlers(unittest.TestCase):
    def test_sigterm_and_sigint_raise_keyboard_interrupt(self):
        code = ("import os, signal, sys\nsys.path.insert(0, sys.argv[1])\nimport run_signals\n"
                "signal.signal(signal.SIGINT, signal.SIG_IGN)\nrun_signals.interrupt_on_stop_signals()\nseen = []\n"
                "for sig in (signal.SIGINT, signal.SIGTERM):\n"
                "    try:\n        os.kill(os.getpid(), sig)\n        signal.pause() if hasattr(signal, 'pause') else None\n"
                "    except KeyboardInterrupt:\n        seen.append(sig.name)\nprint(' '.join(seen))\n")
        done = subprocess.run([sys.executable, "-I", "-B", "-c", code, str(EVAL)], capture_output=True, text=True, timeout=30)
        self.assertEqual(done.stdout.strip(), "SIGINT SIGTERM")

    def test_both_entry_points_install_the_handlers_first(self):
        for module in (official_session, runner):
            with self.subTest(module=module.__name__), mock.patch.object(module, "interrupt_on_stop_signals") as installed, \
                    mock.patch("sys.stdout"), self.assertRaises(SystemExit):
                module.main(["--help"])
            installed.assert_called_once_with()


if __name__ == "__main__":
    unittest.main()
