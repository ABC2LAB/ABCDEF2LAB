"""B에만 있는 CLI 사용 수 세기(cli_usage, 탐색 분리 배치 결정안 §10) 시험."""

from __future__ import annotations

import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from cli_usage import (  # noqa: E402
    CliUsageError, analyze_command, command_words, count_usage, not_found, tool_input_command, validate_names,
)


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

    def test_review_shell_contexts_are_counted(self):
        # #682 리뷰: 묶인 옵션(-ec)과 큰따옴표 안 명령 치환도 실제로 실행된다.
        cases = {
            "sh -ec 'npx --version'": ["sh", "npx"],
            "printf '%s\\n' \"$(npx --version)\"": ["npx", "printf"],
            "echo `npx -v`": ["npx", "echo"],
            "bash -o pipefail -c 'pip list | head'": ["bash", "pip", "head"],
            "bash -lc \"sh -c 'npx q'\"": ["bash", "sh", "npx"],
            'x="$(cat a | npm v)"': ["cat", "npm"],
            "echo '$(npx not)'": ["echo"],  # 작은따옴표 안은 치환이 아니다
        }
        for command, words in cases.items():
            with self.subTest(command=command):
                self.assertEqual(command_words(command), words)

    def test_review_round_two_contexts(self):
        # #682 리뷰 2차: 제어 낱말, 데이터 속 '<<EOF', 따옴표 없는 heredoc 본문의 치환, 셸 +옵션.
        cases = {
            "if npx --version; then :; fi": ["npx", ":"],
            "echo '<<EOF'\nnpx --version": ["echo", "npx"],
            "cat <<EOF\n$(npx --version)\nEOF": ["npx", "cat"],
            "sh +e -c 'npx --version'": ["sh", "npx"],
            "while true; do npx a; done": ["true", "npx"],
            "! npx a": ["npx"],
            "{ npx a; }": ["npx"],
            "case $x in a) npx b;; esac": ["npx"],
        }
        for command, words in cases.items():
            with self.subTest(command=command):
                self.assertEqual(command_words(command), words)
        # 확장되지 않는 heredoc(따옴표·백슬래시 구분어)과 본문의 평문, for 목록은 명령이 아니다
        for command, words in {"cat <<'EOF'\n$(npx --version)\nEOF": ["cat"], "cat <<-\\EOF\n$(npx q)\nEOF": ["cat"],
                               "cat <<EOF\nnpx plain text\nEOF": ["cat"], "for x in npx pip; do echo $x; done": ["echo"],
                               "cat <<<\"npx here-string\"": ["cat"],
                               "cat <<< word\nnpx --version": ["cat", "npx"]}.items():  # here-string 뒤 줄은 본문이 아니다
            with self.subTest(command=command):
                self.assertEqual(command_words(command), words)

    def test_review_round_three_heredoc_bodies_and_comments(self):
        # #682 리뷰 3차: 따옴표 없는 heredoc 본문의 따옴표는 글자라 '$(…)'도 실행되고, 주석 속 <<EOF는 heredoc이 아니다.
        cases = {
            "cat <<EOF\n'$(npx --version)'\nEOF": ["npx", "cat"],
            "# cat <<EOF\nnpx --version": ["npx"],
            "echo a # x <<EOF\nnpx b": ["echo", "npx"],
            "cat <<'EOF'\n'$(npx q)'\nEOF": ["cat"],      # 따옴표 구분어: 본문은 확장되지 않는다
            "cat <<EOF\n\\$(npx q)\nEOF": ["cat"],      # 백슬래시로 막은 치환
            "x=1#<<EOF\nnpx c": [],                         # 낱말 중간 #는 주석이 아니다(<<EOF는 heredoc, 다음 줄은 본문)
        }
        for command, words in cases.items():
            with self.subTest(command=command):
                self.assertEqual(command_words(command), words)

    def test_review_round_four_comments_follow_one_rule(self):
        # #682 리뷰 4차: 주석은 나누기·치환·heredoc 모두 같은 규칙으로 뺀다(따옴표 상태는 줄 너머까지).
        cases = {
            "# $(npx --version)\nprintf x": ["printf"],          # 주석 속 치환은 실행되지 않는다
            "(:)# cat <<EOF\nnpx --version": [":", "npx"],       # 메타 문자 `)` 뒤 `#`도 주석이다
            "echo x|#$(npx no)\nnpx w": ["echo", "npx"],          # `|` 뒤도 주석(파이프는 다음 줄로 이어진다)
            'echo "a\n# b $(npx q)"': ["npx", "echo"],            # 여러 줄 큰따옴표 안의 `#`는 글자
            "echo $# ${#x} a#b $(npx z)": ["npx", "echo"],         # `$#`·`${#x}`·낱말 가운데 `#`는 주석이 아니다
            "cat <<EOF\n# $(npx in-body)\nEOF\necho after": ["npx", "cat", "echo"],  # 확장 본문의 `#`는 글자
            "cat <<A <<B\n$(npx a)\nA\n$(pip b)\nB\nls": ["npx", "pip", "cat", "ls"],  # 한 줄의 heredoc 둘
            "echo ${x} y": ["echo"],                               # `${…}`를 쪼개지 않는다
            "{ npx a; }": ["npx"],
            "echo a\\ #$(npx q)": ["npx", "echo"],             # 막은 공백 뒤 `#`는 낱말 가운데라 주석이 아니다
            "echo \\#$(npx r)": ["npx", "echo"],               # 막은 `#`
            "echo a \\\n#$(npx s)\nls": ["echo", "ls"],       # 줄 이음 뒤 경계의 `#`는 주석
            "echo 'a\n$(npx q)'": ["echo"],                      # 여러 줄 작은따옴표 안은 치환이 아니다
            "echo 'a\n<<EOF\n' ; npx w": ["echo", "npx"],        # 줄 넘는 따옴표 안의 `<<`는 heredoc이 아니다
            "cat <<EOF\nbody\nEOF\n# $(npx q)\nls": ["cat", "ls"],  # heredoc 뒤 첫 줄의 `#`도 주석
        }
        for command, words in cases.items():
            with self.subTest(command=command):
                self.assertEqual(command_words(command), words)

    def test_unresolvable_calls_are_uncertain_not_zero(self):
        for command in ('eval "$CMD"', "$TOOL --x", "source ./run.sh", ". ./run.sh", "echo \"$(npx", "echo `npx", "sh -c 'a \"b'"):
            with self.subTest(command=command):
                self.assertTrue(analyze_command(command)[1])
        for command in ("npx a", "sh -ec 'npx b'", 'echo "$(date)"'):
            with self.subTest(command=command):
                self.assertFalse(analyze_command(command)[1])
        got = count_usage([{"command": {"command": "eval $X"}}, {"command": {"command": "sh -ec 'npx -v'"}}], ["npx"],
                          with_failures=False)
        self.assertEqual((got["uncertain_calls"], got["calls"]), (1, {"npx": 1}))

    def test_shell_dash_c_is_looked_into(self):
        self.assertEqual(command_words("bash -c 'npx a && pip list'"), ["bash", "npx", "pip"])
        deep = "sh -c \"sh -c 'sh -c \\\"sh -c \\\\\\\"sh -c npx\\\\\\\"\\\"'\""
        words, uncertain = analyze_command(deep)
        self.assertNotIn("npx", words)  # 깊이 3을 넘는 중첩은 읽지 않고
        self.assertTrue(uncertain)      # 미확인으로 남긴다


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
                               "unreadable_calls": 1, "uncertain_calls": 0, "calls": {"npx": 2, "pip": 1, "vim": 1},
                               "not_found": {"npx": 1}, "other_failures": {"npx": 1, "pip": 1, "vim": 1}})
        calls_only = count_usage(records, ["npx"], with_failures=False)
        self.assertEqual(set(calls_only), {"format", "names", "bash_calls", "unreadable_calls", "uncertain_calls", "calls"})
        self.assertNotIn("install", json.dumps(got))  # 원문 조각이 결과에 없다


if __name__ == "__main__":
    unittest.main()
