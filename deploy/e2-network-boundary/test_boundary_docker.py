"""Disposable B-only Docker isolation probe against a synthetic target.

This is not NE01/02/08/09 acceptance: it does not invoke the real A service or
C's worker lifecycle. It never publishes a host port or contacts a real target.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import shutil
import subprocess
import sys
import time
from uuid import uuid4


TARGET_CODE = r'''
import http.server, json, socket, threading
counts = {"http": 0, "udp": 0, "http6": 0, "udp6": 0, "connect": 0}
class Handler(http.server.BaseHTTPRequestHandler):
    def do_CONNECT(self):
        counts["connect"] += 1
        self.send_response(200)
        self.send_header("Content-Length", "0")
        self.end_headers()
    def do_GET(self):
        if self.path == "/count":
            body = json.dumps(counts).encode()
        else:
            counts["http6" if self.server.address_family == socket.AF_INET6 else "http"] += 1
            body = b"synthetic"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *_): pass
class HTTP6(http.server.HTTPServer):
    address_family = socket.AF_INET6
    def server_bind(self):
        self.socket.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
        super().server_bind()
def udp(family, address, port, key):
    sock = socket.socket(family, socket.SOCK_DGRAM)
    if family == socket.AF_INET6:
        sock.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
    sock.bind((address, port))
    while True:
        sock.recvfrom(4096)
        counts[key] += 1
threading.Thread(target=udp, args=(socket.AF_INET, "0.0.0.0", 8081, "udp"), daemon=True).start()
threading.Thread(target=udp, args=(socket.AF_INET6, "::", 8083, "udp6"), daemon=True).start()
threading.Thread(target=lambda: HTTP6(("::", 8082), Handler).serve_forever(), daemon=True).start()
http.server.HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
'''

COUNT_CODE = "import json,urllib.request; print(urllib.request.urlopen('http://127.0.0.1:8080/count',timeout=1).read().decode())"
CONTROL_CODE = r'''
import socket,sys
v4,v6,name=sys.argv[1:]
assert socket.getaddrinfo(name,8080)
for family,address,port,path in ((socket.AF_INET,v4,8080,"/control4"),(socket.AF_INET6,v6,8082,"/control6")):
    sock=socket.socket(family,socket.SOCK_STREAM)
    sock.settimeout(2)
    sock.connect((address,port))
    sock.sendall(("GET "+path+" HTTP/1.0\r\n\r\n").encode())
    assert b"200" in sock.recv(100)
    sock.close()
for family,address,port in ((socket.AF_INET,v4,8081),(socket.AF_INET6,v6,8083)):
    sock=socket.socket(family,socket.SOCK_DGRAM)
    sock.sendto(b"synthetic-control",(address,port))
    sock.close()
sock=socket.create_connection((v4,8080),timeout=2)
sock.sendall(b"CONNECT synthetic.invalid:443 HTTP/1.1\r\nHost: synthetic.invalid:443\r\n\r\n")
assert b"200" in sock.recv(100)
sock.close()
'''


def docker_bin() -> str:
    value = shutil.which("docker.exe") or shutil.which("docker")
    if not value:
        raise RuntimeError("docker_cli_unavailable")
    return value


def docker(*args: str, input_text: str | None = None, timeout: int = 25) -> str:
    result = subprocess.run([docker_bin(), *args], input=input_text, text=True,
                            capture_output=True, timeout=timeout, check=False)
    if result.returncode:
        raise RuntimeError("docker_command_failed")
    return result.stdout.strip()


def count(target: str) -> dict[str, int]:
    return json.loads(docker("exec", target, "python", "-c", COUNT_CODE))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="python:3.11-slim", help="locally cached Python image; never pulled")
    args = parser.parse_args()
    suffix = uuid4().hex[:12]
    network = "e2b-" + suffix
    target = "e2b-target-" + suffix
    made_network = made_target = False
    observations: dict[str, object] = {"status": "FAIL", "scope": "B-only synthetic", "checks": {}, "target_received": None}
    try:
        docker("image", "inspect", args.image)
        docker("network", "create", "--internal", "--ipv6",
               "-o", "com.docker.network.bridge.gateway_mode_ipv4=isolated",
               "-o", "com.docker.network.bridge.gateway_mode_ipv6=isolated",
               "--label", "abc2lab.e2b.disposable=true", network)
        made_network = True
        docker("run", "-d", "--pull=never", "--name", target,
               "--label", "abc2lab.e2b.disposable=true", "--network", network,
               args.image, "python", "-u", "-c", TARGET_CODE)
        made_target = True
        baseline = None
        for _ in range(15):
            try:
                baseline = count(target)
                break
            except (RuntimeError, json.JSONDecodeError):
                time.sleep(0.2)
        if baseline is None:
            raise RuntimeError("synthetic_target_not_ready")
        ipv4 = docker("inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", target)
        ipv6 = docker("inspect", "-f", "{{range .NetworkSettings.Networks}}{{.GlobalIPv6Address}}{{end}}", target)
        if not ipv4 or not ipv6:
            raise RuntimeError("synthetic_dual_stack_unavailable")
        for _ in range(15):
            try:
                docker("run", "--rm", "--pull=never", "--network", network,
                       args.image, "python", "-c", CONTROL_CODE, ipv4, ipv6, target)
                baseline = count(target)
                if all(value >= 1 for value in baseline.values()):
                    break
            except RuntimeError:
                pass
            time.sleep(0.2)
        else:
            raise RuntimeError("synthetic_positive_control_failed")
        script = Path(__file__).with_name("probe_network.py").read_text()
        result = subprocess.run(
            [docker_bin(), "run", "--rm", "-i", "--pull=never", "--network", "none",
             "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
             "--read-only", "--pids-limit", "128", "--user", "10001:10001",
             "-e", "HTTP_PROXY=http://" + ipv4 + ":8080", args.image,
             "python", "-", "--ipv4", ipv4, "--ipv6", ipv6,
             "--tcp-port", "8080", "--udp-port", "8081",
             "--ipv6-tcp-port", "8082", "--ipv6-udp-port", "8083", "--dns-name", target],
            input=script, text=True, capture_output=True, timeout=40, check=False,
        )
        if not result.stdout.strip():
            raise RuntimeError("synthetic_probe_unavailable")
        probe = json.loads(result.stdout)
        after = count(target)
        observations["checks"] = {item["name"]: not item["connected_or_sent"] for item in probe["checks"]}
        observations["target_received"] = {key: after[key] - baseline[key] for key in baseline}
        if result.returncode == 0 and all(observations["checks"].values()) and all(value == 0 for value in observations["target_received"].values()):
            observations["status"] = "PASS"
    except (RuntimeError, subprocess.TimeoutExpired, OSError, ValueError, json.JSONDecodeError) as error:
        observations["error"] = str(error) if isinstance(error, RuntimeError) else "test_runtime_error"
    finally:
        if made_target:
            try:
                docker("rm", "-f", target)
            except (RuntimeError, subprocess.TimeoutExpired):
                observations["cleanup"] = "target_remove_failed"
        if made_network:
            try:
                docker("network", "rm", network)
            except (RuntimeError, subprocess.TimeoutExpired):
                observations["cleanup"] = "network_remove_failed"
    print(json.dumps(observations, sort_keys=True))
    return 0 if observations["status"] == "PASS" and "cleanup" not in observations else 1


if __name__ == "__main__":
    sys.exit(main())
