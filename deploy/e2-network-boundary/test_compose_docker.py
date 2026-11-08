"""Start the B Compose template with inert, disposable Python processes.

This validates actual Docker configuration only. The two sleep processes are
not the A sender or C worker; no acceptance result is inferred from them.
"""

from __future__ import annotations

import json
import argparse
import os
from pathlib import Path
import subprocess
import sys
from uuid import uuid4

sys.path.insert(0, str(Path(__file__).resolve().parent))
from test_boundary_docker import daemon_identity, docker, docker_bin
from verify_runtime import _inspect, _inspect_containers, check


def compose(files: list[str], project: str, env: dict[str, str], *args: str) -> str:
    command = [docker_bin(), "compose", "-p", project]
    for file in files:
        command += ["-f", file]
    result = subprocess.run([*command, *args], env=env, text=True,
                            capture_output=True, timeout=40, check=False)
    if result.returncode:
        raise RuntimeError("compose_command_failed:" + args[0])
    return result.stdout.strip()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.parse_args()
    suffix = uuid4().hex[:12]
    project = "e2b-" + suffix
    run_ref = "synthetic-" + suffix
    network = "e2b-target-" + suffix
    target = "e2b-target-container-" + suffix
    volume = "e2b-channel-" + suffix
    files = [str(Path(__file__).with_name("compose.yaml")),
             str(Path(__file__).with_name("compose.fixture.yaml"))]
    outcome: dict[str, object] = {"status": "FAIL", "scope": "B-only actual Compose config", "issues": []}
    made_network = made_volume = made_target = False
    env = dict(os.environ)
    synthetic = {
        "E2_TOOL_IMAGE": "python:3.11-slim", "E2_SENDER_IMAGE": "python:3.11-slim",
        "E2_TOOL_UID": "10001", "E2_TOOL_GID": "10001",
        "E2_SENDER_UID": "10002", "E2_SENDER_GID": "10002",
        "E2_CHANNEL_VOLUME": volume, "E2_TARGET_NETWORK": network,
        "E2_RUN_REF": run_ref,
    }
    env.update(synthetic)
    env["WSLENV"] = ":".join(filter(None, [env.get("WSLENV", ""), *synthetic]))
    try:
        outcome["daemon"] = daemon_identity()
        docker("image", "inspect", "python:3.11-slim")
        docker("network", "create", "--internal", "--ipv6",
               "-o", "com.docker.network.bridge.gateway_mode_ipv4=isolated",
               "-o", "com.docker.network.bridge.gateway_mode_ipv6=isolated", "--label",
               "org.abc2lab.e2.run=" + run_ref, "--label",
               "abc2lab.e2b.disposable=true", network)
        made_network = True
        docker("volume", "create", "--label", "org.abc2lab.e2.run=" + run_ref,
               "--label", "abc2lab.e2b.disposable=true", volume)
        made_volume = True
        docker("run", "-d", "--pull=never", "--name", target,
               "--label", "abc2lab.e2b.disposable=true", "--network", network,
               "python:3.11-slim", "python", "-c", "import time; time.sleep(60)")
        made_target = True
        json.loads(compose(files, project, env, "config", "--format", "json"))
        compose(files, project, env, "up", "-d", "--no-build", "--pull", "never")
        tool_id = compose(files, project, env, "ps", "-q", "tool")
        sender_id = compose(files, project, env, "ps", "-q", "sender")
        if not tool_id or not sender_id:
            raise RuntimeError("compose_containers_missing")
        all_containers = _inspect_containers()
        issues = check(_inspect("container", tool_id), _inspect("container", sender_id),
                       _inspect("container", target),
                       _inspect("network", network), _inspect("volume", volume), volume, network,
                       run_ref, project, all_containers)
        outcome["issues"] = issues
        if not issues:
            outcome["status"] = "PASS"
    except (RuntimeError, subprocess.TimeoutExpired, OSError, ValueError, json.JSONDecodeError) as error:
        outcome["error"] = str(error) if isinstance(error, RuntimeError) else "test_runtime_error"
    finally:
        try:
            compose(files, project, env, "down", "-v", "--remove-orphans")
        except (RuntimeError, subprocess.TimeoutExpired):
            outcome["cleanup"] = "owned_project_remove_failed"
        if made_target:
            try:
                docker("rm", "-f", target)
            except (RuntimeError, subprocess.TimeoutExpired):
                outcome["cleanup"] = "owned_target_remove_failed"
        if made_network:
            try:
                docker("network", "rm", network)
            except (RuntimeError, subprocess.TimeoutExpired):
                outcome["cleanup"] = "owned_network_remove_failed"
        if made_volume:
            try:
                docker("volume", "rm", volume)
            except (RuntimeError, subprocess.TimeoutExpired):
                outcome["cleanup"] = "owned_volume_remove_failed"
    print(json.dumps(outcome, sort_keys=True))
    return 0 if outcome["status"] == "PASS" and "cleanup" not in outcome else 1


if __name__ == "__main__":
    sys.exit(main())
