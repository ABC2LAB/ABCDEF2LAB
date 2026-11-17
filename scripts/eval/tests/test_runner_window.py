"""격리 구간 감시 판정: 데몬 이벤트(11/03 실측 모양)와 호스트 리슨 표본."""

from __future__ import annotations

import json
import os
import re
import sys
import time
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_checks import Listener
from runner_config import REPO_ROOT
from runner_window import (
    DOCKER_EVENTS_BUFFER, ENGINE_DB_HEALTHCHECK, RUNNER_HELPER_LABEL, BackgroundWatch, HostWatch, WatchSample, WindowError,
    events_reasons, healthcheck_actions, judge_events, watch_reasons, watch_summary,
)

NET = {"n" * 64: "engine_net", "t" * 64: "target_net"}
IDS = {"engine": "e" * 64, "target": "a" * 64, "eval-meter": "m" * 64, "eval-llm-gw": "g" * 64,
       "engine-db": "d" * 64}
RUN = {cid: service for service, cid in IDS.items()}
SEALABLE = frozenset({"eval-meter", "eval-llm-gw"})
HEALTH = frozenset({"engine-db"})
OTHER = "f" * 64
PROBE = "c" * 64  # 러너가 구간 중 띄운 post probe(--cidfile ID)


START = 1_000_000_000_000_000_000  # 구간 시작(나노초)


def event(kind, action, actor, at=START + 1, **attributes):
    return {"Type": kind, "Action": action, "Actor": {"ID": actor, "Attributes": attributes},
            "time": at // 10**9, "timeNano": at, "scope": "local"}


def judge(events, complete=True, runner=frozenset()):
    return judge_events(events, START, NET, RUN, SEALABLE, HEALTH, runner, complete)


class TestHealthcheckActions(unittest.TestCase):
    def test_cmd_and_cmd_shell_forms(self):
        rendered = {"services": {
            "a": {"healthcheck": {"test": ["CMD", "curl", "--fail", "http://x/health"]}},
            "b": {"healthcheck": {"test": ["CMD-SHELL", 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"']}},
            "c": {"healthcheck": {"test": ["NONE"]}},
            "d": {"healthcheck": {"test": ["CMD", "true"], "disable": True}},
            "e": {"image": "x"}}}
        self.assertEqual(healthcheck_actions(rendered), {"a": "curl --fail http://x/health", "b": ENGINE_DB_HEALTHCHECK})

    def test_unknown_shapes_are_refused(self):
        for test in (["CMD"], ["CMD-SHELL", "a", "b"], ["BOGUS", "x"], [], "curl x", ["CMD", 1]):
            with self.subTest(test=test), self.assertRaises(WindowError):
                healthcheck_actions({"services": {"a": {"healthcheck": {"test": test}}}})
        for rendered in (None, {}, {"services": []}):
            with self.subTest(rendered=rendered), self.assertRaises(WindowError):
                healthcheck_actions(rendered)


class TestPerServiceHealthchecks(unittest.TestCase):
    """탐색 분리 배치 O: 제품 서비스마다 healthcheck 명령이 다르다. 표의 명령만 그 서비스에 허용한다."""

    PREP, PG = "p" * 64, "q" * 64
    RUN2 = dict(RUN, **{"p" * 64: "preparation", "q" * 64: "postgres"})
    CHECKS = {"preparation": "test -S /p/ipc/preparation.sock", "postgres": "pg_isready -U postgres -d abc2lab"}

    def judge(self, events, checks=None):
        return judge_events(events, START, NET, self.RUN2, SEALABLE, frozenset(), frozenset(), True,
                            healthchecks=self.CHECKS if checks is None else checks)

    def lifecycle(self, cid, command, exec_id):
        return [event("container", "exec_create: " + command, cid, execID=exec_id),
                event("container", "exec_start: " + command, cid, execID=exec_id),
                event("container", "exec_die", cid, execID=exec_id, exitCode="0"),
                event("container", "health_status: healthy", cid)]

    def test_declared_commands_pass(self):
        events = self.lifecycle(self.PREP, self.CHECKS["preparation"], "x1") + self.lifecycle(self.PG, self.CHECKS["postgres"], "x2")
        self.assertEqual(self.judge(events)["boundary_events"], [])

    def test_other_commands_or_services_are_violations(self):
        cases = {
            "swapped_command": self.lifecycle(self.PREP, self.CHECKS["postgres"], "x1")[:1],
            "shell_on_preparation": [event("container", "exec_create: /bin/sh -c id", self.PREP, execID="x9")],
            "engine_db_default_not_allowed": [event("container", "exec_create: " + ENGINE_DB_HEALTHCHECK, IDS["engine-db"], execID="x3")],
            "unhealthy": [event("container", "health_status: unhealthy", self.PG)],
            "die_of_unknown_exec": [event("container", "exec_die", self.PREP, execID="zz", exitCode="0")],
        }
        for name, events in cases.items():
            with self.subTest(case=name):
                self.assertTrue(self.judge(events)["boundary_events"])

    def test_lookback_exec_ids_follow_the_table(self):
        before = START - 5
        created = [event("container", "exec_create: " + self.CHECKS["postgres"], self.PG, at=before, execID="L1"),
                   event("container", "exec_start: " + self.CHECKS["postgres"], self.PG, at=before, execID="L1")]
        die = [event("container", "exec_die", self.PG, execID="L1", exitCode="0")]
        self.assertEqual(self.judge(created + die)["boundary_events"], [])
        wrong = [event("container", "exec_create: id", self.PG, at=before, execID="L2")]
        self.assertTrue(self.judge(wrong + [event("container", "exec_die", self.PG, execID="L2", exitCode="0")])["boundary_events"])

    def test_default_keeps_the_engine_db_rule(self):
        events = self.lifecycle(IDS["engine-db"], ENGINE_DB_HEALTHCHECK, "d1")
        self.assertEqual(judge_events(events, START, NET, RUN, SEALABLE, HEALTH, frozenset(), True)["boundary_events"], [])
        with self.assertRaises(WindowError):
            self.judge([], checks={"preparation": ""})


class TestRunnerHelperAndStop(unittest.TestCase):
    """경계 원자료 꺼내기(계약 §4.1, 결정 B): 러너가 구간 안에서 경계만 정지하고 도우미로 볼륨을 복사한다. 예외는 그 도우미와
    그 정지에만 적용된다."""

    HELPER = "b" * 64
    BOUNDARY = "x" * 64
    RUN2 = dict(RUN, **{"x" * 64: "explore-boundary"})

    def judge(self, events, helpers=None, stoppable=frozenset({"explore-boundary"}), runner=frozenset()):
        return judge_events(events, START, NET, self.RUN2, SEALABLE, HEALTH, runner, True,
                            runner_helpers={self.HELPER: "boundary-evidence"} if helpers is None else helpers,
                            stoppable=stoppable)

    def helper(self, action, **extra):
        return event("container", action, self.HELPER, **{RUNNER_HELPER_LABEL: "boundary-evidence", **extra})

    def stop_sequence(self, cid=None):
        cid = cid or self.BOUNDARY
        return [event("container", "kill", cid, signal="15"), event("container", "die", cid, exitCode="0"),
                event("network", "disconnect", "n" * 64, container=cid), event("network", "disconnect", "t" * 64, container=cid),
                event("container", "stop", cid)]

    def test_boundary_stop_and_labeled_helper_pass(self):
        events = self.stop_sequence() + [self.helper(a) for a in ("create", "attach", "start", "die", "destroy")]
        summary = self.judge(events)
        self.assertEqual((summary["boundary_events"], summary["runner_container_events"]), ([], 5))
        self.assertEqual(events_reasons(summary), [])

    def test_helper_network_events_only_the_none_network(self):
        none = {"name": "none", "type": "null"}
        ok = [self.helper("create"), event("network", "connect", "z" * 64, container=self.HELPER, **none),
              self.helper("start"), self.helper("die"), event("network", "disconnect", "z" * 64, container=self.HELPER, **none),
              self.helper("destroy")]
        for events in (ok, [e for e in ok if e["Type"] != "network"]):  # 이벤트가 있든 없든 통과
            with self.subTest(network_events=len(events)):
                summary = self.judge(self.stop_sequence() + events)
                self.assertEqual((summary["boundary_events"], events_reasons(summary)), ([], []))
        # 리뷰 반례: 정상 정지·도우미 생명주기 사이에 회차 밖 망 연결을 끼우면 위반이다(최종 상태가 망 없음이어도).
        foreign = {"name": "outside", "type": "bridge"}
        cases = {
            "connect_foreign": [event("network", "connect", "o" * 64, container=self.HELPER, **foreign)],
            "disconnect_foreign": [event("network", "disconnect", "o" * 64, container=self.HELPER, **foreign)],
            "connect_run_network": [event("network", "connect", "n" * 64, container=self.HELPER, name="target", type="bridge")],
            "none_but_bridge_type": [event("network", "connect", "o" * 64, container=self.HELPER, name="none", type="bridge")],
            "none_twice": [event("network", "connect", "z" * 64, container=self.HELPER, **none)] * 2,
        }
        cases.update({  # 정상 none 이벤트 없이 하나만 온 경우(이름·종류 각각만 어긋남)
            "only_name_other": [event("network", "connect", "o" * 64, container=self.HELPER, name="outside", type="null")],
            "only_name_other_no_type": [event("network", "connect", "o" * 64, container=self.HELPER, name="outside")],
            "only_type_other": [event("network", "connect", "o" * 64, container=self.HELPER, name="none", type="bridge")],
        })
        quiet = [e for e in ok if e["Type"] != "network"]
        for name, extra in cases.items():
            with self.subTest(case=name):
                lifecycle = quiet if name.startswith("only_") else ok
                summary = self.judge(self.stop_sequence() + lifecycle[:3] + extra + lifecycle[3:])
                self.assertTrue(summary["boundary_events"])
                self.assertEqual(summary["boundary_events"][-1]["service"], "runner_helper")
                self.assertIn("window_boundary_changed", events_reasons(summary))
        # 도우미 예외는 선언한 도우미 ID에만 있다: 목록이 비면 같은 이벤트도 도우미 예외가 아니다.
        self.assertEqual(self.judge([event("network", "connect", "z" * 64, container=self.HELPER, **none)], helpers={})
                         ["runner_container_events"], 0)

    def test_label_alone_is_not_an_exception(self):
        # 같은 라벨을 단 다른 컨테이너(러너가 띄운 ID가 아님)는 회차 밖 컨테이너다.
        spoof = event("container", "start", OTHER, **{RUNNER_HELPER_LABEL: "boundary-evidence"})
        self.assertEqual(self.judge([spoof])["boundary_events"], [{"type": "container", "action": "start", "service": "foreign"}])
        # 러너 ID라도 라벨이 없거나 역할이 다르면 예외가 아니다.
        for attributes in ({}, {RUNNER_HELPER_LABEL: "other-role"}):
            with self.subTest(attributes=attributes):
                summary = self.judge([event("container", "start", self.HELPER, **attributes)])
                self.assertEqual(summary["boundary_events"], [{"type": "container", "action": "start", "service": "runner_helper"}])
        # 도우미 예외는 probe에 번지지 않는다: 도우미 목록이 비면 같은 ID·라벨도 회차 밖이다.
        self.assertEqual(len(self.judge([self.helper("start")], helpers={})["boundary_events"]), 1)

    def test_helper_only_gets_its_lifecycle_once(self):
        for action, extra in (("exec_create: sh -c id", {"execID": "h1"}), ("kill", {"signal": "9"}), ("restart", {}),
                              ("pause", {})):
            with self.subTest(action=action):
                self.assertEqual(len(self.judge([self.helper(action, **extra)])["boundary_events"]), 1)
        twice = self.judge([self.helper("start"), self.helper("start")])
        self.assertEqual(twice["boundary_events"], [{"type": "container", "action": "start", "service": "runner_helper"}])

    def test_stop_exception_is_only_the_declared_graceful_stop(self):
        cases = {
            "sigkill": [event("container", "kill", self.BOUNDARY, signal="9")],
            "die_without_sigterm": [event("container", "die", self.BOUNDARY)],
            "restart_after_stop": self.stop_sequence() + [event("container", "start", self.BOUNDARY)],
            "second_stop": self.stop_sequence() + [event("container", "kill", self.BOUNDARY, signal="15")],
            "disconnect_before_die": [event("container", "kill", self.BOUNDARY, signal="15"),
                                      event("network", "disconnect", "n" * 64, container=self.BOUNDARY)],
            "disconnect_twice": self.stop_sequence() + [event("network", "disconnect", "n" * 64, container=self.BOUNDARY)],
            "other_service_stopped": self.stop_sequence(IDS["engine"]),
            "seal_signal_on_boundary": [event("container", "kill", self.BOUNDARY, signal="10")],
        }
        for name, events in cases.items():
            with self.subTest(case=name):
                self.assertTrue(self.judge(events)["boundary_events"])
        # 정지 대상을 선언하지 않은 회차에서는 경계 정지도 위반이다.
        self.assertTrue(self.judge(self.stop_sequence(), stoppable=frozenset())["boundary_events"])


class TestJudgeEvents(unittest.TestCase):
    def test_expected_events_pass(self):
        events = [
            event("container", "kill", IDS["eval-meter"], signal="10", name="m"),
            event("container", "kill", IDS["eval-llm-gw"], signal="10", name="g"),
            event("container", "exec_create: " + ENGINE_DB_HEALTHCHECK, IDS["engine-db"], execID="x1"),
            event("container", "exec_start: " + ENGINE_DB_HEALTHCHECK, IDS["engine-db"], execID="x1"),
            event("container", "exec_die", IDS["engine-db"], execID="x1", exitCode="0"),
            event("container", "health_status: healthy", IDS["engine-db"]),
            # 러너 자신의 probe(회차 밖, 엔진 netns 공유)는 cidfile ID로만 허용한다.
            event("container", "create", PROBE, name="probe"), event("container", "start", PROBE),
            event("container", "die", PROBE, exitCode="0"),
            # 이미 있던 회차 밖 컨테이너의 종료와 회차 밖 망·볼륨 이벤트는 새 namespace 공유를 만들지 않는다.
            event("container", "die", OTHER, exitCode="0"), event("container", "destroy", OTHER),
            event("network", "connect", "z" * 64, container=PROBE, name="none"),
            event("volume", "mount", "v" * 64, container=IDS["engine-db"]),
        ]
        summary = judge(events, runner=frozenset({PROBE}))
        self.assertEqual((summary["boundary_events"], summary["complete"], summary["event_count"]), ([], True, 13))
        self.assertEqual(summary["kinds"]["container.kill"], 2)
        self.assertEqual(summary["runner_container_events"], 3)
        self.assertEqual(events_reasons(summary), [])

    def test_boundary_changes_are_caught(self):
        cases = {
            # 스모크 고장 주입과 같은 일: 엔진을 대상 망에 붙임
            "engine_attached_to_target_net": event("network", "connect", "t" * 64, container=IDS["engine"]),
            "foreign_attached_to_run_net": event("network", "connect", "t" * 64, container=OTHER),
            "run_container_to_outside_net": event("network", "connect", "z" * 64, container=IDS["engine"]),
            "disconnect": event("network", "disconnect", "n" * 64, container=IDS["eval-meter"]),
            "run_network_destroyed": event("network", "destroy", "t" * 64),
            "target_restart": event("container", "restart", IDS["target"]),
            "target_exec": event("container", "exec_create: sh -c 'echo x'", IDS["target"], execID="y"),
            "meter_killed_other_signal": event("container", "kill", IDS["eval-meter"], signal="9"),
            "engine_seal_signal": event("container", "kill", IDS["engine"], signal="10"),
            "db_other_exec": event("container", "exec_create: /bin/sh -c psql", IDS["engine-db"], execID="z"),
            "db_unknown_exec_die": event("container", "exec_die", IDS["engine-db"], execID="nope"),
            "db_unhealthy": event("container", "health_status: unhealthy", IDS["engine-db"]),
            "db_healthcheck_without_exec_id": event("container", "exec_create: " + ENGINE_DB_HEALTHCHECK,
                                                    IDS["engine-db"]),
            "engine_stop": event("container", "stop", IDS["engine"]),
            # 회차 밖 컨테이너는 `--network container:<회차 컨테이너>`로 망 이벤트 없이 namespace를 같이 쓸 수 있다.
            "foreign_container_created": event("container", "create", OTHER),
            "foreign_container_started": event("container", "start", OTHER),
            "foreign_container_restarted": event("container", "restart", OTHER),
        }
        for name, item in cases.items():
            with self.subTest(name=name):
                summary = judge([item])
                self.assertEqual(len(summary["boundary_events"]), 1)
                self.assertEqual(events_reasons(summary), ["window_boundary_changed"])
        attach = judge([cases["engine_attached_to_target_net"]])["boundary_events"][0]
        self.assertEqual(attach, {"type": "network", "action": "connect", "network": "target_net", "service": "engine"})
        foreign = judge([cases["foreign_attached_to_run_net"]])["boundary_events"][0]
        self.assertEqual(foreign["service"], "foreign")
        # 러너 probe ID라도 정상 생명주기 밖의 동작은 위반(#126 리뷰: 같은 CID에 exec·kill·restart 주입)
        for action, extra in (("exec_create: sh -c id", {"execID": "p1"}), ("exec_start: sh -c id", {"execID": "p1"}),
                              ("exec_die", {"execID": "p1"}), ("kill", {"signal": "9"}), ("restart", {}),
                              ("stop", {}), ("pause", {})):
            with self.subTest(probe_action=action):
                summary = judge([event("container", action, PROBE, **extra)], runner=frozenset({PROBE}))
                self.assertEqual(summary["boundary_events"],
                                 [{"type": "container", "action": action.split(":", 1)[0], "service": "runner_probe"}])
                self.assertEqual(summary["runner_container_events"], 0)
        # 정상 생명주기도 각 한 번뿐이다(다시 시작하면 namespace 공유가 다시 살아난다).
        lifecycle = [event("container", a, PROBE) for a in ("create", "attach", "start", "die", "destroy")]
        normal = judge(lifecycle, runner=frozenset({PROBE}))
        self.assertEqual((normal["runner_container_events"], normal["boundary_events"]), (5, []))
        again = judge(lifecycle + [event("container", "start", PROBE)], runner=frozenset({PROBE}))
        self.assertEqual(again["boundary_events"], [{"type": "container", "action": "start", "service": "runner_probe"}])
        # 두 probe(pre·post)는 각자 한 번씩
        other_probe = "5" * 64
        both = judge(lifecycle + [event("container", a, other_probe) for a in ("create", "start")],
                     runner=frozenset({PROBE, other_probe}))
        self.assertEqual((both["runner_container_events"], both["boundary_events"]), (7, []))
        started = judge([cases["foreign_container_started"]], runner=frozenset({PROBE}))["boundary_events"]
        self.assertEqual(started, [{"type": "container", "action": "start", "service": "foreign"}])
        # 러너 probe로 인정하는 건 정확히 그 ID다. 회차 컨테이너 ID를 넣어도 회차 컨테이너 규칙이 먼저다.
        self.assertEqual(len(judge([cases["target_restart"]], runner=frozenset({IDS["target"]}))["boundary_events"]), 1)

    def test_completeness_follows_the_stream_and_replay(self):
        # 구간 안 이벤트는 스트림으로 받으므로 개수와 무관하다(실측: healthcheck만으로 초당 1.5건).
        many = [event("container", "die", OTHER)] * (DOCKER_EVENTS_BUFFER * 4)  # 판정 밖 이벤트
        self.assertTrue(judge(many)["complete"])
        self.assertEqual(events_reasons(judge(many, complete=False)), ["window_events_incomplete"])
        # 구간 시작 전 재생분(lookback)이 데몬 버퍼 크기에 닿으면 앞쪽이 버려졌을 수 있다.
        replay = [event("container", "create", OTHER, at=START - 5)] * (DOCKER_EVENTS_BUFFER - 1)
        self.assertTrue(judge(replay)["complete"])
        self.assertFalse(judge(replay + [event("container", "create", OTHER, at=START - 1)])["complete"])

    def test_lookback_only_teaches_healthcheck_exec_ids(self):
        # 11/03 스모크: 구간 직전에 시작해 구간 안에서 끝난 healthcheck exec가 위반으로 잡혔다.
        straddling = [
            event("container", "exec_create: " + ENGINE_DB_HEALTHCHECK, IDS["engine-db"], at=START - 10, execID="h0"),
            event("container", "exec_start: " + ENGINE_DB_HEALTHCHECK, IDS["engine-db"], at=START - 9, execID="h0"),
            event("container", "exec_die", IDS["engine-db"], at=START + 5, execID="h0", exitCode="0"),
        ]
        summary = judge(straddling)
        self.assertEqual((summary["boundary_events"], summary["event_count"], summary["lookback_events"]), ([], 1, 2))
        # 구간 전 다른 exec는 배우지 않으므로 구간 안의 그 exec_die는 위반이다.
        other = [event("container", "exec_create: /bin/sh -c psql", IDS["engine-db"], at=START - 10, execID="x"),
                 event("container", "exec_die", IDS["engine-db"], at=START + 5, execID="x")]
        self.assertEqual(len(judge(other)["boundary_events"]), 1)
        # 구간 전 사건(대상 재시작 등)은 판정하지 않는다(pre 점검이 맡는다).
        before = [event("container", "restart", IDS["target"], at=START - 1)]
        self.assertEqual(judge(before)["boundary_events"], [])

    def test_malformed_events_fail_closed(self):
        for bad in ([None], [{"Type": "container"}], [{"Type": 1, "Action": "x", "Actor": {"ID": "a"}}],
                    [{"Type": "container", "Action": "start", "Actor": []}],
                    [{"Type": "container", "Action": "start", "Actor": {"ID": 7}}],
                    [{"Type": "container", "Action": "start", "Actor": {"ID": "a", "Attributes": []},
                      "timeNano": START + 1}]):
            with self.subTest(bad=str(bad)[:40]), self.assertRaises(WindowError):
                judge(bad)
        with self.assertRaises(WindowError):
            judge_events({}, START, NET, RUN, SEALABLE, HEALTH, frozenset(), True)
        for stamp in ("1", None, 1.5, True):
            with self.subTest(stamp=stamp), self.assertRaises(WindowError):
                judge([dict(event("container", "start", OTHER), timeNano=stamp)])
        with self.assertRaises(WindowError):
            judge_events([], "0", NET, RUN, SEALABLE, HEALTH, frozenset(), True)

    def test_healthcheck_matches_compose_declaration(self):
        # compose의 CMD-SHELL과 다르면 정상 healthcheck가 위반으로 잡히거나, 다른 exec가 허용된다.
        text = (REPO_ROOT / "deploy/engine-isolated/compose.yaml").read_text(encoding="utf-8")
        tests = re.findall(r"^\s*test:\s*(\[.*\])\s*$", text, re.M)
        self.assertEqual(len(tests), 1)
        kind, command = json.loads(tests[0])
        self.assertEqual(kind, "CMD-SHELL")
        self.assertEqual("/bin/sh -c " + command.replace("$$", "$"), ENGINE_DB_HEALTHCHECK)


class TestHostWatch(unittest.TestCase):
    def test_summary_counts_edges_and_violations(self):
        samples = [WatchSample(1.0, (), None), WatchSample(4.0, ("host.listen.0.0.0.0:80: NOT_ALLOWED",), None),
                   WatchSample(6.0, (), "host_listen_read_failed")]
        summary = watch_summary(0.5, 10.0, samples, 5)
        self.assertEqual((summary["samples"], summary["max_gap_seconds"], summary["window_seconds"]), (3, 4.0, 9.5))
        self.assertEqual(summary["violations"], ["host.listen.0.0.0.0:80: NOT_ALLOWED"])
        self.assertEqual(summary["errors"], ["host_listen_read_failed"])
        self.assertEqual(watch_reasons(summary), ["window_host_condition_violation"])
        quiet = watch_summary(0.0, 4.0, [WatchSample(2.0, (), None)], 2)
        self.assertEqual(watch_reasons(quiet), [])
        gap = watch_summary(0.0, 4.0, [WatchSample(1.0, (), None)], 2)  # 마지막 표본~끝 3초
        self.assertEqual(watch_reasons(gap), ["window_host_watch_gap"])
        late_start = watch_summary(0.0, 3.5, [WatchSample(3.0, (), None)], 2)  # 시작~첫 표본 3초
        self.assertEqual(watch_reasons(late_start), ["window_host_watch_gap"])
        read_error_only = watch_summary(0.0, 1.0, [WatchSample(0.5, (), "host_listen_read_failed")], 5)
        self.assertEqual(watch_reasons(read_error_only), ["window_host_condition_violation"])

    def test_summary_rejects_impossible_samples(self):
        for started, finished, samples in ((0.0, 1.0, []), (2.0, 1.0, [WatchSample(1.5, (), None)]),
                                           (0.0, 1.0, [WatchSample(2.0, (), None)])):
            with self.assertRaises(WindowError):
                watch_summary(started, finished, samples, 5)

    def test_sample_records_violations_and_read_errors(self):
        allowed = frozenset({Listener("10.255.255.254", 53)})
        listeners = iter([frozenset({Listener("10.255.255.254", 53), Listener("127.0.0.1", 9)}),
                          frozenset({Listener("0.0.0.0", 3000)})])

        def read():
            try:
                return next(listeners)
            except StopIteration:
                raise OSError("proc gone") from None
        watch = HostWatch(read, allowed, iter([1.0, 2.0, 3.0]).__next__)
        for _ in range(3):
            watch.sample()
        self.assertEqual([s.violations for s in watch.samples], [(), ("host.listen.0.0.0.0:3000: NOT_ALLOWED",), ()])
        self.assertEqual([s.error for s in watch.samples], [None, None, "host_listen_read_failed"])

    def test_background_watch_samples_until_stopped(self):
        watch = HostWatch(lambda: frozenset(), frozenset(), time.monotonic)
        handle = BackgroundWatch(watch, 0.01).start()
        time.sleep(0.08)
        started, finished, samples = handle.stop()
        self.assertGreaterEqual(len(samples), 3)  # 시작·주기·멈춤 표본
        self.assertLess(started, finished)
        summary = watch_summary(started, finished, samples, 1)
        self.assertEqual(watch_reasons(summary), [])
        with self.assertRaises(WindowError):
            BackgroundWatch(watch, 0)

    def test_stop_takes_a_final_sample(self):
        # 간격이 길어 주기 표본이 없을 때도 시작·멈춤 표본 두 개로 구간 양끝을 덮는다.
        watch = HostWatch(lambda: frozenset(), frozenset(), time.monotonic)
        started, finished, samples = BackgroundWatch(watch, 60).start().stop()
        self.assertEqual(len(samples), 2)
        self.assertEqual(samples[-1].monotonic, finished)


if __name__ == "__main__":
    unittest.main()
