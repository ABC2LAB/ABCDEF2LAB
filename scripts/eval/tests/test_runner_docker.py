"""러너 docker 경계. 실제 docker를 실행하지 않고 가짜 실행기로 인자·환경·출력 해석만 본다."""

from __future__ import annotations

import io
import json
import os
import re
import sys
import tarfile
import tempfile
import threading
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import runner_docker
from runner_docker import CommandResult, DaemonIdentity, Docker, DockerError, client_environment


class FakeExecutor:
    def __init__(self, *results: CommandResult):
        self.results = list(results)
        self.calls = []

    def __call__(self, args, env, stdin, timeout):
        self.calls.append((list(args), dict(env), stdin, timeout))
        return self.results.pop(0)


class TestRunBounded(unittest.TestCase):
    def test_passes_the_callers_limits_and_returns_the_exit_code_to_it(self):
        calls = []

        def bounded(args, env, timeout, max_stdout):
            calls.append((args, env, timeout, max_stdout))
            return runner_docker.BoundedResult(3, b"out", b"")
        docker = Docker(FakeExecutor(), {"DOCKER_HOST": "unix:///x"}, 1, bounded=bounded)
        result = docker.run_bounded(("run", "x"), 5.0, 9)
        self.assertEqual(calls, [(["run", "x"], {"DOCKER_HOST": "unix:///x"}, 5.0, 9)])
        self.assertEqual((result.returncode, result.stdout), (3, b"out"))


class FakeStream:
    """docker events 프로세스 대역. 실제처럼 terminate() 전까지 stdout이 열려 있다(파이프).

    died=True면 구간 도중 프로세스가 끝난 것처럼 poll()이 종료 코드를 낸다. eof_early=True면 프로세스는 살아 있는데
    stdout만 먼저 닫힌다(#126 리뷰의 조기 EOF).
    """

    def __init__(self, lines, died=False, eof_early=False):
        read_fd, self._write_fd = os.pipe()
        self.stdout = os.fdopen(read_fd, "rb")
        self.died, self.terminated = died, False
        self._lock = threading.Lock()

        def feed():
            for line in lines:
                os.write(self._write_fd, line)
            if eof_early:
                self._close()
        self._feeder = threading.Thread(target=feed, daemon=True)
        self._feeder.start()
        if eof_early:
            self._feeder.join(timeout=5)

    def _close(self):
        with self._lock:
            if self._write_fd is not None:
                os.close(self._write_fd)
                self._write_fd = None

    def poll(self):
        return 1 if self.died else None

    def terminate(self):
        self.terminated = True
        self._feeder.join(timeout=5)
        self._close()

    def wait(self, timeout=None):
        return 0


def ok(value) -> CommandResult:
    return CommandResult(0, json.dumps(value).encode())


def server(version="29.1.3", **overrides) -> dict:
    return {"Version": version, "Os": "linux", "Arch": "amd64", "Platform": {"Name": "Docker Engine - Community"}} | overrides


class TestClientEnvironment(unittest.TestCase):
    def test_only_docker_client_names_pass_from_shell(self):
        shell = {"PATH": "/usr/bin", "DOCKER_HOST": "unix:///run/docker.sock", "OPENAI_API_KEY": "sk-secret",
                 "EVAL_ENGINE_IMAGE": "shell/override:1", "ARTEX_LLM_MODEL": "other", "COMPOSE_FILE": "x.yaml"}
        env = client_environment(shell, {"EVAL_ENGINE_IMAGE": "runner/value:1"})
        self.assertEqual(env, {"PATH": "/usr/bin", "DOCKER_HOST": "unix:///run/docker.sock",
                               "EVAL_ENGINE_IMAGE": "runner/value:1"})

    def test_runner_value_may_not_shadow_client_name(self):
        with self.assertRaises(DockerError):
            client_environment({"PATH": "/usr/bin"}, {"PATH": "/tmp"})


class TestDocker(unittest.TestCase):
    def test_daemon_identity_and_call_shape(self):
        fake = FakeExecutor(ok(server()))
        identity = Docker(fake, {"PATH": "/usr/bin"}, 30.0).daemon()
        self.assertEqual((identity.version, identity.os, identity.arch, identity.platform),
                         ("29.1.3", "linux", "amd64", "Docker Engine - Community"))
        args, env, stdin, timeout = fake.calls[0]
        self.assertEqual(args, ["version", "--format", "{{json .Server}}"])
        self.assertEqual((env, stdin, timeout), ({"PATH": "/usr/bin"}, None, 30.0))

    def test_server_version_floor(self):
        for version in ("28.0.0", "28.0.1", "29.1.3", "28.0.0+dfsg1", "100.0.0"):
            Docker(FakeExecutor(ok(server(version))), {}, 1).daemon()
        for version in ("27.5.1", "28.0.0-rc.1", "27.99.99", "v29.0.0", "29", "", "29.a.0"):
            with self.assertRaises(DockerError) as caught:
                Docker(FakeExecutor(ok(server(version))), {}, 1).daemon()
            self.assertIn(str(caught.exception), ("docker_server_too_old", "docker_version_output_invalid"))

    def test_event_stream_reads_until_window_end(self):
        lines = [json.dumps({"Type": "container", "Action": "start", "Actor": {"ID": "x"}, "timeNano": t}).encode() + b"\n"
                 for t in (10, 20, 30)]
        process = FakeStream(lines)
        opened = []
        docker = Docker(FakeExecutor(), {"PATH": "/usr/bin"}, 1, stream_opener=lambda a, e: opened.append((a, e)) or process,
                        sleep=lambda seconds: None)
        stream = docker.event_stream("2026-11-03T04:39:30.712177000Z")
        events, complete = stream.stop(20)
        self.assertEqual(opened, [(["events", "--since", "2026-11-03T04:39:30.712177000Z", "--format", "{{json .}}"],
                                   {"PATH": "/usr/bin"})])
        self.assertEqual(([e["timeNano"] for e in events], complete), ([10, 20], True))
        self.assertTrue(process.terminated)

    def test_event_stream_incomplete_cases(self):
        good = json.dumps({"Type": "network", "Action": "connect", "Actor": {"ID": "n"}, "timeNano": 1}).encode() + b"\n"
        cases = {"died_midway": (FakeStream([good], died=True)),
                 "stdout_closed_while_alive": FakeStream([good], eof_early=True),
                 "unparseable": FakeStream([good, b"{not json\n"]),
                 "not_object": FakeStream([b"[1]\n"]),
                 "huge_line": FakeStream([b"{" + b" " * (64 * 1024 + 10) + b"}\n"])}
        for name, process in cases.items():
            with self.subTest(name=name):
                docker = Docker(FakeExecutor(), {}, 1, stream_opener=lambda a, e, p=process: p, sleep=lambda s: None)
                stream = docker.event_stream("2026-11-03T04:39:30Z")
                if name == "stdout_closed_while_alive":
                    stream._reader.join(timeout=5)  # 조기 EOF를 reader가 먼저 본 뒤에 러너가 멈춘다
                _, complete = stream.stop(10**19)
                self.assertFalse(complete)
        for since in ("2026-11-03 04:39:30", "1h", "--filter=x", "", "2026-11-03T04:39:30+09:00"):
            with self.subTest(since=since), self.assertRaises(DockerError) as caught:
                Docker(FakeExecutor(), {}, 1, stream_opener=lambda a, e: FakeStream([])).event_stream(since)
            self.assertEqual(str(caught.exception), "docker_events_window_invalid")

    def test_distribution_daemon_with_empty_platform_name(self):
        # Ubuntu docker.io 29.1.3 실측: Platform.Name == "". 빈 이름도 식별값으로 남고 다른 이름과는 다르다.
        identity = Docker(FakeExecutor(ok(server(Platform={"Name": ""}))), {}, 1).daemon()
        self.assertEqual(identity, DaemonIdentity("29.1.3", "linux", "amd64", ""))
        self.assertNotEqual(identity, Docker(FakeExecutor(ok(server())), {}, 1).daemon())

    def test_daemon_output_must_be_complete(self):
        for value in (server(Os=""), server(Version=""), server(Arch=""), server(Platform="x"), server(Arch=None),
                      server(Platform={}), server(Platform={"Name": None}), server(Platform={"Name": 7}), [], "29.1.3"):
            with self.assertRaises(DockerError) as caught:
                Docker(FakeExecutor(ok(value)), {}, 1).daemon()
            self.assertEqual(str(caught.exception), "docker_version_output_invalid")

    def test_failures_use_fixed_codes_without_output(self):
        secret = b"OPENAI_API_KEY=sk-secret /srv/eval/runs"
        cases = [
            (CommandResult(1, secret), "docker_version_failed"),
            (CommandResult(0, b"not json " + secret), "docker_version_failed_output_invalid"),
            (CommandResult(0, b"\xff"), "docker_version_failed_output_invalid"),
        ]
        for result, code in cases:
            with self.assertRaises(DockerError) as caught:
                Docker(FakeExecutor(result), {}, 1).daemon()
            self.assertEqual(str(caught.exception), code)
            self.assertNotIn("sk-secret", repr(caught.exception))

    def test_output_size_capped(self):
        with patch.object(runner_docker, "MAX_OUTPUT_BYTES", 8):
            with self.assertRaises(DockerError) as caught:
                Docker(FakeExecutor(CommandResult(0, b"123456789")), {}, 1).run(["x"], code="c")
        self.assertEqual(str(caught.exception), "c_output_too_large")

    def test_image_id_is_content_id_and_reference_follows_separator(self):
        fake = FakeExecutor(ok("sha256:" + "a" * 64))
        self.assertEqual(Docker(fake, {}, 1).image_id("example/o:fixed"), "sha256:" + "a" * 64)
        self.assertEqual(fake.calls[0][0], ["image", "inspect", "--format", "{{json .Id}}", "--", "example/o:fixed"])
        for value in ("sha256:" + "A" * 64, "a" * 64, ["sha256:" + "a" * 64], None):
            with self.assertRaises(DockerError):
                Docker(FakeExecutor(ok(value)), {}, 1).image_id("example/o:fixed")
        with self.assertRaises(DockerError) as caught:
            Docker(FakeExecutor(CommandResult(1, b"")), {}, 1).image_id("example/o:fixed")
        self.assertEqual(str(caught.exception), "docker_image_missing")


ID_A, ID_B = "a" * 64, "b" * 64
IMAGE_ID = "sha256:" + "c" * 64


class TestFactGathering(unittest.TestCase):
    def test_project_filters_and_id_lists(self):
        fake = FakeExecutor(CommandResult(0, f"{ID_B}\n{ID_A}\n".encode()), CommandResult(0, f"{ID_A}\n".encode()),
                            CommandResult(0, b""))
        docker = Docker(fake, {}, 1)
        self.assertEqual(docker.project_network_ids("eval-ours-1"), [ID_A, ID_B])
        self.assertEqual(docker.project_container_ids("eval-ours-1"), [ID_A])
        self.assertEqual(docker.running_container_ids(), [])
        self.assertEqual(fake.calls[0][0], ["network", "ls", "--no-trunc", "--quiet", "--filter",
                                            "label=com.docker.compose.project=eval-ours-1"])
        self.assertEqual(fake.calls[1][0], ["ps", "--all", "--no-trunc", "--quiet", "--filter",
                                            "label=com.docker.compose.project=eval-ours-1"])
        self.assertEqual(fake.calls[2][0], ["ps", "--no-trunc", "--quiet"])

    def test_subnet_check_calls(self):
        # 대역 겹침 검사: 호스트의 모든 망(필터 없음)과, up과 같은 인자로 렌더한 compose 설정을 JSON으로 읽는다.
        rendered = {"networks": {"e2_net": {"ipam": {"config": [{"subnet": "10.250.0.0/24"}]}}},
                    "services": {"gw": {"environment": {"OPENROUTER_API_KEY": "canary-secret"}}}}
        fake = FakeExecutor(CommandResult(0, f"{ID_B}\n{ID_A}\n".encode()), CommandResult(0, json.dumps(rendered).encode()),
                            CommandResult(1, b"canary-secret in stderr-ish output"))
        docker = Docker(fake, {}, 1)
        base = ["compose", "-p", "eval-ours-1", "--env-file", "/x/secrets.env", "-f", "a.yaml"]
        self.assertEqual(docker.all_network_ids(), [ID_A, ID_B])
        self.assertEqual(docker.compose_config(base), rendered)
        self.assertEqual(fake.calls[0][0], ["network", "ls", "--no-trunc", "--quiet"])
        self.assertEqual(fake.calls[1][0], [*base, "config", "--format", "json"])
        # 렌더 실패는 고정 code만 남는다(출력·비밀을 오류에 싣지 않는다).
        with self.assertRaises(DockerError) as caught:
            docker.compose_config(base)
        self.assertEqual(str(caught.exception), "docker_compose_config_failed")
        self.assertNotIn("canary", repr(caught.exception))

    def test_project_name_checked_before_docker_call(self):
        for project in ("Eval", "eval ours", "x,label=y", "", "-p"):
            fake = FakeExecutor()
            with self.assertRaises(DockerError):
                Docker(fake, {}, 1).project_network_ids(project)
            with self.assertRaises(DockerError):
                Docker(fake, {}, 1).project_container_ids(project)
            self.assertEqual(fake.calls, [])

    def test_id_output_must_be_full_unique_ids(self):
        for out in (b"abc\n", ("A" * 64).encode(), f"{ID_A}\n{ID_A}\n".encode(), b"\xff"):
            with self.assertRaises(DockerError) as caught:
                Docker(FakeExecutor(CommandResult(0, out)), {}, 1).running_container_ids()
            self.assertEqual(str(caught.exception), "docker_container_list_failed_output_invalid")

    def test_inspect_uses_json_lines_and_skips_empty(self):
        fake = FakeExecutor(CommandResult(0, b'{"Id": "n1"}\n{"Id": "n2"}\n'),
                            CommandResult(0, b'{"id": "c"}\n'))
        docker = Docker(fake, {}, 1)
        self.assertEqual(docker.inspect_networks([ID_A, ID_B]), [{"Id": "n1"}, {"Id": "n2"}])
        self.assertEqual(docker.inspect_containers([ID_A]), [{"id": "c"}])
        self.assertEqual(fake.calls[0][0], ["network", "inspect", "--format", "{{json .}}", "--", ID_A, ID_B])
        self.assertEqual(fake.calls[1][0], ["container", "inspect", "--format", runner_docker.CONTAINER_TEMPLATE,
                                            "--", ID_A])
        self.assertEqual(docker.inspect_networks([]), [])
        self.assertEqual(docker.inspect_containers([]), [])
        self.assertEqual(len(fake.calls), 2)
        with self.assertRaises(DockerError) as caught:
            Docker(FakeExecutor(CommandResult(0, b"{bad\n")), {}, 1).inspect_networks([ID_A])
        self.assertEqual(str(caught.exception), "docker_network_inspect_failed_output_invalid")

    def test_container_template_never_selects_environment_or_whole_config(self):
        template = runner_docker.CONTAINER_TEMPLATE
        self.assertNotIn("Env", template)
        self.assertNotIn("{{json .Config}}", template)
        self.assertNotIn("{{json .}}", template)
        self.assertNotIn("{{json .Config.Labels}}", template)
        self.assertEqual(set(re.findall(r'"([a-z_]+)":', template)),
                         {"id", "project", "service", "image", "running", "started_at", "restart_count", "ports",
                          "networks", "network_mode", "pid_mode"})
        self.assertIn("{{json .State.StartedAt}}", template)
        self.assertIn("{{json .RestartCount}}", template)
        self.assertNotIn("{{json .HostConfig}}", template)  # Binds 등 경로가 들 수 있어 필드 두 개만
        self.assertIn('"network_mode":{{json .HostConfig.NetworkMode}}', template)
        self.assertIn('"pid_mode":{{json .HostConfig.PidMode}}', template)


class TestProbeCall(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.cidfile = Path(self._tmp.name) / "post-probe.cid"

    def tearDown(self):
        self._tmp.cleanup()

    def writing_cid(self, content, result=CommandResult(0, b'{"results":[]}')):
        """docker CLI처럼 --cidfile 경로에 컨테이너 ID를 쓰고 결과를 돌려주는 실행기."""
        calls = []

        def executor(args, env, stdin, timeout):
            calls.append((list(args), dict(env), stdin, timeout))
            if content is not None:
                Path(args[args.index("--cidfile") + 1]).write_bytes(content)
            return result
        executor.calls = calls
        return executor

    def test_probe_joins_engine_netns_without_privileges_or_pull(self):
        fake = self.writing_cid(ID_B.encode())
        out, container_id = Docker(fake, {}, 1).probe(IMAGE_ID, ID_A, b'{"plan":1}', 1000, 1001, self.cidfile)
        self.assertEqual((out, container_id), (b'{"results":[]}', ID_B))
        args, _, stdin, _ = fake.calls[0]
        self.assertEqual(args, ["run", "--rm", "-i", "--pull", "never", "--cidfile", str(self.cidfile),
                                "--network", f"container:{ID_A}",
                                "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
                                "--user", "1000:1001", "--", IMAGE_ID, "-role=probe"])
        self.assertEqual(stdin, b'{"plan":1}')
        self.assertFalse(any(arg in ("-v", "--volume", "--mount", "--privileged") for arg in args))

    def test_probe_container_id_must_be_one_full_id(self):
        # cidfile이 없거나 ID가 아니면 그 probe를 러너 것으로 인정할 수 없다(구간 감시 허용 목록이 비게 됨).
        for content in (None, b"", ID_B[:12].encode(), (ID_B + "\n").encode(), (ID_B + ID_B).encode(),
                        ID_B.upper().encode(), b"\xff" * 64):
            with self.subTest(content=content[:8] if content else content):
                self.cidfile.unlink(missing_ok=True)
                with self.assertRaises(DockerError) as caught:
                    Docker(self.writing_cid(content), {}, 1).probe(IMAGE_ID, ID_A, b"{}", 1, 1, self.cidfile)
                self.assertEqual(str(caught.exception), "docker_probe_cid_invalid")

    def test_probe_inputs_checked_before_call(self):
        cases = [(IMAGE_ID, "short", 1, 1), (IMAGE_ID, "", 1, 1), (IMAGE_ID, ID_A, -1, 1), (IMAGE_ID, ID_A, 1, True),
                 (IMAGE_ID, ID_A, "1", 1), ("example/m:fixed", ID_A, 1, 1), ("example/m@" + IMAGE_ID, ID_A, 1, 1),
                 ("a" * 64, ID_A, 1, 1)]
        for image, engine, uid, gid in cases:
            fake = FakeExecutor()
            with self.assertRaises(DockerError):
                Docker(fake, {}, 1).probe(image, engine, b"{}", uid, gid, self.cidfile)
            self.assertEqual(fake.calls, [])
        with self.assertRaises(DockerError) as caught:
            Docker(FakeExecutor(CommandResult(2, b"")), {}, 1).probe(IMAGE_ID, ID_A, b"{}", 1, 1, self.cidfile)
        self.assertEqual(str(caught.exception), "docker_probe_failed")


def statfs_report(*rows):
    return json.dumps({"results": [dict(zip(("path", "mount_id", "size_bytes", "used_bytes", "available_bytes"), row))
                                   for row in rows]}).encode()


def tar_bytes(members):
    """(이름, 내용 또는 종류) 목록으로 tar를 만든다. 종류: ("symlink", 대상) 또는 ("dir", None)."""
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w") as archive:
        for name, value in members:
            info = tarfile.TarInfo(name)
            if isinstance(value, tuple):
                kind, target = value
                info.type = tarfile.SYMTYPE if kind == "symlink" else tarfile.DIRTYPE
                if target:
                    info.linkname = target
                archive.addfile(info)
            else:
                info.size = len(value)
                archive.addfile(info, io.BytesIO(value))
    return buffer.getvalue()


class TestStopContainer(unittest.TestCase):
    def test_sigterm_stop_with_grace(self):
        fake = FakeExecutor(runner_docker.CommandResult(0, b""))
        Docker(fake, {}, 1).stop_container("b" * 64, 30)
        self.assertEqual(fake.calls[0][0], ["stop", "--time", "30", "b" * 64])
        for container, seconds in (("short", 30), ("b" * 64, 0), ("b" * 64, 601), ("b" * 64, True), ("b" * 64, "30")):
            with self.subTest(container=container[:5], seconds=seconds), self.assertRaises(DockerError) as caught:
                Docker(FakeExecutor(), {}, 1).stop_container(container, seconds)
            self.assertEqual(str(caught.exception), "docker_stop_invalid")
        with self.assertRaises(DockerError) as caught:
            Docker(FakeExecutor(runner_docker.CommandResult(1, b"")), {}, 1).stop_container("b" * 64, 30)
        self.assertEqual(str(caught.exception), "docker_stop_failed")


class TestHelperCopy(unittest.TestCase):
    """경계 원자료 꺼내기 도우미(계약 §4.1 결정 B): 망 없이·읽기 전용으로 볼륨을 붙여 정해진 파일만 복사한다."""

    NAMES = ("boundary-records.jsonl", "boundary-sealed.json")

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.cidfile = Path(self._tmp.name) / "boundary-copy.cid"

    def tearDown(self):
        self._tmp.cleanup()

    def bounded(self, stdout, returncode=0, cid=b"b" * 64, diagnostic=b""):
        calls = []

        def run(args, env, timeout, max_stdout):
            calls.append((list(args), timeout, max_stdout))
            Path(args[args.index("--cidfile") + 1]).write_bytes(cid)
            return runner_docker.BoundedResult(returncode, stdout, diagnostic)
        run.calls = calls
        return run

    def copy(self, bounded, executor=None, **changes):
        options = dict(image=IMAGE_ID, volume="abc2lab-eval-ours-1_explore-evidence", directory="run", names=self.NAMES,
                       role="boundary-evidence", uid=1001, gid=1001, cidfile=self.cidfile, max_bytes=1024, timeout=30)
        options.update(changes)
        return Docker(executor or FakeExecutor(), {}, 1, bounded=bounded).copy_volume_files(**options)

    def test_copies_named_files_without_network_or_write(self):
        fake = self.bounded(tar_bytes([("boundary-records.jsonl", b'{"seq":1}\n'), ("boundary-sealed.json", b"{}")]))
        files, container_id = self.copy(fake)
        self.assertEqual(files, {"boundary-records.jsonl": b'{"seq":1}\n', "boundary-sealed.json": b"{}"})
        self.assertEqual(container_id, "b" * 64)
        args, timeout, max_stdout = fake.calls[0]
        self.assertEqual(args, ["run", "--rm", "--pull", "never", "--log-driver", "none", "--cidfile", str(self.cidfile),
                                "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
                                "--user", "1001:1001", "--label", "abc2lab.eval.runner-helper=boundary-evidence",
                                "--mount", "type=volume,source=abc2lab-eval-ours-1_explore-evidence,target=/e,readonly",
                                "--entrypoint", "tar", "--", IMAGE_ID, "-C", "/e/run", "-cf", "-", "--", *self.NAMES])
        self.assertEqual(timeout, 30)
        self.assertGreaterEqual(max_stdout, 2 * 1024)

    def test_output_must_be_exactly_the_named_regular_files(self):
        ok = [("boundary-records.jsonl", b"x"), ("boundary-sealed.json", b"{}")]
        cases = {
            "missing": ok[:1],
            "extra": ok + [("other.json", b"{}")],
            "duplicate": ok + [("boundary-sealed.json", b"{}")],
            "symlink": [("boundary-records.jsonl", ("symlink", "/etc/passwd")), ok[1]],
            "directory": [("boundary-records.jsonl", ("dir", None)), ok[1]],
            "too_large": [("boundary-records.jsonl", b"x" * 1025), ok[1]],
            "path_name": [("run/boundary-records.jsonl", b"x"), ok[1]],
        }
        for name, members in cases.items():
            with self.subTest(case=name), self.assertRaises(DockerError) as caught:
                self.copy(self.bounded(tar_bytes(members)))
            self.assertEqual(str(caught.exception), "docker_helper_output_invalid")
        with self.assertRaises(DockerError) as caught:
            self.copy(self.bounded(b"not a tar"))
        self.assertEqual(str(caught.exception), "docker_helper_output_invalid")
        # 상한과 같은 크기는 받는다(경계 하나 아래).
        files, _ = self.copy(self.bounded(tar_bytes([("boundary-records.jsonl", b"x" * 1024), ok[1]])))
        self.assertEqual(len(files["boundary-records.jsonl"]), 1024)

    def test_ledger_names_and_volume_root(self):
        # 제품 승인 장부·호출 기록은 점으로 시작하는 긴 이름이고, 시험 DNS 제어 토큰은 볼륨 루트에 있다.
        ledger, calls = ".openrouter-" + "a" * 64 + ".ledger", ".openrouter-" + "a" * 64 + ".calls"
        fake = self.bounded(tar_bytes([(ledger, b"r1 1 1 1\n"), (calls, b"{}\n")]))
        files, _ = self.copy(fake, names=(ledger, calls), directory="config", role="product-records", uid=1000, gid=1000)
        self.assertEqual(set(files), {ledger, calls})
        fake = self.bounded(tar_bytes([("control-token", b"t")]))
        self.copy(fake, names=("control-token",), directory=".", role="dns-token", uid=1000, gid=1000)
        self.assertEqual(fake.calls[0][0][fake.calls[0][0].index("-C") + 1], "/e/.")

    def test_failures_and_bad_inputs_use_fixed_codes(self):
        empty = CommandResult(0, b"")
        with self.assertRaises(DockerError) as caught:
            self.copy(self.bounded(b"", returncode=2), FakeExecutor(empty, empty))
        self.assertEqual(str(caught.exception), "docker_helper_copy_failed")
        with self.assertRaises(DockerError) as caught:
            self.copy(self.bounded(tar_bytes([(n, b"x") for n in self.NAMES]), cid=b"short"))
        self.assertEqual(str(caught.exception), "docker_probe_cid_invalid")
        for change in ({"image": "abc2lab-product:local"}, {"volume": "../x"}, {"volume": "Vol"}, {"directory": "../run"},
                       {"directory": "a/b"}, {"names": ()}, {"names": ("a", "a")}, {"names": ("../x",)}, {"names": ("a/b",)},
                       {"names": (".",)}, {"names": ("..",)}, {"names": ("...",)}, {"names": ("x" * 129,)}, {"directory": ".."},
                       {"directory": "./run"}, {"directory": ""},
                       {"role": "Boundary"}, {"uid": -1}, {"gid": "1001"}, {"max_bytes": 0}, {"subpath": ("..",)},
                       {"subpath": (".",)}, {"subpath": ("a/b",)}, {"subpath": ("",)}, {"subpath": ("-x",)}, {"subpath": ("x" * 129,)},
                       {"subpath": (3,)}, {"subpath": ("a", "b", "c")}, {"subpath": "prep-1"},
                       {"subpath": "ab"}):
            fake = self.bounded(b"")
            with self.subTest(change=change), self.assertRaises(DockerError):
                self.copy(fake, **change)
            self.assertEqual(fake.calls, [])

    def test_missing_file_is_told_apart_from_other_tar_failures(self):
        # #785 리뷰: 파일·디렉터리가 없어서만 실패한 것(제품이 아직 안 냄)만 따로 알리고, 그 밖의 tar 실패는 복사 실패다.
        # 줄은 2026-11-19 제품 이미지(GNU tar)에서 실제로 나온 그대로다.
        missing_file = b"tar: missing.jsonl: Cannot stat: No such file or directory\ntar: Exiting with failure status due to previous errors\n"
        missing_dir = b"tar: /e/runs/d1/e2: Cannot open: No such file or directory\ntar: Error is not recoverable: exiting now\n"
        mixed = missing_file + b"tar: other: Cannot open: Permission denied\n"
        for name, diagnostic, code in (("file", missing_file, runner_docker.HELPER_FILE_MISSING),
                                       ("dir", missing_dir, runner_docker.HELPER_FILE_MISSING),
                                       ("mixed", mixed, "docker_helper_copy_failed"),
                                       ("permission", b"tar: x: Cannot open: Permission denied\n", "docker_helper_copy_failed"),
                                       ("end_only", b"tar: Exiting with failure status due to previous errors\n", "docker_helper_copy_failed"),
                                       ("empty", b"", "docker_helper_copy_failed")):
            with self.subTest(case=name), self.assertRaises(DockerError) as caught:
                # 실패 뒤 도우미 정리: 남은 도우미 목록 조회 두 번(없음)
                self.copy(self.bounded(b"", returncode=2, diagnostic=diagnostic), FakeExecutor(CommandResult(0, b""), CommandResult(0, b"")))
            self.assertEqual(str(caught.exception), code)

    def test_failed_copy_removes_this_calls_helper_and_confirms(self):
        # #693 리뷰: 시간 초과면 Docker CLI만 끝나고 도우미가 보호 볼륨을 잡고 남을 수 있다. 이 호출의 라벨·볼륨으로 찾아
        # 지우고 다시 봐서 없어야 원래 실패를 그대로 올린다. 남으면 docker_helper_cleanup_failed다.
        left = "d" * 64
        listing = ["ps", "-a", "-q", "--no-trunc", "--filter", "label=abc2lab.eval.runner-helper=boundary-evidence",
                   "--filter", "volume=abc2lab-eval-ours-1_explore-evidence"]

        def timing_out(args, env, timeout, max_stdout):
            Path(args[args.index("--cidfile") + 1]).write_bytes(left.encode())
            raise DockerError("docker_timeout")
        ok, empty, failed = CommandResult(0, b""), CommandResult(0, b""), CommandResult(1, b"")
        found = CommandResult(0, (left + "\n").encode())
        cases = {
            "removed": ([found, ok, empty], "docker_timeout", 3),
            "rm_fails_but_gone": ([found, failed, empty], "docker_timeout", 3),
            "still_there": ([found, ok, found], "docker_helper_cleanup_failed", 3),
            "nothing_left": ([empty, empty], "docker_timeout", 2),
            "listing_fails": ([failed], "docker_helper_cleanup_failed", 1),
            "recheck_fails": ([found, ok, failed], "docker_helper_cleanup_failed", 3),
            "odd_listing": ([CommandResult(0, b"not-an-id\n")], "docker_helper_cleanup_failed", 1),
        }
        for name, (results, code, count) in cases.items():
            executor = FakeExecutor(*results)
            with self.subTest(case=name), self.assertRaises(DockerError) as caught:
                self.copy(timing_out, executor)
            self.assertEqual(str(caught.exception), code)
            from runner_docker import HelperCleanupError
            self.assertEqual(isinstance(caught.exception, HelperCleanupError), code == "docker_helper_cleanup_failed")
            self.assertEqual(len(executor.calls), count)
            self.assertEqual(executor.calls[0][0], listing)
            if count == 3:
                self.assertEqual(executor.calls[1][0], ["rm", "-f", "--", left])
                self.assertEqual(executor.calls[2][0], listing)
        # 운영자 중단(KeyboardInterrupt)도 다시 올리기 전에 같은 정리를 하고, 정리가 실패하면 HelperCleanupInterrupt다.
        from runner_docker import HelperCleanupInterrupt

        def interrupted(args, env, timeout, max_stdout):
            Path(args[args.index("--cidfile") + 1]).write_bytes(left.encode())
            raise KeyboardInterrupt
        for results, expected in (([found, ok, empty], KeyboardInterrupt), ([found, ok, found], HelperCleanupInterrupt),
                                  ([failed], HelperCleanupInterrupt)):
            executor = FakeExecutor(*results)
            with self.subTest(results=len(results), expected=expected.__name__):
                with self.assertRaises(KeyboardInterrupt) as caught:
                    self.copy(interrupted, executor)
                self.assertIs(type(caught.exception), expected)
                self.assertEqual(executor.calls[0][0], listing)
        # 출력 모양이 틀린 경우(도우미는 이미 끝남, --rm)에는 정리 호출을 하지 않는다.
        executor = FakeExecutor()
        with self.assertRaises(DockerError):
            self.copy(self.bounded(b"not a tar"), executor)
        self.assertEqual(executor.calls, [])

    def test_subpath_reads_below_the_directory(self):
        # 제품 회차 실행 조건: product-state 볼륨 runs/<초안 ID>/state/runtime-conditions.json.
        fake = self.bounded(tar_bytes([("runtime-conditions.json", b"{}")]))
        files, _ = self.copy(fake, volume="abc2lab-eval-ours-1_product-state", directory="runs", subpath=("prep-0aF.b_c", "state"),
                             names=("runtime-conditions.json",))
        self.assertEqual(files, {"runtime-conditions.json": b"{}"})
        args = fake.calls[0][0]
        self.assertEqual(args[args.index("-C") + 1], "/e/runs/prep-0aF.b_c/state")


class TestStatfs(unittest.TestCase):
    """tmpfs 사용량은 대상 밖의 계측기 statfs 역할로 잰다(#140 리뷰: 엔진 안 df는 엔진 도구가 바꿀 수 있고,
    경로만으로는 다른 마운트를 잴 수 있음 → 보고의 마운트 ID를 호출자가 구간 전후로 대조)."""

    def test_runs_meter_image_outside_the_target_with_only_ptrace(self):
        report = statfs_report(("/app/data", 41, 8 << 20, 1 << 20, 7 << 20))
        fake = FakeExecutor(CommandResult(0, report))
        usage = Docker(fake, {}, 1).statfs(IMAGE_ID, ID_A, ["/app/data"])
        self.assertEqual(usage, [{"mount_id": 41, "size_bytes": 8 << 20, "used_bytes": 1 << 20,
                                  "available_bytes": 7 << 20}])
        args, _, stdin, _ = fake.calls[0]
        self.assertEqual(args, ["run", "--rm", "-i", "--pull", "never", "--network", "none", "--pid", f"container:{ID_A}",
                                "--read-only", "--cap-drop", "ALL", "--cap-add", "SYS_PTRACE",
                                "--security-opt", "no-new-privileges:true", "--user", "0:0", "--", IMAGE_ID,
                                "-role=statfs"])
        self.assertEqual(json.loads(stdin), {"paths": ["/app/data"]})  # 대상 컨테이너 안의 마운트 지점
        self.assertNotIn("exec", args)  # 대상 안의 실행파일은 실행하지 않는다
        self.assertFalse(any(arg in ("-v", "--volume", "--mount", "--privileged") for arg in args))

    def test_report_must_match_the_request(self):
        paths = ["/app/data"]
        good = ("/app/data", 41, 8192, 1024, 7168)
        self.assertEqual(runner_docker.parse_statfs_report(statfs_report(good), paths)[0]["mount_id"], 41)
        bad = [statfs_report(), statfs_report(good, good), statfs_report(("/app", 41, 8192, 1024, 7168)),
               statfs_report(("/proc/1/root/app/data", 41, 8192, 1024, 7168)),
               statfs_report(("/app/data", 41, 0, 0, 0)), statfs_report(("/app/data", 41, 8192, 9000, 0)),
               statfs_report(("/app/data", 41, 8192, 1024, 7169)),  # 남은 공간 > 크기 - 사용
               statfs_report(("/app/data", 41, 8192, -1, 7168)),
               statfs_report(("/app/data", 41, 8192.0, 1024, 7168)),
               statfs_report(("/app/data", 41, True, 1024, 7168)),
               statfs_report(("/app/data", 41, 8192, 1024, False)),  # JSON false는 0이 아니다
               statfs_report(("/app/data", 0, 8192, 1024, 7168)),  # 마운트 ID 없음
               statfs_report(("/app/data", True, 8192, 1024, 7168)),
               statfs_report(("/app/data", "41", 8192, 1024, 7168)),
               json.dumps({"results": [{"path": "/app/data", "size_bytes": 8192, "used_bytes": 1024,
                                        "available_bytes": 7168}]}).encode(),  # 마운트 ID 필드 누락
               json.dumps({"results": [], "extra": 1}).encode(), b"[]", b"\xff", b"",
               json.dumps({"results": [dict(zip(("path", "mount_id", "size_bytes", "used_bytes", "available_bytes"),
                                                good), extra=1)]}).encode()]
        for output in bad:
            with self.subTest(output=output[:60]), self.assertRaises(DockerError) as caught:
                runner_docker.parse_statfs_report(output, paths)
            self.assertEqual(str(caught.exception), "docker_statfs_output_invalid")

    def test_inputs_checked_before_call(self):
        for image, container, paths in ((IMAGE_ID, "short", ["/app/data"]), ("example/m:1", ID_A, ["/app/data"]),
                                        (IMAGE_ID, ID_A, []), (IMAGE_ID, ID_A, ["app/data"]),
                                        (IMAGE_ID, ID_A, ["/app/../etc"]), (IMAGE_ID, ID_A, ["/app data"]),
                                        (IMAGE_ID, ID_A, [None])):
            fake = FakeExecutor()
            with self.subTest(image=image[:8], container=container[:5], paths=paths), self.assertRaises(DockerError):
                Docker(fake, {}, 1).statfs(image, container, paths)
            self.assertEqual(fake.calls, [])
        with self.assertRaises(DockerError) as caught:
            Docker(FakeExecutor(CommandResult(1, b"")), {}, 1).statfs(IMAGE_ID, ID_A, ["/app/data"])
        self.assertEqual(str(caught.exception), "docker_statfs_failed")


class TestComposeCalls(unittest.TestCase):
    def test_up_never_pulls_or_builds_and_waits(self):
        fake = FakeExecutor(CommandResult(0, b""), CommandResult(0, b""))
        docker = Docker(fake, {}, 1)
        base = ["compose", "-p", "eval-ours-1", "--env-file", "/s.env", "-f", "/c.yaml"]
        docker.compose_up(base, 120)
        docker.compose_down(base, 30)
        self.assertEqual(fake.calls[0][0], base + ["up", "--detach", "--pull", "never", "--no-build", "--wait",
                                                   "--wait-timeout", "120"])
        self.assertEqual(fake.calls[1][0], base + ["down", "--volumes", "--remove-orphans", "--timeout", "30"])
        for method, code in ((docker.compose_up, "docker_compose_up_failed"),
                             (docker.compose_down, "docker_compose_down_failed")):
            with self.assertRaises(DockerError) as caught:
                method.__func__(Docker(FakeExecutor(CommandResult(1, b"")), {}, 1), base, 1)
            self.assertEqual(str(caught.exception), code)

    def test_signal_only_usr1_to_full_id(self):
        fake = FakeExecutor(CommandResult(0, b""))
        Docker(fake, {}, 1).signal(ID_A, "USR1")
        self.assertEqual(fake.calls[0][0], ["kill", "--signal", "USR1", ID_A])
        for container, name in ((ID_A, "KILL"), (ID_A, "TERM"), ("short", "USR1"), ("", "USR1")):
            fake = FakeExecutor()
            with self.assertRaises(DockerError):
                Docker(fake, {}, 1).signal(container, name)
            self.assertEqual(fake.calls, [])


class TestRunDocker(unittest.TestCase):
    def test_runs_without_shell_and_maps_timeout(self):
        import subprocess

        with patch.object(subprocess, "run") as run:
            run.return_value = subprocess.CompletedProcess([], 0, b"{}", None)
            result = runner_docker.run_docker(["version"], {"PATH": "/usr/bin"}, None, 5.0)
        self.assertEqual(result, CommandResult(0, b"{}"))
        args, kwargs = run.call_args
        self.assertEqual(args[0], ["docker", "version"])
        self.assertNotIn("shell", kwargs)
        self.assertEqual(kwargs["env"], {"PATH": "/usr/bin"})
        self.assertEqual(kwargs["timeout"], 5.0)
        for error, code in ((subprocess.TimeoutExpired(["docker"], 5.0), "docker_timeout"),
                            (FileNotFoundError(), "docker_unavailable")):
            with patch.object(subprocess, "run", side_effect=error):
                with self.assertRaises(DockerError) as caught:
                    runner_docker.run_docker(["version"], {}, None, 5.0)
            self.assertEqual(str(caught.exception), code)


if __name__ == "__main__":
    unittest.main()
