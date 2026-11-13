"""제어 채널 클라이언트(docs/contracts/e2-runner-control-proposal.md). 가짜 서버만 쓰며 제품 E②로 돌린 것이 아니다."""

from __future__ import annotations

import json
import os
import socket
import struct
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from e2_control import (FORMAT, MAX_MESSAGE_BYTES, SOCKET_NAME, ControlChannel, E2ControlError, E2ControlUnavailable,  # noqa: E402
                        ServiceExpectation, Waiting, close_execution, wait_service_ready)

RUN, RESERVATION = "eval-run-1", "reservation-1"
SHA_A, SHA_B, MAC = "a" * 64, "b" * 64, "c" * 64
EVALUATION = {"llm_backend": "api", "target_kind": "public_practice"}
EXPECTED = ServiceExpectation(config_sha256=SHA_A, header_mode="evaluation_caller_passthrough", evaluation=EVALUATION,
                              policy_fingerprint_sha256=SHA_B)
SECRET_LIKE = "synthetic-value-41c2"


def message(kind, **fields):
    return {"format": FORMAT, "type": kind, "eval_run_id": RUN, "reservation_ref": RESERVATION, **fields}


def line(value):
    return json.dumps(value).encode() + b"\n"


def ready(state="service_ready", **changes):
    filled = {"reason": None, "config_sha256": SHA_A, "header_mode": EXPECTED.header_mode, "evaluation": dict(EVALUATION),
              "policy_fingerprint_sha256": SHA_B}
    empty = {"reason": None, "config_sha256": None, "header_mode": None, "evaluation": None, "policy_fingerprint_sha256": None}
    return message("ready", state=state, **{**(filled if state == "service_ready" else empty), **changes})


def head(state="running", **changes):
    filled = {"execution_ref": "execution-1", "run_ref": "e2-run-1", "engine_task_id": "17", "policy_fingerprint_sha256": SHA_B,
              "checkpoint": {"sequence": 4, "mac": MAC}}
    empty = {"execution_ref": None, "run_ref": None, "engine_task_id": None, "policy_fingerprint_sha256": None, "checkpoint": None}
    return message("head", state=state, **{**(empty if state == "no_execution" else filled), **changes})


def closed(**changes):
    return message("close", **{"close_request_ref": "close-1", "execution_ref": "execution-1", "admission_closed": True, **changes})


def status(state="complete", **changes):
    filled = {"reason": None, "close_request_ref": "close-1", "execution_ref": "execution-1", "manifest_sha256": SHA_A,
              "checkpoint": {"sequence": 6, "mac": MAC}}
    closing = {"reason": None, "close_request_ref": "close-1", "execution_ref": "execution-1", "manifest_sha256": None, "checkpoint": None}
    incomplete = closing | {"reason": "audit_failed"}
    missing = closing | {"execution_ref": None}
    base = {"complete": filled, "closing": closing, "incomplete": incomplete, "no_execution": missing}[state]
    return message("close_status", state=state, **{**base, **changes})


class FakeControl:
    """요청 한 줄을 받아 정해 둔 답을 돌려주는 Unix socket 서버. 답이 None이면 답 없이 닫는다."""

    def __init__(self, directory, answer):
        self.answer, self.requests, self.fd = answer, [], os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.bind(f"/proc/self/fd/{self.fd}/{SOCKET_NAME}")
        self.sock.listen(8)
        self.thread = threading.Thread(target=self._serve, daemon=True)
        self.thread.start()

    def _serve(self):
        while True:
            try:
                conn, _ = self.sock.accept()
            except OSError:
                return
            with conn:
                data = b""
                while not data.endswith(b"\n"):
                    chunk = conn.recv(4096)
                    if not chunk:
                        break
                    data += chunk
                if not data:
                    continue
                self.requests.append(json.loads(data))
                reply = self.answer(self.requests[-1])
                if isinstance(reply, threading.Event):
                    reply.wait(5)
                elif callable(reply):
                    reply(conn)
                elif reply is not None:
                    conn.sendall(reply)

    def close(self):
        # shutdown이 accept에서 기다리는 스레드를 깨운다. close만으로는 깨지 않는다.
        try:
            self.sock.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        self.sock.close()
        self.thread.join(5)
        os.close(self.fd)


class FakeTime:
    def __init__(self):
        self.t, self.sleeps = 0.0, []

    def sleep(self, seconds):
        self.sleeps.append(seconds)
        self.t += seconds

    def waiting(self, poll=2):
        return Waiting(lambda: self.t, self.sleep, poll)


def dribble(data, size=64, pause=0.05):
    """조금씩 천천히 보내는 답. 호출자가 끊으면 멈춘다."""
    def send(conn):
        try:
            for start in range(0, len(data), size):
                conn.sendall(data[start:start + size])
                time.sleep(pause)
        except OSError:
            pass
    return send


def padded(value, total=4096):
    body = json.dumps(value).encode()
    return body + b" " * (total - len(body)) + b"\n"


class Case(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.temp = Path(temp.name)
        self.directory = self.temp / "control"
        self.directory.mkdir(mode=0o700)
        self.time = FakeTime()

    def serve(self, *replies, always=None):
        queue = list(replies)

        def answer(_request):
            if always is not None:
                return always
            return queue.pop(0) if queue else None
        fake = FakeControl(self.directory, answer)
        self.addCleanup(fake.close)
        return fake

    def channel(self, timeout=2):
        return ControlChannel(self.directory, RUN, RESERVATION, timeout)

    def rejected(self, code, action, error=E2ControlError):
        with self.assertRaises(error) as caught:
            action()
        self.assertEqual(caught.exception.code, code)
        self.assertNotIn(SECRET_LIKE, str(caught.exception))
        self.assertIsNone(caught.exception.__cause__)
        self.assertTrue(caught.exception.__suppress_context__)
        return caught.exception


class TestTransport(Case):
    def test_request_carries_the_run_values(self):
        fake = self.serve(line(ready()))
        self.assertEqual(self.channel().ready()["state"], "service_ready")
        self.assertEqual(fake.requests, [{"format": FORMAT, "type": "ready", "eval_run_id": RUN, "reservation_ref": RESERVATION}])

    def test_close_requests_name_the_reference(self):
        fake = self.serve(line(closed()), line(status()))
        self.channel().close("close-1")
        self.channel().close_status("close-1")
        self.assertEqual([(r["type"], r["close_request_ref"]) for r in fake.requests], [("close", "close-1"), ("close_status", "close-1")])

    def test_long_directory_paths_still_connect(self):
        deep = self.temp / ("d" * 60) / ("e" * 60)
        deep.mkdir(parents=True)
        os.chmod(deep, 0o700)
        fake = FakeControl(deep, lambda _r: line(ready()))
        self.addCleanup(fake.close)
        self.assertEqual(ControlChannel(deep, RUN, RESERVATION, 2).ready()["state"], "service_ready")

    def test_no_listener_is_unavailable(self):
        self.rejected("control_unreachable", self.channel().ready, E2ControlUnavailable)

    def test_directory_must_be_private_and_not_a_link(self):
        os.chmod(self.directory, 0o750)
        self.rejected("control_dir_not_private", self.channel().ready)
        os.chmod(self.directory, 0o700)
        link = self.temp / "link"
        link.symlink_to(self.directory)
        self.rejected("control_dir_unavailable", ControlChannel(link, RUN, RESERVATION, 2).ready)
        self.rejected("control_dir_unavailable", ControlChannel(self.temp / "absent", RUN, RESERVATION, 2).ready)
        with mock.patch("e2_control.os.geteuid", return_value=os.geteuid() + 1):
            self.rejected("control_dir_not_private", self.channel().ready)

    def test_peer_with_another_uid_gets_no_message(self):
        fake = self.serve(always=line(ready()))
        real = os.geteuid()
        with mock.patch("e2_control.os.geteuid", side_effect=[real, real + 1]):
            self.rejected("control_peer_uid_mismatch", self.channel().ready)
        self.assertEqual(fake.requests, [])

    def test_cut_or_silent_replies_are_unavailable(self):
        self.serve()
        self.rejected("response_truncated", self.channel().ready, E2ControlUnavailable)

    def test_timeout_is_unavailable(self):
        stuck = threading.Event()
        self.addCleanup(stuck.set)
        self.serve(always=stuck)
        self.rejected("control_deadline", self.channel(timeout=0.05).ready, E2ControlUnavailable)

    def test_broken_transfer_is_unavailable(self):
        fake = self.serve(always=line(ready()))
        with mock.patch.object(socket.socket, "sendall", side_effect=BrokenPipeError("synthetic")):
            self.rejected("control_io_failed", self.channel().ready, E2ControlUnavailable)
        self.assertEqual(fake.requests, [])

    def test_message_size_limits(self):
        self.serve(b" " * (MAX_MESSAGE_BYTES + 1) + b"\n")
        self.rejected("response_too_large", self.channel().ready)
        self.rejected("request_too_large", lambda: self.channel().close("x" * MAX_MESSAGE_BYTES))

    def test_largest_reply_is_accepted(self):
        data = line(ready()).rstrip(b"\n")
        self.serve(data + b" " * (MAX_MESSAGE_BYTES - len(data) - 1) + b"\n")
        self.assertEqual(self.channel().ready()["state"], "service_ready")

    def test_replies_must_be_one_closed_object_for_this_run(self):
        cases = ((line(ready()) + b"{}\n", "response_invalid"), (b"not json\n", "response_invalid"), (b"\xff\n", "response_invalid"),
                 (b'{"state": NaN}\n', "response_invalid"), (b'{"a": 1, "a": 2}\n', "response_duplicate_key"), (b"[]\n", "response_format"),
                 (line(ready() | {"format": "other/v2"}), "response_format"), (line(ready() | {"eval_run_id": "other"}), "response_other_run"),
                 (line(ready() | {"reservation_ref": "other"}), "response_other_run"), (line(closed()), "response_fields"),
                 (line(ready() | {"extra": 1}), "response_fields"),
                 # 필드가 같아도 다른 요청의 답은 받지 않는다.
                 (line(ready() | {"type": "head"}), "response_fields"))
        for reply, code in cases:
            with self.subTest(code=code, reply=reply[:30]):
                fake = FakeControl(self.directory, lambda _r, data=reply: data)
                try:
                    self.rejected(code, self.channel().ready)
                finally:
                    fake.close()
                    (self.directory / SOCKET_NAME).unlink()
        missing = ready()
        del missing["reason"]
        self.serve(line(missing))
        self.rejected("response_fields", self.channel().ready)

    def test_refusal_carries_a_fixed_code_only(self):
        fake = FakeControl(self.directory, lambda _r: line(message("error", code="closed")))
        self.rejected("refused_closed", self.channel().ready)
        fake.close()
        (self.directory / SOCKET_NAME).unlink()
        fake = FakeControl(self.directory, lambda _r: line(message("error", code=SECRET_LIKE)))
        self.rejected("response_error_invalid", self.channel().ready)
        fake.close()
        (self.directory / SOCKET_NAME).unlink()
        fake = FakeControl(self.directory, lambda _r: line(message("error", code="synthetic_secret_123")))
        self.rejected("response_error_invalid", self.channel().ready)
        fake.close()
        (self.directory / SOCKET_NAME).unlink()
        fake = FakeControl(self.directory, lambda _r: line(message("error", code=7)))
        self.rejected("response_error_invalid", self.channel().ready)
        fake.close()
        (self.directory / SOCKET_NAME).unlink()
        self.serve(line(message("error", code="closed", detail="x")))
        self.rejected("response_error_invalid", self.channel().ready)


class ReplyCase(Case):
    def replies(self, cases, action):
        for reply, code in cases:
            with self.subTest(code=code):
                fake = FakeControl(self.directory, lambda _r, data=line(reply): data)
                try:
                    self.rejected(code, action)
                finally:
                    fake.close()
                    (self.directory / SOCKET_NAME).unlink()


class TestReadyShape(ReplyCase):
    def test_states_accept_only_their_fields(self):
        for reply in (ready(), ready("starting"), ready("failed", reason="config_rejected"), ready(evaluation=None)):
            with self.subTest(state=reply["state"]):
                fake = FakeControl(self.directory, lambda _r, data=line(reply): data)
                try:
                    self.assertEqual(self.channel().ready()["state"], reply["state"])
                finally:
                    fake.close()
                    (self.directory / SOCKET_NAME).unlink()

    def test_bad_shapes(self):
        self.replies(((ready("other"), "ready_state_invalid"), (ready(reason=SECRET_LIKE), "ready_reason_invalid"), (ready("failed", reason="synthetic_secret_123"), "ready_reason_invalid"),
                      (ready("failed", reason=[]), "ready_reason_invalid"), (ready(state=[]), "ready_state_invalid"),
                      (ready(header_mode="synthetic_secret_123"), "ready_fields_missing"), (ready(header_mode="test_account_passthrough", evaluation=None), "ready_fields_missing"), (ready(header_mode=[]), "ready_fields_missing"),
                      (ready(evaluation=EVALUATION | {"llm_backend": "synthetic_secret_123"}), "ready_evaluation_invalid"),
                      (ready(evaluation=EVALUATION | {"target_kind": "synthetic_secret_123"}), "ready_evaluation_invalid"),
                      (ready(config_sha256=None), "ready_fields_missing"), (ready(policy_fingerprint_sha256="x"), "ready_fields_missing"),
                      (ready(header_mode=None), "ready_fields_missing"), (ready(evaluation={}), "ready_evaluation_invalid"),
                      (ready(evaluation="api"), "ready_evaluation_invalid"), (ready(evaluation=EVALUATION | {"run_ref": "x"}), "ready_evaluation_invalid"),
                      (ready(evaluation={"llm_backend": 7, "target_kind": "x"}), "ready_evaluation_invalid"),
                      (ready("starting", config_sha256=SHA_A), "ready_fields_early"), (ready("failed", header_mode="preserve"), "ready_fields_early"),
                      (ready("starting", evaluation=EVALUATION), "ready_fields_early"),
                      (ready("failed", policy_fingerprint_sha256=SHA_B), "ready_fields_early")), self.channel().ready)


class TestBind(ReplyCase):
    def test_bind_sends_and_checks_the_value(self):
        fake = self.serve(line(message("bind", reset_generation="target-1")))
        self.assertIsNone(self.channel().bind("target-1"))
        self.assertEqual(fake.requests[0]["reset_generation"], "target-1")

    def test_bad_replies(self):
        self.replies(((message("bind", reset_generation="other"), "bind_mismatch"), (message("bind"), "response_fields"),
                      (message("bind", reset_generation="target-1", extra=1), "response_fields"),
                      (message("error", code="bind_conflict"), "refused_bind_conflict")), lambda: self.channel().bind("target-1"))


class TestSetupFailures(Case):
    """자원 부족·peer 확인 실패도 고정 오류다. 시작 단계는 typed 오류, 종료 단계는 결과로 남는다."""

    def test_socket_creation_failure(self):
        with mock.patch("e2_control.socket.socket", side_effect=OSError("synthetic")):
            self.rejected("control_setup_failed", self.channel().ready, E2ControlUnavailable)
            self.rejected("service_ready_timeout", lambda: wait_service_ready(self.channel(), EXPECTED, self.time.waiting(), 1))
            result = close_execution(self.channel(), "close-1", self.time.waiting(), 1, 1)
        self.assertEqual((result.state, result.reason, result.admission_closed), ("unavailable", "control_setup_failed", False))

    def test_peer_check_failure(self):
        fake = self.serve(always=line(ready()))
        with mock.patch.object(socket.socket, "getsockopt", side_effect=OSError("synthetic")):
            self.rejected("control_setup_failed", self.channel().ready, E2ControlUnavailable)
        with mock.patch("e2_control.struct.unpack", side_effect=struct.error("synthetic")):
            self.rejected("control_setup_failed", self.channel().ready, E2ControlUnavailable)
        self.assertEqual(fake.requests, [])

    def test_directory_stat_failure(self):
        with mock.patch("e2_control.os.fstat", side_effect=OSError("synthetic")):
            self.rejected("control_dir_unavailable", self.channel().ready)

    def test_failures_keep_no_descriptors(self):
        before = len(os.listdir("/proc/self/fd"))
        with mock.patch("e2_control.socket.socket", side_effect=OSError("synthetic")):
            self.rejected("control_setup_failed", self.channel().ready, E2ControlUnavailable)
        with mock.patch("e2_control.os.fstat", side_effect=OSError("synthetic")):
            self.rejected("control_dir_unavailable", self.channel().ready)
        os.chmod(self.directory, 0o750)
        self.rejected("control_dir_not_private", self.channel().ready)
        os.chmod(self.directory, 0o700)
        self.rejected("control_unreachable", self.channel().ready, E2ControlUnavailable)
        self.assertEqual(len(os.listdir("/proc/self/fd")), before)


class TestDeadlines(Case):
    """단계 시한이 소켓 읽기까지 간다. 조금씩 오는 답이 시한을 늘리지 못한다."""

    def real(self, poll=0.01):
        return Waiting(time.monotonic, time.sleep, poll)

    def test_a_dribbling_reply_cannot_outlast_one_exchange(self):
        self.serve(always=dribble(padded(ready())))
        started = time.monotonic()
        self.rejected("control_deadline", self.channel(timeout=0.2).ready, E2ControlUnavailable)
        self.assertLess(time.monotonic() - started, 1.5)

    def test_a_dribbling_reply_cannot_outlast_the_close_phase(self):
        def answer(request):
            return line(closed()) if request["type"] == "close" else dribble(padded(status()))
        fake = FakeControl(self.directory, answer)
        self.addCleanup(fake.close)
        started = time.monotonic()
        result = close_execution(self.channel(timeout=30), "close-1", self.real(), 1, 0.2)
        self.assertLess(time.monotonic() - started, 1.5)
        self.assertEqual((result.state, result.reason, result.ack), ("unavailable", "control_deadline", None))

    def test_a_dribbling_reply_cannot_outlast_the_admission_wait(self):
        self.serve(always=dribble(padded(closed())))
        started = time.monotonic()
        result = close_execution(self.channel(timeout=30), "close-1", self.real(), 0.2, 1)
        self.assertLess(time.monotonic() - started, 1.5)
        self.assertEqual((result.state, result.reason, result.admission_closed), ("unavailable", "control_deadline", False))

    def test_a_dribbling_reply_cannot_outlast_the_ready_wait(self):
        self.serve(always=dribble(padded(ready())))
        started = time.monotonic()
        self.rejected("service_ready_timeout", lambda: wait_service_ready(self.channel(timeout=30), EXPECTED, self.real(), 0.2))
        self.assertLess(time.monotonic() - started, 1.5)

    def test_success_arriving_after_the_deadline_is_not_taken(self):
        now = [0.0]

        def answer(_request):
            now[0] = 100.0
            return line(ready())
        fake = FakeControl(self.directory, answer)
        self.addCleanup(fake.close)
        self.rejected("control_deadline", lambda: self.channel().ready(lambda: 5 - now[0]), E2ControlUnavailable)

    def test_a_connect_timeout_is_the_deadline(self):
        with mock.patch.object(socket.socket, "connect", side_effect=TimeoutError("synthetic")):
            self.rejected("control_deadline", self.channel().ready, E2ControlUnavailable)

    def test_an_expired_phase_does_not_connect(self):
        fake = self.serve(always=line(ready()))
        self.rejected("control_deadline", lambda: self.channel().ready(lambda: 0), E2ControlUnavailable)
        self.assertEqual(fake.requests, [])


class TestHeadShape(ReplyCase):
    def test_states(self):
        for reply in (head(), head("closing"), head("complete"), head("incomplete", checkpoint=None), head("no_execution")):
            with self.subTest(state=reply["state"]):
                fake = FakeControl(self.directory, lambda _r, data=line(reply): data)
                try:
                    self.assertEqual(self.channel().head()["state"], reply["state"])
                finally:
                    fake.close()
                    (self.directory / SOCKET_NAME).unlink()

    def test_bad_shapes(self):
        self.replies(((head("other"), "head_state_invalid"), (head() | {"state": []}, "head_state_invalid"), (head() | {"state": {}}, "head_state_invalid"), (head(execution_ref=None), "head_fields_missing"), (head(run_ref="http://x"), "head_fields_missing"),
                      (head(engine_task_id=17), "head_fields_missing"), (head(engine_task_id="0"), "head_fields_missing"),
                      (head(policy_fingerprint_sha256=None), "head_fields_missing"), (head("no_execution", execution_ref="x"), "head_fields_early"),
                      (head("no_execution", checkpoint={"sequence": 0, "mac": MAC}), "head_fields_early"), (head(checkpoint={"sequence": -1, "mac": MAC}), "checkpoint_invalid"),
                      (head(checkpoint={"sequence": True, "mac": MAC}), "checkpoint_invalid"), (head(checkpoint={"sequence": 1, "mac": "x"}), "checkpoint_invalid"),
                      (head(checkpoint={"sequence": 1}), "checkpoint_invalid"), (head(checkpoint=[]), "checkpoint_invalid")), self.channel().head)


class TestWaitServiceReady(Case):
    def wait(self, expected=EXPECTED, timeout=10):
        return wait_service_ready(self.channel(), expected, self.time.waiting(), timeout)

    def test_starting_then_ready(self):
        fake = self.serve(line(ready("starting")), line(ready("starting")), line(ready()))
        self.assertEqual(self.wait()["state"], "service_ready")
        self.assertEqual((len(fake.requests), self.time.sleeps), (3, [2, 2]))

    def test_every_value_is_compared_with_what_the_runner_set(self):
        for name, other, code in (("config_sha256", SHA_B, "config_sha256_mismatch"), ("header_mode", "preserve", "header_mode_mismatch"),
                                  ("evaluation", None, "evaluation_mismatch"), ("policy_fingerprint_sha256", SHA_A, "policy_fingerprint_sha256_mismatch")):
            with self.subTest(name=name):
                fake = FakeControl(self.directory, lambda _r, data=line(ready(**{name: other})): data)
                try:
                    self.rejected(code, self.wait)
                finally:
                    fake.close()
                    (self.directory / SOCKET_NAME).unlink()

    def test_failed_service_stops_at_once(self):
        fake = self.serve(always=line(ready("failed", reason="config_rejected")))
        self.rejected("service_failed", self.wait)
        self.assertEqual((len(fake.requests), self.time.sleeps), (1, []))

    def test_never_ready_stops_at_the_deadline_without_another_request(self):
        fake = self.serve(always=line(ready("starting")))
        self.rejected("service_ready_timeout", lambda: self.wait(timeout=5))
        self.assertEqual((len(fake.requests), self.time.sleeps), (3, [2, 2, 1]))

    def test_a_slow_answer_past_the_deadline_does_not_sleep_negative(self):
        def answer(_request):
            self.time.t += 100
            return line(ready("starting"))
        fake = FakeControl(self.directory, answer)
        self.addCleanup(fake.close)
        # 실제 time.sleep은 음수에 ValueError를 낸다.
        self.rejected("service_ready_timeout", lambda: self.wait(timeout=5))
        self.assertEqual((len(fake.requests), self.time.sleeps), (1, [0]))

    def test_listener_appearing_late_is_waited_for(self):
        self.rejected("service_ready_timeout", lambda: self.wait(timeout=1))
        self.assertEqual(self.time.sleeps, [1])
        self.serve(line(ready()))
        self.assertEqual(self.wait()["state"], "service_ready")

    def test_protocol_errors_are_not_retried(self):
        fake = self.serve(always=line(ready() | {"eval_run_id": "other"}))
        self.rejected("response_other_run", self.wait)
        self.assertEqual((len(fake.requests), self.time.sleeps), (1, []))


class TestClose(Case):
    def close(self, ack=4, timeout=10):
        return close_execution(self.channel(), "close-1", self.time.waiting(), ack, timeout)

    def test_complete_gives_the_ack_for_the_collector(self):
        fake = self.serve(line(closed()), line(status("closing")), line(status("closing")), line(status()))
        result = self.close()
        self.assertEqual((result.state, result.reason, result.admission_closed, result.execution_ref, result.manifest_sha256),
                         ("complete", None, True, "execution-1", SHA_A))
        self.assertEqual((result.ack.close_request_ref, result.ack.sequence, result.ack.mac, result.ack.manifest_sha256),
                         ("close-1", 6, MAC, SHA_A))
        self.assertEqual([r["type"] for r in fake.requests], ["close", "close_status", "close_status", "close_status"])
        self.assertEqual(self.time.sleeps, [2, 2])

    def test_incomplete_is_returned_without_a_head(self):
        self.serve(line(closed()), line(status("incomplete", manifest_sha256=SHA_A)))
        result = self.close()
        self.assertEqual((result.state, result.reason, result.ack, result.manifest_sha256), ("incomplete", "audit_failed", None, SHA_A))

    def test_no_execution_is_its_own_state(self):
        self.serve(line(closed(execution_ref=None)), line(status("no_execution")))
        result = self.close()
        self.assertEqual((result.state, result.execution_ref, result.manifest_sha256, result.ack), ("no_execution", None, None, None))

    def test_close_is_retried_with_the_same_reference_when_unreachable(self):
        self.assertEqual(self.close(ack=3).state, "unavailable")
        self.assertEqual(self.time.sleeps, [2, 1])
        self.time.sleeps.clear()
        fake = self.serve(None, line(closed()), line(status()))
        self.assertEqual(self.close().state, "complete")
        self.assertEqual([r["close_request_ref"] for r in fake.requests], ["close-1"] * 3)
        self.assertEqual(self.time.sleeps, [2])

    def test_admission_not_closed_stops_before_waiting(self):
        fake = self.serve(always=line(closed(admission_closed=False)))
        result = self.close()
        self.assertEqual((result.state, result.reason, result.admission_closed), ("unavailable", "close_admission_not_closed", False))
        self.assertEqual(len(fake.requests), 1)

    def test_refusal_or_bad_reply_to_close_is_a_result(self):
        self.serve(line(message("error", code="closed")))
        result = self.close()
        self.assertEqual((result.state, result.reason, result.admission_closed), ("unavailable", "refused_closed", False))

    def test_never_finishing_stops_at_the_deadline(self):
        def answer(request):
            return line(closed() if request["type"] == "close" else status("closing"))
        fake = FakeControl(self.directory, answer)
        self.addCleanup(fake.close)
        result = self.close(timeout=5)
        self.assertEqual((result.state, result.reason, result.admission_closed, result.ack), ("unavailable", "close_timeout", True, None))
        self.assertEqual(self.time.sleeps, [2, 2, 1])
        self.assertEqual([r["type"] for r in fake.requests], ["close"] + ["close_status"] * 3)

    def test_losing_the_channel_after_close_stops_at_the_deadline(self):
        fake = self.serve(line(closed()))
        result = self.close(timeout=3)
        self.assertEqual((result.state, result.reason, result.admission_closed), ("unavailable", "response_truncated", True))
        self.assertEqual(self.time.sleeps, [2, 1])
        self.assertEqual(len(fake.requests), 3)

    def test_bad_status_is_a_result_not_an_exception(self):
        for reply, reason in ((status(close_request_ref="close-2"), "close_ref_mismatch"), (status() | {"state": "other"}, "close_state_invalid"),
                              (status(checkpoint=None), "close_fields_missing"), (status(manifest_sha256=None), "close_fields_missing"),
                              (status(execution_ref=None), "close_fields_missing"), (status(manifest_sha256="x"), "close_manifest_invalid"),
                              (status(execution_ref="http://x"), "close_execution_invalid"), (status(reason=SECRET_LIKE), "close_reason_invalid"), (status("incomplete", reason="synthetic_secret_123"), "close_reason_invalid"),
                              (status(reason=[]), "close_reason_invalid"), (status() | {"state": []}, "close_state_invalid"),
                              (status("incomplete", reason=None), "close_incomplete_invalid"),
                              (status("incomplete", checkpoint={"sequence": 6, "mac": MAC}), "close_incomplete_invalid"),
                              (status("closing", checkpoint={"sequence": 6, "mac": MAC}), "close_fields_early"),
                              (status("closing", manifest_sha256=SHA_A), "close_fields_early"),
                              (status("no_execution", execution_ref="x"), "close_fields_early"),
                              (status(checkpoint={"sequence": -1, "mac": MAC}), "checkpoint_invalid")):
            with self.subTest(reason=reason):
                def answer(request, data=line(reply)):
                    return line(closed()) if request["type"] == "close" else data
                fake = FakeControl(self.directory, answer)
                try:
                    result = self.close()
                finally:
                    fake.close()
                    (self.directory / SOCKET_NAME).unlink()
                self.assertEqual((result.state, result.reason, result.ack), ("unavailable", reason, None))

    def test_bad_close_replies(self):
        for reply, reason in ((closed(close_request_ref="close-2"), "close_ref_mismatch"), (closed(admission_closed=1), "close_admission_invalid"),
                              (closed(execution_ref="http://x"), "close_execution_invalid")):
            with self.subTest(reason=reason):
                fake = FakeControl(self.directory, lambda _r, data=line(reply): data)
                try:
                    result = self.close()
                finally:
                    fake.close()
                    (self.directory / SOCKET_NAME).unlink()
                self.assertEqual((result.state, result.reason), ("unavailable", reason))


if __name__ == "__main__":
    unittest.main()
