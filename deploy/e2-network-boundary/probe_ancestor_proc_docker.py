"""Disposable Docker gate for C's nested user/PID/mount/net namespace shape.

This only tests whether the default nonprivileged container can create the
namespaces via unshare or the x86_64 clone syscall used by child launchers.
It does not mount a private proc, authenticate an A peer, or grant permission
to add capabilities or relax seccomp.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path
from uuid import uuid4

sys.path.insert(0, str(Path(__file__).resolve().parent))
from test_boundary_docker import docker, docker_bin


NAMESPACE_CODE = r'''
import ctypes, json, os, platform, signal
libc = ctypes.CDLL(None, use_errno=True)
flags = 0x10000000 | 0x20000000 | 0x00020000 | 0x40000000
result = libc.unshare(flags)
unshare = {"created": result == 0, "errno": ctypes.get_errno() if result else 0}
clone = {"attempted": False, "created": False, "errno": 0}
if result != 0 and platform.machine() == "x86_64":
    # No CLONE_VM: child has a private address space and exits immediately.
    pid = libc.syscall(56, flags | signal.SIGCHLD, 0, 0, 0, 0)
    if pid == 0:
        os._exit(0)
    if pid > 0:
        os.waitpid(pid, 0)
    clone = {"attempted": True, "created": pid > 0,
             "errno": ctypes.get_errno() if pid < 0 else 0}
print(json.dumps({"unshare": unshare, "clone": clone}))
'''


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="python:3.11-slim")
    args = parser.parse_args()
    name = "e2b-ancestor-probe-" + uuid4().hex[:12]
    outcome: dict[str, object] = {"status": "FAIL", "scope": "B-only namespace creation gate",
                                  "candidate_accepted": False}
    try:
        docker("image", "inspect", args.image)
        result = subprocess.run(
            [docker_bin(), "run", "--rm", "--pull=never", "--name", name,
             "--label", "abc2lab.e2b.disposable=true", "--network", "none",
             "--user", "10001:10001", "--cap-drop", "ALL",
             "--security-opt", "no-new-privileges:true", "--read-only",
             "--pids-limit", "128", args.image, "python", "-c", NAMESPACE_CODE],
            text=True, capture_output=True, timeout=20, check=False,
        )
        if result.returncode != 0:
            outcome["reason"] = "disposable_container_unavailable"
        else:
            observation = json.loads(result.stdout)
            unshare = observation.get("unshare") or {}
            clone = observation.get("clone") or {}
            if (unshare.get("created") is False and unshare.get("errno") == 1 and
                    clone.get("attempted") is True and clone.get("created") is False and
                    clone.get("errno") == 1):
                outcome.update(status="SKIP", candidate_state="BLOCKED_OS_PERMISSION",
                               reason="namespace_creation_denied")
            elif unshare.get("created") is True or clone.get("created") is True:
                outcome.update(status="SKIP", candidate_state="UNVERIFIED_PRIVATE_PROC",
                               reason="private_proc_not_tested")
            elif clone.get("attempted") is False:
                outcome.update(status="SKIP", candidate_state="UNVERIFIED_ARCHITECTURE",
                               reason="clone_probe_not_supported")
            else:
                outcome["reason"] = "namespace_observation_unexpected"
    except (RuntimeError, subprocess.TimeoutExpired, OSError, ValueError):
        outcome["reason"] = "probe_unavailable"
    finally:
        try:
            names = docker("ps", "-a", "--filter", "label=abc2lab.e2b.disposable=true",
                           "--format", "{{.Names}}").splitlines()
            if name in names:
                docker("rm", "-f", name)
        except (RuntimeError, subprocess.TimeoutExpired):
            outcome["cleanup"] = "owned_container_remove_failed"
    print(json.dumps(outcome, sort_keys=True))
    return 0 if outcome["status"] == "SKIP" and "cleanup" not in outcome else 1


if __name__ == "__main__":
    sys.exit(main())
