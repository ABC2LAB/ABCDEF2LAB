"""평가 머신 환경 점검(env_check) 시험. 실제 Docker·파일 대신 가짜 명령·읽기를 넣는다."""

from __future__ import annotations

import contextlib
import datetime
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
             ("docker", "buildx"), ("docker", "compose"), ("go", "version"), ("openssl", "version"), ("git", "--version"),
             ("apt-config", "dump"), ("dpkg-query", "-W"), ("systemctl", "is-active")}
NOW = datetime.datetime(2026, 11, 27, 0, 0, tzinfo=datetime.timezone.utc)
REBOOT_REQUIRED, REBOOT_PENDING = (key for key, _ in env_check.WINDOWS_REBOOT_KEYS)
APT_ON = ('APT::Periodic::Update-Package-Lists "1";\nAPT::Periodic::Unattended-Upgrade "1";\n'
          'Unattended-Upgrade::Allowed-Origins:: "${distro_id}:${distro_codename}-security";\n')


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
        self.power_values = {"STANDBYIDLE": (0, 0), "HIBERNATEIDLE": (0, 172800)}
        # Windows 업데이트: 재부팅 대기 키(있으면 대기), 일시 중지 만료 시각(None = 없음), None이면 레지스트리를 못 읽음
        self.reboot_keys: set[str] = set()
        self.pause_expiry: str | None = "2026-12-04T00:00:00Z"
        self.registry_readable = True
        # 개발 머신 실측 모양: 자동 보안 업데이트 켜짐, 엔진 패키지 고정(측정 날 hold)
        self.apt = APT_ON
        self.packages = {"unattended-upgrades": "ii ", "docker.io": "hi ", "containerd": "hi ", "runc": "hi "}
        self.timer = "active"
        self.dpkg_code: int | None = None
        self.desktop_running: bool | None = False

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
        if args[:2] == ["apt-config", "dump"]:
            return 0, self.apt
        if args[:2] == ["dpkg-query", "-W"]:
            if self.dpkg_code is not None:
                return self.dpkg_code, ""
            names = args[4:]
            lines = "".join(f"{name}\t{self.packages[name]}\n" for name in names if name in self.packages)
            return (0 if all(name in self.packages for name in names) else 1), lines
        if args[:2] == ["systemctl", "is-active"]:
            return (0 if self.timer == "active" else 3), self.timer + "\n"
        raise AssertionError(f"모르는 명령: {args}")

    def reg(self, key, value):
        if not self.registry_readable:
            return None
        if value is None:
            return key in self.reboot_keys, ""
        if self.pause_expiry is None:
            return False, ""
        return True, f"\r\nHKEY_LOCAL_MACHINE\\...\\Settings\r\n    {value}    REG_SZ    {self.pause_expiry}\r\n\r\n"

    def read_text(self, path):
        return {"/proc/net/tcp": self.tcp, "/proc/net/tcp6": None, "/proc/meminfo": self.meminfo, "/proc/net/route": self.routes}.get(path)

    def power(self, subgroup, setting):
        value = self.power_values.get(setting)
        if value is None:
            return None
        ac, dc = value
        # 한국어 Windows 출력 모양(로캘 글자는 깨져도 AC·DC·0x는 ASCII)
        return (f"    GUID 별칭: {setting}\n    현재 AC 전원 설정 색인: 0x{ac:08x}\n"
                f"    현재 DC 전원 설정 색인: 0x{dc:08x}\n")

    def checks(self, config=None):
        return {r.name: r for r in run_checks(CONFIG if config is None else config, REPO, self.run, self.read_text, lambda p: self.free,
                                              self.power, self.reg, lambda: NOW, lambda image: self.desktop_running)}


class TestAllPass(unittest.TestCase):
    def test_dev_machine_values_pass_and_only_read_commands_run(self):
        host = FakeHost()
        results = host.checks()
        self.assertEqual([name for name, r in results.items() if not r.ok], [])
        self.assertEqual(set(results), {"docker_server", "docker_client", "docker_native", "compose", "go", "python", "openssl", "git",
                                        "memory", "disk", "power", "docker_updates", "containers", "host_listen", "ports", "subnets", "buildx",
                                        "base_images"})
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
            "power": lambda h: h.power_values.update(STANDBYIDLE=(1800, 600)),
            "docker_updates": lambda h: h.packages.update({"docker.io": "ii "}),
        }
        for name, change in cases.items():
            with self.subTest(name=name):
                failed = self.failed(change)
                self.assertIn(name, failed)
                if name not in ("buildx",):  # buildx가 없으면 imagetools 조회도 실패해 기본 이미지 항목도 같이 실패할 수 있다
                    self.assertEqual(set(failed), {name}, failed)

    def test_power_settings(self):
        # 2026-11-20 사용자: 측정 PC가 절전·최대 절전으로 멈추지 않게. 전원 연결(AC) 값만 판정하고 배터리 값은 사유에 적는다.
        host = FakeHost()
        ok = host.checks()["power"]
        self.assertTrue(ok.ok and not ok.skipped)
        self.assertIn("최대 절전 AC 0초·DC 172800초", ok.reason)
        host.power_values["HIBERNATEIDLE"] = (3600, 0)
        bad = host.checks()["power"]
        self.assertFalse(bad.ok)
        self.assertIn("최대 절전", bad.reason)
        # Windows 전원 설정을 못 읽으면(WSL 아님) 직접 확인하라고 SKIP
        host.power_values = {}
        skipped = host.checks()["power"]
        self.assertTrue(skipped.skipped)
        self.assertTrue(skipped.line().startswith("SKIP power:"))
        # AC 값이 없는 출력은 FAIL(읽지 못함)
        reg, now = FakeHost().reg, (lambda: NOW)
        self.assertFalse(env_check.check_power(lambda sub, setting: "no index here", reg, now).ok)
        # 영어 Windows 출력도 같은 값으로 읽는다
        english = "Current AC Power Setting Index: 0x00000000\nCurrent DC Power Setting Index: 0x00000e10\n"
        self.assertTrue(env_check.check_power(lambda sub, setting: english, reg, now).ok)

    def test_windows_update_reboot_and_pause(self):
        # 2026-11-20 사용자: 재부팅을 기다리는 Windows 업데이트가 없고, 측정 날 업데이트를 일시 중지한다(공식 회차 절차 6절)
        host = FakeHost()
        ok = host.checks()["power"]
        self.assertTrue(ok.ok and not ok.skipped, ok.reason)
        self.assertIn("재부팅 대기 없음, 일시 중지 2026-12-04T00:00:00Z까지", ok.reason)
        for key, label in ((REBOOT_REQUIRED, "Windows 업데이트 재부팅 대기"), (REBOOT_PENDING, "구성 요소 업데이트 재부팅 대기")):
            with self.subTest(key=label):
                host = FakeHost()
                host.reboot_keys.add(key)
                bad = host.checks()["power"]
                self.assertFalse(bad.ok)
                self.assertIn(label, bad.reason)
                self.assertIn("재부팅 대기 있음", bad.reason)
        for expiry, words in ((None, "일시 중지 없음"), ("2026-11-26T23:59:59Z", "에 끝남"), ("2026-11-27T00:00:00Z", "에 끝남")):
            with self.subTest(expiry=expiry):
                host = FakeHost()
                host.pause_expiry = expiry
                bad = host.checks()["power"]
                self.assertFalse(bad.ok)
                self.assertIn("업데이트 일시 중지 안 됨", bad.reason)
                self.assertIn(words, bad.reason)
        # 모르는 시각 모양은 일시 중지로 읽지 않는다
        host = FakeHost()
        host.pause_expiry = "2026-12-04 00:00:00"
        self.assertFalse(host.checks()["power"].ok)
        # 레지스트리를 못 읽으면 직접 확인하라고 SKIP(전원 설정은 읽혀도)
        host = FakeHost()
        host.registry_readable = False
        skipped = host.checks()["power"]
        self.assertTrue(skipped.skipped)
        self.assertIn("Windows 업데이트 상태를 읽을 수 없음", skipped.reason)
        # 재부팅 대기 키 하나라도 못 읽으면 일시 중지가 읽혀도 '확인 못 함'이다(없음으로 읽지 않는다)
        base = FakeHost().reg
        for key in (REBOOT_REQUIRED, REBOOT_PENDING):
            with self.subTest(unreadable=key):
                self.assertIsNone(env_check.windows_update_state(lambda k, v: None if k == key else base(k, v), lambda: NOW))
        # 절전이 켜져 있으면 업데이트가 괜찮아도 FAIL
        host = FakeHost()
        host.power_values["STANDBYIDLE"] = (600, 0)
        self.assertFalse(host.checks()["power"].ok)

    def test_docker_self_update(self):
        # 2026-11-20 사용자: 측정 중 Docker가 스스로 업데이트·재시작하지 않게
        host = FakeHost()
        held = host.checks()["docker_updates"]
        self.assertTrue(held.ok and not held.skipped, held.reason)
        self.assertIn("고정·제외", held.reason)
        host.packages.update({"docker.io": "ii ", "runc": "ii "})
        bad = host.checks()["docker_updates"]
        self.assertFalse(bad.ok)
        self.assertIn("sudo apt-mark hold docker.io runc", bad.reason)
        # 제외 목록(정규식)에 있으면 고정과 같다
        host.apt = APT_ON + 'Unattended-Upgrade::Package-Blacklist:: "docker\\.io$";\nUnattended-Upgrade::Package-Blacklist:: "ru";\n'
        self.assertTrue(host.checks()["docker_updates"].ok)
        host.apt = APT_ON + 'Unattended-Upgrade::Package-Blacklist:: "docker.io$";\n'
        self.assertFalse(host.checks()["docker_updates"].ok)  # runc가 남음
        # 자동 업그레이드가 꺼져 있으면(설정 0·패키지 없음·타이머 꺼짐) 고정하지 않아도 된다
        for change in (lambda h: setattr(h, "apt", APT_ON.replace('Unattended-Upgrade "1"', 'Unattended-Upgrade "0"')),
                       lambda h: setattr(h, "apt", 'APT::Periodic::Update-Package-Lists "1";\n'),
                       lambda h: h.packages.pop("unattended-upgrades"),
                       lambda h: h.packages.update({"unattended-upgrades": "rc "}),
                       lambda h: setattr(h, "timer", "inactive")):
            with self.subTest(change=change):
                host = FakeHost()
                host.packages.update({"docker.io": "ii "})
                change(host)
                result = host.checks()["docker_updates"]
                self.assertTrue(result.ok, result.reason)
                self.assertIn("자동 업그레이드 꺼짐", result.reason)
        # Windows에서 Docker Desktop이 돌면 FAIL(종료), 확인 못 하면 통과하되 사유에 남긴다
        host = FakeHost()
        host.desktop_running = True
        self.assertIn("Docker Desktop이 돌고 있음", self.failed(lambda h: setattr(h, "desktop_running", True))["docker_updates"])
        host.desktop_running = None
        unknown = host.checks()["docker_updates"]
        self.assertTrue(unknown.ok)
        self.assertIn("Docker Desktop 실행 여부는 확인 못 함", unknown.reason)
        # Docker Desktop 엔진은 설정 파일 키를 확인하지 못해 직접 확인(SKIP). 엔진 종류 항목(docker_native)이 따로 FAIL이다.
        host = FakeHost()
        host.os = "Docker Desktop"
        self.assertTrue(host.checks()["docker_updates"].skipped)
        # 읽지 못하면 FAIL(dpkg-query 종료 코드 1은 '목록 일부가 없음'이라 정상, 2는 오류)
        for change in (lambda h: h.failing.add(("apt-config", "dump")), lambda h: setattr(h, "dpkg_code", 2),
                       lambda h: h.failing.add(("docker", "info"))):
            with self.subTest(change=change):
                host = FakeHost()
                change(host)
                self.assertFalse(host.checks()["docker_updates"].ok)

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
