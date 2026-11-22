"""공식 회차 세션 실행(official_session) 시험. 러너·Docker·네트워크는 부르지 않는다(가짜 실행·점검 모드)."""

from __future__ import annotations

import contextlib
import io
import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from decimal import Decimal
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import official_session as o  # noqa: E402
from official_session import Outcome, Schedule, parse_outcomes, run_session  # noqa: E402

STALL = "provider_fault_stall"


def drive(outcomes: list[Outcome], repeats: int = 3) -> tuple[Schedule, list[tuple[str, int]], list[dict]]:
    schedule, calls, logs = Schedule(repeats), [], []
    pending = list(outcomes)

    def run_one(side: str, repeat: int) -> Outcome:
        calls.append((o.LETTER_OF[side], repeat))
        return pending.pop(0) if pending else Outcome("complete")

    run_session(schedule, run_one, logs.append)
    return schedule, calls, logs


class TestSchedule(unittest.TestCase):
    def test_all_complete_follows_boobbo_with_repeat_numbers(self):
        schedule, calls, logs = drive([])
        self.assertEqual(calls, [("B", 0), ("O", 0), ("O", 1), ("B", 1), ("B", 2), ("O", 2)])
        self.assertTrue(schedule.done())
        self.assertEqual(logs[-1]["event"], "summary")
        self.assertEqual((logs[-1]["complete"], logs[-1]["stop_reason"]), (True, None))
        self.assertEqual(logs[-1]["order"], ["B", "O", "O", "B", "B", "O"])

    def test_invalid_reruns_the_same_slot_with_the_same_repeat(self):
        schedule, calls, _ = drive([Outcome("complete"), Outcome("invalid", ["window_boundary_changed"])])
        self.assertEqual(calls, [("B", 0), ("O", 0), ("O", 0), ("O", 1), ("B", 1), ("B", 2), ("O", 2)])
        self.assertEqual(schedule.attempts, {"baseline": 3, "ours": 4})
        self.assertTrue(schedule.done())

    def test_attempt_limit_2n_stops_before_the_seventh_attempt(self):
        # B가 계속 무효면 6번째 시도 뒤 7번째를 시작하지 않는다(2N = 6)
        schedule, calls, logs = drive([Outcome("invalid", ["drain_timeout"])] * 10)
        self.assertEqual(calls, [("B", 0)] * 6)
        self.assertEqual(schedule.stop_reason, "attempt_limit:baseline")
        self.assertFalse(schedule.done())
        self.assertFalse(schedule.failed)
        self.assertEqual(logs[-1]["stop_reason"], "attempt_limit:baseline")

    def test_attempt_limit_counts_valid_and_invalid_of_one_side(self):
        # O: 유효 2 + 무효 4 = 6 시도, 유효 N 미만이라 마지막 O 칸을 시작하지 않는다
        outcomes = [Outcome("complete"),                                     # B0
                    Outcome("invalid", ["a"]), Outcome("invalid", ["a"]),   # O0 ×2
                    Outcome("complete"),                                     # O0
                    Outcome("invalid", ["a"]), Outcome("invalid", ["a"]),   # O1 ×2
                    Outcome("complete"),                                     # O1
                    Outcome("complete"), Outcome("complete")]                # B1, B2
        schedule, calls, _ = drive(outcomes)
        self.assertEqual(calls[-1], ("B", 2))
        self.assertEqual(schedule.attempts["ours"], 6)
        self.assertEqual(schedule.stop_reason, "attempt_limit:ours")

    def test_fault_stall_limit_is_measurement_failure(self):
        # 쪽별 provider_fault_stall 무효가 2회면 측정 전체 실패(다른 사유와 섞여 있어도 센다)
        schedule, calls, _ = drive([Outcome("invalid", [STALL]), Outcome("invalid", ["x", STALL])])
        self.assertEqual(calls, [("B", 0), ("B", 0)])
        self.assertEqual((schedule.stop_reason, schedule.failed), ("fault_stall_limit:baseline", True))
        # 한 번은 실패가 아니다
        schedule, _, _ = drive([Outcome("invalid", [STALL])])
        self.assertTrue(schedule.done())
        self.assertEqual(schedule.stalls["baseline"], 1)

    def test_config_error_is_not_an_attempt_and_stops(self):
        schedule, calls, _ = drive([Outcome("complete"), Outcome("config_error", ["runner_exit_2"])])
        self.assertEqual(calls, [("B", 0), ("O", 0)])
        self.assertEqual(schedule.attempts, {"baseline": 1, "ours": 0})
        self.assertEqual(schedule.stop_reason, "runner_config_error")

    def test_stop_outcome_keeps_its_reason_and_is_not_an_attempt(self):
        schedule, _, _ = drive([Outcome("stop", ["run_wait_timeout:B0"])])
        self.assertEqual(schedule.stop_reason, "run_wait_timeout:B0")
        self.assertEqual(schedule.attempts["baseline"], 0)

    def test_before_run_can_stop_the_session(self):
        schedule, logs = Schedule(3), []
        calls = []
        run_session(schedule, lambda side, repeat: calls.append(side) or Outcome("complete"), logs.append,
                    before_run=lambda side, repeat: "usage_cap" if side == "ours" else None)
        self.assertEqual(calls, ["baseline"])
        self.assertEqual(schedule.stop_reason, "usage_cap")
        self.assertEqual(logs[-2]["event"], "stopped_before_run")

    def test_repeats_one_and_invalid_input(self):
        schedule, calls, _ = drive([], repeats=1)
        self.assertEqual(calls, [("B", 0), ("O", 0)])
        for bad in (0, -1, 1.5, "3"):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                Schedule(bad)


class TestParseOutcomes(unittest.TestCase):
    def test_parse(self):
        got = parse_outcomes("complete, invalid:provider_fault_stall+drain_timeout ,config_error,stop:x")
        self.assertEqual([(g.status, g.reasons) for g in got],
                         [("complete", []), ("invalid", [STALL, "drain_timeout"]), ("config_error", []), ("stop", ["x"])])
        for bad in ("done", "invalid", "invalid:", "complete,xx"):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                parse_outcomes(bad)


class TestDryRunCli(unittest.TestCase):
    def test_dry_run_writes_log_and_exit_codes(self):
        with tempfile.TemporaryDirectory() as root:
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                code = o.main(["--session-dir", root + "/s", "--dry-run", "--outcomes", "complete,invalid:window_boundary_changed"])
            self.assertEqual(code, o.EXIT_DONE)
            lines = [json.loads(l) for l in Path(root, "s", o.LOG_NAME).read_text(encoding="utf-8").splitlines()]
            self.assertEqual([(l["side"], l["repeat"], l["status"]) for l in lines if l["event"] == "attempt"],
                             [("B", 0, "complete"), ("O", 0, "invalid"), ("O", 0, "complete"), ("O", 1, "complete"),
                              ("B", 1, "complete"), ("B", 2, "complete"), ("O", 2, "complete")])
            self.assertTrue(all(l["dry_run"] for l in lines))
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(o.main(["--session-dir", root + "/f", "--dry-run", "--outcomes", f"invalid:{STALL},invalid:{STALL}"]),
                                 o.EXIT_FAILED)
                self.assertEqual(o.main(["--session-dir", root + "/t", "--dry-run", "--outcomes", "stop:run_wait_timeout:B0"]),
                                 o.EXIT_STOPPED)
            with contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(o.main(["--session-dir", root + "/e", "--dry-run", "--outcomes", "bogus"]), o.EXIT_INPUT)
                self.assertEqual(o.main(["--session-dir", root + "/r"]), o.EXIT_INPUT)  # 실제 실행에 필요한 칸 없음


class TestResume(unittest.TestCase):
    def lines(self, *items, dry=False):
        return [{"event": "attempt", "side": side, "repeat": repeat, "status": status, "reasons": reasons, "dry_run": dry}
                for side, repeat, status, reasons in items]

    def test_replay_continues_from_the_next_slot(self):
        schedule = Schedule(3)
        n = o.replay(schedule, self.lines(("B", 0, "complete", []), ("O", 0, "invalid", ["x"]), ("O", 0, "complete", []))
                     + [{"event": "attempt", "side": "O", "repeat": 1, "status": "stop", "reasons": ["run_wait_timeout:O1"]},
                        {"event": "summary"}])
        self.assertEqual(n, 3)  # 멈춤 줄은 시도로 세지 않는다
        self.assertEqual(schedule.next_slot(), ("ours", 1))
        self.assertEqual(schedule.attempts, {"baseline": 1, "ours": 2})
        self.assertIsNone(schedule.stop_reason)

    def test_replay_rejects_out_of_order_and_dry_logs(self):
        with self.assertRaises(ValueError):
            o.replay(Schedule(3), self.lines(("O", 0, "complete", [])))
        with self.assertRaises(ValueError):
            o.replay(Schedule(3), self.lines(("B", 1, "complete", [])))
        with self.assertRaises(ValueError):
            o.replay(Schedule(3), self.lines(("B", 0, "complete", []), dry=True))

    def test_replay_keeps_stall_and_attempt_limits(self):
        schedule = Schedule(3)
        o.replay(schedule, self.lines(("B", 0, "invalid", [STALL]), ("B", 0, "invalid", [STALL])))
        self.assertEqual((schedule.stop_reason, schedule.failed), ("fault_stall_limit:baseline", True))

    def test_cli_resume(self):
        with tempfile.TemporaryDirectory() as root:
            session = Path(root, "s")
            session.mkdir()
            log = session / o.LOG_NAME
            log.write_text("\n".join(json.dumps(l) for l in self.lines(("B", 0, "complete", []))) + "\n")
            with contextlib.redirect_stderr(io.StringIO()):
                # 기록이 있는데 --resume 없이 시작하면 거부
                self.assertEqual(o.main(["--session-dir", str(session), "--dry-run"]), o.EXIT_INPUT)
                # --resume은 점검 모드와 같이 쓰지 않는다
                self.assertEqual(o.main(["--session-dir", str(session), "--dry-run", "--resume"]), o.EXIT_INPUT)
            # 기록만으로 이미 멈춘 세션은 잇지 않는다
            stalled = Path(root, "t")
            stalled.mkdir()
            (stalled / o.LOG_NAME).write_text("\n".join(json.dumps(l) for l in self.lines(
                ("B", 0, "invalid", [STALL]), ("B", 0, "invalid", [STALL]))) + "\n")
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(o.main(["--session-dir", str(stalled), "--resume", "--tree", root, "--config", root + "/c",
                                         "--preflight", root + "/p", "--admin-password-file", root + "/a"]), o.EXIT_FAILED)
            # 기록의 사용량 기준값이 숫자가 아니면 잇지 않는다
            bad_base = Path(root, "u")
            bad_base.mkdir()
            (bad_base / o.LOG_NAME).write_text(json.dumps({"event": "usage_base", "usage_usd": "x"}) + "\n")
            with contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(o.main(["--session-dir", str(bad_base), "--resume", "--tree", root, "--config", root + "/c",
                                         "--preflight", root + "/p", "--admin-password-file", root + "/a"]), o.EXIT_INPUT)


class TestWaits(unittest.TestCase):
    def test_wait_process_returns_code_or_kills_group_after_timeout(self):
        done = subprocess.Popen([sys.executable, "-c", "pass"], start_new_session=True)
        self.assertEqual(o.wait_process(done, timeout=30, grace=5), 0)
        slow = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"], start_new_session=True,
                                stderr=subprocess.DEVNULL)
        started = time.monotonic()
        self.assertIsNone(o.wait_process(slow, timeout=0.5, grace=5))
        self.assertLess(time.monotonic() - started, 20)
        self.assertIsNotNone(slow.poll())  # 시한 뒤 끝났다(SIGINT)

    def test_wait_process_escalates_to_sigkill(self):
        stubborn = subprocess.Popen([sys.executable, "-c",
                                     "import signal, time; signal.signal(signal.SIGINT, signal.SIG_IGN); time.sleep(60)"],
                                    start_new_session=True, stderr=subprocess.DEVNULL)
        time.sleep(0.3)
        self.assertIsNone(o.wait_process(stubborn, timeout=0.2, grace=0.5))
        self.assertIsNotNone(stubborn.poll())

    def test_wait_containers_gone(self):
        counts = iter([3, 1, 0])
        self.assertTrue(o.wait_containers_gone(lambda: next(counts), timeout=100, interval=1, sleep=lambda s: None))
        clock = iter([0, 0, 50, 101])
        self.assertFalse(o.wait_containers_gone(lambda: 2, timeout=100, interval=1, sleep=lambda s: None,
                                                clock=lambda: next(clock)))
        # 읽기 실패(None)는 0으로 보지 않는다
        clock = iter([0, 101])
        self.assertFalse(o.wait_containers_gone(lambda: None, timeout=100, interval=1, sleep=lambda s: None,
                                                clock=lambda: next(clock)))


class TestRealRunner(unittest.TestCase):
    RUN_ID = "baseline-20261120t000000z-0000abcd"

    def setUp(self):
        self.root = Path(tempfile.mkdtemp())
        self.runs = self.root / "runs"
        self.runs.mkdir()
        self.runner = o.RealRunner(self.root, self.root / "c.json", self.root / "p.json", self.runs, self.root / "admin",
                                   dict(o.DEFAULT_CONDITIONS), run_timeout=5, grace=1, post_timeout=1, log_dir=self.root)

    def fake_popen(self, code, make_dir: bool, result):
        runs, run_id = self.runs, self.RUN_ID

        class P:
            pid = 999999

            def __init__(self, args, **kw):
                self.args = args
                if make_dir:
                    d = runs / run_id
                    d.mkdir()
                    if result is not None:
                        (d / o.RESULT_NAME).write_text(result if isinstance(result, str) else json.dumps(result))

            def wait(self, timeout=None):
                return code
        return P

    def go(self, code, make_dir, result, containers=0, timed_out=False, networks=0):
        for d in list(self.runs.iterdir()):
            for f in d.iterdir():
                f.unlink()
            d.rmdir()
        patches = [mock.patch("official_session.subprocess.Popen", self.fake_popen(code, make_dir, result)),
                   mock.patch("official_session.running_containers", return_value=containers),
                   mock.patch("official_session.eval_networks", return_value=networks)]
        if timed_out:
            patches.append(mock.patch("official_session.wait_process", return_value=None))
        with contextlib.ExitStack() as stack:
            for patch in patches:
                stack.enter_context(patch)
            return self.runner("baseline", 0)

    def test_normal_outcomes(self):
        ok = self.go(0, True, {"status": "complete"})
        self.assertEqual((ok.status, ok.run_id, ok.stop_after), ("complete", self.RUN_ID, None))
        bad = self.go(1, True, {"status": "invalid", "reasons": [STALL]})
        self.assertEqual((bad.status, bad.reasons, bad.stop_after), ("invalid", [STALL], None))
        # 멈춤 무효면 결과의 멈춤 기록에서 멈춤 사건과 그때 진행 중이던 모델 호출 수를 같이 넘긴다(2026-11-22 사용자)
        stalled = self.go(1, True, {"status": "invalid", "reasons": [STALL], "fault_stall": {"events": [
            {"kind": "upstream_cut", "at_seconds": 5.0, "idle_seconds": 200.0, "idle_censored": True, "inflight_at_fault": 1,
             "inflight_model_calls_at_fault": 1, "stall": True}]}})
        self.assertEqual([e["inflight_model_calls_at_fault"] for e in stalled.fault_stall], [1])
        self.assertIsNone(ok.fault_stall)

    def test_created_run_directory_always_counts_as_an_attempt(self):
        # 2026-11-20 사용자: 회차 디렉터리가 생겼으면 무슨 일이 있어도 시도 1회. 결과 없으면 무효(사유 = 멈춘 사유) 뒤 멈춤.
        timeout = self.go(0, True, None, timed_out=True)
        self.assertEqual((timeout.status, timeout.reasons, timeout.run_id, timeout.stop_after),
                         ("invalid", ["run_wait_timeout:B0"], self.RUN_ID, "run_wait_timeout:B0"))
        missing = self.go(1, True, None)
        self.assertEqual((missing.status, missing.reasons, missing.stop_after),
                         ("invalid", ["result_missing:B0"], "result_missing:B0"))
        broken = self.go(1, True, "{not json")
        self.assertEqual((broken.status, broken.stop_after), ("invalid", "result_unreadable:B0"))
        mismatch = self.go(0, True, {"status": "invalid", "reasons": ["x"]})
        self.assertEqual((mismatch.status, mismatch.reasons, mismatch.stop_after),
                         ("invalid", ["result_status_mismatch:B0", "x"], "result_status_mismatch:B0"))
        # 결과 파일이 있으면 그 상태로 센다: 끝까지 간 회차는 컨테이너만 늦게 내려가도 버려지지 않는다
        late = self.go(0, True, {"status": "complete"}, containers=3)
        self.assertEqual((late.status, late.run_id, late.stop_after), ("complete", self.RUN_ID, "containers_left:B0"))
        # 종료 코드 2인데 회차 디렉터리가 생겼으면 시도로 센다(무효)
        cfg_dir = self.go(2, True, None)
        self.assertEqual((cfg_dir.status, cfg_dir.stop_after), ("invalid", "result_missing:B0"))

    def test_no_run_directory_is_not_an_attempt(self):
        self.assertEqual(self.go(2, False, None).status, "config_error")
        timeout = self.go(0, False, None, timed_out=True)
        self.assertEqual((timeout.status, timeout.reasons), ("stop", ["run_wait_timeout:B0"]))
        self.assertEqual(self.go(1, False, None).reasons, ["no_run_directory:B0"])
        # 회차 디렉터리가 없어도 남은 컨테이너는 멈춘 사유에 같이 적는다
        self.assertEqual(self.go(0, False, None, timed_out=True, containers=2).reasons,
                         ["run_wait_timeout:B0", "containers_left:B0"])

    def test_containers_are_awaited_after_every_kind_of_end(self):
        # 2026-11-20 사용자: 시한 넘김·결과 없음·결과 못 읽음·상태 불일치 뒤에도 컨테이너가 내려가는지 기다리고, 안 내려가면 사유에 같이
        cases = {"timeout": dict(code=0, result=None, timed_out=True), "missing": dict(code=1, result=None),
                 "broken": dict(code=1, result="{"), "mismatch": dict(code=0, result={"status": "invalid"}),
                 "complete": dict(code=0, result={"status": "complete"})}
        for name, case in cases.items():
            with self.subTest(case=name):
                gone = self.go(case["code"], True, case["result"], timed_out=case.get("timed_out", False))
                left = self.go(case["code"], True, case["result"], timed_out=case.get("timed_out", False), containers=1)
                self.assertEqual(left.status, gone.status)  # 시도 세기는 그대로
                self.assertEqual(left.run_id, self.RUN_ID)
                want = "containers_left:B0" if gone.stop_after is None else f"{gone.stop_after},containers_left:B0"
                self.assertEqual(left.stop_after, want)
        # 설정 오류(회차 디렉터리 없음, 종료 코드 2)는 기다리지 않는다
        with mock.patch("official_session.wait_containers_gone", side_effect=AssertionError("waited")):
            self.assertEqual(self.go(2, False, None).status, "config_error")

    def test_real_session_checks_containers_before_each_run_including_resume(self):
        # main의 실제 경로: 회차 전 확인이 러너보다 먼저 불리고, 남아 있으면 러너를 부르지 않고 시도 0으로 멈춘다
        with tempfile.TemporaryDirectory() as root:
            config = Path(root, "c.json")
            config.write_text(json.dumps({"runs_root": str(Path(root, "runs"))}))
            Path(root, "runs").mkdir()
            common = ["--tree", root, "--config", str(config), "--preflight", root + "/p", "--admin-password-file", root + "/a"]
            started = mock.Mock(side_effect=AssertionError("runner started"))
            with mock.patch("official_session.running_containers", return_value=2), \
                    mock.patch("official_session.eval_networks", side_effect=AssertionError("networks read before containers")), \
                    mock.patch("official_session.RealRunner.__call__", started), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(o.main(["--session-dir", root + "/s", *common]), o.EXIT_STOPPED)
                lines = [json.loads(l) for l in Path(root, "s", o.LOG_NAME).read_text().splitlines()]
                self.assertEqual(lines[0]["reason"], "containers_present")
                self.assertEqual(lines[-1]["attempts"], {"baseline": 0, "ours": 0})
                # --resume도 같은 확인을 먼저 한다
                self.assertEqual(o.main(["--session-dir", root + "/s", "--resume", *common]), o.EXIT_STOPPED)
            started.assert_not_called()
            # 컨테이너는 없지만 앞 회차 평가 망이 남았으면 같은 자리에서 멈춘다(시도 0)
            with mock.patch("official_session.running_containers", return_value=0), \
                    mock.patch("official_session.eval_networks", return_value=3), \
                    mock.patch("official_session.RealRunner.__call__", started), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(o.main(["--session-dir", root + "/n", *common]), o.EXIT_STOPPED)
                lines = [json.loads(l) for l in Path(root, "n", o.LOG_NAME).read_text().splitlines()]
                self.assertEqual((lines[0]["reason"], lines[-1]["attempts"]), ("networks_present", {"baseline": 0, "ours": 0}))
            started.assert_not_called()

    def test_networks_left_after_a_run_stop_the_session(self):
        late = self.go(0, True, {"status": "complete"}, networks=1)
        self.assertEqual((late.status, late.run_id, late.stop_after), ("complete", self.RUN_ID, "networks_left:B0"))
        with mock.patch.object(self.runner, "post_timeout", 0):
            gone = self.go(0, True, {"status": "complete"}, containers=1, networks=1)
        self.assertEqual(gone.stop_after, "containers_left:B0")  # 컨테이너가 먼저다

    def test_eval_networks_counts_only_eval_projects(self):
        done = mock.Mock(returncode=0, stdout="eval-ours-1\neval-ours-1\nother\nabc2lab-rehearsal\n")
        with mock.patch("official_session.subprocess.run", return_value=done) as run:
            self.assertEqual(o.eval_networks(), 2)
        self.assertIn("label=com.docker.compose.project", run.call_args.args[0])
        with mock.patch("official_session.subprocess.run", return_value=mock.Mock(returncode=1, stdout="")):
            self.assertIsNone(o.eval_networks())
        with mock.patch("official_session.subprocess.run", side_effect=OSError):
            self.assertIsNone(o.eval_networks())

    def test_containers_present_before_a_run_stops_without_counting(self):
        self.assertIsNone(o.containers_stop(lambda: 0))
        self.assertEqual(o.containers_stop(lambda: 3), "containers_present")
        self.assertEqual(o.containers_stop(lambda: None), "containers_unknown")
        # 평가 compose 망이 남아 있으면 다음 회차 고정 대역과 겹친다(2026-11-20 compose_up_failed)
        self.assertIsNone(o.containers_stop(lambda: 0, lambda: 0))
        self.assertEqual(o.containers_stop(lambda: 0, lambda: 2), "networks_present")
        self.assertEqual(o.containers_stop(lambda: 0, lambda: None), "networks_unknown")
        self.assertEqual(o.containers_stop(lambda: 1, lambda: 2), "containers_present")
        schedule, calls, logs = Schedule(1), [], []
        run_session(schedule, lambda side, repeat: calls.append(side) or Outcome("complete"), logs.append,
                    before_run=lambda side, repeat: o.containers_stop(lambda: 1))
        self.assertEqual((calls, schedule.attempts["baseline"], schedule.stop_reason), ([], 0, "containers_present"))

    def test_schedule_counts_stopped_attempts_and_resume_keeps_them(self):
        # 시한 넘긴 회차는 시도로 센 뒤 멈추고, --resume은 그 시도를 다시 세어 같은 칸부터 잇는다(2N에 걸린다)
        schedule, logs = Schedule(1), []
        outcomes = iter([Outcome("invalid", ["run_wait_timeout:B0"], "r1", stop_after="run_wait_timeout:B0")])
        run_session(schedule, lambda side, repeat: next(outcomes), logs.append)
        self.assertEqual((schedule.attempts["baseline"], schedule.stop_reason), (1, "run_wait_timeout:B0"))
        resumed = Schedule(1)
        self.assertEqual(o.replay(resumed, logs), 1)
        self.assertIsNone(resumed.stop_reason)
        self.assertEqual(resumed.next_slot(), ("baseline", 0))
        self.assertEqual(resumed.attempts["baseline"], 1)
        # 시한 넘김이 되풀이되면 2N에서 더 시작하지 않는다
        again = Schedule(1)
        o.replay(again, logs + logs[:1])
        self.assertEqual(again.next_slot(), None)
        self.assertEqual(again.stop_reason, "attempt_limit:baseline")
        # 끝까지 간 회차가 컨테이너만 늦게 내려가 멈췄으면, 잇는 세션은 다음 칸부터 간다
        late = Schedule(1)
        run_session(late, lambda side, repeat: Outcome("complete", [], "r2", stop_after="containers_left:B0"), (lines := []).append)
        self.assertEqual((late.valid["baseline"], late.stop_reason), (1, "containers_left:B0"))
        cont = Schedule(1)
        o.replay(cont, lines)
        self.assertEqual(cont.next_slot(), ("ours", 0))

    def test_baseline_gets_admin_password_file_and_conditions(self):
        seen = {}

        class P:
            pid = 1

            def __init__(self, args, **kw):
                seen["args"] = args

            def wait(self, timeout=None):
                return 2
        with mock.patch("official_session.subprocess.Popen", P):
            self.runner("baseline", 2)
        args = seen["args"]
        self.assertEqual(args[args.index("--side") + 1], "baseline")
        self.assertEqual(args[args.index("--repeat") + 1], "2")
        self.assertEqual(args[args.index("--condition") + 1], "원본@35b")
        self.assertIn("--admin-password-file", args)
        with mock.patch("official_session.subprocess.Popen", P):
            self.runner("ours", 0)
        self.assertNotIn("--admin-password-file", seen["args"])
        self.assertEqual(seen["args"][seen["args"].index("--condition") + 1], "우리@35b")


class TestUsageGuard(unittest.TestCase):
    def guard(self, *responses):
        pending = list(responses)

        def read():
            value = pending.pop(0)
            if isinstance(value, Exception):
                raise value
            return value
        return o.UsageGuard(read, cap_usd=3.0, floor_usd=0.30)

    def test_unknown_values_stop_before_a_paid_run(self):
        # #819 리뷰: 남은 한도를 모르면(None) 하한을 확인하지 못한 것이라 시작하지 않는다
        self.assertEqual(self.guard({"usage": 0.0, "limit_remaining": None}).check(), "remaining_unknown")
        # 첫 사용량을 모르면 TypeError 없이 멈춘다
        guard = self.guard({"usage": None, "limit_remaining": 10.0}, {"usage": 1.0, "limit_remaining": 10.0})
        self.assertEqual(guard.check(), "usage_unknown")
        self.assertIsNone(guard.base)
        for bad in (True, float("nan"), "1.0", float("inf")):
            with self.subTest(bad=bad):
                self.assertEqual(self.guard({"usage": bad, "limit_remaining": 10.0}).check(), "usage_unknown")
                self.assertEqual(self.guard({"usage": 1.0, "limit_remaining": bad}).check(), "remaining_unknown")
        self.assertEqual(self.guard(OSError("network")).check(), "usage_read_failed")
        self.assertEqual(self.guard(["not", "a", "dict"]).check(), "usage_unknown")

    def test_cap_and_floor_from_the_first_check(self):
        guard = self.guard({"usage": 10.0, "limit_remaining": 40.0}, {"usage": 12.9, "limit_remaining": 37.1},
                           {"usage": 13.0, "limit_remaining": 37.0})
        self.assertIsNone(guard.check())
        self.assertEqual(guard.base, 10.0)
        self.assertIsNone(guard.check())
        self.assertEqual(guard.check(), "usage_cap")
        self.assertEqual(self.guard({"usage": 0.0, "limit_remaining": 0.30}).check(), "remaining_floor")

    def test_base_is_reported_once_and_can_be_restored(self):
        seen = []
        guard = o.UsageGuard(lambda: {"usage": 5.0, "limit_remaining": 40.0}, 3.0, 0.30, on_base=seen.append)
        guard.check()
        guard.check()
        self.assertEqual(seen, [5.0])
        # --resume: 기록의 기준값으로 시작하면 세션 전체 기준으로 건다(다시 0부터 세지 않음)
        restored = o.UsageGuard(lambda: {"usage": 8.0, "limit_remaining": 40.0}, 3.0, 0.30, base=5.0, on_base=seen.append)
        self.assertEqual(restored.check(), "usage_cap")
        self.assertEqual(seen, [5.0])

    def test_runner_is_not_called_when_the_guard_stops(self):
        guard, calls, logs = self.guard({"usage": 0.0, "limit_remaining": None}), [], []
        schedule = run_session(Schedule(3), lambda side, repeat: calls.append(side) or Outcome("complete"), logs.append,
                               before_run=lambda side, repeat: guard.check())
        self.assertEqual(calls, [])
        self.assertEqual(schedule.stop_reason, "remaining_unknown")
        self.assertEqual(logs[-2]["reason"], "remaining_unknown")

    def test_key_file_and_cap_go_together(self):
        with tempfile.TemporaryDirectory() as root:
            common = ["--tree", root, "--config", root + "/c", "--preflight", root + "/p", "--admin-password-file", root + "/a"]
            with contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(o.main(["--session-dir", root + "/s1", *common, "--key-file", root + "/k"]), o.EXIT_INPUT)
                self.assertEqual(o.main(["--session-dir", root + "/s2", *common, "--usage-cap-usd", "3"]), o.EXIT_INPUT)


class TestManifest(unittest.TestCase):
    def test_new_approval_id_each_time_and_private(self):
        with tempfile.TemporaryDirectory() as root:
            template, target = Path(root, "t.json"), Path(root, "m.json")
            template.write_text(json.dumps({"version": 3, "approval_id": "x", "model": "m"}))
            o.write_manifest(template, target, 6)
            first = json.loads(target.read_text())
            self.assertEqual(os.stat(target).st_mode & 0o777, 0o600)
            o.write_manifest(template, target, 6)
            second = json.loads(target.read_text())
            self.assertNotEqual(first["approval_id"], second["approval_id"])
            self.assertTrue(second["approval_id"].startswith("official-o-"))
            self.assertRegex(second["expires_at"], r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$")
            self.assertEqual(second["model"], "m")

    def test_session_caps_replace_template_caps(self):
        # 사용자 2026-11-22: 한 세션의 O manifest는 시작 때 규칙으로 정한 상한을 쓴다(틀의 고정 숫자가 아니라)
        with tempfile.TemporaryDirectory() as root:
            template, target = Path(root, "t.json"), Path(root, "m.json")
            template.write_text(json.dumps({"model": "m", "max_input_price_usd_micros_per_million": 1,
                                            "max_output_price_usd_micros_per_million": 2}))
            caps = {"max_input_price_usd_micros_per_million": 285000, "max_output_price_usd_micros_per_million": 2550000}
            o.write_manifest(template, target, 6, caps)
            value = json.loads(target.read_text())
            self.assertEqual((value["max_input_price_usd_micros_per_million"], value["max_output_price_usd_micros_per_million"]),
                             (285000, 2550000))
            o.write_manifest(template, target, 6)
            self.assertEqual(json.loads(target.read_text())["max_input_price_usd_micros_per_million"], 1)  # 규칙 없으면 틀 그대로


class TestPriceCapResume(unittest.TestCase):
    CAPS = {"max_input_price_usd_micros_per_million": 285000, "max_output_price_usd_micros_per_million": 2550000}

    def test_session_caps_come_from_the_first_passing_record(self):
        lines = [{"event": "price_caps", "headroom_ok": False, "max_input_price_usd_micros_per_million": 9,
                  "max_output_price_usd_micros_per_million": 9},
                 {"event": "price_caps", "headroom_ok": True, **self.CAPS},
                 {"event": "price_caps", "headroom_ok": True, "max_input_price_usd_micros_per_million": 1,
                  "max_output_price_usd_micros_per_million": 1}]
        self.assertEqual(o.session_caps(lines), self.CAPS)
        self.assertIsNone(o.session_caps([{"event": "attempt"}]))
        with self.assertRaises(ValueError):
            o.session_caps([{"event": "price_caps", "headroom_ok": True, "max_input_price_usd_micros_per_million": "285000",
                             "max_output_price_usd_micros_per_million": 2550000}])

    def test_resume_with_recorded_caps_needs_provider_probe_and_headroom_must_not_exceed_factor(self):
        with tempfile.TemporaryDirectory() as root:
            session = Path(root, "s")
            session.mkdir()
            Path(session, o.LOG_NAME).write_text(json.dumps({"event": "price_caps", "headroom_ok": True, **self.CAPS}) + "\n")
            err = io.StringIO()
            with contextlib.redirect_stderr(err), contextlib.redirect_stdout(io.StringIO()):
                code = o.main(["--session-dir", str(session), "--resume"])
            self.assertEqual(code, o.EXIT_INPUT)
            self.assertIn("resume_needs_provider_probe", err.getvalue())
            err = io.StringIO()
            with contextlib.redirect_stderr(err), contextlib.redirect_stdout(io.StringIO()):
                code = o.main(["--session-dir", root + "/t", "--dry-run", "--price-cap-factor", "1.1", "--price-min-headroom", "1.2"])
            self.assertEqual(code, o.EXIT_INPUT)
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                o.main(["--session-dir", root + "/u", "--dry-run", "--price-cap-factor", "abc"])


class TestStallRecord(unittest.TestCase):
    # 사용자 2026-11-22: 멈춤 무효가 나면 그 순간 진행 중인 모델 호출이 있었는지 세션 기록에 같이 남긴다
    RESULT = {"status": "invalid", "reasons": ["provider_fault_stall"], "fault_stall": {"judged": True, "events": [
        {"kind": "rate_limited", "at_seconds": 10.0, "idle_seconds": 3.0, "idle_censored": False, "inflight_at_fault": 0,
         "inflight_model_calls_at_fault": 0, "stall": False},
        {"kind": "upstream_cut", "at_seconds": 50.0, "idle_seconds": 190.5, "idle_censored": False, "inflight_at_fault": 2,
         "inflight_model_calls_at_fault": 1, "stall": True, "silence_seconds": 190.5}]}}

    def test_stall_events_keep_only_stalls_with_inflight_counts(self):
        self.assertEqual(o.stall_events(self.RESULT, ["provider_fault_stall"]),
                         [{"kind": "upstream_cut", "at_seconds": 50.0, "idle_seconds": 190.5, "idle_censored": False,
                           "inflight_at_fault": 2, "inflight_model_calls_at_fault": 1}])
        self.assertIsNone(o.stall_events(self.RESULT, ["window_boundary_changed"]))  # 멈춤 무효가 아니면 칸 없음
        self.assertEqual(o.stall_events({"status": "invalid"}, ["provider_fault_stall"]), [])

    def test_session_log_and_summary_count_stalls_with_inflight_model_call_and_resume_keeps_them(self):
        inflight = [{"kind": "upstream_cut", "inflight_at_fault": 1, "inflight_model_calls_at_fault": 1}]
        # 대상 요청만 걸려 있던 멈춤(모델 호출 0)은 세지 않는다 — 스트리밍 모델 호출 경우만 가른다
        quiet = [{"kind": "rate_limited", "inflight_at_fault": 1, "inflight_model_calls_at_fault": 0}]
        outcomes = [Outcome("invalid", ["provider_fault_stall"], "b1", fault_stall=inflight),
                    Outcome("complete", [], "b2"), Outcome("invalid", ["provider_fault_stall"], "o1", fault_stall=quiet),
                    Outcome("complete", [], "o2")]
        logs = []
        schedule = run_session(Schedule(1), lambda side, repeat: outcomes.pop(0), logs.append)
        attempts = [l for l in logs if l["event"] == "attempt"]
        self.assertEqual([l.get("fault_stall") for l in attempts], [inflight, None, quiet, None])
        summary = schedule.summary()
        self.assertEqual(summary["fault_stalls"], {"baseline": 1, "ours": 1})  # 판정·상한 셈은 그대로
        self.assertEqual(summary["fault_stalls_inflight_model_call"], {"baseline": 1, "ours": 0})
        resumed = Schedule(1)
        o.replay(resumed, [dict(l, dry_run=False) for l in attempts])
        self.assertEqual(resumed.summary()["fault_stalls_inflight_model_call"], {"baseline": 1, "ours": 0})


if __name__ == "__main__":
    unittest.main()


# ── 시작 전 운영 점검(2026-11-22 사용자): 포트·메모리 여유·시작 시간대·제공자 429 ──
def proc_tcp(*ports: int, address: str = "0100007F") -> str:
    """/proc/net/tcp 모양: LISTEN(0A) 행. 주소는 리틀엔디언 16진수(127.0.0.1 = 0100007F)."""
    head = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
    rows = "".join(f"   {i}: {address}:{p:04X} 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1\n"
                   for i, p in enumerate(ports))
    return head + rows


SPLIT_CONFIG = {"ports": {"engine_api_host": 18787, "obs_host": 39291}, "split": {"product_port": 28787, "dns_control_port": 29083}}


class TestStartChecks(unittest.TestCase):
    def test_busy_ports_names_the_config_port_another_process_listens_on(self):
        # 2026-11-22: 재부팅 뒤 VS Code 원격 서버가 127.0.0.1:39291(ports.obs_host)을 잡아 compose_up_failed
        read = lambda path: proc_tcp(39291, 5000) if path == "/proc/net/tcp" else None  # noqa: E731
        self.assertEqual(o.busy_ports(SPLIT_CONFIG, read), ["ports.obs_host=39291"])
        free = lambda path: proc_tcp(5000) if path == "/proc/net/tcp" else None  # noqa: E731
        self.assertEqual(o.busy_ports(SPLIT_CONFIG, free), [])
        self.assertIsNone(o.busy_ports(SPLIT_CONFIG, lambda path: None))  # 못 읽으면 모름(None) — 통과로 보지 않는다

    def test_wait_until_retries_then_gives_up_at_the_deadline(self):
        clock, waits, answers = [0.0], [], [(False, "heavy_ok: no"), (False, "heavy_ok: no"), (True, "heavy_ok: yes")]
        ok = o.wait_until(lambda: answers.pop(0), wait=200, interval=60, on_wait=waits.append,
                          sleep=lambda s: clock.__setitem__(0, clock[0] + s), clock=lambda: clock[0])
        self.assertTrue(ok)
        self.assertEqual(waits, ["heavy_ok: no", "heavy_ok: no"])
        clock[0] = 0.0
        never = o.wait_until(lambda: (False, "heavy_ok: no"), wait=100, interval=60, on_wait=lambda d: None,
                             sleep=lambda s: clock.__setitem__(0, clock[0] + s), clock=lambda: clock[0])
        self.assertFalse(never)

    def test_run_check_uses_exit_code_and_last_line(self):
        self.assertEqual(o.run_check(["sh", "-c", "echo a; echo heavy_ok: yes"]), (True, "heavy_ok: yes"))
        self.assertEqual(o.run_check(["sh", "-c", "echo heavy_ok: no; exit 75"]), (False, "heavy_ok: no"))

    def test_start_window_is_kst(self):
        import datetime as dt
        window = o.parse_window("14-18")
        self.assertTrue(o.in_window(dt.datetime(2026, 11, 22, 5, 0, tzinfo=dt.timezone.utc), window))   # 14:00 KST
        self.assertFalse(o.in_window(dt.datetime(2026, 11, 22, 9, 0, tzinfo=dt.timezone.utc), window))  # 18:00 KST
        self.assertFalse(o.in_window(dt.datetime(2026, 11, 22, 4, 59, tzinfo=dt.timezone.utc), window))
        for bad in ("18-14", "x-3", "14", "0-25"):
            with self.assertRaises(ValueError):
                o.parse_window(bad)

    MANIFEST = {"model": "m/x", "provider_slug": "slug/fp8", "base_url": "https://gateway.example/api/v1",
                "expected_provider_name": "Provider", "max_input_price_usd_micros_per_million": 200000,
                "max_output_price_usd_micros_per_million": 800000}

    def test_provider_probe_counts_429_and_routes_to_the_same_provider(self):
        sent, replies = [], [(200, {"provider": "Provider"})] * 8 + [(429, {}), (500, {})]
        def post(req):
            sent.append(json.loads(req.data))
            return replies[len(sent) - 1]
        out = o.provider_probe(self.MANIFEST, "k", post=post)
        self.assertEqual((out["ok"], out["rate_limited"], out["other"]), (8, 1, 1))
        self.assertEqual(out["providers"], ["Provider"])
        self.assertEqual(len(sent), 10)
        body = sent[0]
        self.assertEqual(body["model"], "m/x")
        self.assertEqual(body["max_tokens"], 1)
        self.assertEqual(body["provider"]["only"], ["slug/fp8"])
        self.assertFalse(body["provider"]["allow_fallbacks"])

    PRICE_OK = staticmethod(lambda m: {"found": True, "input_usd_micros_per_million": 1, "output_usd_micros_per_million": 1})

    def test_endpoint_price_reads_the_same_tag_in_manifest_units(self):
        # 2026-11-22 실제 표: PrimeIntellect 0.000000176/0.000001578 USD/토큰 → 176000/1578000(부동소수 오차 없이 정수)
        table = {"data": {"endpoints": [
            {"provider_name": "Other", "tag": "other/fp8", "pricing": {"prompt": "0.00000005", "completion": "0.0000007"}},
            {"provider_name": "Provider", "tag": "slug/fp8", "pricing": {"prompt": "0.000000176", "completion": "0.000001578"}}]}}
        urls = []
        get = lambda url: (urls.append(url), (200, table))[1]  # noqa: E731
        out = o.endpoint_price(self.MANIFEST, get)
        self.assertEqual(urls, ["https://gateway.example/api/v1/models/m/x/endpoints"])
        self.assertEqual((out["found"], out["input_usd_micros_per_million"], out["output_usd_micros_per_million"]),
                         (True, 176000, 1578000))
        self.assertFalse(o.endpoint_price(dict(self.MANIFEST, provider_slug="missing/fp8"), get)["found"])
        broken = {"data": {"endpoints": [{"tag": "slug/fp8", "pricing": {"prompt": "x", "completion": "1"}}]}}
        self.assertFalse(o.endpoint_price(self.MANIFEST, lambda url: (200, broken))["found"])

    def test_rule_caps_are_price_times_factor_and_headroom_is_20_percent(self):
        # 사용자 2026-11-22: 상한 = 세션 시작 직전 재조회 단가 × 1.5, 시작 점검은 상한이 지금 단가보다 20% 이상 높으면 통과
        priced = {"input_usd_micros_per_million": 190000, "output_usd_micros_per_million": 1700000}
        caps = o.rule_caps(priced)
        self.assertEqual(caps, {"max_input_price_usd_micros_per_million": 285000, "max_output_price_usd_micros_per_million": 2550000})
        self.assertEqual(o.rule_caps({"input_usd_micros_per_million": 176001, "output_usd_micros_per_million": 1})["max_input_price_usd_micros_per_million"],
                         264002)  # 올림(264001.5 → 264002)
        self.assertTrue(o.caps_headroom_ok(caps, priced))
        exactly = {"input_usd_micros_per_million": 237500, "output_usd_micros_per_million": 2125000}  # 상한이 정확히 1.2배
        self.assertTrue(o.caps_headroom_ok(caps, exactly))
        risen = {"input_usd_micros_per_million": 237500, "output_usd_micros_per_million": 2125001}
        self.assertFalse(o.caps_headroom_ok(caps, risen))
        free = {"input_usd_micros_per_million": 0, "output_usd_micros_per_million": 0}
        self.assertTrue(o.caps_headroom_ok(o.rule_caps(free), free))

    def test_start_checks_set_session_caps_by_rule_and_stop_on_unknown_price_or_thin_headroom(self):
        import datetime as dt
        afternoon = lambda: dt.datetime(2026, 11, 22, 6, 0, tzinfo=dt.timezone.utc)  # noqa: E731
        ok_probe = dict(calls=10, ok=10, rate_limited=0, other=0, statuses={"200": 10}, providers=["Provider"])
        calls, logs = [], []
        def probe(m, k):
            calls.append(1)
            return ok_probe
        now_price = lambda m: {"found": True, "input_usd_micros_per_million": 190000, "output_usd_micros_per_million": 1700000}  # noqa: E731
        with tempfile.TemporaryDirectory() as d:
            args = self._args(Path(d))
            # 단가를 못 찾으면 상한을 정하지 않고 멈춘다(유료 점검 0)
            self.assertEqual(o.session_start_checks(args, None, None, lambda e: None, now=afternoon,
                                                    price=lambda m: {"found": False}, probe=probe), "provider_price_unknown")
            self.assertEqual(calls, [])
            # 새 세션: 상한 = 단가 × 1.5, 세션 기록에 단가와 상한을 남긴다
            caps = {}
            self.assertIsNone(o.session_start_checks(args, None, None, logs.append, now=afternoon, price=now_price, probe=probe,
                                                     caps_sink=caps))
            self.assertEqual(caps, {"max_input_price_usd_micros_per_million": 285000,
                                    "max_output_price_usd_micros_per_million": 2550000})
            rec = [l for l in logs if l["event"] == "price_caps"][0]
            self.assertEqual((rec["source"], rec["rule_factor"], rec["min_headroom"], rec["input_usd_micros_per_million"],
                              rec["max_output_price_usd_micros_per_million"], rec["headroom_ok"]),
                             ("rule", "1.5", "1.2", 190000, 2550000, True))
            # 이어 돌리기: 기록된 상한을 그대로 쓰고, 그사이 단가가 올라 20% 여유가 없으면 멈춘다(유료 점검 0)
            calls.clear(); logs.clear()
            risen = lambda m: {"found": True, "input_usd_micros_per_million": 240000, "output_usd_micros_per_million": 2130000}  # noqa: E731
            again = {}
            self.assertEqual(o.session_start_checks(args, None, None, logs.append, now=afternoon, price=risen, probe=probe,
                                                    recorded_caps=caps, caps_sink=again), "provider_price_headroom")
            self.assertEqual((calls, again), ([], {}))
            self.assertEqual([(l["source"], l["headroom_ok"]) for l in logs if l["event"] == "price_caps"], [("session_log", False)])
            # 조금 오른 정도(20% 안쪽)면 기록된 상한 그대로 잇는다
            slight = lambda m: {"found": True, "input_usd_micros_per_million": 230000, "output_usd_micros_per_million": 2000000}  # noqa: E731
            self.assertIsNone(o.session_start_checks(args, None, None, lambda e: None, now=afternoon, price=slight, probe=probe,
                                                     recorded_caps=caps, caps_sink=again))
            self.assertEqual(again, caps)

    def test_budget_is_checked_and_based_before_any_paid_probe_call(self):
        """#848 동찬 P2: 유료 점검 호출 전에 비용 보호를 확인하고 세션 사용량 기준을 기록한다(점검 비용이 세션 상한에 든다)."""
        import datetime as dt
        afternoon = lambda: dt.datetime(2026, 11, 22, 6, 0, tzinfo=dt.timezone.utc)  # noqa: E731
        order, reads = [], iter([{"usage": 1.00, "limit_remaining": 10.0}, {"usage": 1.02, "limit_remaining": 9.98}])
        def read():
            order.append("usage")
            return next(reads)
        logs = []
        guard = o.UsageGuard(read, cap_usd=0.01, floor_usd=0.30, on_base=lambda base: logs.append({"event": "usage_base", "usage_usd": base}))
        def probe(m, k):
            order.append("probe")
            return dict(calls=10, ok=10, rate_limited=0, other=0, statuses={"200": 10}, providers=["Provider"])
        with tempfile.TemporaryDirectory() as d:
            stop = o.session_start_checks(self._args(Path(d)), None, None, logs.append, now=afternoon, price=self.PRICE_OK,
                                          probe=probe, budget=guard.check)
        self.assertIsNone(stop)
        self.assertEqual(order, ["usage", "probe"])  # 확인·기준이 먼저
        self.assertEqual(guard.base, 1.00)  # 점검 호출 전 사용량이 세션 기준
        self.assertEqual([l["event"] for l in logs], ["provider_price", "price_caps", "usage_base", "provider_probe"])
        self.assertEqual(guard.check(), "usage_cap")  # 점검 비용(0.02)이 세션 상한(0.01)에 들어간다

    def test_budget_is_rechecked_before_each_retry_batch(self):
        """#848 동찬 P2(재시도): 첫 묶음에 429가 섞여 다시 점검할 때, 첫 묶음 비용으로 상한에 걸리면 다음 묶음은 0건."""
        import datetime as dt
        afternoon = lambda: dt.datetime(2026, 11, 22, 6, 0, tzinfo=dt.timezone.utc)  # noqa: E731
        reads = iter([{"usage": 1.00, "limit_remaining": 10.0}, {"usage": 1.03, "limit_remaining": 9.97}])
        guard = o.UsageGuard(lambda: next(reads), cap_usd=0.02, floor_usd=0.30)
        calls, slept, logs = [], [], []
        def probe(m, k):
            calls.append(1)
            return dict(calls=10, ok=9, rate_limited=1, other=0, statuses={"200": 9, "429": 1}, providers=["Provider"])
        with tempfile.TemporaryDirectory() as d:
            stop = o.session_start_checks(self._args(Path(d)), None, None, logs.append, now=afternoon, price=self.PRICE_OK,
                                          probe=probe, sleep=slept.append, budget=guard.check)
        self.assertEqual(stop, "usage_cap")
        self.assertEqual(len(calls), 1)  # 둘째 묶음은 보내지 않는다
        self.assertEqual([l["event"] for l in logs], ["provider_price", "price_caps", "provider_probe", "provider_probe_wait"])

    def test_budget_passes_each_batch_until_probe_clears(self):
        import datetime as dt
        afternoon = lambda: dt.datetime(2026, 11, 22, 6, 0, tzinfo=dt.timezone.utc)  # noqa: E731
        checks, results = [], iter([dict(rate_limited=1), dict(rate_limited=0)])
        def probe(m, k):
            r = next(results)
            return dict(calls=10, ok=10 - r["rate_limited"], rate_limited=r["rate_limited"], other=0, statuses={}, providers=["Provider"])
        with tempfile.TemporaryDirectory() as d:
            stop = o.session_start_checks(self._args(Path(d)), None, None, lambda e: None, now=afternoon, price=self.PRICE_OK,
                                          probe=probe, sleep=lambda s: None, budget=lambda: checks.append(1))
        self.assertIsNone(stop)
        self.assertEqual(len(checks), 2)  # 묶음마다 한 번씩

    def test_budget_stop_sends_zero_probe_calls(self):
        import datetime as dt
        afternoon = lambda: dt.datetime(2026, 11, 22, 6, 0, tzinfo=dt.timezone.utc)  # noqa: E731
        calls = []
        with tempfile.TemporaryDirectory() as d:
            args = self._args(Path(d))
            for reason in ("remaining_floor", "usage_cap", "usage_read_failed"):
                self.assertEqual(o.session_start_checks(args, None, None, lambda e: None, now=afternoon, price=self.PRICE_OK,
                                                        probe=lambda m, k: calls.append(1), budget=lambda r=reason: r), reason)
        self.assertEqual(calls, [])

    def _args(self, tmp: Path, **over):
        manifest = tmp / "manifest.json"
        manifest.write_text(json.dumps(self.MANIFEST), encoding="utf-8")
        key = tmp / "key"
        key.write_text("synthetic-key\n", encoding="utf-8")
        base = dict(provider_probe=True, o_manifest_template=manifest, key_file=key, probe_tries=3, probe_wait=1800,
                    price_cap_factor=o.PRICE_CAP_FACTOR, price_min_headroom=o.PRICE_MIN_HEADROOM)
        base.update(over)
        return mock.Mock(**base)

    def test_start_checks_delay_on_429_then_start_and_log_each_probe(self):
        import datetime as dt
        with tempfile.TemporaryDirectory() as d:
            logs, slept = [], []
            results = [dict(calls=10, ok=9, rate_limited=1, other=0, statuses={"200": 9, "429": 1}, providers=["Provider"]),
                       dict(calls=10, ok=10, rate_limited=0, other=0, statuses={"200": 10}, providers=["Provider"])]
            stop = o.session_start_checks(self._args(Path(d)), (14, 18), None, logs.append,
                                          now=lambda: dt.datetime(2026, 11, 22, 6, 0, tzinfo=dt.timezone.utc),
                                          probe=lambda m, k: results.pop(0), sleep=slept.append, price=self.PRICE_OK)
        self.assertIsNone(stop)
        self.assertEqual(slept, [1800])
        events = [l["event"] for l in logs]
        self.assertEqual(events, ["start_window", "provider_price", "price_caps", "provider_probe", "provider_probe_wait",
                                  "start_window", "provider_probe"])
        self.assertNotIn("synthetic-key", json.dumps(logs))

    def test_start_checks_stop_outside_window_on_errors_and_after_tries(self):
        import datetime as dt
        evening = lambda: dt.datetime(2026, 11, 22, 11, 0, tzinfo=dt.timezone.utc)  # noqa: E731  20:00 KST
        afternoon = lambda: dt.datetime(2026, 11, 22, 6, 0, tzinfo=dt.timezone.utc)  # noqa: E731
        limited = dict(calls=10, ok=0, rate_limited=10, other=0, statuses={"429": 10}, providers=[])
        broken = dict(calls=10, ok=0, rate_limited=0, other=10, statuses={"401": 10}, providers=[])
        wrong = dict(calls=10, ok=10, rate_limited=0, other=0, statuses={"200": 10}, providers=["Other"])
        with tempfile.TemporaryDirectory() as d:
            args = self._args(Path(d))
            self.assertEqual(o.session_start_checks(args, (14, 18), None, lambda e: None, now=evening), "outside_start_window")
            self.assertEqual(o.session_start_checks(args, None, None, lambda e: None, now=afternoon,
                                                    probe=lambda m, k: broken, sleep=lambda s: None, price=self.PRICE_OK),
                             "provider_probe_failed")
            self.assertEqual(o.session_start_checks(args, None, None, lambda e: None, now=afternoon,
                                                    probe=lambda m, k: wrong, sleep=lambda s: None, price=self.PRICE_OK),
                             "provider_probe_failed")
            self.assertEqual(o.session_start_checks(args, None, None, lambda e: None, now=afternoon,
                                                    probe=lambda m, k: limited, sleep=lambda s: None, price=self.PRICE_OK),
                             "provider_rate_limited")
            no_probe = self._args(Path(d), provider_probe=False)
            self.assertIsNone(o.session_start_checks(no_probe, (14, 18), None, lambda e: None, now=afternoon))
