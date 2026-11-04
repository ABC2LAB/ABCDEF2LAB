"""Disposable Unix-channel transport test with a FIXTURE sender, not A.

The fixture ignores tool-supplied destinations and sends one fixed GET to an
owned synthetic HTTP target. This proves channel reachability with a tool that
has no IP network. It does not prove admission, TLS, budget, audit, or C drain.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import subprocess
import sys
import time
from uuid import uuid4

sys.path.insert(0, str(Path(__file__).resolve().parent))
from test_boundary_docker import COUNT_CODE, TARGET_CODE, docker, docker_bin


SENDER_CODE = r'''
import http.client, os, socket, sys
path = "/run/e2-channel/registered.sock"
server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
server.bind(path)
os.chmod(path, 0o666)  # Disposable fixture only; C owns production identity/ACL.
server.listen(4)
while True:
    conn, _ = server.accept()
    with conn:
        if conn.recv(64) != b"registered-get":
            conn.sendall(b"blocked")
            continue
        target = http.client.HTTPConnection(sys.argv[1], 8080, timeout=2)
        target.request("GET", "/through-channel", headers={"Host": "synthetic.invalid"})
        response = target.getresponse()
        response.read()
        conn.sendall(b"ok" if response.status == 200 else b"error")
        target.close()
'''

TOOL_CODE = r'''
import socket
conn = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
conn.settimeout(3)
conn.connect("/run/e2-channel/registered.sock")
conn.sendall(b"registered-get")
assert conn.recv(16) == b"ok"
print("channel_ok")
'''


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="python:3.11-slim")
    args = parser.parse_args()
    suffix = uuid4().hex[:12]
    network = "e2b-channel-" + suffix
    volume = "e2b-channel-" + suffix
    target = "e2b-target-" + suffix
    sender = "e2b-sender-" + suffix
    created: list[tuple[str, str]] = []
    outcome: dict[str, object] = {"status": "FAIL", "scope": "B-only fixture transport", "target_received": None}
    try:
        docker("image", "inspect", args.image)
        docker("network", "create", "--internal", "--ipv6",
               "-o", "com.docker.network.bridge.gateway_mode_ipv4=isolated",
               "-o", "com.docker.network.bridge.gateway_mode_ipv6=isolated",
               "--label", "abc2lab.e2b.disposable=true", network)
        created.append(("network", network))
        docker("volume", "create", "--label", "abc2lab.e2b.disposable=true", volume)
        created.append(("volume", volume))
        docker("run", "-d", "--pull=never", "--name", target, "--network", network,
               "--label", "abc2lab.e2b.disposable=true", args.image,
               "python", "-u", "-c", TARGET_CODE)
        created.append(("container", target))
        for _ in range(15):
            try:
                json.loads(docker("exec", target, "python", "-c", COUNT_CODE))
                break
            except (RuntimeError, ValueError):
                time.sleep(0.2)
        else:
            raise RuntimeError("synthetic_target_not_ready")
        ipv4 = docker("inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", target)
        docker("run", "-d", "--pull=never", "--name", sender,
               "--label", "abc2lab.e2b.disposable=true", "--network", network,
               "--mount", "type=volume,src=" + volume + ",dst=/run/e2-channel",
               args.image, "python", "-u", "-c", SENDER_CODE, ipv4)
        created.append(("container", sender))
        for _ in range(15):
            try:
                if docker("exec", sender, "python", "-c",
                          "import os; assert os.path.exists('/run/e2-channel/registered.sock')") == "":
                    break
            except RuntimeError:
                time.sleep(0.2)
        else:
            raise RuntimeError("fixture_channel_not_ready")
        result = subprocess.run(
            [docker_bin(), "run", "--rm", "--pull=never", "--network", "none",
             "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
             "--read-only", "--pids-limit", "128", "--user", "10001:10001",
             "--mount", "type=volume,src=" + volume + ",dst=/run/e2-channel,readonly",
             args.image, "python", "-c", TOOL_CODE],
            text=True, capture_output=True, timeout=20, check=False,
        )
        counts = json.loads(docker("exec", target, "python", "-c", COUNT_CODE))
        outcome["target_received"] = counts["http"]
        if result.returncode == 0 and result.stdout.strip() == "channel_ok" and counts == {"http": 1, "udp": 0, "http6": 0, "udp6": 0, "connect": 0}:
            outcome["status"] = "PASS"
    except (RuntimeError, subprocess.TimeoutExpired, OSError, ValueError) as error:
        outcome["error"] = str(error) if isinstance(error, RuntimeError) else "test_runtime_error"
    finally:
        for kind, name in reversed(created):
            try:
                if kind == "container":
                    docker("rm", "-f", name)
                elif kind == "network":
                    docker("network", "rm", name)
                else:
                    docker("volume", "rm", name)
            except (RuntimeError, subprocess.TimeoutExpired):
                outcome["cleanup"] = "owned_resource_remove_failed"
    print(json.dumps(outcome, sort_keys=True))
    return 0 if outcome["status"] == "PASS" and "cleanup" not in outcome else 1


if __name__ == "__main__":
    sys.exit(main())
