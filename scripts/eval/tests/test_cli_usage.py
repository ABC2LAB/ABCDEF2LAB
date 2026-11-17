"""B에만 있는 CLI 사용 수 세기(cli_usage, 탐색 분리 배치 결정안 §10) 시험."""

from __future__ import annotations

import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from cli_usage import CliUsageError, command_words, count_usage, not_found, tool_input_command, validate_names  # noqa: E402


class TestCommandWords(unittest.TestCase):
    def test_operators_wrappers_and_paths(self):
        cases = {
            "npx playwright test": ["npx"],
            "cd /work && FOO=1 /usr/local/bin/playwright-cli open http://x": ["cd", "playwright-cli"],
            "curl -s http://a | jq . ; pip3 install x": ["curl", "jq", "pip3"],
            "timeout 30 npx -y foo": ["npx"],
            "sudo -u x env -i vim f": ["vim"],
            "x=$(npm root -g); ls $x": ["npm", "ls"],
            "xargs -I{} npx {}": ["npx"],
            "ls & npx b": ["ls", "npx"],
        }
        for command, words in cases.items():
            with self.subTest(command=command):
                self.assertEqual(command_words(command), words)

    def test_quotes_comments_redirections_heredocs(self):
        cases = {
            'echo "npx here" && python3 -c "print(1)"': ["echo", "python3"],
            "curl -s http://a 2>&1 | head -5": ["curl", "head"],
            "# comment npx\ncurl x": ["curl"],
            "echo a # npx tail": ["echo"],
            "cmd &> /dev/null; npx b": ["cmd", "npx"],
            "a=1#notcomment; ls": ["ls"],
            "cat <<EOF\nnpx inside heredoc\nEOF\nnpm -v": ["cat", "npm"],
            "python3 - <<'PY'\nimport os\nPY": ["python3"],
        }
        for command, words in cases.items():
            with self.subTest(command=command):
                self.assertEqual(command_words(command), words)

    def test_shell_dash_c_is_looked_into(self):
        self.assertEqual(command_words("bash -c 'npx a && pip list'"), ["bash", "npx", "pip"])
        self.assertEqual(command_words("sh -c \"sh -c 'sh -c \\\"sh -c npx\\\"'\""), ["sh", "sh", "sh", "sh"])  # 깊이 3까지


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
        self.assertEqual(got, {"format": "abc2lab.cli-usage/v1", "names": ["gpg", "npx", "pip", "vim"], "bash_calls": 5,
                               "unreadable_calls": 1, "calls": {"npx": 2, "pip": 1, "vim": 1},
                               "not_found": {"npx": 1}, "other_failures": {"npx": 1, "pip": 1, "vim": 1}})
        calls_only = count_usage(records, ["npx"], with_failures=False)
        self.assertEqual(set(calls_only), {"format", "names", "bash_calls", "unreadable_calls", "calls"})
        self.assertNotIn("install", json.dumps(got))  # 원문 조각이 결과에 없다


if __name__ == "__main__":
    unittest.main()
