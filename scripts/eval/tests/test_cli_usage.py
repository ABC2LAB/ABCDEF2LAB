"""B에만 있는 CLI 이름의 관측 세기(cli_usage, 탐색 분리 배치 결정안 §10) 시험.

2026-11-18 사용자 결정: 셸 문법을 해석하지 않는다. 줄 이음을 붙이고 따옴표 문자를 지운 뒤, 이름이 낱말 경계로 나오면 센다.
등장 0은 "원문에서 이름이 관측되지 않음(미확인)"이다.
"""

from __future__ import annotations

import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from cli_usage import (  # noqa: E402
    FORMAT, MAX_OBSERVED_LINE_CHARS, MAX_OBSERVED_LINES, MAX_SHAPE_INPUT_BYTES, CliUsageError, clip_shape, command_shape, count_usage, shell_tokens,
    normalize, not_found, observed_lines, pick_line,
    occurrences, tool_input_command, validate_names,
)


class TestOccurrences(unittest.TestCase):
    def test_word_boundary_occurrences(self):
        cases = {
            "npx playwright test": 1,
            "/usr/local/bin/npx a; npx b": 2,  # 경로 뒤·연산자 앞도 센다
            "x=$(npx root); echo npx": 2,  # 치환 안·인자도 센다(문법을 해석하지 않는 관측 수)
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

    def test_line_joins_and_quote_splicing_are_normalized(self):
        # 줄 이음(백슬래시+줄바꿈)을 붙이고 따옴표 문자를 지운 뒤 찾는다(#682 동찬 지적의 두 경우)
        self.assertEqual(normalize("n\\\npx 'a' \"b\""), "npx a b")
        for command in ("n\\\npx a", "'n''px' a", "\"np\"x a", "n\\\n'p'\"x\" a", "x; 'npx' b"):
            with self.subTest(command=command):
                self.assertEqual(occurrences(command, "npx"), 1)
        # 따옴표를 지워 이름 글자가 붙으면 다른 낱말이다(bash에서도 한 낱말 `npxx`)
        self.assertEqual(occurrences("'npx'x a", "npx"), 0)
        # 줄 이음이 아닌 줄바꿈(백슬래시 없음)은 낱말 경계다
        self.assertEqual(occurrences("n\npx", "npx"), 0)

    def test_indirect_names_are_not_observed(self):
        # 변수·eval·인코딩을 거쳐 이름을 만드는 호출은 관측하지 못한다(결정안 §10의 한계)
        for command in ("a=np; b=x; $a$b run", "eval \"$(printf 'n%sx' p)\" run", "$(echo bnB4 | base64 -d) run"):
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
        self.assertEqual((none["occurrences"], none["names_seen"]), ({}, []))  # 목록이 비면 "원문에서 관측되지 않음(미확인)"(결정안 §10)
        spliced = count_usage([{"command": {"command": "'n''px' a && p\\\nip b"}}], ["npx", "pip"], with_failures=False)
        self.assertEqual(spliced["occurrences"], {"npx": 1, "pip": 1})  # 기록 세기에도 같은 정규화를 쓴다



class TestObservedShapes(unittest.TestCase):
    """관측된 명령은 원문 대신 모양만 남긴다(#736 리뷰): 관측 이름·셸 연산자·한 글자 짧은 옵션, 긴 옵션은 --<opt>, 나머지는 <arg>."""

    def test_shapes_keep_names_operators_and_flag_names_only(self):
        records = [{"command": {"command": "cd /tmp\nexport A=1\nnpx playwright test"}},
                   {"command": {"command": "echo hi"}},  # 관측 없음
                   {"command": {"command": "n\\\npx b\nls"}},  # 줄 이음으로 쪼갠 이름: 이은 줄을 고른다
                   {"command": {"command": "x='p''ip' && run"}},  # 따옴표 이어 붙인 이름: 낱말은 이름이 아니다
                   {"command": {"command": "/usr/local/bin/pip install -q --upgrade --index-url=https://x y"}},
                   {"command": "not json"}]
        picked, observed = observed_lines(records, ["npx", "pip"])
        self.assertEqual(observed, 4)
        self.assertEqual(picked, [{"names": ["npx"], "shape": "npx <arg> <arg>"},
                                  {"names": ["npx"], "shape": "npx <arg>"},
                                  {"names": ["pip"], "shape": "<arg> && <arg>"},
                                  {"names": ["pip"], "shape": "<path>/pip <arg> -q --<opt> --<opt>=<arg> <arg>"}])
        self.assertEqual(pick_line("a\nb", ["npx"]), "a")  # 이름이 정규화 뒤에만 보이면 첫 줄
        self.assertEqual(observed_lines([{"command": {"command": "curl x"}}], ["npx"]), ([], 0))

    def test_secrets_in_any_argument_form_are_not_kept(self):
        # 자격 가림(dlp.CredentialText)이 못 가리는 별도 옵션 인자를 포함해 어떤 인자도 원문으로 남지 않는다
        for command in ("mysql -u root -p synthsecret", "npx run -psynthsecret", "npx --password synthsecret",
                        "npx --password=synthsecret", "TOKEN=synthsecret npx x", "npx 'synth secret'",
                        "npx run --token=\"synthfirst\nsynthsecond\"", "npx -u user:synthsecret http://h",
                        # 별도 인자 값이 긴 옵션 이름 모양(따옴표 있든 없든, #736 리뷰 재현)
                        "npx --password '--synth-secret-1'", "npx --password --synth-secret-1", "npx \"--synth=x\"",
                        "npx --Synth-Secret", "npx --synth_secret.1"):
            with self.subTest(command=command):
                picked, _ = observed_lines([{"command": {"command": command}}], ["npx", "mysql"])
                self.assertNotIn("synth", json.dumps(picked))

    def test_only_unquoted_shell_operators_are_kept(self):
        # 따옴표로 감싼 기호만의 값은 연산자가 아니라 문자열 인자다(#736 리뷰 재현: 25자 합성 비밀번호가 남던 것)
        secret = "&&&|;;<><>&&&|;;<><>&&|<>"
        self.assertEqual(len(secret), 25)
        for command in (f"npx --password '{secret}'", f'npx --password "{secret}"', f"npx --password {secret[:2]}\\{secret[2:]}"):
            with self.subTest(command=command):
                picked, _ = observed_lines([{"command": {"command": command}}], ["npx"])
                self.assertNotIn(secret, json.dumps(picked))
                self.assertNotIn("&&&", json.dumps(picked))
        self.assertEqual(command_shape(f"npx --password '{secret}'", ["npx"]), "npx --<opt> <arg>")
        # 따옴표 안 '&&'는 연산자가 아니라 printf 인자다: npx가 별도 명령처럼 읽히지 않는다
        self.assertEqual(command_shape("printf '%s' '&&' npx", ["npx"]), "<arg> <arg> <arg> npx")
        self.assertEqual(command_shape("a && npx x || b; c | d >> f 2>&1 &", ["npx"]),
                         "<arg> && npx <arg> || <arg> ; <arg> | <arg> >> <arg> <arg> >& <arg> &")
        # 셸 연산자 목록 밖의 기호 묶음(따옴표 밖)은 가린다
        self.assertEqual(command_shape("npx x ;;&| y &&& z", ["npx"]), "npx <arg> <op> <arg> <op> <arg>")
        # 따옴표·역슬래시가 든 이름·옵션 모양 낱말도 문자열 인자다
        self.assertEqual(command_shape("'npx' \\-x \"--long\"", ["npx"]), "<arg> <arg> <arg>")

    def test_shell_tokens_follow_shell_quoting(self):
        self.assertEqual(shell_tokens("a'b c'd \"e\\\"f\" g\\ h ''"),
                         [("ab cd", True, False), ('e"f', True, False), ("g h", True, False), ("", True, False)])
        self.assertEqual(shell_tokens('x "a\\nb" 2>&1'), [("x", False, False), ("a\\nb", True, False), ("2", False, False),
                                                          (">&", False, True), ("1", False, False)])
        for bad in ("a 'b", 'a "b', "a \\"):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                shell_tokens(bad)

    def test_unparseable_and_long_commands_are_withheld(self):
        for command, reason in (('npx run "open', "shape_unparseable"), ("npx " + "a" * MAX_SHAPE_INPUT_BYTES, "shape_input_too_long")):
            with self.subTest(reason=reason):
                (item,), _ = observed_lines([{"command": {"command": command}}], ["npx"])
                self.assertEqual(item, {"names": ["npx"], "shape": None, "withheld": reason})

    def test_bounds(self):
        many, observed = observed_lines([{"command": {"command": "npx x"}}] * (MAX_OBSERVED_LINES + 3), ["npx"])
        self.assertEqual((len(many), observed), (MAX_OBSERVED_LINES, MAX_OBSERVED_LINES + 3))
        flags = "npx " + " ".join(["-a"] * 600)  # 모양도 근거 상한으로 자른다
        (item,), _ = observed_lines([{"command": {"command": flags}}], ["npx"])
        self.assertTrue(item["shape"].endswith(" [...]"))
        self.assertEqual(len(item["shape"]), MAX_OBSERVED_LINE_CHARS + len(" [...]"))
        self.assertEqual(command_shape("npx -a", ["npx"]), "npx -a")
        # 긴 옵션 모양 낱말은 글자 모양과 상관없이 이름을 가린 --<opt>다(소문자 이름만 옵션으로 보지 않는다)
        self.assertEqual(command_shape("npx --Synth-Secret --synth_secret.1 --x=1 -- --", ["npx"]),
                         "npx --<opt> --<opt> --<opt>=<arg> <arg> <arg>")
        self.assertEqual(clip_shape("x" * 10), "x" * 10)


if __name__ == "__main__":
    unittest.main()
