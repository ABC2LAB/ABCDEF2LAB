"""러너 docker 경계. 실제 docker를 실행하지 않고 가짜 실행기로 인자·환경·출력 해석만 본다."""

from __future__ import annotations

import json
import os
import re
import sys
import threading
import unittest
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
                          "networks"})
        self.assertIn("{{json .State.StartedAt}}", template)
        self.assertIn("{{json .RestartCount}}", template)


class TestProbeCall(unittest.TestCase):
    def test_probe_joins_engine_netns_without_privileges_or_pull(self):
        fake = FakeExecutor(CommandResult(0, b'{"results":[]}'))
        out = Docker(fake, {}, 1).probe(IMAGE_ID, ID_A, b'{"plan":1}', 1000, 1001)
        self.assertEqual(out, b'{"results":[]}')
        args, _, stdin, _ = fake.calls[0]
        self.assertEqual(args, ["run", "--rm", "-i", "--pull", "never", "--network", f"container:{ID_A}",
                                "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
                                "--user", "1000:1001", "--", IMAGE_ID, "-role=probe"])
        self.assertEqual(stdin, b'{"plan":1}')
        self.assertFalse(any(arg in ("-v", "--volume", "--mount", "--privileged") for arg in args))

    def test_probe_inputs_checked_before_call(self):
        cases = [(IMAGE_ID, "short", 1, 1), (IMAGE_ID, "", 1, 1), (IMAGE_ID, ID_A, -1, 1), (IMAGE_ID, ID_A, 1, True),
                 (IMAGE_ID, ID_A, "1", 1), ("example/m:fixed", ID_A, 1, 1), ("example/m@" + IMAGE_ID, ID_A, 1, 1),
                 ("a" * 64, ID_A, 1, 1)]
        for image, engine, uid, gid in cases:
            fake = FakeExecutor()
            with self.assertRaises(DockerError):
                Docker(fake, {}, 1).probe(image, engine, b"{}", uid, gid)
            self.assertEqual(fake.calls, [])
        with self.assertRaises(DockerError) as caught:
            Docker(FakeExecutor(CommandResult(2, b"")), {}, 1).probe(IMAGE_ID, ID_A, b"{}", 1, 1)
        self.assertEqual(str(caught.exception), "docker_probe_failed")


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
