"""B에만 있는 CLI 등장 수(상한) 세기(cli_usage, 탐색 분리 배치 결정안 §10) 시험.

2026-11-18 사용자 결정: 셸 문법을 해석하지 않고, Bash 입력 원문에 이름이 낱말 경계로 나오면 센다(실행 수의 상한).
"""

from __future__ import annotations

import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from cli_usage import (  # noqa: E402
    FORMAT, CliUsageError, count_usage, not_found, occurrences, tool_input_command, validate_names,
)


class TestOccurrences(unittest.TestCase):
    def test_word_boundary_occurrences(self):
        cases = {
            "npx playwright test": 1,
            "/usr/local/bin/npx a; npx b": 2,  # 경로 뒤·연산자 앞도 센다
            "x=$(npx root); echo npx": 2,  # 치환 안·인자도 센다(문법을 해석하지 않는 상한)
            "echo 'npx' \"npx\" # npx": 3,  # 따옴표·주석 안도 센다
            "cat <<E\nnpx\nE": 1,  # heredoc 본문도 센다
            "npx2 my-npx npx.cmd npx_x xnpx": 0,  # 이름 글자가 붙으면 다른 낱말
            "npm npxx": 0,
            "": 0,
        }
        for command, count in cases.items():
            with self.subTest(command=command):
                self.assertEqual(occurrences(command, "npx"), count)

    def test_names_with_dots_and_dashes_do_not_overlap(self):
        # 긴 이름과 그 앞부분인 짧은 이름이 서로를 세지 않는다(`.`·`-`·`+`·숫자는 이름 글자다)
        command = "python3.12-config --x; python3.12 -V; pip3 a; pip b; vim.basic f; vi g"
        self.assertEqual({name: occurrences(command, name) for name in
                          ("python3.12-config", "python3.12", "python3", "pip3", "pip", "vim.basic", "vim", "vi")},
                         {"python3.12-config": 1, "python3.12": 1, "python3": 0, "pip3": 1, "pip": 1, "vim.basic": 1,
                          "vim": 0, "vi": 1})

    def test_split_names_are_not_counted(self):
        # 문법을 해석하지 않으므로 쪼갠 이름(줄 이음·따옴표 이어 붙이기)은 세지 못한다(결정안 §10에 적은 한계)
        for command in ("n\\\npx a", "'n''px' a", "\"np\"x a"):
            with self.subTest(command=command):
                self.assertEqual(occurrences(command, "npx"), 0)


class TestCounting(unittest.TestCase):
    def test_tool_input_and_names(self):
        self.assertEqual(tool_input_command('{"command":"ls","timeout_ms":5}'), "ls")
        self.assertEqual(tool_input_command({"command": "ls"}), "ls")
        self.assertIsNone(tool_input_command("not json"))
        self.assertIsNone(tool_input_command('{"cmd":"ls"}'))
        self.assertEqual(validate_names(["b", "a"]), ("a", "b"))
        for bad in (["a", "a"], ["-x"], [""], "npx", [1], ["a b"]):
            with self.subTest(bad=bad), self.assertRaises(CliUsageError):
                validate_names(bad)

    def test_not_found_markers(self):
        self.assertTrue(not_found("npx", "bash: line 1: npx: command not found", None))
        self.assertTrue(not_found("pip", "/bin/sh: 1: pip: not found", 2))
        self.assertTrue(not_found("vim", "env: 'vim': No such file or directory", 1))
        self.assertTrue(not_found("npx", "", 127))
        self.assertFalse(not_found("npx", "npxx: command not found", 1))  # 다른 이름
        self.assertFalse(not_found("npx", "ok", 0))

    def test_counts_and_failures(self):
        records = [
            {"command": '{"command":"npx x"}', "output": "bash: npx: command not found", "exit_code": 127},
            {"command": {"command": "pip install a && npx b && npx c"}, "output": "err", "exit_code": 1},
            {"command": {"command": "curl x"}, "output": "", "exit_code": 0},
            {"command": {"command": "vim f"}, "output": "", "exit_code": 0, "failed": True},
            {"command": "not json"},
        ]
        got = count_usage(records, ["npx", "pip", "vim", "gpg"], with_failures=True)
        # 등장 수는 모든 입력의 등장 횟수 합(둘째 호출의 npx 두 번 포함), 실패는 이름이 나온 호출 수
        self.assertEqual(got, {"format": FORMAT, "names": ["gpg", "npx", "pip", "vim"], "bash_calls": 5, "unreadable_calls": 1,
                               "occurrences": {"npx": 3, "pip": 1, "vim": 1}, "names_seen": ["npx", "pip", "vim"],
                               "not_found": {"npx": 1}, "other_failures": {"npx": 1, "pip": 1, "vim": 1}})
        counts_only = count_usage(records, ["npx"], with_failures=False)
        self.assertEqual(set(counts_only), {"format", "names", "bash_calls", "unreadable_calls", "occurrences", "names_seen"})
        self.assertNotIn("install", json.dumps(got))  # 원문 조각이 결과에 없다
        none = count_usage([{"command": {"command": "curl x"}}], ["npx"], with_failures=False)
        self.assertEqual((none["occurrences"], none["names_seen"]), ({}, []))  # 목록이 비면 사용 없음으로 확정한다(결정안 §10)


if __name__ == "__main__":
    unittest.main()
