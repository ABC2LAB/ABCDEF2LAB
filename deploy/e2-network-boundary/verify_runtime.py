"""Read-only Docker object checks for the E② B process boundary.

This is a deployment verifier, not an authority or a replacement for probes.
It deliberately reports fixed issue codes instead of raw Docker environment
or mount details, which may contain credentials.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
from typing import Any

CHANNEL = "/run/e2-channel"
RUN_LABEL = "org.abc2lab.e2.run"
ROLE_LABEL = "org.abc2lab.e2.role"
_CONTAINER_ID = re.compile(r"[0-9a-f]{64}")


def _published(ports: Any) -> bool:
    # An EXPOSE declaration appears as {"80/tcp": null}; it is not a host bind.
    return isinstance(ports, dict) and any(value for value in ports.values())


def _nonroot_ids(value: Any) -> tuple[int, int] | None:
    # Image usernames or implicit groups can resolve to privileged IDs.
    parts = str(value or "").split(":")
    if len(parts) != 2 or not all(part.isascii() and part.isdecimal() and len(part) <= 10 for part in parts):
        return None
    uid, gid = (int(part) for part in parts)
    return (uid, gid) if 0 < uid < 0xFFFFFFFF and 0 < gid < 0xFFFFFFFF else None


def _extra_privilege(host: dict[str, Any]) -> bool:
    return any(host.get(key) for key in ("CapAdd", "Devices", "DeviceRequests", "DeviceCgroupRules")) or any("unconfined" in str(value).lower() for value in host.get("SecurityOpt") or [])


def _docker_bin(explicit: str | None = None) -> str:
    value = explicit or os.environ.get("E2_DOCKER_CLI") or "docker"
    value = shutil.which(value) if "/" not in value else value
    if value is None:
        raise RuntimeError("docker_cli_unavailable")
    return value


def _inspect(kind: str, name: str, docker_cli: str | None = None) -> dict[str, Any]:
    docker = _docker_bin(docker_cli)
    cmd = [docker, kind, "inspect", name] if kind in {"network", "volume"} else [docker, "inspect", "--type", kind, name]
    result = subprocess.run(cmd, text=True, capture_output=True, check=False, timeout=15)
    if result.returncode:
        raise RuntimeError("docker_inspect_failed")
    value = json.loads(result.stdout)
    if not isinstance(value, list) or len(value) != 1 or not isinstance(value[0], dict):
        raise RuntimeError("docker_inspect_shape")
    return value[0]


def _inspect_containers(docker_cli: str | None = None) -> list[dict[str, Any]]:
    docker = _docker_bin(docker_cli)
    listed = subprocess.run([docker, "ps", "-aq"], text=True, capture_output=True,
                            check=False, timeout=15)
    if listed.returncode:
        raise RuntimeError("docker_container_list_failed")
    ids = [value for value in listed.stdout.splitlines() if value]
    if not ids:
        return []
    result = subprocess.run([docker, "inspect", "--type", "container", *ids],
                            text=True, capture_output=True, check=False, timeout=15)
    if result.returncode:
        raise RuntimeError("docker_container_inspect_failed")
    value = json.loads(result.stdout)
    if not isinstance(value, list) or not all(isinstance(item, dict) for item in value):
        raise RuntimeError("docker_container_inspect_shape")
    return value


def _daemon_identity(docker_cli: str | None = None) -> dict[str, str]:
    docker = _docker_bin(docker_cli)
    result = subprocess.run([docker, "info", "--format", "{{json .}}"],
                            text=True, capture_output=True, check=False, timeout=15)
    if result.returncode:
        raise RuntimeError("docker_daemon_identity_unavailable")
    value = json.loads(result.stdout)
    if not isinstance(value, dict):
        raise RuntimeError("docker_daemon_identity_shape")
    fields = ("ID", "Name", "ServerVersion", "OperatingSystem")
    if any(type(value.get(field)) is not str or not value[field].strip() for field in fields):
        raise RuntimeError("docker_daemon_identity_fields")
    return {field: value[field] for field in fields}


def _namespace_issues(tool: dict[str, Any], sender: dict[str, Any], target: dict[str, Any],
                      containers: list[dict[str, Any]]) -> list[str]:
    protected = {value for value in (tool.get("Id"), sender.get("Id"), target.get("Id"))
                 if isinstance(value, str) and _CONTAINER_ID.fullmatch(value)}
    known_ids = {
        value for container in containers
        if _CONTAINER_ID.fullmatch(value := str(container.get("Id", "")))
    }
    issues = []
    if len(protected) != 3 or not protected <= known_ids:
        issues.append("protected_namespace_identity_unresolved")
    for container in containers:
        if container.get("Id") in protected:
            continue
        host = container.get("HostConfig") or {}
        for kind, mode in (("network", host.get("NetworkMode")),
                           ("pid", host.get("PidMode")),
                           ("ipc", host.get("IpcMode"))):
            if not isinstance(mode, str) or not mode.startswith("container:"):
                continue
            peer = mode.removeprefix("container:")
            if peer in protected:
                issues.append(f"unapproved_{kind}_namespace_peer")
            elif peer not in known_ids:
                issues.append(f"unresolved_{kind}_namespace_peer")
    return sorted(set(issues))


def check(tool: dict[str, Any], sender: dict[str, Any], target: dict[str, Any], network: dict[str, Any], volume: dict[str, Any], channel_volume: str, network_name: str, run_ref: str, project: str, all_containers: list[dict[str, Any]] | None = None) -> list[str]:
    issues: list[str] = []
    th = tool.get("HostConfig") or {}
    sh = sender.get("HostConfig") or {}
    tn = (tool.get("NetworkSettings") or {}).get("Networks") or {}
    sn = (sender.get("NetworkSettings") or {}).get("Networks") or {}
    tool_ids = _nonroot_ids((tool.get("Config") or {}).get("User"))
    sender_ids = _nonroot_ids((sender.get("Config") or {}).get("User"))

    if (tool.get("State") or {}).get("Running") is not True:
        issues.append("tool_not_running")
    if (sender.get("State") or {}).get("Running") is not True:
        issues.append("sender_not_running")
    for obj, role in ((tool, "tool"), (sender, "sender")):
        pid = (obj.get("State") or {}).get("Pid")
        if type(pid) is not int or pid < 1:
            issues.append(role + "_process_pid_unavailable")
    if (target.get("State") or {}).get("Running") is not True:
        issues.append("target_not_running")
    if not run_ref or not project:
        issues.append("run_binding_missing")
    for obj, role in ((tool, "tool"), (sender, "sender")):
        labels = (obj.get("Config") or {}).get("Labels") or {}
        if labels.get(RUN_LABEL) != run_ref or labels.get(ROLE_LABEL) != role or labels.get("com.docker.compose.project") != project:
            issues.append(role + "_run_binding_mismatch")
    if (network.get("Labels") or {}).get(RUN_LABEL) != run_ref:
        issues.append("target_network_run_mismatch")
    if volume.get("Name") != channel_volume or (volume.get("Labels") or {}).get(RUN_LABEL) != run_ref:
        issues.append("channel_volume_run_mismatch")
    if volume.get("Driver") != "local" or volume.get("Scope") != "local" or volume.get("Options") not in ({}, None):
        issues.append("channel_volume_driver_or_options")

    if th.get("NetworkMode") != "none" or set(tn) != {"none"}:
        issues.append("tool_has_ip_network")
    if th.get("Privileged") or th.get("PidMode") == "host" or th.get("IpcMode") != "private" or _extra_privilege(th):
        issues.append("tool_has_host_privilege")
    if not th.get("ReadonlyRootfs") or "ALL" not in [x.upper() for x in th.get("CapDrop") or []]:
        issues.append("tool_filesystem_or_caps")
    if "no-new-privileges:true" not in (th.get("SecurityOpt") or []):
        issues.append("tool_new_privileges")
    if not isinstance(th.get("PidsLimit"), int) or th["PidsLimit"] < 1:
        issues.append("tool_unbounded_processes")
    if th.get("PortBindings") or _published((tool.get("NetworkSettings") or {}).get("Ports")):
        issues.append("tool_published_port")
    if tool_ids is None:
        issues.append("tool_root_user")
    # The default private PID namespaces remain fail-closed until C can prove
    # exact peer identity and prevent cross-UID /proc metadata disclosure.
    if th.get("PidMode") != "" or th.get("UsernsMode") != "":
        issues.append("tool_pid_or_user_namespace_unapproved")

    mounts = tool.get("Mounts") or []
    if len(mounts) != 1 or mounts[0].get("Destination") != CHANNEL or mounts[0].get("Name") != channel_volume or mounts[0].get("RW") is not False or mounts[0].get("Type") != "volume":
        issues.append("tool_unapproved_mount")
    smounts = sender.get("Mounts") or []
    if len(smounts) != 1 or smounts[0].get("Destination") != CHANNEL or smounts[0].get("Name") != channel_volume or smounts[0].get("RW") is not True or smounts[0].get("Type") != "volume":
        issues.append("sender_channel_mismatch")

    if network.get("Internal") is not True or network.get("Name") != network_name or not network.get("Id") or network.get("Driver") != "bridge" or network.get("Scope") != "local" or network.get("Attachable") is not False:
        issues.append("target_network_not_internal")
    options = network.get("Options") or {}
    if not isinstance(options, dict) or any(options.get("com.docker.network.bridge.gateway_mode_" + family) != "isolated" for family in ("ipv4", "ipv6")):
        issues.append("target_network_gateway_not_isolated")
    if set(sn) != {network_name} or network_name in tn or (sn.get(network_name) or {}).get("NetworkID") != network.get("Id"):
        issues.append("sender_network_mismatch")
    target_networks = (target.get("NetworkSettings") or {}).get("Networks") or {}
    target_ports = (target.get("NetworkSettings") or {}).get("Ports") or {}
    target_host = target.get("HostConfig") or {}
    if set(target_networks) != {network_name}:
        issues.append("target_has_extra_network")
    if target_host.get("PortBindings") or _published(target_ports):
        issues.append("target_published_port")
    members = set((network.get("Containers") or {}).keys())
    expected_members = {sender.get("Id"), target.get("Id")}
    if None in expected_members or len(expected_members) != 2 or members != expected_members or tool.get("Id") in members or (target_networks.get(network_name) or {}).get("NetworkID") != network.get("Id"):
        issues.append("target_network_membership_mismatch")
    if sh.get("Privileged") or sh.get("NetworkMode") == "host" or sh.get("PortBindings") or _published((sender.get("NetworkSettings") or {}).get("Ports")) or _extra_privilege(sh):
        issues.append("sender_host_or_published_port")
    if sender_ids is None or not sh.get("ReadonlyRootfs") or "ALL" not in [x.upper() for x in sh.get("CapDrop") or []] or "no-new-privileges:true" not in (sh.get("SecurityOpt") or []):
        issues.append("sender_privilege_or_filesystem")
    if sh.get("PidMode") != "" or sh.get("UsernsMode") != "" or sh.get("IpcMode") != "private":
        issues.append("sender_namespace_unapproved")
    if tool_ids is not None and sender_ids is not None and (tool_ids[0] == sender_ids[0] or tool_ids[1] == sender_ids[1]):
        issues.append("process_users_not_separated")
    if all_containers is not None:
        issues.extend(_namespace_issues(tool, sender, target, all_containers))
    return sorted(set(issues))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tool", required=True, help="exact tool container name or ID")
    parser.add_argument("--sender", required=True, help="exact sender container name or ID")
    parser.add_argument("--target", required=True, help="exact C-approved target container name or ID")
    parser.add_argument("--network", required=True, help="exact per-run internal network name")
    parser.add_argument("--channel-volume", required=True, help="exact C-prepared external channel volume")
    parser.add_argument("--run-ref", required=True, help="C-verified nonsecret run reference")
    parser.add_argument("--project", required=True, help="exact per-run Compose project")
    parser.add_argument("--docker-cli", help="explicit Docker CLI path/name (default: docker or E2_DOCKER_CLI)")
    args = parser.parse_args()
    try:
        tool = _inspect("container", args.tool, args.docker_cli)
        sender = _inspect("container", args.sender, args.docker_cli)
        target = _inspect("container", args.target, args.docker_cli)
        network = _inspect("network", args.network, args.docker_cli)
        volume = _inspect("volume", args.channel_volume, args.docker_cli)
        containers = _inspect_containers(args.docker_cli)
        issues = check(tool, sender, target, network, volume, args.channel_volume, args.network,
                       args.run_ref, args.project, containers)
        daemon = _daemon_identity(args.docker_cli)
    except (RuntimeError, OSError, json.JSONDecodeError, subprocess.TimeoutExpired):
        print(json.dumps({"status": "FAIL", "issues": ["inspection_unavailable"]}))
        return 1
    print(json.dumps({"status": "PASS" if not issues else "FAIL", "issues": issues,
                      "daemon": daemon}, sort_keys=True))
    return 0 if not issues else 1


if __name__ == "__main__":
    sys.exit(main())
