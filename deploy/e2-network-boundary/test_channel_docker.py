"""Disposable A-wire packet transport test with a FIXTURE sender, not A.

The fixture ignores tool-supplied destinations and sends one fixed GET to an
owned synthetic HTTP target. It observes SCM_CREDENTIALS without authorizing
them. This proves packet reachability with a tool that has no IP network. It
does not prove peer identity, admission, TLS, budget, audit, or C drain.
"""

from __future__ import annotations

import argparse
import errno
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
except OSError as error:
    import json
    print(json.dumps({"status": "stream_rejected", "errno": error.errno}))
else:
    raise AssertionError("stream connected to packet socket")
finally:
    conn.close()
'''

PROTECTION_CODE = r'''
import ctypes, json, os, time
peers = []
for entry in os.listdir('/proc'):
    if not entry.isdecimal() or int(entry) == os.getpid():
        continue
    try:
        with open('/proc/' + entry + '/status') as handle:
            uid = next(int(line.split()[1]) for line in handle if line.startswith('Uid:'))
    except (OSError, StopIteration, ValueError):
        continue
    if uid == 10002:
        peers.append(int(entry))
pid = min(peers) if peers else None
out = {'sender_process_visible': pid is not None,
       'pid_namespace': os.readlink('/proc/self/ns/pid'),
       'net_namespace': os.readlink('/proc/self/ns/net')}
if pid is not None:
    try:
        os.kill(pid, 0)
        out['signal_blocked'] = False
    except PermissionError:
        out['signal_blocked'] = True
    except OSError:
        out['signal_blocked'] = False
    for key, path, listing in (
            ('environ_blocked', '/proc/%d/environ' % pid, False),
            ('mem_blocked', '/proc/%d/mem' % pid, False),
            ('fd_blocked', '/proc/%d/fd' % pid, True),
            ('cmdline_blocked', '/proc/%d/cmdline' % pid, False)):
        try:
            if listing:
                os.listdir(path)
            else:
                with open(path, 'rb') as handle:
                    handle.read(1)
            out[key] = False
        except PermissionError:
            out[key] = True
        except OSError:
            out[key] = False
    libc = ctypes.CDLL(None, use_errno=True)
    attached = libc.ptrace(16, pid, None, None) == 0
    out['ptrace_blocked'] = not attached
    if attached:
        stopped = False
        deadline = time.monotonic() + 1
        while time.monotonic() < deadline:
            try:
                waited, status = os.waitpid(pid, os.WNOHANG | os.WUNTRACED)
            except (ChildProcessError, OSError):
                break
            if waited == pid and os.WIFSTOPPED(status):
                stopped = True
                break
            time.sleep(0.01)
        out['ptrace_cleanup_ok'] = stopped and libc.ptrace(17, pid, None, None) == 0
    else:
        out['ptrace_cleanup_ok'] = True
print(json.dumps(out))
'''

NAMESPACE_CODE = "import json,os; print(json.dumps({'pid_namespace':os.readlink('/proc/self/ns/pid'),'net_namespace':os.readlink('/proc/self/ns/net')}))"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="python:3.11-slim")
    parser.add_argument("--candidate-share-pid", action="store_true",
                        help="disposable experiment only; never enables product Compose")
    args = parser.parse_args()
    suffix = uuid4().hex[:12]
    network = "e2b-channel-" + suffix
    volume = "e2b-channel-" + suffix
    target = "e2b-target-" + suffix
    sender = "e2b-sender-" + suffix
    volume_init = "e2b-volume-init-" + suffix
    packet_tool = "e2b-packet-tool-" + suffix
    stream_tool = "e2b-stream-tool-" + suffix
    protection_tool = "e2b-protection-tool-" + suffix
    lifetime_tool = "e2b-lifetime-tool-" + suffix
    owned: list[tuple[str, str]] = []
    outcome: dict[str, object] = {"status": "FAIL", "scope": "B-only A-wire fixture transport",
                                  "candidate_share_pid": args.candidate_share_pid,
                                  "candidate_accepted": False,
                                  "a_peer_identity": "UNVERIFIED", "target_received": None}
    try:
        docker("image", "inspect", args.image)
        owned.append(("network", network))
        docker("network", "create", "--internal", "--ipv6",
               "-o", "com.docker.network.bridge.gateway_mode_ipv4=isolated",
               "-o", "com.docker.network.bridge.gateway_mode_ipv6=isolated",
               "--label", "abc2lab.e2b.disposable=true", network)
        owned.append(("volume", volume))
        docker("volume", "create", "--label", "abc2lab.e2b.disposable=true", volume)
        owned.append(("container", volume_init))
        docker("run", "--rm", "--pull=never", "--name", volume_init,
               "--label", "abc2lab.e2b.disposable=true", "--network", "none",
               "--mount", "type=volume,src=" + volume + ",dst=/run/e2-channel",
               args.image, "python", "-c",
               "import os; os.chmod('/run/e2-channel', 0o1777)")
        owned.append(("container", target))
        docker("run", "-d", "--pull=never", "--name", target, "--network", network,
               "--label", "abc2lab.e2b.disposable=true", args.image,
               "python", "-u", "-c", TARGET_CODE)
        for _ in range(15):
            try:
                json.loads(docker("exec", target, "python", "-c", COUNT_CODE))
                break
            except (RuntimeError, ValueError):
                time.sleep(0.2)
        else:
            raise RuntimeError("synthetic_target_not_ready")
        ipv4 = docker("inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", target)
        owned.append(("container", sender))
        docker("run", "-d", "--pull=never", "--name", sender,
               "--label", "abc2lab.e2b.disposable=true", "--network", network,
               "--user", "10002:10002", "--cap-drop", "ALL",
               "--security-opt", "no-new-privileges:true", "--read-only",
               "--pids-limit", "128",
               "--mount", "type=volume,src=" + volume + ",dst=/run/e2-channel",
               args.image, "python", "-u", "-c", SENDER_CODE, ipv4)
        for _ in range(15):
            try:
                if docker("exec", sender, "python", "-c",
                          "import os; assert os.path.exists('/run/e2-channel/registered.sock')") == "":
                    break
            except RuntimeError:
                time.sleep(0.2)
        else:
            raise RuntimeError("fixture_channel_not_ready")
        sender_id = docker("inspect", "-f", "{{.Id}}", sender)
        pid_args = ["--pid", "container:" + sender_id] if args.candidate_share_pid else []
        owned.append(("container", packet_tool))
        result = subprocess.run(
            [docker_bin(), "run", "--rm", "-i", "--pull=never", "--name", packet_tool,
             "--label", "abc2lab.e2b.disposable=true", "--network", "none", *pid_args,
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
        owned.append(("container", stream_tool))
        stream_result = docker("run", "--rm", "--pull=never", "--name", stream_tool,
                               "--label", "abc2lab.e2b.disposable=true", "--network", "none",
                               *pid_args,
                               "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
                               "--read-only", "--pids-limit", "128", "--user", "10001:10001",
                               "--mount", "type=volume,src=" + volume + ",dst=/run/e2-channel,readonly",
                               args.image, "python", "-c", STREAM_CODE)
        stream_observation = json.loads(stream_result)
        outcome["stream_fallback_blocked"] = (
            stream_observation == {"status": "stream_rejected", "errno": errno.EPROTOTYPE}
        )
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
        candidate_checks_ok = True
        if args.candidate_share_pid:
            owned.append(("container", protection_tool))
            observed = json.loads(docker(
                "run", "--rm", "--pull=never", "--name", protection_tool,
                "--label", "abc2lab.e2b.disposable=true", "--network", "none",
                *pid_args, "--user", "10001:10001", "--cap-drop", "ALL",
                "--security-opt", "no-new-privileges:true", "--read-only",
                "--pids-limit", "128", args.image, "python", "-c", PROTECTION_CODE))
            sender_ns = json.loads(docker("exec", sender, "python", "-c", NAMESPACE_CODE))
            isolation = {
                "same_pid_namespace": observed["pid_namespace"] == sender_ns["pid_namespace"],
                "separate_network_namespace": observed["net_namespace"] != sender_ns["net_namespace"],
                **{key: observed[key] for key in (
                    "sender_process_visible", "signal_blocked", "ptrace_blocked",
                    "environ_blocked", "mem_blocked", "fd_blocked", "cmdline_blocked",
                    "ptrace_cleanup_ok")},
            }
            outcome["candidate_isolation"] = isolation
            candidate_checks_ok = all(isolation[key] for key in (
                "same_pid_namespace", "separate_network_namespace", "sender_process_visible",
                "signal_blocked", "ptrace_blocked", "ptrace_cleanup_ok",
                "environ_blocked", "mem_blocked", "fd_blocked"))
            owned.append(("container", lifetime_tool))
            docker("run", "-d", "--pull=never", "--name", lifetime_tool,
                   "--label", "abc2lab.e2b.disposable=true", "--network", "none",
                   *pid_args, "--user", "10001:10001", "--cap-drop", "ALL",
                   "--security-opt", "no-new-privileges:true", "--read-only",
                   "--pids-limit", "128", args.image, "python", "-c",
                   "import time; time.sleep(60)")
            docker("stop", "-t", "1", sender)
            for _ in range(15):
                if docker("inspect", "-f", "{{.State.Running}}", lifetime_tool) == "false":
                    break
                time.sleep(0.2)
            outcome["candidate_lifetime_tool_stopped"] = (
                docker("inspect", "-f", "{{.State.Running}}", lifetime_tool) == "false")
            candidate_checks_ok = candidate_checks_ok and outcome["candidate_lifetime_tool_stopped"]
        probe = json.loads(result.stdout) if result.stdout.strip() else {}
        packet_ok = bool(
            result.returncode == 0 and probe.get("status") == "PASS" and
            probe.get("scope") == "packet_ack_only" and
            probe.get("ack") == {"version": 1, "state": "response_observed",
                                 "reason": "", "admitted": True, "response_received": True} and
            observations and observations[-1]["credential_present"] and
            observations[-1]["uid_matches"] and observations[-1]["gid_matches"] and
            (not args.candidate_share_pid or observations[-1]["pid_visible"]) and
            outcome["stream_fallback_blocked"] and
            outcome["positive_target_received"] == 1 and
            outcome["stream_fallback_target_delta"] == 0 and
            counts == {"http": 1, "udp": 0, "http6": 0, "udp6": 0, "connect": 0})
        if args.candidate_share_pid:
            if not packet_ok or not candidate_checks_ok:
                outcome["candidate_state"] = "BLOCKED_PACKET_ISOLATION_OR_LIFETIME"
            elif not isolation["cmdline_blocked"]:
                outcome["candidate_state"] = "BLOCKED_PROC_CMDLINE"
            else:
                outcome["candidate_state"] = "UNVERIFIED_C_HOST_IDENTITY"
        if packet_ok and candidate_checks_ok:
            outcome["status"] = "SKIP" if args.candidate_share_pid else "PASS"
    except (RuntimeError, subprocess.TimeoutExpired, OSError, ValueError, KeyError) as error:
        outcome["error"] = str(error) if isinstance(error, RuntimeError) else "test_runtime_error"
    finally:
        for kind, name in reversed(owned):
            try:
                if kind == "container":
                    names = docker("ps", "-a", "--filter", "label=abc2lab.e2b.disposable=true",
                                   "--format", "{{.Names}}").splitlines()
                    if name in names:
                        docker("rm", "-f", name)
                elif kind == "network":
                    names = docker("network", "ls", "--filter", "label=abc2lab.e2b.disposable=true",
                                   "--format", "{{.Name}}").splitlines()
                    if name in names:
                        docker("network", "rm", name)
                else:
                    names = docker("volume", "ls", "--filter", "label=abc2lab.e2b.disposable=true",
                                   "--format", "{{.Name}}").splitlines()
                    if name in names:
                        docker("volume", "rm", name)
            except (RuntimeError, subprocess.TimeoutExpired):
                outcome["cleanup"] = "owned_resource_remove_failed"
    print(json.dumps(outcome, sort_keys=True))
    return 0 if outcome["status"] in {"PASS", "SKIP"} and "cleanup" not in outcome else 1


if __name__ == "__main__":
    sys.exit(main())
