#!/usr/bin/env python3
"""소유 contract_fixture 전용 Docker 배치. 제품 탐색 경계·실측 보존율을 주장하지 않는다.

python3 -I로 실행한다. Docker/Go/OpenSSL stdout·stderr 원문은 사용자 출력에 싣지 않는다.
"""
from __future__ import annotations

import argparse
import hashlib
import http.server
import ipaddress
import json
import os
import re
import secrets
import socket
import ssl
import stat
import subprocess
import sys
import threading
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FORMAT = "jindo.browser-fixture-harness/v1"
IMAGE = "jindo-p1-explore-browser:b679f7b"
LABEL = "io.jindo.p1.fixture-owner"
MANIFEST = "harness.json"
_NAMES = re.compile(r"jindo-p1-[a-f0-9]{16}-(?:control|browser-net|direct|upstream|link|browser|mcp|proxy|target|driver|positive-b04|positive-b05|positive-b12|positive-b13|positive-b14)\Z")
# 망 역할 순서가 --subnet-base 블록 안 /24 배정 순서다. link는 Chromium과 MCP만 붙는 CDP 연결망이다(계약 §9-1 분리 배치).
NETWORKS = ("control", "browser", "direct", "upstream", "link")
SUBNET_BASE_PREFIX = 21
MCP_PORT = 8931
CDP_PORT = 9222
PROXY_PORT = 8080
# Docker 29에서 internal 망 컨테이너에 loopback publish를 요청하면(게시는 생기지 않음) docker exec 하나가 약 3초 걸린다(실측).
# MCP 컨테이너의 readiness·대조 probe는 이 지연을 넘는 exec 제한을 쓴다. probe 자체의 연결 제한(1.2초)은 그대로다.
PROBE_EXEC_TIMEOUT = 10
_IMAGE_ID = re.compile(r"sha256:[a-f0-9]{64}\Z")


class HarnessError(RuntimeError):
    """고정 사유 코드만 담는다."""


def command(argv, *, timeout=45, env=None, capture=True, check=True):
    try:
        result = subprocess.run(argv, cwd=ROOT, env=env, timeout=timeout, stdin=subprocess.DEVNULL,
                                stdout=subprocess.PIPE if capture else subprocess.DEVNULL,
                                stderr=subprocess.DEVNULL, check=False)
    except (OSError, subprocess.TimeoutExpired):
        raise HarnessError("fixture_command_unavailable_or_timed_out") from None
    if check and result.returncode:
        raise HarnessError("fixture_command_failed")
    if capture and len(result.stdout) > 2 << 20:
        raise HarnessError("fixture_command_output_exceeded")
    return result


def docker(*argv, **kwargs):
    return command(["docker", *argv], **kwargs)


def private_directory(path, *, new=False):
    path = Path(path).absolute()
    if path.is_symlink() or not path.name.startswith("jindo-p1-"):
        raise HarnessError("fixture_directory_invalid")
    if new:
        try:
            path.mkdir(mode=0o700)
        except OSError:
            raise HarnessError("fixture_directory_must_be_new") from None
    try:
        info = path.stat()
    except OSError:
        raise HarnessError("fixture_directory_unavailable") from None
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o700 or path.resolve() != path:
        raise HarnessError("fixture_directory_not_private")
    return path


def write(path, value, mode=0o600):
    raw = value if isinstance(value, bytes) else json.dumps(value, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    with os.fdopen(fd, "wb") as stream:
        stream.write(raw)


def save(run, data):
    # 하네스 내부 고정 파일만 교체한다. Docker argv나 arbitrary path를 manifest에서 받지 않는다.
    path = run / MANIFEST
    temp = run / "harness-next.json"
    write(temp, data)
    temp.replace(path)


def load(run):
    run = private_directory(run)
    path = run / MANIFEST
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077 or info.st_size > 65536:
            raise HarnessError("fixture_manifest_untrusted")
        data = json.load(stream)
    if not isinstance(data, dict) or data.get("format") != FORMAT or not re.fullmatch(r"[a-f0-9]{16}", data.get("owner", "")) or not _IMAGE_ID.fullmatch(data.get("image_id", "")):
        raise HarnessError("fixture_manifest_invalid")
    for group in ("containers", "networks"):
        if not isinstance(data.get(group), dict): raise HarnessError("fixture_manifest_invalid")
        for value in data[group].values():
            if not isinstance(value, dict) or not _NAMES.fullmatch(value.get("name", "")) or not value["name"].startswith("jindo-p1-" + data["owner"] + "-") or not re.fullmatch(r"[a-f0-9]{64}", value.get("id", "")):
                raise HarnessError("fixture_manifest_invalid")
    return run, data


def inspect_owned(data, group, role):
    entry = data[group][role]
    kind = "container" if group == "containers" else "network"
    result = docker(kind, "inspect", entry["id"], check=False)
    if result.returncode: return None
    try:
        item = json.loads(result.stdout)[0]
        labels = ((item.get("Config") or {}).get("Labels") or {}) if kind == "container" else (item.get("Labels") or {})
        name = item["Name"].lstrip("/")
        if item["Id"] != entry["id"] or name != entry["name"] or labels.get(LABEL) != data["owner"]:
            raise HarnessError("fixture_resource_ownership_mismatch")
        return item
    except (KeyError, TypeError, ValueError, IndexError):
        raise HarnessError("fixture_resource_inspect_invalid") from None


def stop_browser(data):
    # MCP를 먼저 멈춰 CDP 재연결 없이 Chromium을 닫는다. 둘 다 소유 ID·이름·label을 대조한다.
    for role in ("mcp", "browser"):
        item = inspect_owned(data, "containers", role)
        if item is not None and item["State"]["Running"]:
            docker("stop", "--time", "5", item["Id"], timeout=15)


def down(run):
    run, data = load(run)
    removed = {"containers": 0, "networks": 0}
    for role in ("driver", "positive_b04", "positive_b05", "positive_b12", "positive_b13", "positive_b14", "mcp", "browser", "proxy", "target"):
        if role not in data["containers"]: continue
        item = inspect_owned(data, "containers", role)
        if item is not None:
            if item["State"]["Running"]:
                docker("stop", "--time", "5", item["Id"], timeout=15)
            docker("rm", item["Id"], timeout=20)
            removed["containers"] += 1
    for role in NETWORKS:
        if role not in data["networks"]: continue
        item = inspect_owned(data, "networks", role)
        if item is not None:
            docker("network", "rm", item["Id"])
            removed["networks"] += 1
    remaining = sum(inspect_owned(data, group, role) is not None for group in ("containers", "networks") for role in data[group])
    return {"format": FORMAT, "role": "contract_fixture", "status": "CLEAN" if remaining == 0 else "FAIL",
            "removed": removed, "remaining_owned_resources": remaining, "product_boundary": "NOT_RUN"}


def certificates(run):
    tls = run / "tls"
    tls.mkdir(mode=0o700)
    ca_key, ca = tls / "ca.key", tls / "boundary-ca.pem"
    leaf_key, request, leaf = tls / "leaf.key", tls / "leaf.csr", tls / "leaf.pem"
    command(["openssl", "genpkey", "-algorithm", "EC", "-pkeyopt", "ec_paramgen_curve:P-256", "-out", str(ca_key)])
    command(["openssl", "req", "-new", "-x509", "-key", str(ca_key), "-out", str(ca), "-days", "2", "-subj", "/CN=JinDo owned fixture CA",
             "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign,cRLSign"])
    command(["openssl", "genpkey", "-algorithm", "EC", "-pkeyopt", "ec_paramgen_curve:P-256", "-out", str(leaf_key)])
    command(["openssl", "req", "-new", "-key", str(leaf_key), "-out", str(request), "-subj", "/CN=target.test"])
    ext = tls / "leaf.ext"
    write(ext, b"basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:*.test,DNS:target.test,DNS:cdn.test,DNS:receiver.test,DNS:metadata.test\n")
    command(["openssl", "x509", "-req", "-in", str(request), "-CA", str(ca), "-CAkey", str(ca_key), "-set_serial", str(secrets.randbits(120) + 1), "-out", str(leaf), "-days", "2", "-extfile", str(ext)])
    for path in (ca_key, leaf_key, request, leaf, ca): path.chmod(0o600)
    ca.chmod(0o444)  # 공개 인증서 하나만 브라우저에 전달한다.
    ca_key.unlink(); request.unlink(); ext.unlink()
    return hashlib.sha256(ssl.PEM_cert_to_DER_cert(ca.read_text())).hexdigest()


def _mount(source, target, *, readonly=True):
    if "," in str(source): raise HarnessError("fixture_mount_path_invalid")
    return ["--mount", "type=bind,src=" + str(source) + ",dst=" + target + (",readonly" if readonly else "")]


def up(args):
    run = private_directory(args.run_dir, new=True)
    data = {"format": FORMAT, "owner": secrets.token_hex(8), "containers": {}, "networks": {}}
    try:
        info = json.loads(docker("image", "inspect", IMAGE).stdout)[0]
        if info["Os"] != "linux" or info["Architecture"] not in ("amd64", "arm64") or not _IMAGE_ID.fullmatch(info["Id"]):
            raise HarnessError("fixture_image_invalid")
        data["image_id"] = info["Id"]
        save(run, data)
        block = ipaddress.IPv4Network(args.subnet_base)
        if block.prefixlen != SUBNET_BASE_PREFIX or not block.is_private or block.is_loopback or block.is_link_local:
            raise HarnessError("fixture_subnet_invalid")
        subnets = dict(zip(NETWORKS, block.subnets(new_prefix=24)))
        existing_ids = docker("network", "ls", "-q").stdout.decode().split()
        if existing_ids:
            for network in json.loads(docker("network", "inspect", *existing_ids).stdout):
                for ipam in (network.get("IPAM", {}).get("Config") or []):
                    if ipam.get("Subnet"):
                        subnet = ipaddress.ip_network(ipam["Subnet"])
                        if subnet.version == 4 and block.overlaps(subnet): raise HarnessError("fixture_subnet_in_use")
        ip = lambda net, host: str(subnets[net].network_address + host)
        for role, subnet in subnets.items():
            suffix = "browser-net" if role == "browser" else role
            name = "jindo-p1-" + data["owner"] + "-" + suffix
            ident = docker("network", "create", "--internal", "--subnet", str(subnet), "--label", LABEL + "=" + data["owner"], name).stdout.decode().strip()
            data["networks"][role] = {"name": name, "id": ident}
            save(run, data)
        for name in ("bin", "control", "target-data"): (run / name).mkdir(mode=0o700)
        env = {k: v for k, v in os.environ.items() if k not in ("GOFLAGS", "GOTOOLCHAIN", "GOPROXY", "GOSUMDB")}
        env["CGO_ENABLED"] = "0"; env["GOOS"] = "linux"; env["GOARCH"] = info["Architecture"]; env["GOTOOLCHAIN"] = "local"; env["GOPROXY"] = "off"
        for role, package in (("target", "./cmd/browser-fixture"), ("proxy", "./cmd/exploration-contract-fixture")):
            command(["go", "build", "-trimpath", "-o", str(run / "bin" / role), package], timeout=120, env=env)
        metadata = Path(args.metadata_script).absolute()
        if metadata.is_symlink() or not metadata.is_file() or metadata.stat().st_size > 131072:
            raise HarnessError("fixture_metadata_script_invalid")
        metadata_raw = metadata.read_bytes()
        write(run / "metadata_only.js", metadata_raw, 0o444)
        ca_hash = certificates(run)
        config = {"format": "jindo.exploration-contract-fixture/v1", "owned_fixture": True,
                  "browser_listen": ip("browser", 10) + ":" + str(PROXY_PORT), "direct_listen": ip("direct", 10) + ":" + str(PROXY_PORT),
                  "control_socket": "/run-fixture/boundary.sock", "target_origin": "https://target.test:8443",
                  "target_address": ip("upstream", 20) + ":8443", "cdn_origin": "https://cdn.test:8444",
                  "cdn_address": ip("upstream", 20) + ":8444", "target_prefixes": ["/"],
                  "max_requests": args.max_requests, "max_external_requests": 20 if args.mode == "external_allowed" else 0, "max_response_bytes": 2 << 20, "duration_seconds": args.duration,
                  "max_concurrency": 16, "ca_cert_file": "/certs/ca.pem", "leaf_cert_file": "/certs/leaf.pem", "leaf_key_file": "/certs/leaf.key"}
        write(run / "proxy.json", config)
        common = ["--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "256", "--memory", "1g", "--cpus", "2", "--log-driver", "none"]
        certmounts = _mount(run / "tls/boundary-ca.pem", "/certs/ca.pem") + _mount(run / "tls/leaf.pem", "/certs/leaf.pem") + _mount(run / "tls/leaf.key", "/certs/leaf.key")
        def create(role, primary, host, mounts, entrypoint, argv, *, extras=()):
            name = "jindo-p1-" + data["owner"] + "-" + role
            cmd = ["create", "--name", name, "--label", LABEL + "=" + data["owner"], "--network", data["networks"][primary]["name"], "--ip", ip(primary, host), *common,
                   "--user", "1000:1000" if role in ("browser", "mcp") else f"{os.geteuid()}:{os.getegid()}", *mounts, *extras]
            if entrypoint: cmd.extend(["--entrypoint", entrypoint])
            ident = docker(*cmd, data["image_id"], *argv).stdout.decode().strip()
            data["containers"][role] = {"name": name, "id": ident}; save(run, data)
            return ident
        create("target", "upstream", 20, certmounts + _mount(run / "bin/target", "/app/fixture") + _mount(run / "target-data", "/fixture-data", readonly=False), "/app/fixture",
               ["-listen", "0.0.0.0:8443", "-cdn-listen", "0.0.0.0:8444", "-cdn-origin", config["cdn_origin"], "-receiver-listen", "0.0.0.0:8445", "-receiver-origin", "https://receiver.test:8445", "-receiver-udp", "0.0.0.0:8447", "-metadata-listen", "0.0.0.0:8446", "-metadata-origin", "https://metadata.test:8446", "-tls-cert", "/certs/leaf.pem", "-tls-key", "/certs/leaf.key", "-log", "/fixture-data/reaches.jsonl"])
        proxy = create("proxy", "browser", 10, certmounts + _mount(run / "bin/proxy", "/app/fixture") + _mount(run / "proxy.json", "/fixture-config.json") + _mount(run / "control", "/run-fixture", readonly=False), "/app/fixture", ["/fixture-config.json"])
        for role in ("direct", "upstream"): docker("network", "connect", "--ip", ip(role, 10), data["networks"][role]["name"], proxy)
        # 계약 §9-1 분리 배치: Chromium은 브라우저 입구망과 CDP 연결망에만, MCP는 제어망과 CDP 연결망에만 붙는다.
        # 같은 namespace의 native 자식이 MCP 제어 포트에 닿던 B16을 namespace 분리로 막는다.
        browser_proxy = "http://" + ip("browser", 10) + ":" + str(PROXY_PORT)
        cdp_listen = ip("link", 20) + ":" + str(CDP_PORT)
        browser = create("browser", "browser", 20, _mount(run / "tls/boundary-ca.pem", "/run/explore-ca/boundary-ca.pem"), "/app/explore-browser-entrypoint",
               ["--proxy-server", browser_proxy, "--cdp-listen", cdp_listen],
               extras=["--tmpfs", "/home/node:rw,nosuid,size=128m,uid=1000,gid=1000", "--tmpfs", "/tmp:rw,nosuid,size=256m,uid=1000,gid=1000", "--shm-size", "256m"])
        docker("network", "connect", "--ip", ip("link", 20), data["networks"]["link"]["name"], browser)
        mcp_listen = ip("control", 20) + ":" + str(MCP_PORT)
        mcp = create("mcp", "control", 20, [], "/app/explore-mcp-entrypoint",
               ["--headless", "--isolated", "--no-webmcp", "--browser", "chromium", "--proxy-server", browser_proxy, "--cdp-endpoint", "http://" + cdp_listen,
                "--output-dir", "/tmp/explore-output", "--host", ip("control", 20), "--port", str(MCP_PORT), "--allowed-hosts", mcp_listen],
               extras=["--publish", "127.0.0.1::" + str(MCP_PORT), "--tmpfs", "/home/node:rw,nosuid,size=64m,uid=1000,gid=1000", "--tmpfs", "/tmp:rw,nosuid,size=128m,uid=1000,gid=1000"])
        docker("network", "connect", "--ip", ip("link", 30), data["networks"]["link"]["name"], mcp)
        for role in ("target", "proxy", "browser", "mcp"): docker("start", data["containers"][role]["id"])
        version_probe = "process.stdout.write(require('/usr/local/lib/node_modules/@playwright/mcp/package.json').version)"
        if docker("exec", mcp, "node", "-e", version_probe).stdout != b"0.0.84":
            raise HarnessError("fixture_mcp_version_mismatch")
        actual = inspect_owned(data, "containers", "mcp")
        published = actual["NetworkSettings"]["Ports"].get(str(MCP_PORT) + "/tcp") or []
        if any(port["HostIp"] != "127.0.0.1" for port in published): raise HarnessError("fixture_publish_invalid")
        loopback_url = "http://127.0.0.1:" + published[0]["HostPort"] + "/mcp" if published else None
        context = {"format": FORMAT, "role": "contract_fixture", "fixture_socket": "/run-fixture/boundary.sock", "browser_socket": "/run-fixture/browser-control.sock",
                   "mcp_url": "http://" + mcp_listen + "/mcp", "target_origin": config["target_origin"], "cdn_origin": config["cdn_origin"],
                   "direct_proxy": "http://" + ip("direct", 10) + ":" + str(PROXY_PORT), "browser_proxy": browser_proxy, "browser_layout": "chromium_mcp_split_cdp",
                   "ca_file": "/public-ca/boundary-ca.pem", "mode": args.mode, "write_mode": args.write_mode,
                   "limits": {"time_budget_seconds": args.duration, "max_requests": args.max_requests,
                              "max_external_requests": 20 if args.mode == "external_allowed" else 0,
                              "max_concurrency": 16, "max_response_bytes": 2 << 20},
                   "result_file": "/run-fixture/result.json", "browser_name": data["containers"]["browser"]["name"], "mcp_name": data["containers"]["mcp"]["name"],
                   "host_run_dir": str(run), "host_loopback_url": loopback_url,
                   "host_publish": "AVAILABLE" if published else "NOT_SUPPORTED_ON_INTERNAL_NETWORK",
                   "metadata_script": "/metadata/metadata_only.js", "metadata_script_sha256": hashlib.sha256(metadata_raw).hexdigest(),
                   "ca_sha256": ca_hash, "image_id": data["image_id"], "mcp_version": "0.0.84",
                   "proxy_binary_sha256": hashlib.sha256((run / "bin/proxy").read_bytes()).hexdigest(),
                   "target_binary_sha256": hashlib.sha256((run / "bin/target").read_bytes()).hexdigest(), "product_boundary": "NOT_RUN"}
        write(run / "control/context.json", context)
        data["context"] = context; data["driver_ip"] = {"control": ip("control", 30), "direct": ip("direct", 20)}; save(run, data)
        deadline = time.monotonic() + 30
        # host-owned readiness probe다. 모델 경로가 아니며 본문은 버린다. MCP 컨테이너에서 Chromium CDP 중계와
        # 자기 제어망 MCP 리슨을 차례로 확인한다(host publisher가 없는 internal Docker에서도 동작).
        cdp_probe = "const h=require('node:http');const q=h.get(" + json.dumps("http://" + cdp_listen + "/json/version") + ",r=>{r.resume();process.exit(r.statusCode===200?0:1)});q.on('error',()=>process.exit(1));q.setTimeout(700,()=>{q.destroy();process.exit(1)});"
        probe = "const h=require('node:http');const q=h.get(" + json.dumps(context["mcp_url"]) + ",r=>{r.resume();process.exit([400,405,406].includes(r.statusCode)?0:1)});q.on('error',()=>process.exit(1));q.setTimeout(700,()=>{q.destroy();process.exit(1)});"
        while time.monotonic() < deadline:
            if not all(inspect_owned(data, "containers", role)["State"]["Running"] for role in ("target", "proxy", "browser", "mcp")):
                raise HarnessError("fixture_container_exited")
            if (run / "control/boundary.sock").exists():
                if (docker("exec", mcp, "node", "-e", cdp_probe, timeout=PROBE_EXEC_TIMEOUT, capture=False, check=False).returncode == 0
                        and docker("exec", mcp, "node", "-e", probe, timeout=PROBE_EXEC_TIMEOUT, capture=False, check=False).returncode == 0):
                    process_layout(data)
                    return context | {"status": "READY"}
            time.sleep(0.2)
        raise HarnessError("fixture_readiness_timeout")
    except Exception:
        if "image_id" in data:
            try: down(run)
            except Exception: pass
        raise


def process_layout(data):
    # 분리 배치가 실제 프로세스로도 지켜지는지 본다. Chromium 컨테이너에 MCP가, MCP 컨테이너에 Chromium이 없어야 한다.
    # docker top 원문(명령행)은 출력하지 않고 고정 표식 유무만 본다.
    for role, forbidden, required in (("browser", ("playwright-mcp", "@playwright/mcp"), "/ms-playwright/"),
                                      ("mcp", ("/ms-playwright/",), "playwright-mcp")):
        item = inspect_owned(data, "containers", role)
        if item is None or not item["State"]["Running"]: raise HarnessError("fixture_process_layout_invalid")
        # docker top은 PID 열이 있어야 컨테이너 프로세스만 고른다.
        listing = docker("top", item["Id"], "-o", "pid,args").stdout.decode(errors="replace")
        if required not in listing or any(marker in listing for marker in forbidden):
            raise HarnessError("fixture_process_layout_invalid")


class LeaseServer(http.server.HTTPServer):
    address_family = socket.AF_UNIX
    def server_bind(self):
        self.socket.bind(self.server_address)
        self.server_name, self.server_port = "owned-fixture", 0
    def get_request(self):
        conn, _ = self.socket.accept()
        return conn, ("owned-unix-client", 0)


_SURFACES = ("receiver", "receiver_conn", "receiver_metadata", "receiver_conn_metadata", "receiver_udp", "target_write", "receiver_cdn", "receiver_conn_cdn")
_FORBIDDEN = _SURFACES[:6]


def receiver_counts(run):
    fd = os.open(run / "target-data/reaches.jsonl", os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_size > 2 << 20:
            raise HarnessError("fixture_receiver_log_untrusted")
        raw = stream.read()
    if raw and not raw.endswith(b"\n"): raise HarnessError("fixture_receiver_log_incomplete")
    counts = dict.fromkeys(_SURFACES, 0)
    for index, line in enumerate(raw.splitlines(), 1):
        row = json.loads(line)
        if (not isinstance(row, dict) or set(row) - {"seq", "surface", "scenario", "method", "upgrade"}
                or type(row.get("seq")) is not int or row["seq"] != index or row.get("surface") not in counts):
            raise HarnessError("fixture_receiver_log_invalid")
        counts[row["surface"]] += 1
    return counts


def deltas(before, after):
    result = {key: after[key] - before[key] for key in _SURFACES}
    if any(value < 0 for value in result.values()): raise HarnessError("fixture_receiver_log_regressed")
    return result


def owned_ip(data, role, network):
    item = inspect_owned(data, "containers", role)
    if item is None or not item["State"]["Running"]: raise HarnessError("fixture_probe_container_unavailable")
    ip = item["NetworkSettings"]["Networks"][data["networks"][network]["name"]]["IPAddress"]
    if not ipaddress.ip_address(ip).is_private: raise HarnessError("fixture_probe_address_invalid")
    return item["Id"], ip


def tcp_probe(container, address, port, *, allowed):
    # URI·호스트명을 받지 않고 inspect로 확인한 소유 컨테이너 주소만 사용한다.
    code = ("const n=require('node:net');const s=n.connect(" + json.dumps({"host": address, "port": port}) + ");"
            "s.on('connect',()=>{s.destroy();process.exit(" + ("0" if allowed else "10") + ")});"
            "s.on('error',()=>process.exit(" + ("11" if allowed else "0") + "));"
            "s.setTimeout(1200,()=>{s.destroy();process.exit(" + ("12" if allowed else "0") + ")});")
    return docker("exec", container, "node", "-e", code, timeout=4, capture=False, check=False).returncode == 0


_REACH = {0: "connected", 20: "refused", 21: "unreachable", 22: "timeout", 23: "other_error", 24: "sent"}


def reach_probe(container, address, port, transport):
    # tcp_probe처럼 inspect로 확인한 소유 컨테이너 주소만 쓰고, 결과는 고정 분류 이름만 돌려준다.
    # refused(상대 namespace 도달, 리슨 없음)와 unreachable(경로 없음)을 나눠야 "망 자체에 닿지 않음"을 말할 수 있다.
    target = json.dumps({"host": address, "port": port})
    unreachable = "['ENETUNREACH','EHOSTUNREACH'].includes(e.code)"
    if transport == "tcp":
        code = ("const n=require('node:net'),t=" + target + ";const s=n.connect(t);"
                "s.on('connect',()=>{s.destroy();process.exit(0)});"
                "s.on('error',e=>process.exit(e.code==='ECONNREFUSED'?20:" + unreachable + "?21:23));"
                "s.setTimeout(1200,()=>{s.destroy();process.exit(22)});")
    elif transport == "udp":
        # UDP는 도달 여부를 응답으로 알 수 없다. 경로가 없으면 송신 자체가 ENETUNREACH로 실패한다.
        code = ("const d=require('node:dgram').createSocket('udp4'),t=" + target + ";"
                "d.send(Buffer.from('jindo-b16'),t.port,t.host,e=>{d.close();process.exit(!e?24:" + unreachable + "?21:23)});")
    else:
        raise HarnessError("fixture_probe_transport_invalid")
    result = docker("exec", container, "node", "-e", code, timeout=PROBE_EXEC_TIMEOUT, capture=False, check=False)
    return _REACH.get(result.returncode, "probe_error")


def b16_probes(run, data):
    """B16 분리 배치 대조. Chromium namespace는 MCP 제어 포트·엔진 제어망에, driver/직접 namespace는 CDP에 닿지 않아야 한다.

    양성 두 개(MCP→CDP, driver→MCP)는 같은 probe가 실제 연결을 잡는다는 대조다. 결과는 사적 실행 디렉터리에 남긴다.
    """
    chromium, chromium_ingress = owned_ip(data, "browser", "browser")
    _, chromium_link = owned_ip(data, "browser", "link")
    mcp, mcp_control = owned_ip(data, "mcp", "control")
    _, mcp_link = owned_ip(data, "mcp", "link")
    driver, driver_control = owned_ip(data, "driver", "control")
    proxy, proxy_browser = owned_ip(data, "proxy", "browser")
    no_route, no_connection, connected = ("unreachable",), ("refused", "unreachable", "timeout"), ("connected",)
    # unreachable은 그 망으로 가는 경로 자체가 없다는 뜻이라 포트와 무관하다. 리슨이 없는 driver(엔진 역할)도 그래서 MCP_PORT로 본다.
    plan = (
        ("chromium_to_mcp_control_tcp", "browser", chromium, "mcp.control", mcp_control, MCP_PORT, "tcp", no_route),
        ("chromium_to_mcp_control_udp", "browser", chromium, "mcp.control", mcp_control, MCP_PORT, "udp", no_route),
        ("chromium_to_mcp_link_tcp", "browser", chromium, "mcp.link", mcp_link, MCP_PORT, "tcp", no_connection),
        ("chromium_to_engine_control_tcp", "browser", chromium, "driver.control", driver_control, MCP_PORT, "tcp", no_route),
        ("driver_to_chromium_cdp_tcp", "driver", driver, "browser.link", chromium_link, CDP_PORT, "tcp", no_route),
        ("boundary_to_chromium_ingress_cdp_tcp", "proxy", proxy, "browser.browser", chromium_ingress, CDP_PORT, "tcp", no_connection),
        ("mcp_to_boundary_browser_ingress_tcp", "mcp", mcp, "proxy.browser", proxy_browser, PROXY_PORT, "tcp", no_route),
        ("mcp_to_chromium_cdp_tcp", "mcp", mcp, "browser.link", chromium_link, CDP_PORT, "tcp", connected),
        ("driver_to_mcp_control_tcp", "driver", driver, "mcp.control", mcp_control, MCP_PORT, "tcp", connected),
    )
    result = {}
    for name, source, container, label, address, port, transport, expected in plan:
        observed = reach_probe(container, address, port, transport)
        result[name] = {"from": source, "to": label + ":" + str(port) + "/" + transport, "expected": list(expected),
                        "observed": observed, "status": "PASS" if observed in expected else "FAIL"}
    write(run / "b16-probes.json", result)
    if any(row["status"] != "PASS" for row in result.values()): raise HarnessError("fixture_b16_probe_failed")
    return result


def positive_receiver_probe(run, data):
    container, ip = owned_ip(data, "target", "upstream")
    before = receiver_counts(run)
    # 공개 CA를 명시하고 TLS 오류를 무시하지 않는다.
    code = ("const h=require('node:https'),f=require('node:fs');const o=" + json.dumps({"host": ip, "port": 8445, "servername": "receiver.test", "path": "/hit/B04-fetch", "agent": False}) + ";o.ca=f.readFileSync('/certs/ca.pem');"
            "const q=h.get(o,r=>{r.resume();r.on('end',()=>process.exit(r.statusCode>=200&&r.statusCode<300?0:1))});q.on('error',()=>process.exit(1));q.setTimeout(1500,()=>{q.destroy();process.exit(1)});")
    if docker("exec", container, "node", "-e", code, timeout=4, capture=False, check=False).returncode != 0 or not tcp_probe(container, ip, 8445, allowed=True):
        raise HarnessError("fixture_receiver_positive_probe_failed")
    until = time.monotonic() + 2
    while True:
        after = receiver_counts(run)
        delta = deltas(before, after)
        if delta["receiver"] == 1 and delta["receiver_conn"] == 2: return delta, after
        if time.monotonic() > until: raise HarnessError("fixture_receiver_positive_record_missing")
        time.sleep(0.05)


def negative_namespace_probes(data):
    browser, _ = owned_ip(data, "browser", "browser")
    direct, _ = owned_ip(data, "driver", "direct")
    _, target = owned_ip(data, "target", "upstream")
    _, direct_proxy = owned_ip(data, "proxy", "direct")
    result = {}
    for name, container, address, port in (("browser_target_tcp_blocked", browser, target, 8443),
          ("browser_receiver_tcp_blocked", browser, target, 8445), ("browser_direct_proxy_tcp_blocked", browser, direct_proxy, PROXY_PORT),
          ("driver_target_tcp_blocked", direct, target, 8443), ("driver_receiver_tcp_blocked", direct, target, 8445)):
        result[name] = tcp_probe(container, address, port, allowed=False)
    if not all(result.values()): raise HarnessError("fixture_namespace_negative_probe_failed")
    return result


_POSITIVE_JS = r"""
const fs=require('node:fs'),net=require('node:net');
const cases={B04:'/bypass/B04',B05:'/bypass/B05',B12:'/bypass/B12',B13:'/bypass/B13',B14:'/bypass/B14'};
const scenario=process.argv[2],ip=process.argv[3];
if(!Object.hasOwn(cases,scenario)||net.isIP(ip)!==4)process.exit(2);
const {chromium}=require(require.resolve('playwright',{paths:['/usr/local/lib/node_modules/@playwright/mcp']}));
(async()=>{
 let browser;const summary={scenario,browser_started:false,navigation_completed:false,browser_closed:false};
 try {
  const hosts=['target.test','receiver.test','metadata.test','cdn.test'].map(x=>'MAP '+x+' '+ip).join(', ');
  browser=await chromium.launch({headless:true,args:['--no-proxy-server','--host-resolver-rules='+hosts]});
  summary.browser_started=true;
  const context=await browser.newContext();
  const page=await context.newPage();
  try {await page.goto('https://target.test:8443'+cases[scenario],{waitUntil:'domcontentloaded',timeout:10000});summary.navigation_completed=true;}catch(_){summary.navigation_completed=false;}
  await page.waitForTimeout(1800);
 } catch(_) {summary.failure='chromium_fixture_unavailable';}
 finally {if(browser){try{await browser.close();summary.browser_closed=true;}catch(_){summary.failure='chromium_fixture_close_failed';}}}
 fs.writeFileSync('/positive-results/'+scenario+'.json',JSON.stringify(summary),{mode:0o600});
 process.exit(summary.browser_started&&summary.browser_closed?0:1);
})().catch(()=>process.exit(1));
"""


def positive_chromium(run, data):
    # 경계 없는 Chromium을 upstream-only namespace에 둔다. 모델이 실행 코드를 공급하지 않는다.
    _, target_ip = owned_ip(data, "target", "upstream")
    script = run / "positive-control.js"
    write(script, _POSITIVE_JS.encode(), 0o444)
    results = run / "positive-results"
    results.mkdir(mode=0o700)
    reports = {}
    required = {
        "B04": [("receiver", "B04-" + v) for v in ("img", "script", "fetch")] + [("receiver_metadata", "B04-md-" + v) for v in ("img", "script", "fetch")],
        "B05": [("receiver", "B05-" + v) for v in ("frame", "fetch")] + [("receiver_metadata", "B05-md-" + v) for v in ("frame", "fetch")],
        "B12": [("receiver", "B12-prefetch")],
        "B13": [("receiver", "B13-blob"), ("receiver", "B13-data"), ("receiver_metadata", "B13-md-blob")],
        "B14": [("target_write", "B14-" + v) for v in ("form", "fetch", "beacon")],
    }
    def scenario_rows(scenario):
        # receiver_counts가 같은 보호 파일의 크기·행 모양·마감을 먼저 검증한다.
        raw = (run / "target-data/reaches.jsonl").read_bytes()
        rows = [json.loads(line) for line in raw.splitlines()]
        return {surface + ":" + name: sum(r.get("surface") == surface and r.get("scenario") == name for r in rows)
                for surface, name in required[scenario]}
    for index, scenario in enumerate(("B04", "B05", "B12", "B13", "B14"), 30):
        role = "positive_" + scenario.lower()
        name = "jindo-p1-" + data["owner"] + "-positive-" + scenario.lower()
        before = receiver_counts(run); variants_before = scenario_rows(scenario)
        trust = 'set -eu; mkdir -p "$HOME/.pki/nssdb"; certutil -d "sql:$HOME/.pki/nssdb" -N --empty-password; certutil -d "sql:$HOME/.pki/nssdb" -A -t "C,," -n owned-fixture -i /run/explore-ca/boundary-ca.pem; exec node /app/positive.js "$@"'
        ident = docker("create", "--name", name, "--label", LABEL + "=" + data["owner"], "--network", data["networks"]["upstream"]["name"],
            "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "256", "--memory", "1g", "--cpus", "2", "--user", f"{os.geteuid()}:{os.getegid()}", "--log-driver", "none",
            *_mount(run / "tls/boundary-ca.pem", "/run/explore-ca/boundary-ca.pem"), *_mount(script, "/app/positive.js"), *_mount(results, "/positive-results", readonly=False),
            "--tmpfs", f"/home/node:rw,nosuid,size=128m,uid={os.geteuid()},gid={os.getegid()}", "--tmpfs", "/tmp:rw,nosuid,size=128m", "--shm-size", "256m",
            "--env", "NODE_EXTRA_CA_CERTS=/run/explore-ca/boundary-ca.pem", "--entrypoint", "/bin/sh", data["image_id"], "-c", trust, "positive-chromium", scenario, target_ip).stdout.decode().strip()
        data["containers"][role] = {"name": name, "id": ident}; save(run, data)
        fd = os.open(run / ("positive-" + scenario.lower() + ".log"), os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "wb") as output:
            try:
                subprocess.run(["docker", "start", "--attach", ident], cwd=ROOT, stdin=subprocess.DEVNULL, stdout=output, stderr=output, timeout=25, check=False)
            except subprocess.TimeoutExpired:
                item = inspect_owned(data, "containers", role)
                if item is not None and item["State"]["Running"]: docker("stop", "--time", "2", ident, timeout=7)
        item = inspect_owned(data, "containers", role)
        after = receiver_counts(run); delta = deltas(before, after)
        variants_after = scenario_rows(scenario)
        variants = {key: variants_after[key] - variants_before[key] for key in variants_before}
        browser = {"browser_started": False, "navigation_completed": False, "browser_closed": False}
        path = results / (scenario + ".json")
        if path.is_file() and not path.is_symlink() and path.stat().st_size < 4096:
            raw = json.loads(path.read_bytes())
            if raw.get("scenario") != scenario or any(type(raw.get(k)) is not bool for k in browser): raise HarnessError("fixture_positive_browser_result_invalid")
            browser = {k: raw[k] for k in browser}
        passed = browser["browser_started"] and browser["browser_closed"] and item is not None and not item["State"]["Running"] and item["State"]["ExitCode"] == 0 and all(value > 0 for value in variants.values())
        if scenario in ("B04", "B05", "B13"):
            passed = passed and delta["receiver"] > 0 and delta["receiver_metadata"] > 0
        elif scenario == "B12":
            passed = passed and delta["receiver_conn"] > 0 and delta["receiver_conn_metadata"] > 0
        else:
            passed = passed and all(value > 0 for value in variants.values())
        reports[scenario] = {"status": "PASS" if passed else "UNDECIDABLE", "reason": "uncontrolled_chromium_reached" if passed else "browser_positive_control_unproven",
                             **browser, "receiver_delta": delta, "variant_counts": variants}
    return reports


def driver(args):
    run, data = load(args.run_dir)
    if "context" not in data or "driver" in data["containers"]: raise HarnessError("fixture_driver_state_invalid")
    binary = Path(args.binary).absolute()
    if binary.is_symlink() or not binary.is_file(): raise HarnessError("fixture_driver_binary_invalid")
    if (run / "control/result.json").exists(): raise HarnessError("fixture_driver_result_already_exists")
    socket_path = run / "control/browser-control.sock"
    closed = threading.Event()
    failed = threading.Event()
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            if self.path != "/close": self.send_error(404); return
            try:
                stop_browser(data); closed.set()
                self.send_response(200); self.end_headers(); self.wfile.write(b'{"status":"closed"}\n')
            except Exception:
                failed.set(); self.send_error(503)
        def do_GET(self): self.send_error(405)
        def log_message(self, *args): pass
    server = LeaseServer(str(socket_path), Handler)
    socket_path.chmod(0o600)
    thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
    try:
        name = "jindo-p1-" + data["owner"] + "-driver"
        argv = ["create", "--name", name, "--label", LABEL + "=" + data["owner"], "--network", data["networks"]["control"]["name"], "--ip", data["driver_ip"]["control"],
                "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--memory", "512m", "--log-driver", "none", "--user", f"{os.geteuid()}:{os.getegid()}",
                *_mount(binary, "/app/fixture-driver"), *_mount(run / "tls/boundary-ca.pem", "/public-ca/boundary-ca.pem"), *_mount(run / "metadata_only.js", "/metadata/metadata_only.js"), *_mount(run / "control", "/run-fixture", readonly=False),
                "--tmpfs", "/tmp:rw,nosuid,size=64m", "--env", "JINDO_P1_FIXTURE_CONTEXT=/run-fixture/context.json", "--entrypoint", "/bin/sh", data["image_id"], "-c", 'while [ ! -e /run-fixture/start-driver ]; do sleep 0.1; done; exec /app/fixture-driver "$@"', "jindo-driver", *args.driver_arg]
        ident = docker(*argv).stdout.decode().strip()
        data["containers"]["driver"] = {"name": name, "id": ident}
        data["driver_binary_sha256"] = hashlib.sha256(binary.read_bytes()).hexdigest(); save(run, data)
        docker("network", "connect", "--ip", data["driver_ip"]["direct"], data["networks"]["direct"]["name"], ident)
        chromium_controls = positive_chromium(run, data)
        positive, positive_baseline = positive_receiver_probe(run, data)
        # attach를 먼저 열어 gate 뒤 실행되는 실제 Go PID1의 출력만 보호 파일에 보존한다.
        fd = os.open(run / "driver-output.log", os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        try:
            with os.fdopen(fd, "wb") as output:
                process = subprocess.Popen(["docker", "start", "--attach", ident], cwd=ROOT, stdin=subprocess.DEVNULL, stdout=output, stderr=output)
                until = time.monotonic() + 10
                while True:
                    item = inspect_owned(data, "containers", "driver")
                    if item is not None and item["State"]["Running"]: break
                    if time.monotonic() > until: raise HarnessError("fixture_driver_start_timeout")
                    time.sleep(0.1)
                namespace = negative_namespace_probes(data)
                separation = b16_probes(run, data)
                baseline = receiver_counts(run)
                namespace_delta = deltas(positive_baseline, baseline)
                if any(namespace_delta[key] for key in _FORBIDDEN): raise HarnessError("fixture_namespace_receiver_reached")
                write(run / "control/start-driver", b"start\n")
                process.wait(timeout=args.timeout)
        except (OSError, subprocess.TimeoutExpired):
            raise HarnessError("fixture_driver_wait_failed") from None
        finally:
            if 'process' in locals() and process.poll() is None:
                process.terminate()
                try: process.wait(timeout=3)
                except subprocess.TimeoutExpired: process.kill(); process.wait()
        waited = docker("wait", ident, timeout=5, check=False)
        if waited.returncode: raise HarnessError("fixture_driver_wait_failed")
        code = int(waited.stdout)
        actual = inspect_owned(data, "containers", "driver")
        if actual is None or actual["State"]["Running"] or actual["State"]["ExitCode"] != code:
            raise HarnessError("fixture_driver_exit_unverified")
        stopped = inspect_owned(data, "containers", "browser")
        mcp_stopped = inspect_owned(data, "containers", "mcp")
        report = None
        result = run / "control/result.json"
        if result.exists():
            fd = os.open(result, os.O_RDONLY | os.O_NOFOLLOW)
            with os.fdopen(fd, "rb") as stream:
                info = os.fstat(stream.fileno())
                if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077 or info.st_size > 65536:
                    raise HarnessError("fixture_driver_result_untrusted")
                raw = json.load(stream)
            if (raw.get("format") != "jindo.exploration-fixture-result/v1" or raw.get("boundary_kind") != "owned_contract_fixture"
                    or raw.get("mode") != data["context"]["mode"] or raw.get("production_acceptance") != "NOT_RUN" or raw.get("model_run") != "NOT_RUN"):
                raise HarnessError("fixture_driver_result_invalid")
            report = {"status": raw.get("status") if raw.get("status") in ("PASS", "FAIL") else "FAIL"}
            if "failure" in raw:
                report["failure"] = raw["failure"] if isinstance(raw["failure"], str) and re.fullmatch(r"[a-z_]{1,64}", raw["failure"]) else "invalid_failure_code"
            for key in ("forwarded", "external", "active_connections", "observed", "inferred", "model_calls", "diagnosis_attempts", "tool_error_count"):
                if key in raw:
                    if type(raw[key]) is not int or raw[key] < 0: raise HarnessError("fixture_driver_result_invalid")
                    report[key] = raw[key]
            if "provider_failure" in raw:
                value = raw["provider_failure"]
                report["provider_failure"] = value if isinstance(value, str) and re.fullmatch(r"[a-z_]{0,64}", value) else "invalid_failure_code"
            if "observed_query_keys" in raw:
                if type(raw["observed_query_keys"]) is not bool: raise HarnessError("fixture_driver_result_invalid")
                report["observed_query_keys"] = raw["observed_query_keys"]
            if "resource_observations" in raw:
                resources = raw["resource_observations"]
                if (not isinstance(resources, dict) or set(resources) != {"/assets/app.js", "/assets/app.css", "/assets/logo.svg", "/api/items"}
                        or any(type(value) is not bool for value in resources.values())):
                    raise HarnessError("fixture_driver_result_invalid")
                report["resource_observations"] = resources
            for key in ("proof", "denied", "form_ref_shape"):
                if key in raw:
                    if (not isinstance(raw[key], dict) or any(not isinstance(k, str) or not re.fullmatch(r"[A-Za-z0-9_]{1,64}", k)
                        or (type(v) is not bool if key != "denied" else type(v) is not int or v < 0) for k, v in raw[key].items())):
                        raise HarnessError("fixture_driver_result_invalid")
                    report[key] = raw[key]
        receiver_delta = deltas(baseline, receiver_counts(run))
        forbidden_delta = {key: receiver_delta[key] for key in _FORBIDDEN}
        if data["context"]["mode"] == "full_local":
            forbidden_delta.update({key: receiver_delta[key] for key in ("receiver_cdn", "receiver_conn_cdn")})
        success = (all(value == 0 for value in forbidden_delta.values()) and code == 0 and closed.is_set() and not failed.is_set() and stopped is not None
                   and not stopped["State"]["Running"] and mcp_stopped is not None and not mcp_stopped["State"]["Running"]
                   and report is not None and report["status"] == "PASS")
        return {"format": FORMAT, "role": "contract_fixture", "status": "PASS" if success else "FAIL",
                "driver_exit_code": code, "driver_binary_sha256": data["driver_binary_sha256"], "browser_lease_closed": closed.is_set(), "browser_running_after_close": None if stopped is None else stopped["State"]["Running"],
                "mcp_running_after_close": None if mcp_stopped is None else mcp_stopped["State"]["Running"],
                "result": report, "uncontrolled_chromium": chromium_controls, "positive_receiver_counts": positive, "namespace_probes": namespace, "b16_probes": separation,
                "namespace_receiver_delta": namespace_delta, "driver_receiver_delta": receiver_delta,
                "forbidden_receiver_delta": forbidden_delta, "product_boundary": "NOT_RUN", "model_run": "NOT_RUN"}
    finally:
        server.shutdown(); server.server_close(); thread.join(timeout=3); socket_path.unlink(missing_ok=True)


def main(argv=None):
    os.umask(0o077)
    parser = argparse.ArgumentParser(description="소유 browser contract_fixture Docker 하네스")
    sub = parser.add_subparsers(dest="action", required=True)
    p = sub.add_parser("up")
    p.add_argument("--run-dir", required=True)
    p.add_argument("--subnet-base", default="10.94.240.0/21")
    p.add_argument("--mode", choices=("full_local", "external_allowed"), default="external_allowed")
    p.add_argument("--write-mode", choices=("read_only",), default="read_only")
    p.add_argument("--metadata-script", default=str(ROOT / "skills/api-recon/scripts/metadata_only.js"))
    p.add_argument("--max-requests", type=int, default=128)
    p.add_argument("--duration", type=int, default=180)
    p = sub.add_parser("driver")
    p.add_argument("--run-dir", required=True); p.add_argument("--binary", required=True)
    p.add_argument("--driver-arg", action="append", default=[]); p.add_argument("--timeout", type=int, default=240)
    p = sub.add_parser("down"); p.add_argument("--run-dir", required=True)
    args = parser.parse_args(argv)
    try:
        result = up(args) if args.action == "up" else driver(args) if args.action == "driver" else down(args.run_dir)
    except (HarnessError, OSError, ValueError, KeyError, TypeError) as exc:
        code = str(exc) if isinstance(exc, HarnessError) else "fixture_input_or_file_invalid"
        print(json.dumps({"format": FORMAT, "role": "contract_fixture", "status": "FAIL", "reason": code, "product_boundary": "NOT_RUN"}))
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0 if result["status"] in ("READY", "PASS", "CLEAN") else 1


if __name__ == "__main__": sys.exit(main())
