"""탐색 경계(explore-boundary)·보호 입력 준비(explore-init) 구성 시험. 컨테이너는 띄우지 않고 Compose 렌더링과 파일만 본다.

docs/implementation/explore-boundary-core.md·deploy/product/README.md의 배치를 고정한다. 기본 설치에는 경계가 없고,
`--profile exploration`일 때만 경계가 브라우저 입구·직접 입구의 고정 주소로 붙는다. 경계는 비root(1001)이고 능력이
없으며, 보호 입력은 경계 전용 볼륨으로만 읽는다. 엔진은 직접 입구 망과 제어 소켓 볼륨에만 붙는다.
"""

import ipaddress
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
COMPOSE = ROOT / "deploy/product/compose.yaml"
DOCKERFILE = ROOT / "deploy/product/Dockerfile"
EXAMPLE = ROOT / "deploy/product/explore-boundary.example.json"
INIT = ROOT / "deploy/product/explore-init-entrypoint.sh"
BOUNDARY, INIT_SERVICE, ENGINE = "explore-boundary", "explore-init", "preparation"


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
        synthetic = {"PRODUCT_SOURCE_SHA": "a" * 40, "PRODUCT_INSTALL_FILE": str(install), "PRODUCT_POSTGRES_PASSWORD_FILE": str(password)}
        env = {k: v for k, v in os.environ.items() if not k.startswith(("EXPLORE_", "PRODUCT_"))}
        env.update(synthetic)
        env["WSLENV"] = ":".join(filter(None, [env.get("WSLENV", ""), *synthetic]))
        args = [docker, "compose", "-f", str(COMPOSE)]
        for profile in profiles:
            args += ["--profile", profile]
        result = subprocess.run(args + ["config", "--format", "json"], env=env, text=True, capture_output=True, timeout=30, check=False)
    if result.returncode != 0:
        raise AssertionError("Compose render failed: " + result.stderr[:500])
    return json.loads(result.stdout)


class ExploreBoundaryCompose(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.base = render()
        cls.on = render("exploration")
        cls.example = json.loads(EXAMPLE.read_text(encoding="utf-8"))

    def test_default_install_has_no_boundary(self):
        self.assertNotIn(BOUNDARY, self.base["services"])
        self.assertNotIn(INIT_SERVICE, self.base["services"])

    def test_boundary_is_unprivileged_and_reads_only_its_volume(self):
        svc = self.on["services"][BOUNDARY]
        self.assertEqual(svc["user"], "1001:1000")
        self.assertEqual(svc.get("cap_add", []), [])
        self.assertEqual(svc["cap_drop"], ["ALL"])
        self.assertTrue(svc["read_only"])
        self.assertIn("no-new-privileges:true", svc["security_opt"])
        self.assertEqual(svc["entrypoint"], ["/app/explore-boundary"])
        self.assertEqual(svc["command"], ["-config", "/i/boundary.json"])
        self.assertNotIn("secrets", svc)
        mounts = {m["target"]: m for m in svc["volumes"]}
        self.assertEqual(set(mounts), {"/i", "/run/explore-control"})
        self.assertTrue(mounts["/i"].get("read_only"))
        self.assertEqual(mounts["/i"]["source"], "explore-boundary-input")
        self.assertEqual(mounts["/run/explore-control"]["source"], "explore-control")
        self.assertEqual(svc["depends_on"][INIT_SERVICE]["condition"], "service_completed_successfully")

    def test_boundary_networks_and_listen_addresses_match_example(self):
        svc = self.on["services"][BOUNDARY]
        self.assertEqual(set(svc["networks"]), {"explore_browser_ingress", "explore_direct", "target", "frontend"})
        browser_ip = svc["networks"]["explore_browser_ingress"]["ipv4_address"]
        direct_ip = svc["networks"]["explore_direct"]["ipv4_address"]
        self.assertEqual(self.example["browser_listen"], browser_ip + ":3128")
        self.assertEqual(self.example["direct_listen"], direct_ip + ":3129")
        nets = self.on["networks"]
        for name, ip in (("explore_browser_ingress", browser_ip), ("explore_direct", direct_ip)):
            self.assertTrue(nets[name]["internal"])
            subnet = ipaddress.ip_network(nets[name]["ipam"]["config"][0]["subnet"])
            self.assertIn(ipaddress.ip_address(ip), subnet)
        self.assertNotIn("control", svc["networks"])
        # 엔진은 frontend도 같이 써서 서비스 이름이 그 망 주소로 풀린다. 직접 입구는 이 망에만 있는 별칭으로 부른다.
        self.assertIn("explore-direct", svc["networks"]["explore_direct"].get("aliases", []))
        readme = (ROOT / "deploy/product/README.md").read_text(encoding="utf-8")
        self.assertIn('"direct_proxy": "http://explore-direct:3129"', readme)
        self.assertNotIn("explore-boundary:3129", readme)

    def test_init_has_no_network_and_minimal_capabilities(self):
        svc = self.on["services"][INIT_SERVICE]
        self.assertEqual(svc["network_mode"], "none")
        self.assertEqual(sorted(svc["cap_add"]), ["CHOWN", "DAC_OVERRIDE", "FOWNER"])
        self.assertEqual(sorted(s["source"] for s in svc["secrets"]), ["explore_boundary_config", "explore_ca_cert", "explore_ca_key", "explore_path_key"])
        self.assertEqual({m["target"] for m in svc["volumes"]}, {"/i", "/c", "/s"})
        script = INIT.read_text(encoding="utf-8")
        for want in ("install -o 1001 -g 1001 -m 0600", "chmod 0700 /i", "-m 0644 \"$in/explore_ca_cert\" /c/boundary-ca.pem", "chown 1001:1000 /s", "chmod 0750 /s"):
            self.assertIn(want, script)
        # CA 개인키는 공개 볼륨에 쓰지 않는다.
        self.assertNotIn("explore_ca_key\" /c", script)

    def test_engine_joins_direct_ingress_and_control_socket_only(self):
        svc = self.on["services"][ENGINE]
        self.assertIn("explore_direct", svc["networks"])
        self.assertNotIn("explore_browser_ingress", svc["networks"])
        self.assertIn("/run/explore-control", {m["target"] for m in svc["volumes"]})
        self.assertNotIn("/i", {m["target"] for m in svc["volumes"]})
        self.assertEqual(svc["user"].split(":")[0], str(self.example["engine_uid"]))
        self.assertEqual(svc["user"].split(":")[1], str(self.example["engine_gid"]))

    def test_example_blocks_internal_endpoints(self):
        controls = set(self.example["control_endpoints"])
        for service, network in ((ENGINE, "control"), (ENGINE, "target"), (ENGINE, "frontend"), ("postgres", "control"),
                                 ("explore-mcp", "explore_control"), ("explore-browser", "explore_cdp"), ("explore-mcp", "explore_cdp")):
            ip = self.on["services"][service]["networks"][network]["ipv4_address"]
            self.assertTrue(ip + ":0" in controls or any(c.startswith(ip + ":") for c in controls), service + " " + network)

    def test_dockerfile_ships_boundary_and_init(self):
        text = DOCKERFILE.read_text(encoding="utf-8")
        self.assertIn("-o /out/explore-boundary ./cmd/explore-boundary", text)
        self.assertIn("COPY --from=go-build /out/explore-boundary /app/explore-boundary", text)
        self.assertIn("deploy/product/explore-init-entrypoint.sh /app/explore-init-entrypoint", text)


if __name__ == "__main__":
    unittest.main()
