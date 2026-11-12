"""탐색 브라우저 샌드박스(explore-browser) 구성 시험. 컨테이너는 띄우지 않고 Compose 렌더링과 파일만 본다.

docs/contracts/exploration-boundary.md §5·§5.1·§9-1의 조건을 고정한다. 옵션 묶음(--host·--port·--allowed-hosts)이
어긋나거나, 목록 밖 MCP 옵션·제어 비밀·추가 망이 붙으면 실패한다.
"""

import ipaddress
import json
import os
import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
COMPOSE = ROOT / "deploy/product/compose.yaml"
DOCKERFILE = ROOT / "deploy/product/Dockerfile"
ENTRYPOINT = ROOT / "deploy/product/explore-browser-entrypoint.sh"
SERVICE = "explore-browser"
# 값을 받는 옵션과 값 없는 옵션. 이 밖의 옵션은 넣지 않는다(계약 §5.1 CLI 옵션).
VALUE_FLAGS = {"--browser", "--proxy-server", "--output-dir", "--port", "--host", "--allowed-hosts"}
BARE_FLAGS = {"--headless", "--isolated", "--no-webmcp"}
# 망마다 붙을 수 있는 서비스. 엔진(preparation)과 탐색 경계(explore-boundary)는 후속 변경에서 붙는다.
NETWORK_MEMBERS = {
    "explore_control": {SERVICE, "preparation"},
    "explore_browser_ingress": {SERVICE, "explore-boundary"},
}
SECRET_WORDS = re.compile(r"KEY|TOKEN|SECRET|PASSWORD|DSN|CREDENTIAL", re.IGNORECASE)


def render(*profiles):
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
                     "PRODUCT_POSTGRES_PASSWORD_FILE": str(password)}
        env = {k: v for k, v in os.environ.items() if not k.startswith(("EXPLORE_", "PRODUCT_"))}
        env.update(synthetic)
        env["WSLENV"] = ":".join(filter(None, [env.get("WSLENV", ""), *synthetic]))
        args = [docker, "compose", "-f", str(COMPOSE)]
        for profile in profiles:
            args += ["--profile", profile]
        result = subprocess.run(args + ["config", "--format", "json"], env=env, text=True, capture_output=True,
                                timeout=30, check=False)
    if result.returncode != 0:
        raise AssertionError("Compose render failed: " + result.stderr[:500])
    return json.loads(result.stdout)


def parse_command(command):
    """옵션을 이름 → 값(값 없는 옵션은 True)으로 바꾼다. 같은 옵션이 두 번 나오거나 목록 밖이면 실패한다."""
    flags, items = {}, list(command)
    while items:
        name = items.pop(0)
        if name in flags or name not in VALUE_FLAGS | BARE_FLAGS:
            raise AssertionError("허용 목록 밖이거나 중복된 MCP 옵션: " + name)
        if name in BARE_FLAGS:
            flags[name] = True
            continue
        if not items or items[0].startswith("--"):
            raise AssertionError("값이 없는 옵션: " + name)
        flags[name] = items.pop(0)
    return flags


class ExploreBrowserCompose(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.config = render("exploration")
        cls.service = cls.config["services"][SERVICE]

    def test_off_without_the_exploration_profile(self):
        self.assertNotIn(SERVICE, render()["services"])
        self.assertEqual(self.service["profiles"], ["exploration"])

    def test_mcp_option_bundle(self):
        flags = parse_command(self.service["command"])
        self.assertEqual(set(flags), VALUE_FLAGS | BARE_FLAGS)
        port = flags["--port"]
        self.assertRegex(port, r"^[1-9][0-9]{0,4}$")
        # 엔진이 쓰는 이름(Host 헤더)만 받고, 그 이름은 제어망 주소로 풀린다.
        self.assertEqual(flags["--allowed-hosts"], f"{SERVICE}:{port}")
        control = self.service["networks"]["explore_control"]["ipv4_address"]
        self.assertEqual(flags["--host"], control)
        subnet = self.config["networks"]["explore_control"]["ipam"]["config"][0]["subnet"]
        self.assertIn(ipaddress.ip_address(control), ipaddress.ip_network(subnet))
        self.assertFalse(ipaddress.ip_address(control).is_loopback or ipaddress.ip_address(control).is_unspecified)
        self.assertRegex(flags["--proxy-server"], r"^http://explore-boundary:[1-9][0-9]{0,4}$")
        # 이미지에 깐 Playwright Chromium을 쓴다. 외부 Chrome·다른 브라우저 채널은 쓰지 않는다.
        self.assertEqual(flags["--browser"], "chromium")
        tmpfs = [entry.split(":", 1)[0] for entry in self.service["tmpfs"]]
        self.assertTrue(any(flags["--output-dir"].startswith(mount + "/") for mount in tmpfs))

    def test_bundle_follows_the_port_setting(self):
        # 포트 설정 하나가 --port와 --allowed-hosts에 같이 들어간다.
        text = COMPOSE.read_text(encoding="utf-8")
        self.assertEqual(text.count("${EXPLORE_BROWSER_MCP_PORT:-8931}"), 2)

    def test_isolation(self):
        s = self.service
        self.assertEqual(set(s["networks"]), set(NETWORK_MEMBERS))
        for name in NETWORK_MEMBERS:
            self.assertTrue(self.config["networks"][name].get("internal"), name)
        for name, allowed in NETWORK_MEMBERS.items():
            members = {svc for svc, spec in self.config["services"].items() if name in (spec.get("networks") or {})}
            self.assertLessEqual(members, allowed, name)
        for key in ("ports", "expose", "secrets", "network_mode", "cap_add", "privileged", "devices", "env_file"):
            self.assertFalse(s.get(key), key)
        for key in s.get("environment") or {}:
            self.assertNotRegex(key, SECRET_WORDS)
        self.assertEqual(len(s["volumes"]), 1)
        volume = s["volumes"][0]
        self.assertEqual((volume["type"], volume["source"], volume["target"], volume.get("read_only")),
                         ("volume", "explore-ca", "/run/explore-ca", True))
        self.assertTrue(s["read_only"])
        self.assertEqual(s["cap_drop"], ["ALL"])
        self.assertIn("no-new-privileges:true", s["security_opt"])
        self.assertEqual(s["user"], "1000:1000")
        self.assertEqual(s["build"]["target"], SERVICE)


class ExploreBrowserImage(unittest.TestCase):
    def stages(self):
        stages, current = {}, None
        for line in DOCKERFILE.read_text(encoding="utf-8").splitlines():
            match = re.match(r"FROM\s+\S+(?:\s+\S+)*\s+AS\s+(\S+)", line)
            if match:
                current = match.group(1)
                stages[current] = []
            elif current:
                stages[current].append(line)
        return stages

    def test_default_target_is_still_the_product(self):
        self.assertEqual(list(self.stages())[-1], "product")

    def test_stage_has_no_engine_binary_or_secret(self):
        body = "\n".join(self.stages()[SERVICE])
        self.assertNotIn("--from=", body)
        for name in ("/app/artex", "first-controlled-diagnosis", "e2-bash-sandbox", "/app/keys", "/app/policy"):
            self.assertNotIn(name, body)
        # 모델에 노출하는 도구 목록(계약 §5.1·server/mcp_tool_policy.go)의 기준 버전과 같다.
        self.assertIn("@playwright/mcp@0.0.84", body)
        self.assertIn("ENTRYPOINT [\"/app/explore-browser-entrypoint\"]", body)
        policy = (ROOT / "server/mcp_tool_policy.go").read_text(encoding="utf-8")
        self.assertIn("@playwright/mcp@0.0.84", policy)


@unittest.skipIf(Path("/run/explore-ca/boundary-ca.pem").exists(), "이 호스트에 경계 CA가 있다")
class ExploreBrowserEntrypoint(unittest.TestCase):
    def test_refuses_to_start_without_the_boundary_ca(self):
        with tempfile.TemporaryDirectory() as tmp:
            bin_dir, marker = Path(tmp, "bin"), Path(tmp, "ran")
            bin_dir.mkdir()
            for name in ("playwright-mcp", "certutil"):
                stub = bin_dir / name
                stub.write_text(f"#!/bin/sh\necho {name} >> {marker}\n", encoding="utf-8")
                stub.chmod(0o755)
            result = subprocess.run(["sh", str(ENTRYPOINT), "--headless"], text=True, capture_output=True, timeout=10,
                                    env={"PATH": f"{bin_dir}:/usr/bin:/bin", "HOME": tmp}, check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(marker.exists())
            self.assertIn("CA", result.stderr)

    def test_never_disables_tls_checks(self):
        # 주석은 빼고 실제로 실행되는 줄만 본다.
        text = "\n".join(line for line in ENTRYPOINT.read_text(encoding="utf-8").splitlines()
                         if not line.lstrip().startswith("#"))
        for flag in ("--ignore-https-errors", "--no-sandbox", "--config", "--caps", "--init-page", "--init-script"):
            self.assertNotIn(flag, text)
        self.assertIn('exec playwright-mcp "$@"', text)


if __name__ == "__main__":
    unittest.main()


class EngineNamespaceListen(unittest.TestCase):
    """엔진 netns(preparation, api·workbench가 공유)에는 모든 인터페이스에 여는 TCP 리슨이 없어야 한다.

    엔진이 탐색 제어망(explore_control)에 붙을 때 그 망에 포트가 열리지 않게 하려는 조건이다(계약 §9-1).
    preparation·api는 설치 설정에서 127.0.0.1에만 리슨하고(product_install_linux.go), workbench는 frontend 망 고정 주소에만 리슨한다.
    """

    @classmethod
    def setUpClass(cls):
        cls.config = render()

    def test_workbench_listens_only_on_the_frontend_address(self):
        services = self.config["services"]
        frontend = services["preparation"]["networks"]["frontend"]["ipv4_address"]
        subnet = self.config["networks"]["frontend"]["ipam"]["config"][0]["subnet"]
        self.assertIn(ipaddress.ip_address(frontend), ipaddress.ip_network(subnet))
        command = services["workbench"]["command"]
        listen = command[command.index("-listen") + 1]
        host, _, port = listen.rpartition(":")
        self.assertEqual(host, frontend)
        self.assertEqual(services["workbench"]["network_mode"], "service:preparation")
        self.assertIn(f"http://{frontend}:{port}/", " ".join(services["workbench"]["healthcheck"]["test"]))
        published = services["preparation"]["ports"]
        self.assertEqual([(p["host_ip"], str(p["target"])) for p in published], [("127.0.0.1", port)])

    def test_engine_joins_the_exploration_control_network_without_listening(self):
        # 엔진은 탐색 제어망에서 MCP 클라이언트로 나가기만 한다. 그 망에 붙은 서비스는 엔진과 브라우저 샌드박스뿐이다.
        services = self.config["services"]
        self.assertIn("explore_control", services["preparation"]["networks"])
        members = {name for name, spec in services.items() if "explore_control" in (spec.get("networks") or {})}
        self.assertEqual(members, {"preparation"})
        listen = services["workbench"]["command"][services["workbench"]["command"].index("-listen") + 1]
        self.assertNotIn(listen.rpartition(":")[0], ("", "0.0.0.0", "::"))

    def test_engine_loopback_listeners_stay_loopback(self):
        source = (ROOT / "cmd/first-controlled-diagnosis/product_install_linux.go").read_text(encoding="utf-8")
        listens = re.findall(r'(?:ListenAddr|ReportListenAddr)(?::\s*|\s*=\s*)"([^"]+)"', source)
        self.assertTrue(listens)
        for listen in listens:
            self.assertTrue(listen.startswith("127.0.0.1:"), listen)
