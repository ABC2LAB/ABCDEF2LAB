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
import runner_split  # noqa: E402  러너가 설치 입력·경계 설정에 쓰는 주소(4-7a). overlay와 같아야 한다.
import tool_surface  # noqa: E402  도구 면 측정(결정안 §4)이 MCP를 띄우는 기능 칸. 제품 explore-mcp 명령과 같아야 한다.
import runner_split_probe  # noqa: E402  여러 출발점 점검(4-8)이 이 렌더에서 입구를 읽는다.
import runner_window  # noqa: E402  구간 감시가 이 렌더에서 서비스별 healthcheck 명령을 읽는다.
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
             "EVAL_METER_DRAIN_GRACE": "10s", "EVAL_METER_REQUEST_CAP": "300", "EVAL_EVIDENCE_DIR": EVIDENCE_DIR, "EVAL_OBS_PORT": "8090",
             "EVAL_OBS_PATH": "/score", "EVAL_OBS_HOST_PORT": "49190", "EVAL_PATH_KEY_FILE": "/srv/eval/path.key"}


def key_mount_safe(mount: dict, source: str) -> bool:
    """경로 가림 키 마운트의 보안 조건: 그 파일을 읽기 전용 bind로, 호스트에 경로를 만들지 않고(없는 키 파일을 데몬이
    디렉터리로 만들지 않게) 다른 bind 옵션 없이 붙인다. Compose 판마다 다른 렌더 표현(빈 bind, 명시한 false)은 같게 본다."""
    bind = mount.get("bind", {})
    return (mount.get("type") == "bind" and mount.get("source") == source and mount.get("target") == "/run/eval-path-key"
            and mount.get("read_only") is True and isinstance(bind, dict) and set(bind) <= {"create_host_path"}
            and bind.get("create_host_path", False) is False)


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


SYNTHETIC_OVERLAY = "deploy/eval/compose.split-synthetic.yaml"


class EvalSplitSyntheticOverlayTests(unittest.TestCase):
    """합성 모델 overlay(2026-11-18 사용자 결정 (a)): 평가 전용이고, 주소 핀은 스크립트 모델 주소·포트 하나다."""

    @classmethod
    def setUpClass(cls):
        extra = dict(SYNTHETIC, PRODUCT_TAG="synthtest", EVAL_SCRIPTED_MODEL_SCRIPT="/srv/repo/scripts/eval/scripted_product_llm.py",
                     EVAL_SCRIPTED_MODEL_PLAN="/srv/eval-run/scripted-plan.json")
        cls.base = render(PRODUCT_FILES + (OVERLAY,), SYNTHETIC)
        cls.rendered = render(PRODUCT_FILES + (OVERLAY, SYNTHETIC_OVERLAY), extra)

    def test_scripted_model_matches_the_runner_pin(self):
        service = self.rendered["services"][runner_split.SCRIPTED_MODEL_HOST]
        self.assertEqual(service["networks"], {"control": {"ipv4_address": runner_split.SCRIPTED_MODEL_IP}})
        command = service["command"]
        self.assertEqual(command[command.index("--host") + 1], runner_split.SCRIPTED_MODEL_IP)
        self.assertEqual(command[command.index("--port") + 1], str(runner_split.SCRIPTED_MODEL_PORT))
        self.assertEqual(command[command.index("--origin") + 1], f"http://{TARGET_NAME}:8080")
        self.assertEqual(runner_split.SCRIPTED_MODEL_BASE_URL,
                         f"http://{runner_split.SCRIPTED_MODEL_HOST}:{runner_split.SCRIPTED_MODEL_PORT}/v1")
        self.assertIn(ipaddress.ip_address(runner_split.SCRIPTED_MODEL_IP), ipaddress.ip_network(NETWORK_MAP["control"][1]))

    def test_scripted_model_is_contained(self):
        service = self.rendered["services"][runner_split.SCRIPTED_MODEL_HOST]
        self.assertNotIn("ports", service)  # 호스트 publish 없음
        self.assertTrue(service["read_only"])
        self.assertEqual(service["cap_drop"], ["ALL"])
        self.assertIn("no-new-privileges:true", service["security_opt"])
        self.assertEqual(service["user"], "4242:4242")
        self.assertTrue(all(m["type"] == "bind" and m.get("read_only") is True for m in service["volumes"]))
        self.assertEqual({m["target"] for m in service["volumes"]}, {"/eval/scripted_product_llm.py", "/eval/plan.json"})
        self.assertTrue(self.rendered["networks"]["control"].get("internal"))  # 내부망 하나에만 붙는다
        # 제품 이미지 healthcheck를 물려받지 않는다(구간 감시가 렌더에 없는 exec 이벤트를 경계 변경으로 본다)
        self.assertEqual(service.get("healthcheck"), {"disable": True})
        self.assertNotIn(runner_split.SCRIPTED_MODEL_HOST, runner_window.healthcheck_actions(self.rendered))

    def test_overlay_adds_only_the_scripted_model(self):
        # 제품·평가 서비스는 그대로이고 서비스 하나만 더한다(제품 설정·보안 칸을 바꾸지 않는다).
        self.assertEqual(set(self.rendered["services"]) - set(self.base["services"]), {runner_split.SCRIPTED_MODEL_HOST})
        for name, service in self.base["services"].items():
            with self.subTest(service=name):
                self.assertEqual({k: v for k, v in self.rendered["services"][name].items() if k != "image"},
                                 {k: v for k, v in service.items() if k != "image"})

    def test_synthetic_mode_is_not_offered_by_the_product(self):
        # 제품 기본 설정·설치 예시·화면에는 이 모드가 없다(평가 overlay에만 있다).
        product = [ROOT / name for name in PRODUCT_FILES] + [ROOT / "deploy/product/README.md"]
        product += sorted((ROOT / "deploy/product").glob("*.json")) + sorted((ROOT / "web/src").rglob("*.ts*"))
        for path in product:
            text = path.read_text(encoding="utf-8")
            with self.subTest(path=str(path.relative_to(ROOT))):
                self.assertNotIn(runner_split.SCRIPTED_MODEL_HOST, text)
                self.assertNotIn("scripted_product_llm", text)
                self.assertNotIn("compose.split-synthetic", text)


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

    def test_e2_host_secret_volume_stays_out_of_runtime_and_api(self):
        # DB 자격·키·저널은 독립 Host와 일회성 설치자만 읽는다.
        for rendered in (self.product, self.split):
            services = rendered["services"]
            host = services["e2-host"]
            self.assertEqual(host["user"], "1002:1000")
            self.assertEqual(set(host["networks"]), {"control"})
            self.assertTrue(host["read_only"])
            self.assertEqual(host["cap_drop"], ["ALL"])
            by_target = {m["target"]: m["source"] for m in host["volumes"]}
            secret, socket = by_target["/h"], by_target["/run/e2-host"]
            holders = {name for name, service in services.items()
                       if any(m.get("source") == secret for m in service.get("volumes") or [])}
            self.assertEqual(holders, {"install", "e2-host"})
            socket_holders = {name for name, service in services.items()
                              if any(m.get("source") == socket for m in service.get("volumes") or [])}
            self.assertEqual(socket_holders, {"install", "e2-host", "preparation"})

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

    def test_runner_addresses_match_the_overlay(self):
        # 러너가 설치 입력·경계 설정에 쓰는 주소(runner_split)가 overlay 렌더 결과와 같다. 한쪽만 바뀌면 깨진다.
        self.assertEqual(str(runner_split.EVAL_BAND), str(EVAL_BAND))
        for name, (product, evaluation) in NETWORK_MAP.items():
            with self.subTest(network=name):
                self.assertEqual(runner_split.PREFIX_MAP[product.rsplit(".", 1)[0] + "."], evaluation.rsplit(".", 1)[0] + ".")
        self.assertEqual(len(runner_split.PREFIX_MAP), len(NETWORK_MAP))
        services = self.split["services"]
        self.assertEqual(services["postgres"]["networks"]["control"]["ipv4_address"], runner_split.DATABASE_IP)
        self.assertEqual(services["fixture-dns"]["networks"]["control"]["ipv4_address"], runner_split.FIXTURE_DNS_IP)
        self.assertEqual(self.split["networks"]["target"]["ipam"]["config"][0]["subnet"], runner_split.TARGET_CIDR)
        meter = services["eval-meter"]["networks"]
        self.assertEqual((meter["target"]["ipv4_address"], meter["eval_explore_ingress"]["ipv4_address"]),
                         (runner_split.METER_MAIN_IP, runner_split.METER_EXPLORE_IP))
        # 경계 설정의 리슨 주소가 overlay의 경계 주소와 같고, 평가 upstream이 계측기 탐색 입구다.
        config = runner_split.boundary_config(runner_split.read_boundary_template(), 8081, 1 << 20)
        boundary = services["explore-boundary"]["networks"]
        self.assertEqual(config["browser_listen"].split(":")[0], boundary["explore_browser_ingress"]["ipv4_address"])
        self.assertEqual(config["direct_listen"].split(":")[0], boundary["explore_direct"]["ipv4_address"])
        self.assertEqual(config["eval_upstream"], f"{meter['eval_explore_ingress']['ipv4_address']}:8081")
        self.assertIn(f"-explore-listen={runner_split.METER_EXPLORE_IP}:8081", services["eval-meter"]["command"])
        # 원자료 디렉터리는 overlay init이 만드는 경로다(볼륨 /evidence 아래 run).
        self.assertEqual(services["explore-evidence-init"]["entrypoint"][-1].replace("/e/", "/evidence/"),
                         runner_split.BOUNDARY_EVIDENCE_DIR)

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


    def test_tool_surface_measures_mcp_with_the_product_capability_flags(self):
        # 도구 목록 측정은 제품 explore-mcp 명령에서 전송·연결 칸만 뺀 인자로 MCP를 띄운다(망 없는 1회용 stdio).
        for render in (self.product, self.split):
            self.assertEqual(tuple(tool_surface.mcp_capability_args(render["services"]["explore-mcp"]["command"])),
                             tool_surface.OURS_MCP_ARGS)

    def test_meter_masks_paths_with_the_b_key(self):
        # 계약 §4.1: 평가 회차 경계의 path_mask_key_file은 계측기 -path-key-file과 같은 키다. O 계측기는 B 계측기와 같은
        # 설정 키 파일을 같은 방식(compose.acceptance.yaml)으로 받는다. 경계는 러너가 같은 바이트로 만든 보호 입력 사본을 받는다
        # (runner_split_inputs, 시험 test_runner_split_inputs). 둘 중 하나라도 빠지면 P03 (메서드, 가린 경로) 대조가 안 된다.
        meter = self.split["services"]["eval-meter"]
        acceptance = (ROOT / "deploy/eval/compose.acceptance.yaml").read_text(encoding="utf-8")
        for flag in ("-path-log", "-path-key-file=/run/eval-path-key"):
            self.assertIn(f"      - {flag}\n", acceptance)
            self.assertEqual(meter["command"].count(flag), 1)
        mounts = [v for v in meter["volumes"] if v.get("target") == "/run/eval-path-key"]
        self.assertEqual(len(mounts), 1)
        self.assertTrue(key_mount_safe(mounts[0], "/srv/eval/path.key"), mounts[0])
        boundary_secrets = {s["source"] if isinstance(s, dict) else s for s in self.split["services"]["explore-init"].get("secrets") or []}
        self.assertIn("explore_path_key", boundary_secrets)

    def test_key_mount_check_accepts_both_compose_renders(self):
        # Compose 2.40은 기본값인 create_host_path: false를 빼고(`bind: {}`), 5.7.0은 남긴다(#672 리뷰). 둘 다 받고,
        # 호스트 경로를 만드는 설정·쓰기 가능·다른 원본은 거부한다.
        base = {"type": "bind", "source": "/k", "target": "/run/eval-path-key", "read_only": True}
        for bind in ({}, {"create_host_path": False}, None):
            mount = dict(base) if bind is None else dict(base, bind=bind)
            self.assertTrue(key_mount_safe(mount, "/k"), mount)
        for change in ({"bind": {"create_host_path": True}}, {"bind": {"create_host_path": False, "propagation": "rshared"}},
                       {"read_only": False}, {"source": "/other"}, {"type": "volume"}, {"bind": "x"}):
            with self.subTest(change=change):
                self.assertFalse(key_mount_safe(dict(base, **change), "/k"))

    def test_split_probe_reads_ports_from_this_render(self):
        # 여러 출발점 점검(결정안 §2.1)은 이 렌더의 명령과 짝인 보호 입력에서 입구를 읽는다. 경계 설정은 러너가 만드는 것
        # (runner_split.boundary_config), 설치 탐색 블록은 제품 README의 값이다.
        boundary = runner_split.boundary_config(runner_split.read_boundary_template(), 8081, 1 << 20)
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


    def test_window_healthcheck_table_from_this_render(self):
        # O 회차 구간 감시는 서비스마다 이 healthcheck exec 명령만 허용한다(경계는 NONE이라 없음).
        actions = runner_window.healthcheck_actions(self.split)
        self.assertEqual(actions, {
            "api": "curl --fail --silent http://127.0.0.1:28081/api/product/health",
            "e2-host": "/bin/sh -c test -S /run/e2-host/audit.sock || test -f /run/e2-host/legacy-unavailable",
            "postgres": "pg_isready -h 127.0.0.1 -U postgres -d abc2lab",
            "preparation": "test -S /p/ipc/preparation.sock",
            "workbench": "curl --fail --silent http://10.252.0.12:8787/api/product/health"})


if __name__ == "__main__":
    unittest.main()
