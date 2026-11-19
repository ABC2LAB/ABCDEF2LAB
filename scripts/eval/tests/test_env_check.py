"""평가 머신 환경 점검(env_check) 시험. 실제 Docker·파일 대신 가짜 명령·읽기를 넣는다."""

from __future__ import annotations

import contextlib
import io
import os
import sys
import unittest
from unittest import mock
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import env_check  # noqa: E402
from env_check import MIN_DOCKER_FREE_BYTES, MIN_MEM_KIB, base_images, route_subnets, run_checks, version_tuple  # noqa: E402

REPO = Path(__file__).resolve().parents[3]
CONFIG = {"ports": {"engine_api_host": 38887, "obs_host": 39291}, "split": {"product_port": 28787, "dns_control_port": 29083},
          "host_listen_allowlist": [{"address": "10.255.255.254", "port": 53, "reason": "WSL DNS"}]}
TCP_HEADER = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"


def tcp_line(address_hex: str, port: int) -> str:
    return f"   0: {address_hex}:{port:04X} 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000 0 1 1\n"


ROUTES = ("Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"
          "eth0\t00000000\t0101A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n"     # 기본 경로(제외)
          "eth0\t0001A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n")   # 192.168.1.0/24

# 읽기만 하는 명령(첫 두 낱말). 이 밖의 명령을 부르면 시험이 실패한다.
READ_ONLY = {("docker", "version"), ("docker", "info"), ("docker", "ps"), ("docker", "network"), ("docker", "image"),
             ("docker", "buildx"), ("docker", "compose"), ("go", "version"), ("openssl", "version"), ("git", "--version")}


class FakeHost:
    def __init__(self):
        self.calls = []
        self.server, self.client, self.os = "29.1.3", "29.1.3", "Ubuntu 24.04.1 LTS"
        self.compose, self.go, self.openssl = "2.40.3", "go version go1.26.3 linux/amd64", "OpenSSL 3.0.13 30 Jan 2024"
        self.containers = ""
        self.network_ids, self.subnets = "n1\nn2\n", "172.17.0.0/16 10.99.0.0/24 "
        self.missing_images: set[str] = set()
        self.unpullable: set[str] = set()
        self.tcp = TCP_HEADER + tcp_line("0100007F", 8080) + tcp_line("FEFFFF0A", 53)  # 127.0.0.1:8080, 10.255.255.254:53
        self.meminfo = f"MemTotal:        {MIN_MEM_KIB} kB\n"
        self.free = MIN_DOCKER_FREE_BYTES
        self.routes = ROUTES
        self.failing: set[tuple[str, ...]] = set()

    def run(self, args, timeout):
        self.calls.append(list(args))
        key = tuple(args[:2])
        if key in self.failing:
            return 1, ""
        if args[:2] == ["docker", "version"]:
            return 0, f"{self.server} {self.client}\n"
        if args[:2] == ["docker", "info"]:
            return 0, (self.os if "OperatingSystem" in args[-1] else "/var/lib/docker") + "\n"
        if args[:2] == ["docker", "ps"]:
            return 0, self.containers
        if args[:3] == ["docker", "network", "ls"]:
            return 0, self.network_ids
        if args[:3] == ["docker", "network", "inspect"]:
            return 0, self.subnets
        if args[:3] == ["docker", "image", "inspect"]:
            return (1, "") if args[-1] in self.missing_images else (0, "sha256:x\n")
        if args[:3] == ["docker", "buildx", "version"]:
            return 0, "github.com/docker/buildx 0.30.1\n"
        if args[:4] == ["docker", "buildx", "imagetools", "inspect"]:
            return (1, "") if args[-1] in self.unpullable else (0, "Name: x\n")
        if args[:2] == ["docker", "compose"]:
            return 0, self.compose + "\n"
        if args[:2] == ["go", "version"]:
            return 0, self.go
        if args[:2] == ["openssl", "version"]:
            return 0, self.openssl
        if args[:2] == ["git", "--version"]:
            return 0, "git version 2.43.0\n"
        raise AssertionError(f"모르는 명령: {args}")

    def read_text(self, path):
        return {"/proc/net/tcp": self.tcp, "/proc/net/tcp6": None, "/proc/meminfo": self.meminfo, "/proc/net/route": self.routes}.get(path)

    def checks(self, config=None):
        return {r.name: r for r in run_checks(CONFIG if config is None else config, REPO, self.run, self.read_text, lambda p: self.free)}


class TestAllPass(unittest.TestCase):
    def test_dev_machine_values_pass_and_only_read_commands_run(self):
        host = FakeHost()
        results = host.checks()
        self.assertEqual([name for name, r in results.items() if not r.ok], [])
        self.assertEqual(set(results), {"docker_server", "docker_client", "docker_native", "compose", "go", "python", "openssl", "git",
                                        "memory", "disk", "containers", "host_listen", "ports", "subnets", "buildx", "base_images"})
        for call in host.calls:
            self.assertIn(tuple(call[:2]), READ_ONLY, call)
            self.assertNotIn("pull", call)
            self.assertNotIn("build", call[:3])


class TestFailures(unittest.TestCase):
    def failed(self, change, config=None):
        host = FakeHost()
        change(host)
        return {name: r.reason for name, r in host.checks(config).items() if not r.ok}

    def test_each_condition_fails_alone(self):
        cases = {
            "docker_server": lambda h: setattr(h, "server", "28.5.1"),
            "docker_client": lambda h: setattr(h, "client", "27.0.0"),
            "docker_native": lambda h: setattr(h, "os", "Docker Desktop"),
            "compose": lambda h: setattr(h, "compose", "2.39.4"),
            "go": lambda h: setattr(h, "go", "go version go1.26.2 linux/amd64"),
            "openssl": lambda h: setattr(h, "openssl", "OpenSSL 1.1.1w"),
            "memory": lambda h: setattr(h, "meminfo", f"MemTotal:        {MIN_MEM_KIB - 1} kB\n"),
            "disk": lambda h: setattr(h, "free", MIN_DOCKER_FREE_BYTES - 1),
            "containers": lambda h: setattr(h, "containers", "abc\n"),
            "host_listen": lambda h: setattr(h, "tcp", TCP_HEADER + tcp_line("00000000", 22)),  # 0.0.0.0:22
            "ports": lambda h: setattr(h, "tcp", TCP_HEADER + tcp_line("0100007F", 28787)),   # 127.0.0.1:제품 포트
            "subnets": lambda h: setattr(h, "subnets", "10.252.2.0/23 "),
            "base_images": lambda h: (h.missing_images.add("postgres:16-bookworm@sha256:5264710be20f409750ac6dc4e5706dcef929c87e241605463f0f9aefa932a8d9"),
                                      h.unpullable.add("postgres:16-bookworm@sha256:5264710be20f409750ac6dc4e5706dcef929c87e241605463f0f9aefa932a8d9")),
            "buildx": lambda h: h.failing.add(("docker", "buildx")),
            "git": lambda h: h.failing.add(("git", "--version")),
        }
        for name, change in cases.items():
            with self.subTest(name=name):
                failed = self.failed(change)
                self.assertIn(name, failed)
                if name not in ("buildx",):  # buildx가 없으면 imagetools 조회도 실패해 기본 이미지 항목도 같이 실패할 수 있다
                    self.assertEqual(set(failed), {name}, failed)

    def test_route_overlap_and_missing_image_that_can_be_pulled(self):
        # 호스트 경로 10.252.0.0/16(/proc/net/route는 리틀엔디언 16진수 0000FC0A·0000FFFF)이 분리 배치 대역과 겹친다.
        failed = self.failed(lambda h: setattr(h, "routes", ROUTES + "eth1\t0000FC0A\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n"))
        self.assertEqual(set(failed), {"subnets"})
        self.assertIn("10.252.0.0/16", failed["subnets"])
        self.assertEqual(self.failed(lambda h: h.missing_images.add("python:3.12-slim-bookworm")), {})  # 로컬엔 없어도 받을 수 있으면 통과

    def test_docker_unreachable_fails_and_missing_config_ports(self):
        self.assertIn("docker", self.failed(lambda h: h.failing.add(("docker", "version"))))
        failed = self.failed(lambda h: None, config={"ports": {"engine_api_host": 38887}, "split": {}, "host_listen_allowlist": []})
        self.assertIn("ports", failed)
        self.assertIn("split.product_port", failed["ports"])

    def test_config_without_split_block_is_a_skip_with_guidance(self):
        # 2026-11-19 재상님 PC: 엔진 직접 배치 예시(runner.example.json)로 돌리면 분리 배치 포트가 없다. 환경 FAIL이 아니다.
        host = FakeHost()
        results = {r.name: r for r in run_checks({k: v for k, v in CONFIG.items() if k != "split"}, REPO, host.run, host.read_text,
                                                 lambda p: host.free)}
        self.assertTrue(results["ports"].skipped)
        self.assertTrue(results["ports"].ok)
        self.assertTrue(results["ports"].line().startswith("SKIP ports: 분리 배치 설정 없음 → 공식 runner.json("))
        self.assertIn("runner.official-split.example.json", results["ports"].reason)

    def test_repo_examples(self):
        # 레포의 공식 분리 배치 예시로는 ports가 점검되고(통과), 엔진 직접 배치 예시로는 건너뛴다.
        import json as _json
        host = FakeHost()
        split = _json.loads((REPO / "docs/evaluation/runner.official-split.example.json").read_text(encoding="utf-8"))
        direct = _json.loads((REPO / "docs/evaluation/runner.example.json").read_text(encoding="utf-8"))
        ports = {r.name: r for r in run_checks(split, REPO, host.run, host.read_text, lambda p: host.free)}["ports"]
        self.assertEqual((ports.ok, ports.skipped), (True, False), ports.reason)
        self.assertTrue({r.name: r for r in run_checks(direct, REPO, host.run, host.read_text, lambda p: host.free)}["ports"].skipped)


class TestParsers(unittest.TestCase):
    def test_versions(self):
        self.assertEqual(version_tuple("Docker version 29.1.3, build x"), (29, 1, 3))
        self.assertEqual(version_tuple("v2.40"), (2, 40))
        self.assertIsNone(version_tuple("none"))

    def test_routes_skip_default(self):
        self.assertEqual(route_subnets(ROUTES), ["192.168.1.0/24"])
        self.assertEqual(route_subnets("header\nbroken line\n"), [])

    def test_base_images_skip_stages_scratch_and_variables(self):
        refs = base_images(REPO)
        self.assertIn("python:3.12-slim-bookworm", refs)
        self.assertTrue(any(ref.startswith("postgres:16-bookworm@sha256:") for ref in refs))
        for skipped in ("scratch", "go-build", "product-runtime", "${ENGINE_IMAGE}"):
            self.assertNotIn(skipped, refs)
        self.assertTrue(all("$" not in ref for ref in refs))


class TestMain(unittest.TestCase):
    def test_exit_codes(self):
        with mock.patch("env_check.run_checks", return_value=[env_check.Result(True, "a", "x")]), \
                mock.patch("env_check.Path.read_text", return_value="{}"), contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(env_check.main(["--config", "c.json"]), 0)
        self.assertIn("PASS 전체: 1/1 통과", out.getvalue())
        for results, code, last in (([env_check.Result(True, "a", "x"), env_check.Result(True, "b", "y", skipped=True)], 2,
                                     "SKIP 전체: 1/2 통과, 건너뜀 1"),
                                    ([env_check.Result(False, "a", "x"), env_check.Result(True, "b", "y", skipped=True)], 1,
                                     "FAIL 전체: 0/2 통과, 건너뜀 1")):
            with self.subTest(code=code), mock.patch("env_check.run_checks", return_value=results), \
                    mock.patch("env_check.Path.read_text", return_value="{}"), contextlib.redirect_stdout(io.StringIO()) as out:
                self.assertEqual(env_check.main(["--config", "c.json"]), code)
            self.assertIn(last, out.getvalue().splitlines()[-1])

    def test_unreadable_config_is_a_failure(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(env_check.main(["--config", "/nonexistent/runner.json"]), 1)
        self.assertEqual(out.getvalue(), "FAIL config: 러너 설정을 읽지 못함\n")


if __name__ == "__main__":
    unittest.main()
