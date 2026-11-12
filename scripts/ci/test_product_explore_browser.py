"""탐색 브라우저 샌드박스(explore-browser·explore-mcp) 구성 시험. 컨테이너는 띄우지 않고 Compose 렌더링과 파일만 본다.

docs/contracts/exploration-boundary.md §5·§5.1·§9-1 분리 배치의 조건을 고정한다. Chromium(explore-browser)과
MCP(explore-mcp)가 다른 network namespace에 있고, 옵션 묶음(--host·--port·--allowed-hosts, --cdp-listen·--cdp-endpoint)이
어긋나거나, 목록 밖 옵션·제어 비밀·추가 망이 붙으면 실패한다.
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
MCP_ENTRYPOINT = ROOT / "deploy/product/explore-mcp-entrypoint.sh"
LAUNCHER = ROOT / "deploy/product/explore-chromium.js"
SERVICE = "explore-browser"
MCP_SERVICE = "explore-mcp"
SERVICES = (SERVICE, MCP_SERVICE)
# 값을 받는 옵션과 값 없는 옵션. 이 밖의 옵션은 넣지 않는다(계약 §5.1 CLI 옵션, §9-1 분리 대안의 --cdp-endpoint).
VALUE_FLAGS = {"--browser", "--proxy-server", "--cdp-endpoint", "--output-dir", "--port", "--host", "--allowed-hosts"}
BARE_FLAGS = {"--headless", "--isolated", "--no-webmcp"}
# Chromium 실행기(explore-chromium.js)가 받는 옵션 전부다. Chromium 기본 옵션은 실행기에 고정돼 있다.
CHROMIUM_FLAGS = {"--proxy-server", "--cdp-listen"}
# 서비스마다 붙는 망. MCP는 브라우저 입구망에, Chromium은 엔진 제어망에 붙지 않는다(B16 분리 배치).
SERVICE_NETWORKS = {
    SERVICE: {"explore_browser_ingress", "explore_cdp"},
    MCP_SERVICE: {"explore_control", "explore_cdp"},
}
# 망마다 붙을 수 있는 서비스. 엔진(preparation)과 탐색 경계(explore-boundary)는 후속 변경에서 붙는다.
NETWORK_MEMBERS = {
    "explore_control": {MCP_SERVICE, "preparation"},
    "explore_browser_ingress": {SERVICE, "explore-boundary"},
    "explore_cdp": {SERVICE, MCP_SERVICE},
}
SECRET_WORDS = re.compile(r"KEY|TOKEN|SECRET|PASSWORD|DSN|CREDENTIAL", re.IGNORECASE)
# 어느 실행 파일에도 나오면 안 되는 옵션. TLS 검증 끄기, 페이지 origin의 CDP 접속 허용, 모델이 바꿀 수 있는 MCP 설정이다.
FORBIDDEN_FLAGS = ("--ignore-https-errors", "--ignore-certificate-errors", "--allow-insecure-localhost",
                   "--remote-allow-origins", "--disable-web-security", "--config", "--caps", "--init-page", "--init-script")


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


def parse_command(command, value_flags=VALUE_FLAGS, bare_flags=BARE_FLAGS):
    """옵션을 이름 → 값(값 없는 옵션은 True)으로 바꾼다. 같은 옵션이 두 번 나오거나 목록 밖이면 실패한다."""
    flags, items = {}, list(command)
    while items:
        name = items.pop(0)
        if name in flags or name not in value_flags | bare_flags:
            raise AssertionError("허용 목록 밖이거나 중복된 옵션: " + name)
        if name in bare_flags:
            flags[name] = True
            continue
        if not items or items[0].startswith("--"):
            raise AssertionError("값이 없는 옵션: " + name)
        flags[name] = items.pop(0)
    return flags


def code_lines(path, comment):
    # 주석은 빼고 실제로 실행되는 줄만 본다.
    return "\n".join(line for line in path.read_text(encoding="utf-8").splitlines()
                     if not line.lstrip().startswith(comment))


class ExploreBrowserCompose(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.config = render("exploration")
        cls.browser = cls.config["services"][SERVICE]
        cls.mcp = cls.config["services"][MCP_SERVICE]

    def address(self, service, network):
        value = self.config["services"][service]["networks"][network]["ipv4_address"]
        subnet = self.config["networks"][network]["ipam"]["config"][0]["subnet"]
        self.assertIn(ipaddress.ip_address(value), ipaddress.ip_network(subnet))
        self.assertFalse(ipaddress.ip_address(value).is_loopback or ipaddress.ip_address(value).is_unspecified)
        return value

    def test_off_without_the_exploration_profile(self):
        services = render()["services"]
        for name in SERVICES:
            self.assertNotIn(name, services)
            self.assertEqual(self.config["services"][name]["profiles"], ["exploration"])

    def test_mcp_option_bundle(self):
        flags = parse_command(self.mcp["command"])
        self.assertEqual(set(flags), VALUE_FLAGS | BARE_FLAGS)
        port = flags["--port"]
        self.assertRegex(port, r"^[1-9][0-9]{0,4}$")
        # 엔진이 쓰는 이름(Host 헤더)만 받고, 그 이름은 제어망 주소로 풀린다.
        self.assertEqual(flags["--allowed-hosts"], f"{MCP_SERVICE}:{port}")
        self.assertEqual(flags["--host"], self.address(MCP_SERVICE, "explore_control"))
        self.assertRegex(flags["--proxy-server"], r"^http://explore-boundary:[1-9][0-9]{0,4}$")
        # 이미지에 깐 Playwright Chromium 판에 맞춘다. 외부 Chrome·다른 브라우저 채널은 쓰지 않는다.
        self.assertEqual(flags["--browser"], "chromium")
        tmpfs = [entry.split(":", 1)[0] for entry in self.mcp["tmpfs"]]
        self.assertTrue(any(flags["--output-dir"].startswith(mount + "/") for mount in tmpfs))

    def test_cdp_bundle(self):
        # MCP는 Chromium 컨테이너의 CDP 연결망 주소 하나로만 붙고, Chromium 실행기는 그 주소에만 CDP를 연다.
        mcp = parse_command(self.mcp["command"])
        chromium = parse_command(self.browser["command"], CHROMIUM_FLAGS, set())
        self.assertEqual(set(chromium), CHROMIUM_FLAGS)
        cdp = self.address(SERVICE, "explore_cdp")
        host, _, port = chromium["--cdp-listen"].rpartition(":")
        self.assertEqual(host, cdp)
        self.assertRegex(port, r"^[1-9][0-9]{0,4}$")
        self.assertEqual(mcp["--cdp-endpoint"], f"http://{cdp}:{port}")
        self.assertNotEqual(port, mcp["--port"])
        # 페이지 요청과 MCP가 만드는 격리 문맥이 같은 경계 입구를 쓴다.
        self.assertEqual(chromium["--proxy-server"], mcp["--proxy-server"])

    def test_bundle_follows_the_port_setting(self):
        # 포트 설정 하나가 --port와 --allowed-hosts에, CDP 포트 설정 하나가 --cdp-listen과 --cdp-endpoint에 같이 들어간다.
        text = COMPOSE.read_text(encoding="utf-8")
        self.assertEqual(text.count("${EXPLORE_BROWSER_MCP_PORT:-8931}"), 2)
        self.assertEqual(text.count("${EXPLORE_BROWSER_CDP_PORT:-9222}"), 2)

    def test_split_layout_keeps_mcp_out_of_the_browser_namespace(self):
        # B16: 같은 namespace의 native 자식이 MCP 제어 포트에 닿지 않게 Chromium과 MCP의 namespace를 나눈다.
        for name in SERVICES:
            s = self.config["services"][name]
            self.assertEqual(set(s["networks"]), SERVICE_NETWORKS[name], name)
            for key in ("network_mode", "pid", "ipc"):
                self.assertFalse(s.get(key), name + " " + key)
        self.assertNotIn("explore_control", self.browser["networks"])
        self.assertNotIn("explore_browser_ingress", self.mcp["networks"])
        self.assertEqual(self.browser["entrypoint"], ["/app/explore-browser-entrypoint"])
        self.assertEqual(self.mcp["entrypoint"], ["/app/explore-mcp-entrypoint"])
        self.assertIn(SERVICE, self.mcp.get("depends_on") or {})

    def test_isolation(self):
        for name, allowed in NETWORK_MEMBERS.items():
            self.assertTrue(self.config["networks"][name].get("internal"), name)
            members = {svc for svc, spec in self.config["services"].items() if name in (spec.get("networks") or {})}
            self.assertLessEqual(members, allowed, name)
        for name in SERVICES:
            s = self.config["services"][name]
            for key in ("ports", "expose", "secrets", "network_mode", "cap_add", "privileged", "devices", "env_file"):
                self.assertFalse(s.get(key), name + " " + key)
            for key in s.get("environment") or {}:
                self.assertNotRegex(key, SECRET_WORDS)
            self.assertTrue(s["read_only"], name)
            self.assertEqual(s["cap_drop"], ["ALL"], name)
            self.assertIn("no-new-privileges:true", s["security_opt"])
            self.assertEqual(s["user"], "1000:1000")
            self.assertEqual(s["build"]["target"], SERVICE)
            self.assertEqual(s["restart"], "no")
        # 경계 CA(공개 부분)는 Chromium만 읽는다. MCP는 대상 TLS를 하지 않으므로 볼륨이 없다.
        self.assertEqual(len(self.browser["volumes"]), 1)
        volume = self.browser["volumes"][0]
        self.assertEqual((volume["type"], volume["source"], volume["target"], volume.get("read_only")),
                         ("volume", "explore-ca", "/run/explore-ca", True))
        self.assertFalse(self.mcp.get("volumes"))


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
        for source, target in ((MCP_ENTRYPOINT, "/app/explore-mcp-entrypoint"), (LAUNCHER, "/app/explore-chromium.js")):
            self.assertIn(f"deploy/product/{source.name} {target}", body)
        policy = (ROOT / "server/mcp_tool_policy.go").read_text(encoding="utf-8")
        self.assertIn("@playwright/mcp@0.0.84", policy)


@unittest.skipIf(Path("/run/explore-ca/boundary-ca.pem").exists(), "이 호스트에 경계 CA가 있다")
class ExploreBrowserEntrypoint(unittest.TestCase):
    def test_refuses_to_start_without_the_boundary_ca(self):
        with tempfile.TemporaryDirectory() as tmp:
            bin_dir, marker = Path(tmp, "bin"), Path(tmp, "ran")
            bin_dir.mkdir()
            for name in ("node", "certutil"):
                stub = bin_dir / name
                stub.write_text(f"#!/bin/sh\necho {name} >> {marker}\n", encoding="utf-8")
                stub.chmod(0o755)
            result = subprocess.run(["sh", str(ENTRYPOINT), "--proxy-server", "http://x:1"], text=True, capture_output=True,
                                    timeout=10, env={"PATH": f"{bin_dir}:/usr/bin:/bin", "HOME": tmp}, check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(marker.exists())
            self.assertIn("CA", result.stderr)

    def test_never_disables_tls_checks(self):
        browser = code_lines(ENTRYPOINT, "#")
        mcp = code_lines(MCP_ENTRYPOINT, "#")
        launcher = code_lines(LAUNCHER, "//")
        for text in (browser, mcp, launcher):
            for flag in FORBIDDEN_FLAGS:
                self.assertNotIn(flag, text)
        # Chromium 컨테이너에는 MCP가 없고, MCP 컨테이너는 Chromium을 띄우지 않는다.
        self.assertIn("exec node /app/explore-chromium.js \"$@\"", browser)
        self.assertNotIn("playwright-mcp", browser)
        self.assertIn("exec playwright-mcp \"$@\"", mcp)
        self.assertNotIn("--no-sandbox", browser + mcp)
        # 샌드박스는 이전 배치(MCP가 chrome-for-testing 채널로 띄운 Chromium)와 같은 조건으로 실행기에서만 끈다.
        self.assertEqual(launcher.count("--no-sandbox"), 1)
        # 페이지의 loopback 요청도 경계를 거친다(Playwright 기본과 같음). CDP는 loopback·모든 인터페이스에 열지 않는다.
        self.assertIn("'--proxy-bypass-list=<-loopback>'", launcher)


@unittest.skipUnless(shutil.which("node"), "Node.js unavailable")
class ExploreChromiumLauncher(unittest.TestCase):
    def run_launcher(self, *args):
        return subprocess.run(["node", str(LAUNCHER), *args], text=True, capture_output=True, timeout=10, check=False,
                              env={"PATH": os.environ.get("PATH", "/usr/bin:/bin")})

    def test_rejects_unsafe_or_unknown_options(self):
        good = ["--proxy-server", "http://explore-boundary:3128", "--cdp-listen", "172.31.255.20:9222"]
        cases = [
            [],
            good[:2],
            good + ["--remote-debugging-address", "0.0.0.0"],
            good + ["--proxy-server", "http://explore-boundary:3128"],
            ["--proxy-server", "http://explore-boundary:3128", "--cdp-listen", "0.0.0.0:9222"],
            ["--proxy-server", "http://explore-boundary:3128", "--cdp-listen", "127.0.0.1:9222"],
            ["--proxy-server", "http://explore-boundary:3128", "--cdp-listen", "explore-browser:9222"],
            ["--proxy-server", "http://explore-boundary:3128", "--cdp-listen", "172.31.255.20:0"],
            ["--proxy-server", "http://user:pass@explore-boundary:3128", "--cdp-listen", "172.31.255.20:9222"],
            ["--proxy-server", "socks5://explore-boundary:3128", "--cdp-listen", "172.31.255.20:9222"],
            ["--proxy-server", "http://explore-boundary", "--cdp-listen", "172.31.255.20:9222"],
        ]
        for args in cases:
            with self.subTest(args=args):
                result = self.run_launcher(*args)
                # 검증 실패는 Chromium을 띄우기 전의 고정 종료 코드다.
                self.assertEqual(result.returncode, 2)


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
        # 엔진은 탐색 제어망에서 MCP 클라이언트로 나가기만 한다. 그 망에 붙은 서비스는 엔진과 MCP 컨테이너(explore-mcp)뿐이다.
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
