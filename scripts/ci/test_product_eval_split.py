"""탐색 분리 배치 평가 overlay(deploy/eval/compose.split.yaml) 렌더 시험. 컨테이너는 띄우지 않고 `docker compose config`만 본다.

탐색 분리 배치 결정안 §2 A(c)의 렌더 시험을 고정한다.
① 모든 주소·대역이 평가 대역 안이다(명령 안의 주소 포함, 제품 기본 대역 172.31.x가 남지 않음).
② 제품 서비스의 보안 칸(user·cap·read_only·security_opt·seccomp·tmpfs·망 모드·망 소속)과 제품 망의 internal이 제품 렌더와
   같다. overlay가 다시 쓴 명령은 제품 명령에서 주소만 바꾼 것이다(제품이 명령을 바꾸면 이 시험이 깨진다).
③ overlay가 엔진(preparation·api·workbench)·브라우저·MCP에 볼륨을 더하지 않는다.
④ 계측기 근거 디렉터리는 계측기·관측 front에만 붙는다.
그리고 계측기 탐색 입구가 탐색 입구 망 주소에만 묶이고, 대상 이름이 target 망에서 계측기 본 입구로 풀리는지 본다.
"""

import ipaddress
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts" / "eval"))
import runner_split_probe  # noqa: E402  여러 출발점 점검(4-8)이 이 렌더에서 입구를 읽는다.
PRODUCT_FILES = ("deploy/product/compose.yaml", "deploy/product/compose.acceptance.yaml",
                 "deploy/product/compose.exploration-acceptance.yaml")
OVERLAY = "deploy/eval/compose.split.yaml"
EVAL_BAND = ipaddress.ip_network("10.252.0.0/20")
# 제품 망 → 평가 망 대역(같은 호스트 번호를 쓴다).
NETWORK_MAP = {"frontend": ("172.31.250.0/24", "10.252.0.0/24"), "control": ("172.31.252.0/24", "10.252.2.0/24"),
               "target": ("172.31.253.0/24", "10.252.3.0/24"), "explore_control": ("172.31.254.0/24", "10.252.4.0/24"),
               "explore_cdp": ("172.31.255.0/24", "10.252.5.0/24"), "explore_direct": ("172.31.247.0/24", "10.252.7.0/24"),
               "explore_browser_ingress": ("172.31.248.0/24", "10.252.8.0/24")}
# 평가 배치에서 띄우지 않는 수용시험 서비스(대상은 평가 target, 모델은 제품 자체 OpenRouter 경로).
UNUSED = {"juice-shop", "target-front", "target-front-b", "synthetic-model"}
# overlay가 더하는 서비스. 제품 서비스가 아니라 ②의 대상이 아니다.
EVAL_SERVICES = {"target", "eval-meter", "eval-obs", "explore-evidence-init"}
# 경계 원자료 볼륨(계약 §4.1, 2026-11-17 결정 B). 경계와 그 디렉터리를 만드는 init에만 붙는다. 러너 도우미는 compose 밖에서
# 봉인·정지 뒤에 읽기 전용으로 붙인다.
BOUNDARY_EVIDENCE_VOLUME = "explore-evidence"
SECURITY_FIELDS = ("user", "cap_add", "cap_drop", "read_only", "security_opt", "tmpfs", "network_mode", "privileged",
                   "pid", "ipc", "group_add", "devices", "sysctls", "ulimits")
NO_EXTRA_VOLUMES = ("preparation", "api", "workbench", "explore-browser", "explore-mcp")
EVIDENCE_DIR = "/srv/eval-run/meter"
TARGET_NAME = "app.eval.test"
SYNTHETIC = {"EVAL_TARGET_NAME": TARGET_NAME, "EVAL_TARGET_IMAGE": "example/target@sha256:" + "1" * 64,
             "EVAL_METER_IMAGE": "example/meter@sha256:" + "2" * 64, "EVAL_RUNNER_UID": "4242", "EVAL_RUNNER_GID": "4242",
             "EVAL_METER_PORT": "8080", "EVAL_METER_EXPLORE_PORT": "8081", "EVAL_TARGET_PORT": "5000",
             "EVAL_METER_DRAIN_GRACE": "10s", "EVAL_EVIDENCE_DIR": EVIDENCE_DIR, "EVAL_OBS_PORT": "8090",
             "EVAL_OBS_PATH": "/score", "EVAL_OBS_HOST_PORT": "49190"}


def render(files, extra=None):
    docker = shutil.which(os.environ.get("E2_DOCKER_CLI", "docker"))
    if not docker:
        if os.environ.get("CI"):
            raise AssertionError("CI에 Docker Compose CLI가 없다")
        raise unittest.SkipTest("Docker Compose CLI unavailable")
    with tempfile.TemporaryDirectory() as tmp:
        install, password = Path(tmp, "install.json"), Path(tmp, "password")
        install.write_text("{}", encoding="utf-8")
        password.write_text("x", encoding="utf-8")
        synthetic = {"PRODUCT_SOURCE_SHA": "a" * 40, "PRODUCT_INSTALL_FILE": str(install),
                     "PRODUCT_POSTGRES_PASSWORD_FILE": str(password), **(extra or {})}
        env = {k: v for k, v in os.environ.items() if not k.startswith(("EXPLORE_", "PRODUCT_", "EVAL_"))}
        env.update(synthetic)
        env["WSLENV"] = ":".join(filter(None, [env.get("WSLENV", ""), *synthetic]))
        args = [docker, "compose", "-p", "evalsplit"]
        for name in files:
            args += ["-f", str(ROOT / name)]
        result = subprocess.run(args + ["--profile", "exploration", "config", "--format", "json"], env=env, text=True,
                                capture_output=True, timeout=30, check=False)
    if result.returncode != 0:
        raise AssertionError("Compose render failed: " + result.stderr[:500])
    return json.loads(result.stdout)


def strings(value):
    if isinstance(value, dict):
        for item in value.values():
            yield from strings(item)
    elif isinstance(value, list):
        for item in value:
            yield from strings(item)
    elif isinstance(value, str):
        yield value


def to_eval(text):
    """제품 주소를 평가 주소로 바꾼다(같은 호스트 번호)."""
    for product, evaluation in NETWORK_MAP.values():
        text = text.replace(product.rsplit(".", 1)[0] + ".", evaluation.rsplit(".", 1)[0] + ".")
    return text


class EvalSplitOverlayTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.product = render(PRODUCT_FILES)
        cls.split = render((*PRODUCT_FILES, OVERLAY), SYNTHETIC)

    def test_every_address_is_in_the_eval_band(self):
        # ① 망 대역·서비스 주소·명령 안 주소가 모두 평가 대역이다. 제품 기본 대역이 어디에도 남지 않는다.
        for name, network in self.split["networks"].items():
            with self.subTest(network=name):
                configs = (network.get("ipam") or {}).get("config") or []
                self.assertTrue(configs, "고정 대역이 없는 망은 Docker가 아무 대역이나 고른다")
                for entry in configs:
                    self.assertTrue(ipaddress.ip_network(entry["subnet"]).subnet_of(EVAL_BAND))
        for name, service in self.split["services"].items():
            for network, endpoint in (service.get("networks") or {}).items():
                address = (endpoint or {}).get("ipv4_address")
                if address:
                    with self.subTest(service=name, network=network):
                        self.assertIn(ipaddress.ip_address(address), EVAL_BAND)
        self.assertFalse([s for s in strings(self.split) if "172.31." in s])
        # 평가 대역끼리 겹치지 않는다(같은 /24를 두 망이 쓰지 않음).
        subnets = [ipaddress.ip_network(c["subnet"]) for n in self.split["networks"].values() for c in n["ipam"]["config"]]
        self.assertEqual(len(subnets), len(set(subnets)))

    def test_product_security_fields_are_unchanged(self):
        # ② 제품 서비스의 보안 칸·망 소속과 제품 망의 internal이 제품 렌더와 같다.
        product_services = set(self.product["services"]) - UNUSED
        self.assertEqual(set(self.split["services"]), product_services | EVAL_SERVICES)
        self.assertFalse(UNUSED & set(self.split["services"]))
        for name in sorted(product_services):
            before, after = self.product["services"][name], self.split["services"][name]
            for field in SECURITY_FIELDS:
                with self.subTest(service=name, field=field):
                    self.assertEqual(after.get(field), before.get(field))
            with self.subTest(service=name, field="networks"):
                added = set(after.get("networks") or {}) - set(before.get("networks") or {})
                # 경계만 계측기 탐색 입구 망이 늘어난다(결정안 §2 표). 그 밖의 제품 서비스는 망 소속이 같다.
                self.assertEqual(added, {"eval_explore_ingress"} if name == "explore-boundary" else set())
                self.assertFalse(set(before.get("networks") or {}) - set(after.get("networks") or {}))
        for name in NETWORK_MAP:
            with self.subTest(network=name):
                self.assertEqual(self.split["networks"][name].get("internal"), self.product["networks"][name].get("internal"))
                product_subnet = self.product["networks"][name]["ipam"]["config"][0]["subnet"]
                self.assertEqual((product_subnet, self.split["networks"][name]["ipam"]["config"][0]["subnet"]), NETWORK_MAP[name])
        for name in ("eval_target_back", "eval_explore_ingress"):
            self.assertTrue(self.split["networks"][name].get("internal"))

    def test_rewritten_commands_only_change_addresses(self):
        # ② overlay가 다시 쓴 명령·헬스체크는 제품 것에서 주소만 바꾼 것이다(제품 명령이 바뀌면 깨져서 다시 맞추게 한다).
        for name in ("workbench", "explore-browser", "explore-mcp"):
            with self.subTest(service=name):
                self.assertEqual(self.split["services"][name]["command"],
                                 [to_eval(item) for item in self.product["services"][name]["command"]])
        self.assertEqual(self.split["services"]["workbench"]["healthcheck"]["test"],
                         [to_eval(item) for item in self.product["services"]["workbench"]["healthcheck"]["test"]])
        for name, service in self.split["services"].items():
            if name in EVAL_SERVICES or name in ("workbench", "explore-browser", "explore-mcp", "fixture-init", "fixture-dns"):
                continue
            with self.subTest(service=name):
                self.assertEqual(service.get("command"), self.product["services"][name].get("command"))

    def test_no_volumes_added_to_engine_browser_mcp(self):
        # ③ 엔진·브라우저·MCP의 볼륨은 제품 렌더와 같다.
        for name in NO_EXTRA_VOLUMES:
            with self.subTest(service=name):
                self.assertEqual(self.split["services"][name].get("volumes"), self.product["services"][name].get("volumes"))

    def test_evidence_dir_only_on_meter_and_observer(self):
        # ④ 계측기 근거 디렉터리는 계측기·관측 front에만 붙는다.
        holders = {name for name, service in self.split["services"].items()
                   for volume in service.get("volumes") or [] if volume.get("source") == EVIDENCE_DIR}
        self.assertEqual(holders, {"eval-meter", "eval-obs"})

    def test_boundary_evidence_volume_only_on_boundary(self):
        # ④ 경계 원자료 볼륨은 경계와 그 init에만 붙는다. 엔진·브라우저·MCP·계측기에는 없다.
        holders = {name for name, service in self.split["services"].items()
                   for volume in service.get("volumes") or [] if volume.get("source") == BOUNDARY_EVIDENCE_VOLUME}
        self.assertEqual(holders, {"explore-boundary", "explore-evidence-init"})
        mounts = {name: [(v["target"], v.get("read_only")) for v in service.get("volumes") or []
                         if v.get("source") == BOUNDARY_EVIDENCE_VOLUME] for name, service in self.split["services"].items()}
        self.assertEqual(mounts["explore-boundary"], [("/evidence", None)])
        # 경계는 init이 디렉터리를 만든 뒤에 뜬다.
        self.assertEqual(self.split["services"]["explore-boundary"]["depends_on"]["explore-evidence-init"]["condition"],
                         "service_completed_successfully")

    def test_evidence_init_is_minimal_and_matches_boundary_uid(self):
        init = self.split["services"]["explore-evidence-init"]
        product_boundary_user = self.product["services"]["explore-boundary"]["user"]
        uid, gid = product_boundary_user.split(":")
        # 경계 uid 소유 0700 디렉터리 하나만 만든다(경계 uid는 제품 렌더에서 읽은 값과 같아야 한다).
        self.assertEqual(init["entrypoint"], ["install", "-d", "-o", uid, "-g", uid, "-m", "0700", "/e/run"])
        self.assertEqual(self.split["services"]["explore-boundary"]["user"], product_boundary_user)
        self.assertEqual((init["network_mode"], init["read_only"], init["cap_drop"], sorted(init["cap_add"]), init["security_opt"]),
                         ("none", True, ["ALL"], ["CHOWN", "FOWNER"], ["no-new-privileges:true"]))
        self.assertEqual(init["image"], self.product["services"]["explore-init"]["image"])
        self.assertEqual([v["source"] for v in init["volumes"]], [BOUNDARY_EVIDENCE_VOLUME])

    def test_meter_ingresses_and_target_name(self):
        meter = self.split["services"]["eval-meter"]
        networks = meter["networks"]
        main, explore = networks["target"]["ipv4_address"], networks["eval_explore_ingress"]["ipv4_address"]
        # 두 입구는 각자 망 주소에만 묶는다. 탐색 입구가 target 망 주소에서 열리면 엔진이 닿는다.
        self.assertIn(f"-listen={main}:8080", meter["command"])
        self.assertIn(f"-explore-listen={explore}:8081", meter["command"])
        self.assertEqual(set(networks), {"target", "eval_explore_ingress", "eval_target_back"})
        # 대상 이름은 target 망에서 계측기 본 입구로 풀린다(망 별칭과 fixture DNS가 같은 주소).
        self.assertEqual(networks["target"]["aliases"], [TARGET_NAME])
        for name in ("fixture-dns", "fixture-init"):
            self.assertEqual(self.split["services"][name]["command"][-1], f"{TARGET_NAME}={main}")
        # 대상은 뒤쪽 망에만 있다. 엔진·경계가 직접 닿지 못한다.
        self.assertEqual(set(self.split["services"]["target"]["networks"]), {"eval_target_back"})
        members = {name for name, service in self.split["services"].items() if "eval_target_back" in (service.get("networks") or {})}
        self.assertEqual(members, {"target", "eval-meter", "eval-obs"})
        ingress = {name for name, service in self.split["services"].items() if "eval_explore_ingress" in (service.get("networks") or {})}
        self.assertEqual(ingress, {"eval-meter", "explore-boundary"})
        # 평가 서비스의 망 소속은 고정이다. 관측 front가 target 망에 붙으면 엔진이 관측 front에 닿는다.
        self.assertEqual(set(self.split["services"]["eval-obs"]["networks"]), {"eval_target_back", "eval_obs"})
        on_target = {name for name, service in self.split["services"].items() if "target" in (service.get("networks") or {})}
        product_on_target = {name for name, service in self.product["services"].items()
                             if "target" in (service.get("networks") or {})} - UNUSED
        self.assertEqual(on_target, product_on_target | {"eval-meter"})
        # 관측 front는 호스트 루프백에만 publish한다.
        self.assertEqual([(p.get("host_ip"), p.get("published")) for p in self.split["services"]["eval-obs"]["ports"]],
                         [("127.0.0.1", "49190")])


    def test_split_probe_reads_ports_from_this_render(self):
        # 여러 출발점 점검(결정안 §2.1)은 이 렌더의 명령과 짝인 보호 입력에서 입구를 읽는다. 경계 설정은 제품 예시의 포트에
        # overlay의 경계 주소를, 설치 탐색 블록은 제품 README의 값을 쓴다(러너가 만드는 것과 같은 짝).
        example = json.loads((ROOT / "deploy/product/explore-boundary.example.json").read_text(encoding="utf-8"))
        nets = self.split["services"]["explore-boundary"]["networks"]
        boundary = {"browser_listen": nets["explore_browser_ingress"]["ipv4_address"] + ":" + example["browser_listen"].rsplit(":", 1)[1],
                    "direct_listen": nets["explore_direct"]["ipv4_address"] + ":" + example["direct_listen"].rsplit(":", 1)[1]}
        readme = (ROOT / "deploy/product/README.md").read_text(encoding="utf-8")
        block = readme[readme.index('"target_policy": { "exploration": {'):]
        exploration = dict(re.findall(r'"(\w+)": "([^"]+)"', block[:block.index("```")]))
        ports = runner_split_probe.split_ports(self.split, boundary, exploration)
        meter = self.split["services"]["eval-meter"]["networks"]
        self.assertEqual((ports.meter_main, ports.meter_explore, ports.target_upstream),
                         ((meter["target"]["ipv4_address"], 8080), (meter["eval_explore_ingress"]["ipv4_address"], 8081), 5000))
        self.assertEqual((ports.mcp, ports.direct, ports.browser_ingress), (("explore-mcp", 8931), ("explore-direct", 3129),
                                                                           ("explore-boundary", 3128)))
        self.assertEqual(ports.cdp, (self.split["services"]["explore-browser"]["networks"]["explore_cdp"]["ipv4_address"], 9222))


if __name__ == "__main__":
    unittest.main()
