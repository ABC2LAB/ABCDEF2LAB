"""Disposable A-wire packet transport test with a FIXTURE sender, not A.

The fixture ignores tool-supplied destinations and sends one fixed GET to an
owned synthetic HTTP target. It observes SCM_CREDENTIALS without authorizing
them. This proves packet reachability with a tool that has no IP network. It
does not prove peer identity, admission, TLS, budget, audit, or C drain.
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
import http.client, json, os, secrets, socket, struct, sys
path = "/run/e2-channel/registered.sock"
server = socket.socket(socket.AF_UNIX, socket.SOCK_SEQPACKET)
server.setsockopt(socket.SOL_SOCKET, socket.SO_PASSCRED, 1)
server.bind(path)
os.chmod(path, 0o666)  # Disposable fixture only; C owns production identity/ACL.
server.listen(4)
while True:
    conn, _ = server.accept()
    with conn:
        conn.settimeout(5)
        conn.setsockopt(socket.SOL_SOCKET, socket.SO_PASSCRED, 1)
        nonce = secrets.token_hex(16)
        conn.send(("E2NB/1 " + nonce).encode("ascii"))
        payload, ancillary, flags, _ = conn.recvmsg(4097, socket.CMSG_SPACE(struct.calcsize("3i")))
        credentials = [struct.unpack("3i", data[:struct.calcsize("3i")])
                       for level, kind, data in ancillary
                       if level == socket.SOL_SOCKET and kind == socket.SCM_CREDENTIALS
                       and len(data) >= struct.calcsize("3i")]
        observed = {"credential_present": len(credentials) == 1,
                    "pid_visible": bool(credentials and credentials[0][0] > 0),
                    "uid_matches": bool(credentials and credentials[0][1] == 10001),
                    "gid_matches": bool(credentials and credentials[0][2] == 10001)}
        print(json.dumps(observed, sort_keys=True), flush=True)
        try:
            request = json.loads(payload)
        except ValueError:
            continue
        if (flags & (socket.MSG_TRUNC | socket.MSG_CTRUNC) or
                not observed["credential_present"] or not isinstance(request, dict) or
                set(request) != {"version", "account_ref", "resource_ref", "nonce"} or
                request != {"version": 1, "account_ref": "account:1",
                            "resource_ref": "item:1", "nonce": nonce}):
            continue
        target = http.client.HTTPConnection(sys.argv[1], 8080, timeout=2)
        target.request("GET", "/through-channel", headers={"Host": "synthetic.invalid"})
        response = target.getresponse()
        response.read()
        if response.status == 200:
            conn.send(json.dumps({"version": 1, "state": "response_observed",
                                  "reason": "", "admitted": True,
                                  "response_received": True}).encode("utf-8"))
        target.close()
'''

STREAM_CODE = r'''
import socket
conn = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
conn.settimeout(2)
try:
    conn.connect("/run/e2-channel/registered.sock")
except OSError:
    print("stream_rejected")
else:
    raise AssertionError("stream connected to packet socket")
finally:
    conn.close()
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
    packet_tool = "e2b-packet-tool-" + suffix
    stream_tool = "e2b-stream-tool-" + suffix
    created: list[tuple[str, str]] = []
    owned_runs: list[str] = []
    outcome: dict[str, object] = {"status": "FAIL", "scope": "B-only A-wire fixture transport",
                                  "a_peer_identity": "UNVERIFIED", "target_received": None}
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
        owned_runs.append(packet_tool)
        result = subprocess.run(
            [docker_bin(), "run", "--rm", "-i", "--pull=never", "--name", packet_tool,
             "--label", "abc2lab.e2b.disposable=true", "--network", "none",
             "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
             "--read-only", "--pids-limit", "128", "--user", "10001:10001",
             "--mount", "type=volume,src=" + volume + ",dst=/run/e2-channel,readonly",
             args.image, "python", "-", "--account-ref", "account:1",
             "--resource-ref", "item:1"],
            input=Path(__file__).with_name("probe_packet_channel.py").read_text(encoding="utf-8"),
            text=True, capture_output=True, timeout=20, check=False,
        )
        before_stream = json.loads(docker("exec", target, "python", "-c", COUNT_CODE))
        outcome["positive_target_received"] = before_stream["http"]
        owned_runs.append(stream_tool)
        stream_result = docker("run", "--rm", "--pull=never", "--name", stream_tool,
                               "--label", "abc2lab.e2b.disposable=true", "--network", "none",
                               "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
                               "--read-only", "--pids-limit", "128", "--user", "10001:10001",
                               "--mount", "type=volume,src=" + volume + ",dst=/run/e2-channel,readonly",
                               args.image, "python", "-c", STREAM_CODE)
        outcome["stream_fallback_blocked"] = stream_result == "stream_rejected"
        counts = json.loads(docker("exec", target, "python", "-c", COUNT_CODE))
        outcome["target_received"] = counts["http"]
        outcome["stream_fallback_target_delta"] = counts["http"] - before_stream["http"]
        observations = [json.loads(line) for line in docker("logs", sender).splitlines() if line]
        if observations:
            outcome["credential_observation"] = observations[-1]
            if not observations[-1]["pid_visible"]:
                # auditpeer rejects PID <= 0. The private sender PID namespace
                # cannot identify this tool process without a C-owned design.
                outcome["a_peer_identity"] = "BLOCKED_PID_NAMESPACE"
        probe = json.loads(result.stdout) if result.stdout.strip() else {}
        if (result.returncode == 0 and probe.get("status") == "PASS" and
                probe.get("ack") == {"version": 1, "state": "response_observed",
                                     "reason": "", "admitted": True, "response_received": True} and
                observations and observations[-1]["credential_present"] and
                observations[-1]["uid_matches"] and observations[-1]["gid_matches"] and
                outcome["stream_fallback_blocked"] and
                outcome["positive_target_received"] == 1 and
                outcome["stream_fallback_target_delta"] == 0 and
                counts == {"http": 1, "udp": 0, "http6": 0, "udp6": 0, "connect": 0}):
            outcome["status"] = "PASS"
    except (RuntimeError, subprocess.TimeoutExpired, OSError, ValueError, KeyError) as error:
        outcome["error"] = str(error) if isinstance(error, RuntimeError) else "test_runtime_error"
    finally:
        for name in reversed(owned_runs):
            try:
                if docker("ps", "-aq", "--filter", "name=^/" + name + "$"):
                    docker("rm", "-f", name)
            except (RuntimeError, subprocess.TimeoutExpired):
                outcome["cleanup"] = "owned_tool_remove_failed"
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
