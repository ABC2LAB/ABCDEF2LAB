"""B에만 있는 CLI 사용 수 세기(cli_usage, 탐색 분리 배치 결정안 §10) 시험."""

from __future__ import annotations

import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from cli_usage import (  # noqa: E402
    CliUsageError, analyze_command, analyze_command_detail, command_words, count_usage, not_found, tool_input_command, validate_names,
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

    def test_review_round_five_heredoc_words_and_substitution_comments(self):
        # #682 리뷰 5차: 구분어는 완전한 셸 낱말, 닫는 줄은 `<<`면 그대로·`<<-`면 앞 탭만 지우고 비교, 치환 안 주석의 괄호는
        # 세지 않는다. 모두 가짜 npx로 실제 bash와 대조한 값이다.
        cases = {
            "cat <<END-TEXT\nbody\nEND-TEXT\nnpx a": ["cat", "npx"],
            "cat <<END\n  END\n$(npx b)\nEND": ["npx", "cat"],          # 공백 들여쓴 줄은 `<<`를 닫지 않는다
            "cat <<-END\n\t\tEND\nnpx c": ["cat", "npx"],             # `<<-`는 앞 탭을 지우고 닫는다
            "cat <<-END\n  END\n$(npx d)\nEND": ["npx", "cat"],         # 공백은 지우지 않는다
            'echo "$(echo x # )\n)"\nnpx e': ["echo", "npx"],
            'echo "$(# note )\nnpx --version\n)"': ["npx", "echo"],     # 리뷰 반례: 주석 다음 줄의 호출은 치환 안이다
            'cat <<"E F"\n$(npx g)\nE F\nls': ["cat", "ls"],             # 따옴표 낱말: 확장 안 함
            "cat <<E\\F\n$(npx h)\nEF\nnpx i": ["cat", "npx"],         # 백슬래시: 구분어 EF, 확장 안 함
            "cat <<'A'B\n$(npx l)\nAB\nnpx m": ["cat", "npx"],          # 따옴표 일부: 구분어 AB
            "cat <<EOF \nx\nEOF \nEOF\nnpx j": ["cat", "npx"],          # 뒤 공백이 있는 줄은 닫지 않는다
            "echo $((1<<2))\nnpx f": ["echo", "npx"],                     # 산술의 <<는 heredoc이 아니다
            "(( x = 1<<2 ))\nnpx k": ["npx"],
            "if (( 1 )); then npx r; fi": ["npx"],
        }
        for command, words in cases.items():
            with self.subTest(command=command):
                self.assertEqual(command_words(command), words)
        # 정적으로 풀지 못하는 문맥은 0건이 아니라 미확인이다
        for command in ("cat <<\nnpx a", "cat <<'EOF\nnpx b", "x=$(case a in a) npx c;; esac)", "y=$(cat <<E\n)\nE\n)",
                        "echo $((1 + 2\nnpx q"):
            with self.subTest(command=command):
                self.assertTrue(analyze_command(command)[1])

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
        # 둘째 호출의 npx는 `&&` 뒤라 실행이 조건에 달렸다 → 확정 수가 아니라 conditional_calls·uncertain_calls(#682 리뷰)
        self.assertEqual(got, {"format": "abc2lab.cli-usage/v1", "names": ["gpg", "npx", "pip", "vim"], "bash_calls": 5,
                               "unreadable_calls": 1, "uncertain_calls": 1, "calls": {"npx": 1, "pip": 1, "vim": 1},
                               "conditional_calls": {"npx": 1},
                               "not_found": {"npx": 1}, "other_failures": {"pip": 1, "vim": 1}})
        calls_only = count_usage(records, ["npx"], with_failures=False)
        self.assertEqual(set(calls_only), {"format", "names", "bash_calls", "unreadable_calls", "uncertain_calls", "calls",
                                           "conditional_calls"})
        self.assertNotIn("install", json.dumps(got))  # 원문 조각이 결과에 없다

    def test_review_round_six_redirections_and_conditional_branches(self):
        # #682 리뷰 6차: 명령 앞 리다이렉션 뒤 이름은 늘 실행되고, `&&`·`||` 뒤나 본문 안 이름은 실행이 조건에 달렸다.
        cases = {
            "</dev/null npx --version": (["npx"], []),
            "2>/dev/null npx c": (["npx"], []),
            "> out npx b": (["npx"], []),
            "false && npx --version\n:": (["false", ":"], ["npx"]),
            "true || npx a": (["true"], ["npx"]),
            "a && b; npx f": (["a", "npx"], ["b"]),
            "a && (b; npx g)": (["a"], ["b", "npx"]),
            "a && { b; npx g; }": (["a"], ["b", "npx"]),
            "if npx --version; then :; fi": (["npx"], [":"]),
            "while npx r; do :; done": (["npx"], [":"]),
            "for x in 1 2; do npx j; done": ([], ["npx"]),
            "case $x in a) npx k;; esac; ls": (["ls"], ["npx"]),
            "a | npx u": (["a", "npx"], []),
            "a && b | npx v": (["a"], ["b", "npx"]),
            "false && echo $(npx q)": (["false"], ["npx", "echo"]),
            "false && sh -c 'npx t'": (["false"], ["sh", "npx"]),
            # #682 리뷰 7차: 연산자 바로 뒤 줄바꿈(주석·빈 줄 포함)은 같은 목록이 이어진다
            "false &&\nnpx --version": (["false"], ["npx"]),
            "true ||\nnpx a": (["true"], ["npx"]),
            "false && # c\nnpx b": (["false"], ["npx"]),
            "false &&\n\nnpx c": (["false"], ["npx"]),
            "a |\nnpx d": (["a", "npx"], []),
            "false && a |\nnpx e": (["false"], ["a", "npx"]),
            "false && a\nnpx f": (["false", "npx"], ["a"]),       # 빈 조각이 아니면 줄바꿈이 목록을 끝낸다
        }
        for command, (sure, maybe) in cases.items():
            definite, conditional, _ = analyze_command_detail(command)
            with self.subTest(command=command):
                self.assertEqual((definite, conditional), (sure, maybe))
        # 함수 정의는 본문이 호출될 때만 실행된다 → 미확인
        for command in ("f() { npx l; }; f", "function g { npx m; }"):
            definite, conditional, unsure = analyze_command_detail(command)
            with self.subTest(command=command):
                self.assertTrue(unsure)
                self.assertNotIn("npx", definite)

    def test_review_round_eight_line_continuation_and_arithmetic_words(self):
        # #682 리뷰 8차: 셸은 따옴표 밖·큰따옴표 안의 백슬래시+줄바꿈을 지우고 앞뒤를 잇는다(명령 이름 가운데·들여쓰기 앞·
        # 치환 안 모두). 작은따옴표 안과 따옴표 있는 heredoc 본문은 글자 그대로다. bash 실측 89건과 일치.
        cases = {
            "n\\\npx --version": (["npx"], [], False),
            "np\\\nx --version": (["npx"], [], False),
            "n\\\n\\\npx --version": (["npx"], [], False),
            "echo a; \\\n  npx --version": (["echo", "npx"], [], False),
            "echo $(n\\\npx --version)": (["npx", "echo"], [], False),
            'echo "$(n\\\npx --version)"': (["npx", "echo"], [], False),
            "`n\\\npx --version`": (["npx"], [], False),
            "false && n\\\npx a": (["false"], ["npx"], False),
            "echo 'n\\\npx'": (["echo"], [], False),
            "echo a\\\n#b; npx --version": (["echo", "npx"], [], False),  # 이은 뒤 `#`는 낱말 가운데라 주석이 아니다
            "# c \\\nnpx --version": (["npx"], [], False),  # 주석은 줄 끝에서 끝난다
            "cat <<E\n$(n\\\npx --version)\nE": (["npx", "cat"], [], False),
            "cat <<'E'\n$(n\\\npx --version)\nE": (["cat"], [], False),
            # 확장되는 heredoc은 이은 줄로 구분어를 비교한다: 첫 `E`는 `lineE`가 되어 닫지 못하고 다음 줄도 본문이다
            "cat <<E\nline\\\nE\nnpx --version\nE": (["cat"], [], False),
            "cat <<-E\n\tx\\\n\tE\nnpx --version\nE": (["cat"], [], True),  # `<<-` 탭 지우기와 겹치면 미확인
            "cat <<E\na\\\\\nE\nnpx --version": (["cat", "npx"], [], False),  # 짝수 백슬래시는 막힌 글자라 잇지 않는다
            '"n\\\npx" --version': (["npx"], [], False),  # 큰따옴표 안 줄 이음도 지운다(따옴표를 벗기면 npx)
            # 산술 확장은 낱말 하나다. 안쪽 공백으로 쪼개 `1`을 명령으로 세거나 뒤 명령을 놓치지 않는다(이번에 발견)
            "x=$(( 1 + 2 )) npx --version": (["npx"], [], False),
            "x=$(( 1 + 2 )); npx --version": (["npx"], [], False),
            "echo $(( 1 + $(npx --version | wc -c) ))": (["npx", "wc", "echo"], [], False),
        }
        for command, expected in cases.items():
            with self.subTest(command=command):
                self.assertEqual(analyze_command_detail(command), (*expected[:2], expected[2]))


if __name__ == "__main__":
    unittest.main()
