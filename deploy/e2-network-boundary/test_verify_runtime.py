"""Synthetic guard tests. They do not prove Docker or A integration."""

import copy
import contextlib
import io
import json
import subprocess
import unittest

from verify_runtime import CHANNEL, check
import probe_network
import verify_runtime
from probe_network import CHILD_BLOCKED_EXIT, CHECK_NAMES, ChildProbeBlocked, attempt, child, expected_failure


VOLUME = "e2-run-123-channel"
NETWORK = "e2-run-123-target"
RUN = "run-123"
PROJECT = "e2-run-123"
NETWORK_ID = "synthetic-network-id"


def objects():
    tool = {
        "Id": "synthetic-tool-id",
        "State": {"Running": True, "Pid": 1001},
        "Config": {"User": "10001:10001", "Labels": {
            "org.abc2lab.e2.run": RUN, "org.abc2lab.e2.role": "tool",
            "com.docker.compose.project": PROJECT}},
        "HostConfig": {
            "NetworkMode": "none", "Privileged": False, "PidMode": "", "UsernsMode": "",
            "IpcMode": "private", "ReadonlyRootfs": True, "CapDrop": ["ALL"],
            "SecurityOpt": ["no-new-privileges:true"], "PidsLimit": 128,
            "PortBindings": {},
        },
        "NetworkSettings": {"Networks": {"none": {}}, "Ports": {}},
        "Mounts": [{"Destination": CHANNEL, "Name": VOLUME, "RW": False, "Type": "volume"}],
    }
    sender = {
        "Id": "synthetic-sender-id",
        "State": {"Running": True, "Pid": 1002},
        "Config": {"User": "10002:10002", "Labels": {
            "org.abc2lab.e2.run": RUN, "org.abc2lab.e2.role": "sender",
            "com.docker.compose.project": PROJECT}},
        "HostConfig": {"NetworkMode": NETWORK, "PidMode": "", "UsernsMode": "", "IpcMode": "private",
                       "Privileged": False, "PortBindings": {},
                       "ReadonlyRootfs": True, "CapDrop": ["ALL"],
                       "SecurityOpt": ["no-new-privileges:true"]},
        "NetworkSettings": {"Networks": {NETWORK: {"NetworkID": NETWORK_ID}}, "Ports": {}},
        "Mounts": [{"Destination": CHANNEL, "Name": VOLUME, "RW": True, "Type": "volume"}],
    }
    target = {"Id": "synthetic-target-id", "State": {"Running": True}, "NetworkSettings": {
        "Networks": {NETWORK: {"NetworkID": NETWORK_ID}}}}
    network = {"Id": NETWORK_ID, "Name": NETWORK, "Internal": True,
               "Driver": "bridge", "Scope": "local", "Attachable": False,
               "Options": {"com.docker.network.bridge.gateway_mode_ipv4": "isolated",
                           "com.docker.network.bridge.gateway_mode_ipv6": "isolated"},
               "Containers": {"synthetic-sender-id": {}, "synthetic-target-id": {}},
               "Labels": {"org.abc2lab.e2.run": RUN}}
    volume = {"Name": VOLUME, "Driver": "local", "Scope": "local", "Options": None,
              "Labels": {"org.abc2lab.e2.run": RUN}}
    return tool, sender, target, network, volume


class RuntimeChecks(unittest.TestCase):
    def setUp(self):
        self.tool, self.sender, self.target, self.network, self.volume = copy.deepcopy(objects())

    def inspect(self):
        return check(self.tool, self.sender, self.target, self.network, self.volume, VOLUME, NETWORK, RUN, PROJECT)

    def test_expected_boundary(self):
        self.assertEqual(self.inspect(), [])

    def test_tool_bridge_or_extra_network_is_rejected(self):
        self.tool["HostConfig"]["NetworkMode"] = "bridge"
        self.assertIn("tool_has_ip_network", self.inspect())
        self.tool["HostConfig"]["NetworkMode"] = "none"
        self.tool["NetworkSettings"]["Networks"][NETWORK] = {}
        self.assertIn("tool_has_ip_network", self.inspect())

    def test_extra_or_writeable_tool_mount_is_rejected(self):
        self.tool["Mounts"].append({"Destination": "/var/run/docker.sock", "RW": True})
        self.assertIn("tool_unapproved_mount", self.inspect())
        self.tool["Mounts"] = [{"Destination": CHANNEL, "Name": VOLUME, "RW": True, "Type": "volume"}]
        self.assertIn("tool_unapproved_mount", self.inspect())

    def test_privilege_and_unbounded_children_are_rejected(self):
        self.tool["HostConfig"]["Privileged"] = True
        self.tool["HostConfig"]["PidsLimit"] = 0
        self.assertIn("tool_has_host_privilege", self.inspect())
        self.assertIn("tool_unbounded_processes", self.inspect())

    def test_cap_add_device_and_unconfined_are_rejected(self):
        self.tool["HostConfig"]["CapAdd"] = ["NET_ADMIN"]
        self.sender["HostConfig"]["DeviceRequests"] = [{"Driver": "nvidia"}]
        self.sender["HostConfig"]["SecurityOpt"] += ["seccomp=unconfined"]
        self.assertIn("tool_has_host_privilege", self.inspect())
        self.assertIn("sender_host_or_published_port", self.inspect())

    def test_sender_must_have_internal_target_network_and_same_channel(self):
        self.network["Internal"] = False
        self.sender["NetworkSettings"]["Networks"] = {"bridge": {}}
        self.sender["Mounts"][0]["Name"] = "wrong-volume"
        issues = self.inspect()
        self.assertIn("target_network_not_internal", issues)
        self.assertIn("sender_network_mismatch", issues)
        self.assertIn("sender_channel_mismatch", issues)

    def test_published_port_and_root_tool_are_rejected(self):
        self.tool["Config"]["User"] = "0:0"
        self.sender["HostConfig"]["PortBindings"] = {"80/tcp": [{"HostPort": "8080"}]}
        self.assertIn("tool_root_user", self.inspect())
        self.assertIn("sender_host_or_published_port", self.inspect())

    def test_sender_root_or_writable_root_is_rejected(self):
        self.sender["Config"]["User"] = "root"
        self.sender["HostConfig"]["ReadonlyRootfs"] = False
        self.assertIn("sender_privilege_or_filesystem", self.inspect())

    def test_pid_namespace_join_or_host_namespace_is_rejected(self):
        self.tool["HostConfig"]["PidMode"] = "container:unapproved-id"
        self.sender["HostConfig"]["PidMode"] = "host"
        self.assertIn("tool_pid_or_user_namespace_unapproved", self.inspect())
        self.assertIn("sender_namespace_unapproved", self.inspect())
        self.tool["HostConfig"]["PidMode"] = "container:" + self.sender["Id"]
        self.assertIn("tool_pid_or_user_namespace_unapproved", self.inspect())

    def test_same_uid_or_gid_and_missing_process_pid_are_rejected(self):
        self.sender["Config"]["User"] = "10001:10002"
        self.tool["State"]["Pid"] = 0
        self.assertIn("process_users_not_separated", self.inspect())
        self.assertIn("tool_process_pid_unavailable", self.inspect())
        self.sender["Config"]["User"] = "10002:10001"
        self.assertIn("process_users_not_separated", self.inspect())

    def test_root_or_implicit_group_and_user_namespace_are_rejected(self):
        self.tool["Config"]["User"] = "10001:0"
        self.sender["Config"]["User"] = "10002"
        self.tool["HostConfig"]["UsernsMode"] = "host"
        self.assertIn("tool_root_user", self.inspect())
        self.assertIn("sender_privilege_or_filesystem", self.inspect())
        self.assertIn("tool_pid_or_user_namespace_unapproved", self.inspect())
        self.tool["Config"]["User"] = "99999999999999999999:10001"
        self.sender["State"]["Pid"] = True
        self.assertIn("tool_root_user", self.inspect())
        self.assertIn("sender_process_pid_unavailable", self.inspect())

    def test_false_no_new_privileges_is_rejected(self):
        self.tool["HostConfig"]["SecurityOpt"] = ["no-new-privileges:false"]
        self.sender["HostConfig"]["SecurityOpt"] = ["no-new-privileges:false"]
        self.assertIn("tool_new_privileges", self.inspect())
        self.assertIn("sender_privilege_or_filesystem", self.inspect())

    def test_sender_extra_host_mount_is_rejected(self):
        self.sender["Mounts"].append({"Destination": "/var/run/docker.sock", "Source": "/var/run/docker.sock", "RW": True})
        self.assertIn("sender_channel_mismatch", self.inspect())

    def test_channel_volume_must_match_run(self):
        self.volume["Labels"]["org.abc2lab.e2.run"] = "other-run"
        self.assertIn("channel_volume_run_mismatch", self.inspect())

    def test_bind_backed_volume_is_rejected(self):
        self.volume["Options"] = {"type": "none", "o": "bind", "device": "/var/run"}
        self.assertIn("channel_volume_driver_or_options", self.inspect())

    def test_unapproved_network_peer_is_rejected(self):
        self.network["Containers"]["unapproved-third-container"] = {}
        self.assertIn("target_network_membership_mismatch", self.inspect())

    def test_unapproved_namespace_peers_are_rejected(self):
        peer = {"Id": "unapproved-peer", "HostConfig": {
            "NetworkMode": "container:" + self.target["Id"],
            "PidMode": "container:" + self.sender["Id"],
        }}
        issues = check(self.tool, self.sender, self.target, self.network, self.volume,
                       VOLUME, NETWORK, RUN, PROJECT, [self.tool, self.sender, self.target, peer])
        self.assertIn("unapproved_network_namespace_peer", issues)
        self.assertIn("unapproved_pid_namespace_peer", issues)

    def test_ipc_namespace_must_be_private(self):
        self.tool["HostConfig"]["IpcMode"] = "shareable"
        self.sender["HostConfig"]["IpcMode"] = "container:other"
        issues = self.inspect()
        self.assertIn("tool_has_host_privilege", issues)
        self.assertIn("sender_namespace_unapproved", issues)

    def test_wrong_target_network_id_is_rejected(self):
        self.target["NetworkSettings"]["Networks"][NETWORK]["NetworkID"] = "other-network"
        self.assertIn("target_network_membership_mismatch", self.inspect())

    def test_nonbridge_or_attachable_target_network_is_rejected(self):
        self.network["Driver"] = "macvlan"
        self.network["Attachable"] = True
        self.assertIn("target_network_not_internal", self.inspect())

    def test_default_internal_bridge_gateway_is_rejected(self):
        self.network["Options"] = {}
        self.assertIn("target_network_gateway_not_isolated", self.inspect())
        self.network["Options"] = {"com.docker.network.bridge.gateway_mode_ipv4": "isolated"}
        self.assertIn("target_network_gateway_not_isolated", self.inspect())

    def test_stopped_target_is_rejected(self):
        self.target["State"]["Running"] = False
        self.assertIn("target_not_running", self.inspect())

    def test_run_project_role_and_network_id_must_match(self):
        self.tool["Config"]["Labels"]["org.abc2lab.e2.run"] = "other-run"
        self.sender["Config"]["Labels"]["com.docker.compose.project"] = "other-project"
        self.network["Labels"]["org.abc2lab.e2.run"] = "other-run"
        self.sender["NetworkSettings"]["Networks"][NETWORK]["NetworkID"] = "other-network-id"
        issues = self.inspect()
        self.assertIn("tool_run_binding_mismatch", issues)
        self.assertIn("sender_run_binding_mismatch", issues)
        self.assertIn("target_network_run_mismatch", issues)
        self.assertIn("sender_network_mismatch", issues)

    def test_unpublished_exposed_port_is_not_a_host_exit(self):
        self.tool["NetworkSettings"]["Ports"] = {"8080/tcp": None}
        self.sender["NetworkSettings"]["Ports"] = {"8080/tcp": None}
        self.assertEqual(self.inspect(), [])

    def test_numeric_zero_and_stopped_containers_are_rejected(self):
        self.tool["Config"]["User"] = "0000:10001"
        self.tool["State"]["Running"] = False
        self.sender["State"]["Running"] = False
        issues = self.inspect()
        self.assertIn("tool_root_user", issues)
        self.assertIn("tool_not_running", issues)
        self.assertIn("sender_not_running", issues)

    def test_probe_reports_unexpected_exception_instead_of_blocked(self):
        result = attempt("direct_ipv4_tcp", lambda: (_ for _ in ()).throw(ValueError("bad probe")))
        self.assertFalse(result["connected_or_sent"])
        self.assertEqual(result["error_kind"], "ValueError")
        self.assertNotEqual(result.get("error_kind"), "OSError")

    def test_probe_check_name_contract_is_exact(self):
        self.assertEqual(len(CHECK_NAMES), 9)
        self.assertEqual(len(set(CHECK_NAMES)), 9)

    def test_probe_rejects_wrong_errno_and_child_exit(self):
        self.assertFalse(expected_failure("direct_ipv4_tcp", {
            "error_kind": "OSError", "errno": 2}))
        self.assertFalse(expected_failure("shell_child", {
            "error_kind": "ChildProbeBlocked", "returncode": 127}))
        self.assertTrue(expected_failure("shell_child", {
            "error_kind": "ChildProbeBlocked", "returncode": CHILD_BLOCKED_EXIT}))

    def test_broken_python_exit_one_is_not_network_blocked(self):
        import subprocess
        result = subprocess.run(["python3", "-c", "if ("], check=False,
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 1)
        self.assertFalse(expected_failure("grandchild", {
            "error_kind": "ChildProbeBlocked", "returncode": result.returncode}))

    def test_child_network_failure_uses_dedicated_exit_code(self):
        import subprocess
        from unittest.mock import patch
        completed = subprocess.CompletedProcess([], CHILD_BLOCKED_EXIT, b"", b"")
        with self.assertRaises(ChildProbeBlocked) as raised:
            with patch("probe_network.subprocess.run", return_value=completed):
                child("127.0.0.1", 1)
        self.assertEqual(raised.exception.returncode, CHILD_BLOCKED_EXIT)

    def test_probe_main_prints_json_for_success_blocked_and_unknown(self):
        import sys
        expected = {
            "direct_ipv4_tcp": {"error_kind": "OSError", "errno": 101},
            "direct_ipv6_tcp": {"error_kind": "OSError", "errno": 101},
            "direct_ipv4_udp": {"error_kind": "OSError", "errno": 101},
            "direct_ipv6_udp": {"error_kind": "OSError", "errno": 101},
            "dns_resolution": {"error_kind": "gaierror", "errno": -2},
            "proxy_replacement": {"error_kind": "URLError", "errno": 101},
            "opaque_connect": {"error_kind": "OSError", "errno": 101},
            "shell_child": {"error_kind": "ChildProbeBlocked", "returncode": 23},
            "grandchild": {"error_kind": "ChildProbeBlocked", "returncode": 23},
        }
        args = ["probe_network.py", "--ipv4", "127.0.0.1", "--ipv6", "::1",
                "--tcp-port", "1", "--udp-port", "2", "--ipv6-tcp-port", "3",
                "--ipv6-udp-port", "4", "--dns-name", "synthetic.invalid"]

        def run_with(results, overrides=None, extra=()):
            overrides = overrides or {}
            output = io.StringIO()
            with unittest.mock.patch.object(sys, "argv", args + list(extra)), \
                    unittest.mock.patch.object(probe_network, "attempt",
                        side_effect=lambda name, action: {"name": name, "connected_or_sent": results[name], **({} if results[name] else overrides.get(name, expected[name]))}), \
                    contextlib.redirect_stdout(output):
                code = probe_network.main()
            return code, json.loads(output.getvalue())

        success_code, success = run_with({name: True for name in CHECK_NAMES}, extra=("--expect-success",))
        self.assertEqual(success_code, 0)
        self.assertTrue(success["positive_control"])
        blocked_code, blocked = run_with({name: False for name in CHECK_NAMES})
        self.assertEqual(blocked_code, 0)
        self.assertEqual(blocked["check_names"], list(CHECK_NAMES))
        self.assertFalse(blocked["unexpected_errors"])
        unknown = {name: False for name in CHECK_NAMES}
        unknown["direct_ipv4_tcp"] = False
        output_code, output = run_with(unknown, {"direct_ipv4_tcp": {"error_kind": "ValueError", "errno": None}})
        self.assertEqual(output_code, 1)
        self.assertEqual(output["unexpected_errors"][0]["error_kind"], "ValueError")

    def test_daemon_identity_requires_nonblank_fields(self):
        import json as json_module
        from unittest.mock import patch
        completed = subprocess.CompletedProcess([], 0, json_module.dumps({
            "ID": "", "Name": "daemon", "ServerVersion": "29", "OperatingSystem": "Linux",
        }), "")
        with patch("verify_runtime.subprocess.run", return_value=completed):
            with self.assertRaises(RuntimeError) as raised:
                verify_runtime._daemon_identity("docker")
        self.assertEqual(str(raised.exception), "docker_daemon_identity_fields")


if __name__ == "__main__":
    unittest.main()
