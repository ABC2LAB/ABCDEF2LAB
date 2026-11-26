"""공식 회차 세션 집계(session_runs.py): 시도·무효 집계, §5.1 교대 순서·상한 경고, 완료 회차 manifest, 입력 오류 fail-closed."""

from __future__ import annotations

import contextlib
import hashlib
import io
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import synthetic_gt  # noqa: E402,F401  합성 정답 목록을 먼저 끼운다(정답표 노출 방지)

from manifest import load_manifests  # noqa: E402
from run_record import sign_run_record  # noqa: E402
from runner import RESULT_FORMAT  # noqa: E402
from session_runs import (  # noqa: E402
    FAULT_STALL_LIMIT_PER_SIDE, LLM_SUMMARY_NAME, RESULT_MISSING, SessionError, auxiliary, build_manifest, expected_order,
    fault_stall_tally, main, rate_limit_table, read_session, tally,
)

CONDITION = {"baseline": "원본@35b", "ours": "우리@35b"}
KEY = bytes(range(32))


STALL_T = 180  # 시험용 멈춤 판정 T(초)

def write(path: Path, value: dict, signed: bool) -> None:
    """러너처럼 완료 결과·회차 기록은 서명해서, 무효 결과는 서명 없이 쓴다."""
    path.write_text(json.dumps(sign_run_record(value, KEY) if signed else value), encoding="utf-8")


class Session:
    """합성 runs_root. 회차 디렉터리 이름은 러너 run_id 형식(<쪽>-<UTC 시각>-<16진 8자리>)."""

    def __init__(self, root: Path):
        self.root = root
        self.count = 0

    def _dir(self, side: str, minute: int) -> Path:
        self.count += 1
        directory = self.root / f"{side}-20261110t10{minute:02d}00z-{self.count:08x}"
        directory.mkdir()
        return directory

    def _result(self, directory: Path, side: str, minute: int, **fields) -> None:
        result = {"format": RESULT_FORMAT, "run_id": directory.name, "side": side,
                  "started_at": f"2026-11-10T10:{minute:02d}:00+00:00", **fields}
        write(directory / "runner-result.json", result, signed=fields.get("status") == "complete")

    def complete(self, side: str, minute: int, repeat: int, condition: str | None = None,
                 task: str = "t" * 64, e2_mode: object = "none", total_requests: object = 400,
                 stall: int | None = STALL_T, **result_fields) -> Path:
        directory = self._dir(side, minute)
        # 러너가 설정한 T로 판정한 멈춤 기록(결정안 §7). stall=None이면 판정하지 않은 회차다.
        fault_stall = ({"format": "abc2lab-eval-runner-fault-stall-v3", "judged": False} if stall is None else
                       {"format": "abc2lab-eval-runner-fault-stall-v3", "judged": True, "stall_seconds": stall, "stalls": 0})
        self._result(directory, side, minute, status="complete", task_request_sha256=task.replace("t", "a"),
                     e2_mode=e2_mode, fault_stall=fault_stall, **result_fields)
        record = {"run_id": directory.name, "engine": side, "condition": condition or CONDITION[side],
                  "repeat_index": repeat, "model_tag": "qwen/qwen3.6-35b-a3b", "reset_generation": "r" * 64,
                  "total_requests": total_requests, "blocked_attempts": None, "elapsed_seconds": 1200.5,
                  "artifacts": {"pre": {"path": "pre/normalized.json"}, "post": {"path": "post/normalized.json"},
                                "records": None}}
        write(directory / "run-record.json", record, signed=True)
        return directory

    def invalid(self, side: str, minute: int, *reasons: str) -> Path:
        directory = self._dir(side, minute)
        self._result(directory, side, minute, status="invalid", reasons=list(reasons), failed_step=reasons[0])
        return directory

    def missing(self, side: str, minute: int) -> Path:
        return self._dir(side, minute)


class SessionCase(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name) / "runs"
        self.root.mkdir()
        self.s = Session(self.root)
        self.key_file = Path(temp.name) / "runner.key"
        fd = os.open(self.key_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "wb") as out:
            out.write(KEY)

    def official(self):
        """N=3, B O O B B O 전부 유효."""
        for minute, (side, repeat) in enumerate([("baseline", 0), ("ours", 0), ("ours", 1), ("baseline", 1),
                                                 ("baseline", 2), ("ours", 2)]):
            self.s.complete(side, minute * 5, repeat)


class TestFixtureE2(SessionCase):
    """가짜 E²(e2_mode=stub) 회차는 공식 세션 집계에 넣지 않는다(프로토콜 §2). 스모크만 플래그로 연다."""

    def test_stub_runs_are_refused_unless_allowed(self):
        self.s.complete("baseline", 1, 0)
        self.s.complete("ours", 2, 0, e2_mode="stub")
        with self.assertRaises(SessionError) as caught:
            read_session(self.root, KEY)
        self.assertTrue(any("e2_mode=stub" in e for e in caught.exception.errors), caught.exception.errors)
        self.assertEqual(len(read_session(self.root, KEY, allow_fixture_e2=True)), 2)

    def test_product_runs_count_without_the_fixture_flag(self):
        self.s.complete("baseline", 1, 0)
        self.s.complete("ours", 2, 0, e2_mode="product")
        self.assertEqual(len(read_session(self.root, KEY)), 2)

    def test_missing_or_unknown_e2_mode_is_refused(self):
        for minute, value in enumerate((None, "other", "")):
            with self.subTest(value=value):
                directory = self.s.complete("ours", minute, minute, e2_mode=value)
                with self.assertRaises(SessionError) as caught:
                    read_session(self.root, KEY, allow_fixture_e2=True)
                self.assertTrue(any("e2_mode" in e for e in caught.exception.errors), caught.exception.errors)
                for child in directory.iterdir():
                    child.unlink()
                directory.rmdir()


class TestSplitSession(SessionCase):
    """탐색 분리 배치 세션(설계안 §6): 작업 요청 지문은 쪽마다 하나다(B 작업 생성 본문·O 설치·등록 조건 지문)."""

    SPLIT_B = {"run_conditions": {"split": True}}

    def test_each_side_keeps_its_own_task_digest(self):
        self.s.complete("baseline", 1, 0, task="b" * 64, **self.SPLIT_B)
        self.s.complete("ours", 2, 0, task="c" * 64, e2_mode="split_product")
        self.s.complete("ours", 3, 1, task="c" * 64, e2_mode="split_product")
        self.assertEqual(len(read_session(self.root, KEY)), 3)

    def test_one_side_with_two_digests_is_refused(self):
        self.s.complete("baseline", 1, 0, task="b" * 64, **self.SPLIT_B)
        self.s.complete("ours", 2, 0, task="c" * 64, e2_mode="split_product")
        self.s.complete("ours", 3, 1, task="d" * 64, e2_mode="split_product")
        with self.assertRaises(SessionError) as caught:
            read_session(self.root, KEY)
        self.assertTrue(any("작업 생성 요청" in e for e in caught.exception.errors), caught.exception.errors)

    def test_split_and_direct_rounds_do_not_mix(self):
        # 엔진 직접 배치 O(분리 배치 표시 없음)와 분리 배치 B가 한 세션에 있으면 멈춘다(작업 지문 규칙이 달라진다)
        self.s.complete("baseline", 1, 0, task="b" * 64, **self.SPLIT_B)
        self.s.complete("ours", 2, 0, task="b" * 64)
        with self.assertRaises(SessionError) as caught:
            read_session(self.root, KEY)
        self.assertTrue(any("섞임" in e for e in caught.exception.errors), caught.exception.errors)

    def test_direct_layout_keeps_one_digest_for_the_session(self):
        self.s.complete("baseline", 1, 0, task="b" * 64)
        self.s.complete("ours", 2, 0, task="c" * 64)
        with self.assertRaises(SessionError) as caught:
            read_session(self.root, KEY)
        self.assertTrue(any("작업 생성 요청" in e for e in caught.exception.errors), caught.exception.errors)


class TestAuxiliary(SessionCase):
    def _run(self, side, minute, repeat, stop, over, seconds):
        directory = self.s.complete(side, minute, repeat)
        result = json.loads((directory / "runner-result.json").read_text(encoding="utf-8"))
        result.pop("signature")
        result["meter"] = {"stop_reason": stop, "over_budget": over}
        write(directory / "runner-result.json", result, signed=True)
        record = json.loads((directory / "run-record.json").read_text(encoding="utf-8"))
        record.pop("signature")
        record["timeline"] = {"engine_start": {"at": "2026-11-10T10:00:00.000000000Z"},
                              "engine_end": {"at": f"2026-11-10T10:00:{seconds:02d}.000000000Z"}}
        write(directory / "run-record.json", record, signed=True)

    def test_each_side_gets_its_stop_reasons_overrun_and_duration(self):
        self._run("baseline", 0, 0, "time_budget", 0, 10)
        self._run("baseline", 1, 1, "request_budget", 2, 20)
        self._run("ours", 2, 0, "time_budget", 1, 30)
        lines = auxiliary(read_session(self.root, KEY))
        self.assertIn("| baseline | 2 | request_budget 1, time_budget 1 | 1 ± 1.41421 | 15 ± 7.07107 |", lines)
        self.assertIn("| ours | 1 | time_budget 1 | 1 (n=1) | 30 (n=1) |", lines)

    def test_missing_values_are_not_counted_as_zero(self):
        self._run("baseline", 0, 0, "time_budget", 0, 10)
        self.s.complete("baseline", 1, 1)  # meter·timeline 없음
        lines = auxiliary(read_session(self.root, KEY))
        self.assertIn("| baseline | 2 | time_budget 1, 미기록 1 | — | — |", lines)

    def test_split_o_stop_reason_comes_from_the_summary_top_level(self):
        # 2026-11-23 사용자: 분리 배치 O는 계측기 요약(meter)이 없고 종료 사유를 회차 요약 맨 위(stop_reason)에 남긴다.
        # '미기록'으로 나오면 안 된다. 예산 초과분은 O 요약에 칸이 없어 모름("—")이다.
        self.s.complete("ours", 0, 0, stop_reason="time_budget", forwarded_at_stop=153)
        self.s.complete("ours", 2, 1, stop_reason="engine_completed")
        self.s.complete("ours", 4, 2)  # 어디에도 없으면 그대로 미기록
        lines = auxiliary(read_session(self.root, KEY))
        self.assertIn("| ours | 3 | engine_completed 1, time_budget 1, 미기록 1 | — | — |", lines)

    def test_a_boolean_overrun_is_not_a_number(self):
        self._run("baseline", 0, 0, "time_budget", True, 10)
        lines = auxiliary(read_session(self.root, KEY))
        self.assertIn("| baseline | 1 | time_budget 1 | — | 10 (n=1) |", lines)

    def test_the_command_prints_the_table(self):
        self._run("baseline", 0, 0, "time_budget", 0, 10)
        out = io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(main([str(self.root), "--repeats", "3", "--runner-key-file", str(self.key_file)]), 0)
        self.assertIn("보조 지표(프로토콜 §5", out.getvalue())
        self.assertIn("| baseline | 1 | time_budget 1 | 0 (n=1) | 10 (n=1) |", out.getvalue())

    def test_invalid_attempts_stay_out_and_an_empty_side_shows_dashes(self):
        self._run("ours", 0, 0, "time_budget", 0, 10)
        self.s.invalid("ours", 1, "probe_failed")
        lines = auxiliary(read_session(self.root, KEY))
        self.assertIn("| ours | 1 | time_budget 1 | 0 (n=1) | 10 (n=1) |", lines)
        self.assertIn("| baseline | 0 | — | — | — |", lines)


class TestRateLimitTable(SessionCase):
    """회차별 모델 호출 429 표(2026-11-26 사용자): 완료 회차는 서명된 근거 해시와 맞는 llm-summary.json만, 무효는 서명 밖 표시."""

    def _summary(self, directory: Path, calls, limited, sign: bool = True) -> None:
        raw = json.dumps({"forwarded_requests": calls, "rate_limited": limited}).encode()
        (directory / LLM_SUMMARY_NAME).write_bytes(raw)
        if sign:
            result = json.loads((directory / "runner-result.json").read_text(encoding="utf-8"))
            result.pop("signature")
            result["evidence"] = {LLM_SUMMARY_NAME: hashlib.sha256(raw).hexdigest()}
            write(directory / "runner-result.json", result, signed=True)

    def _row(self, lines, run_id):
        return next(line for line in lines if f"`{run_id}`" in line)

    def test_signed_summaries_give_counts_ratio_and_side_totals(self):
        b = self.s.complete("baseline", 0, 0)
        o = self.s.complete("ours", 5, 0)
        o2 = self.s.complete("ours", 10, 1)
        self._summary(b, 49, 0)
        self._summary(o, 184, 10)
        self._summary(o2, 112, 45)
        lines = rate_limit_table(read_session(self.root, KEY))
        self.assertEqual(self._row(lines, b.name), f"| 1 | B | `{b.name}` | 2026-11-10 10:00 | 유효 | 49 | 0 | 0.0% |")
        self.assertEqual(self._row(lines, o.name), f"| 2 | O | `{o.name}` | 2026-11-10 10:05 | 유효 | 184 | 10 | 5.4% |")
        self.assertEqual(self._row(lines, o2.name), f"| 3 | O | `{o2.name}` | 2026-11-10 10:10 | 유효 | 112 | 45 | 40.2% |")
        self.assertIn("| B | 1 | 1 | 49 | 0 | 0.0% | 0.0% |", lines)
        self.assertIn("| O | 2 | 2 | 296 | 55 | 18.6% | 40.2% |", lines)

    def test_a_summary_that_does_not_match_the_signed_digest_is_not_used(self):
        o = self.s.complete("ours", 0, 0)
        self._summary(o, 100, 5)
        (o / LLM_SUMMARY_NAME).write_text(json.dumps({"forwarded_requests": 100, "rate_limited": 0}), encoding="utf-8")
        lines = rate_limit_table(read_session(self.root, KEY))
        self.assertEqual(self._row(lines, o.name), f"| 1 | O | `{o.name}` | 2026-11-10 10:00 | 유효 | — | — | — |")
        self.assertIn("| O | 1 | 0 | — | — | — | — |", lines)

    def test_a_complete_run_without_a_signed_digest_shows_dashes(self):
        o = self.s.complete("ours", 0, 0)
        self._summary(o, 100, 5, sign=False)  # 파일은 있는데 서명된 근거 목록에 없음
        lines = rate_limit_table(read_session(self.root, KEY))
        self.assertEqual(self._row(lines, o.name), f"| 1 | O | `{o.name}` | 2026-11-10 10:00 | 유효 | — | — | — |")

    def test_invalid_and_missing_results_are_marked_outside_the_signature_and_left_out_of_totals(self):
        v = self.s.complete("ours", 0, 0)
        self._summary(v, 10, 1)
        bad = self.s.invalid("ours", 5, "provider_fault_stall")
        self._summary(bad, 30, 12, sign=False)
        gone = self.s.missing("ours", 10)
        lines = rate_limit_table(read_session(self.root, KEY))
        self.assertEqual(self._row(lines, bad.name),
                         f"| 2 | O | `{bad.name}` | 2026-11-10 10:05 | 무효 `provider_fault_stall` | 30 | 12 | 40.0% (서명 밖) |")
        self.assertEqual(self._row(lines, gone.name),
                         f"| 3 | O | `{gone.name}` | 2026-11-10 10:10 | 무효 `{RESULT_MISSING}` | — | — | — |")
        self.assertIn("| O | 1 | 1 | 10 | 1 | 10.0% | 10.0% |", lines)

    def test_malformed_counts_are_not_read_as_zero(self):
        cases = [(5, 6), (True, 0), (10, -1), ("10", 1), (None, 0)]
        dirs = []
        for minute, (calls, limited) in enumerate(cases):
            d = self.s.complete("baseline", minute, minute)
            self._summary(d, calls, limited)
            dirs.append(d)
        lines = rate_limit_table(read_session(self.root, KEY))
        for d in dirs:
            self.assertTrue(self._row(lines, d.name).endswith("| 유효 | — | — | — |"), d.name)
        self.assertIn("| B | 5 | 0 | — | — | — | — |", lines)

    def test_zero_calls_has_no_ratio(self):
        b = self.s.complete("baseline", 0, 0)
        self._summary(b, 0, 0)
        lines = rate_limit_table(read_session(self.root, KEY))
        self.assertEqual(self._row(lines, b.name), f"| 1 | B | `{b.name}` | 2026-11-10 10:00 | 유효 | 0 | 0 | — |")
        self.assertIn("| B | 1 | 1 | 0 | 0 | — | — |", lines)

    def test_the_command_prints_the_section(self):
        o = self.s.complete("ours", 0, 0)
        self._summary(o, 20, 2)
        out = io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(main([str(self.root), "--repeats", "3", "--runner-key-file", str(self.key_file)]), 0)
        self.assertIn("회차별 모델 호출 429", out.getvalue())
        self.assertIn(f"| 1 | O | `{o.name}` | 2026-11-10 10:00 | 유효 | 20 | 2 | 10.0% |", out.getvalue())


class TestOrder(unittest.TestCase):
    def test_pairs_alternate_which_side_goes_first(self):
        self.assertEqual(expected_order(3), ["B", "O", "O", "B", "B", "O"])
        self.assertEqual(expected_order(4), ["B", "O", "O", "B", "B", "O", "O", "B"])


class TestTally(SessionCase):
    def test_clean_session_has_no_warnings(self):
        self.official()
        lines, warnings = tally(read_session(self.root, KEY), 3)
        self.assertEqual(warnings, [])
        self.assertIn("| baseline | 원본@35b | 3 | 3 | 0 | 0% | — |", lines)
        self.assertIn("| ours | 우리@35b | 3 | 3 | 0 | 0% | — |", lines)
        self.assertIn("유효 순서: B O O B B O (기대 B O O B B O: 일치)", lines)

    def test_zero_request_runs_stay_valid_and_warn_per_side(self):
        self.s.complete("baseline", 1, 0, total_requests=0)
        self.s.complete("ours", 2, 0, total_requests=None)
        self.s.complete("ours", 3, 1, total_requests=5)
        lines, warnings = tally(read_session(self.root, KEY), 1)
        self.assertIn("| baseline | 원본@35b | 1 | 1 | 0 | 0% | — |", lines)
        zero = [w for w in warnings if w.startswith("경고[대상 요청 0]")]
        self.assertEqual(len(zero), 1)
        self.assertIn("baseline: 유효 회차 1개", zero[0])

    def test_invalid_attempts_are_counted_per_side_with_their_first_reason(self):
        self.s.complete("baseline", 0, 0)
        self.s.invalid("ours", 5, "drain_timeout", "llm_unverified")
        self.s.complete("ours", 10, 0)
        self.s.missing("ours", 15)
        lines, warnings = tally(read_session(self.root, KEY), 3)
        self.assertIn(f"| ours | 우리@35b | 3 | 1 | 2 | 67% | drain_timeout 1, {RESULT_MISSING} 1 |", lines)
        self.assertIn("시도 순서(시작 시각): B✓ O✗ O✓ O✗", lines)
        # 재실행이 교대 순서를 지켰으니 유효 순서는 기대의 앞부분과 같다.
        self.assertIn("유효 순서: B O (기대 B O O B B O: 일치)", lines)
        self.assertTrue(any(w.startswith("경고[유효 부족] ours") for w in warnings))

    def test_fault_stall_counts_are_always_shown_per_side(self):
        # 결정안 §7: 쪽별 provider_fault_stall 무효 수는 0이어도 늘 싣는다. 첫 사유로만 센다(러너가 멈춤을 첫 사유로 둔다).
        self.s.complete("baseline", 0, 0)
        self.s.invalid("ours", 5, "provider_fault_stall", "llm_generation_unverified")
        self.s.invalid("ours", 7, "drain_timeout", "provider_fault_stall")
        self.s.complete("ours", 10, 0)
        lines, failures = fault_stall_tally(read_session(self.root, KEY))
        self.assertIn(f"| baseline | 0 | 0 | 0 | {FAULT_STALL_LIMIT_PER_SIDE} | 0 (0) |", lines)
        self.assertIn(f"| ours | 1 | 0 | 1 | {FAULT_STALL_LIMIT_PER_SIDE} | 0 (1) |", lines)  # 멈춤 기록 파일 없음 → 모름
        self.assertEqual(failures, [])

    def test_stall_inflight_model_calls_are_shown_per_side(self):
        # 사용자 2026-11-22: 멈춤 무효 회차에서 멈춤 사건 때 모델 호출이 진행 중이었는지 쪽별로 센다(공정성 기록, 판정은 그대로).
        # 무효 결과는 서명이 없어 러너 근거 fault-stall.json(v3)을 읽는다. 칸이 없거나(v3 전) 숫자가 아니면 모름
        def stalled(side, minute, events):
            directory = self.s.invalid(side, minute, "provider_fault_stall")
            if events is not None:
                (directory / "fault-stall.json").write_text(json.dumps({"format": "abc2lab-eval-runner-fault-stall-v3",
                                                                        "judged": True, "events": events}))
        stall = lambda **kw: dict({"stall": True, "inflight_at_fault": 1}, **kw)  # noqa: E731
        stalled("baseline", 1, [stall(inflight_model_calls_at_fault=1)])          # 긴 스트리밍 호출 진행 중
        stalled("baseline", 3, [stall(inflight_model_calls_at_fault=0), {"stall": False, "inflight_model_calls_at_fault": 2}])
        stalled("ours", 5, [stall()])                                             # v3 전 기록 → 모름
        stalled("ours", 7, [stall(inflight_model_calls_at_fault=True)])           # 숫자가 아님 → 모름
        lines, _ = fault_stall_tally(read_session(self.root, KEY))
        self.assertIn(f"| baseline | 2 | 0 | 2 | {FAULT_STALL_LIMIT_PER_SIDE} | 1 (0) |", lines)
        self.assertIn(f"| ours | 2 | 0 | 2 | {FAULT_STALL_LIMIT_PER_SIDE} | 0 (2) |", lines)

    def test_provider_fault_end_counts_with_stalls(self):
        # 고정 초안 §26: 러너 상한 전에 제공자 장애로 끝난 회차는 멈춤과 같은 상한에 같이 센다
        self.s.invalid("baseline", 5, "provider_fault_stall")
        self.s.invalid("baseline", 7, "provider_fault_end")
        lines, failures = fault_stall_tally(read_session(self.root, KEY))
        self.assertIn(f"| baseline | 1 | 1 | 2 | {FAULT_STALL_LIMIT_PER_SIDE} | 0 (1) |", lines)
        self.assertEqual(len(failures), 1)
        self.assertIn("provider_fault_end 1회", failures[0])

    def test_fault_stall_limit_on_one_side_fails_the_whole_measurement(self):
        # 고정 초안 §3: 한쪽이라도 상한(2) 이상이면 남은 유효 회차가 있어도 측정 전체 실패, manifest를 쓰지 않는다
        self.official()
        for minute in (40, 45):
            self.s.invalid("baseline", minute, "provider_fault_stall")
        lines, failures = fault_stall_tally(read_session(self.root, KEY))
        self.assertEqual(len(failures), 1)
        self.assertTrue(failures[0].startswith("측정 실패[멈춤 무효] baseline: provider_fault_stall 2회"), failures)
        out = self.root.parent / "manifest.json"
        with contextlib.redirect_stdout(io.StringIO()) as printed, contextlib.redirect_stderr(io.StringIO()) as err:
            code = main([str(self.root), "--repeats", "3", "--runner-key-file", str(self.key_file), "--manifest", str(out)])
        self.assertEqual(code, 3)
        self.assertFalse(out.exists())
        self.assertIn("| baseline | 2 |", printed.getvalue())
        self.assertIn("측정 실패[멈춤 무효]", err.getvalue())

    def test_one_fault_stall_per_side_still_writes_the_manifest(self):
        self.official()
        self.s.invalid("baseline", 40, "provider_fault_stall")
        self.s.invalid("ours", 45, "provider_fault_stall")
        out = self.root.parent / "manifest.json"
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            code = main([str(self.root), "--repeats", "3", "--runner-key-file", str(self.key_file), "--manifest", str(out)])
        self.assertEqual(code, 0)
        self.assertEqual(len(json.loads(out.read_text(encoding="utf-8"))["runs"]), 6)

    def test_unjudged_or_mixed_t_runs_stop_the_session(self):
        # 멈춤 여부를 모르는 완료 회차(T 없이 돈 회차)는 집계하지 않는다. 스모크만 플래그로 연다.
        self.s.complete("baseline", 0, 0)
        self.s.complete("ours", 5, 0, stall=None)
        with self.assertRaises(SessionError) as caught:
            read_session(self.root, KEY)
        self.assertTrue(any("판정하지 않은 완료 회차" in e for e in caught.exception.errors), caught.exception.errors)
        self.assertEqual(len(read_session(self.root, KEY, allow_unjudged_fault_stall=True)), 2)
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(main([str(self.root), "--repeats", "3", "--runner-key-file", str(self.key_file)]), 2)
            self.assertEqual(main([str(self.root), "--repeats", "3", "--runner-key-file", str(self.key_file),
                                   "--allow-unjudged-fault-stall"]), 0)
        # B·O가 다른 T로 판정했으면 플래그가 있어도 비교하지 않는다
        self.s.complete("ours", 10, 1, stall=600)
        for allow in (False, True):
            with self.subTest(allow=allow), self.assertRaises(SessionError) as caught:
                read_session(self.root, KEY, allow_unjudged_fault_stall=allow)
            self.assertTrue(any("T가 회차마다 다름 [180, 600]" in e for e in caught.exception.errors), caught.exception.errors)

    def test_valid_order_that_breaks_the_alternation_is_flagged(self):
        for minute, (side, repeat) in enumerate([("baseline", 0), ("ours", 0), ("baseline", 1), ("ours", 1)]):
            self.s.complete(side, minute * 5, repeat)
        lines, warnings = tally(read_session(self.root, KEY), 3)
        self.assertIn("유효 순서: B O B O (기대 B O O B B O: 불일치)", lines)
        self.assertTrue(any(w.startswith("경고[교대 순서]") for w in warnings))

    def test_more_than_two_n_attempts_on_one_side_is_flagged(self):
        self.official()
        for minute in range(4):
            self.s.invalid("baseline", 40 + minute, "seal_failed")
        _, warnings = tally(read_session(self.root, KEY), 3)
        self.assertEqual([w for w in warnings if w.startswith("경고[시도 상한]")],
                         ["경고[시도 상한] baseline: 시도 7회 > 2N(6) — 더 돌리지 말고 원인부터 고친다"])
        self.assertFalse(any("ours" in w for w in warnings))

    def test_exactly_two_n_attempts_is_not_flagged(self):
        self.official()
        for minute in range(3):
            self.s.invalid("ours", 40 + minute, "seal_failed")
        _, warnings = tally(read_session(self.root, KEY), 3)
        self.assertEqual(warnings, [])


class TestManifest(SessionCase):
    def test_only_complete_runs_in_start_order_and_the_real_loader_accepts_it(self):
        self.s.invalid("baseline", 0, "compose_up_failed")
        self.official()
        payload = build_manifest(self.root, read_session(self.root, KEY))
        self.assertEqual([r["engine"] for r in payload["runs"]], ["baseline", "ours", "ours", "baseline", "baseline", "ours"])
        first = payload["runs"][0]
        directory = (self.root / first["run_id"]).resolve()
        self.assertEqual(first["run_record_path"], str(directory / "run-record.json"))
        self.assertEqual(first["snapshot_path"], str(directory / "post/normalized.json"))
        self.assertEqual(first["pre_snapshot_path"], str(directory / "pre/normalized.json"))
        out = self.root.parent / "manifest.json"
        out.write_text(json.dumps(payload, ensure_ascii=False), encoding="utf-8")
        loaded = load_manifests(str(out))
        self.assertEqual([(m.condition, m.repeat_index) for m in loaded][:2], [("원본@35b", 0), ("우리@35b", 0)])

    def test_main_refuses_to_overwrite_an_existing_manifest(self):
        self.official()
        out = self.root.parent / "manifest.json"
        out.write_text("keep", encoding="utf-8")
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()) as err:
            code = main([str(self.root), "--repeats", "3", "--runner-key-file", str(self.key_file), "--manifest", str(out)])
        self.assertEqual(code, 2)
        self.assertIn("이미 있음", err.getvalue())
        self.assertEqual(out.read_text(encoding="utf-8"), "keep")

    def test_main_writes_a_new_manifest(self):
        self.official()
        out = self.root.parent / "manifest.json"
        with contextlib.redirect_stdout(io.StringIO()) as printed:
            self.assertEqual(main([str(self.root), "--repeats", "3", "--runner-key-file", str(self.key_file), "--manifest", str(out)]), 0)
        self.assertEqual(len(json.loads(out.read_text(encoding="utf-8"))["runs"]), 6)
        self.assertIn("완료 회차 6개", printed.getvalue())

    def test_main_requires_at_least_three_repeats(self):
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(main([str(self.root), "--repeats", "2", "--runner-key-file", str(self.key_file)]), 2)


class TestInputErrors(SessionCase):
    def reject(self, expected: str) -> None:
        with self.assertRaises(SessionError) as caught:
            read_session(self.root, KEY)
        self.assertTrue(any(expected in e for e in caught.exception.errors), caught.exception.errors)

    def edit(self, path: Path, resign: bool = True, **changes) -> None:
        """기본은 바꾼 뒤 다시 서명한다 — 그래야 서명 검사가 아니라 노리는 가드에 걸린다."""
        data = json.loads(path.read_text(encoding="utf-8"))
        signed = "signature" in data
        data.pop("signature", None)
        data.update(changes)
        write(path, data, signed=signed and resign)

    def test_malformed_result_stops_instead_of_being_skipped(self):
        directory = self.s.complete("ours", 0, 0)
        (directory / "runner-result.json").write_text("{", encoding="utf-8")
        self.reject("runner-result.json 읽기·형식 오류")

    def test_result_must_match_its_directory_side_format_and_status(self):
        for field, value in (("run_id", "ours-20261110t100000z-ffffffff"), ("side", "baseline"),
                             ("format", "other"), ("status", "done"), ("started_at", "2026-11-10T10:00:00")):
            with self.subTest(field=field):
                for child in self.root.iterdir():
                    for f in child.iterdir():
                        f.unlink()
                    child.rmdir()
                directory = self.s.complete("ours", 0, 0)
                self.edit(directory / "runner-result.json", **{field: value})
                self.reject("runner-result.json의 형식·run_id·side·started_at·status가 맞지 않음")

    def test_invalid_result_needs_a_reason(self):
        directory = self.s.invalid("ours", 0, "seal_failed")
        self.edit(directory / "runner-result.json", reasons=[])
        self.reject("무효 결과에 사유가 없음")

    def test_complete_result_needs_a_matching_signed_record(self):
        directory = self.s.complete("ours", 0, 0)
        (directory / "run-record.json").unlink()
        self.reject("run-record.json 읽기·형식 오류")
        directory = self.s.complete("baseline", 5, 0)
        self.edit(directory / "run-record.json", engine="ours")
        self.reject("run-record.json의 run_id·engine·condition·repeat_index가 맞지 않음")

    def test_complete_record_needs_snapshot_paths(self):
        directory = self.s.complete("ours", 0, 0)
        self.edit(directory / "run-record.json", artifacts={"pre": {"path": "pre/normalized.json"}})
        self.reject("pre·post 스냅샷 경로가 없음")

    def test_one_side_cannot_mix_condition_names_or_repeat_indexes(self):
        self.s.complete("ours", 0, 0)
        self.s.complete("ours", 5, 1, condition="우리@35b-다른")
        self.s.complete("ours", 10, 1)
        with self.assertRaises(SessionError) as caught:
            read_session(self.root, KEY)
        errors = " ".join(caught.exception.errors)
        self.assertIn("조건 이름이 섞임", errors)
        self.assertIn("같은 repeat_index의 완료 회차가 둘 이상 [1]", errors)

    def test_every_complete_run_used_the_same_task_request(self):
        self.s.complete("baseline", 0, 0)
        self.s.complete("ours", 5, 0, task="b" * 64)
        self.reject("작업 생성 요청이 회차마다 다름(해시 2종)")

    def test_complete_result_needs_its_task_request_hash(self):
        directory = self.s.complete("ours", 0, 0)
        self.edit(directory / "runner-result.json", task_request_sha256=None)
        self.reject("완료 결과에 작업 요청 해시가 없음")

    def test_result_hash_changed_without_resigning_is_rejected(self):
        # 리뷰 재현: B 결과의 작업 해시만 A로 바꾸고 재서명하지 않으면, 같은 작업처럼 보여도 거부해야 한다.
        self.s.complete("baseline", 0, 0)
        tampered = self.s.complete("ours", 5, 0, task="b" * 64)
        self.edit(tampered / "runner-result.json", resign=False, task_request_sha256="a" * 64)
        self.reject("runner-result.json 서명 불일치")

    def test_record_changed_without_resigning_is_rejected(self):
        directory = self.s.complete("ours", 0, 0)
        self.edit(directory / "run-record.json", resign=False, repeat_index=1)
        self.reject("run-record.json 서명 불일치")

    def test_complete_files_need_a_signature_from_this_key(self):
        for name in ("runner-result.json", "run-record.json"):
            for label, change in (("missing", lambda d: d.pop("signature")),
                                  ("other key", lambda d: d.update(sign_run_record(
                                      {k: v for k, v in d.items() if k != "signature"}, bytes(32))))):
                with self.subTest(file=name, label=label):
                    for child in self.root.iterdir():
                        for f in child.iterdir():
                            f.unlink()
                        child.rmdir()
                    path = self.s.complete("ours", 0, 0) / name
                    data = json.loads(path.read_text(encoding="utf-8"))
                    change(data)
                    path.write_text(json.dumps(data), encoding="utf-8")
                    self.reject(f"{name} 서명 불일치")

    def test_main_needs_a_readable_runner_key(self):
        self.official()
        with contextlib.redirect_stderr(io.StringIO()) as err:
            self.assertEqual(main([str(self.root), "--repeats", "3", "--runner-key-file",
                                   str(self.root.parent / "nope.key")]), 2)
        self.assertIn("러너 키를 읽지 못함", err.getvalue())

    def test_other_directories_in_runs_root_are_errors(self):
        (self.root / "notes").mkdir()
        self.reject("notes: 러너 회차 디렉터리 이름이 아님")


if __name__ == "__main__":
    unittest.main()
