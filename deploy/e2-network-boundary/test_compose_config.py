"""Validate Compose's rendered B boundary without starting containers."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import unittest


class ComposeConfig(unittest.TestCase):
    def test_rendered_process_separation(self):
        docker = shutil.which(os.environ.get("E2_DOCKER_CLI", "docker"))
        if not docker:
            self.skipTest("Docker Compose CLI unavailable")
        env = dict(os.environ)
        synthetic = {
            "E2_TOOL_IMAGE": "synthetic-tool:unbuilt",
            "E2_SENDER_IMAGE": "synthetic-sender:unbuilt",
            "E2_TOOL_UID": "10001", "E2_TOOL_GID": "10001",
            "E2_SENDER_UID": "10002", "E2_SENDER_GID": "10002",
            "E2_CHANNEL_VOLUME": "synthetic-channel",
            "E2_TARGET_NETWORK": "synthetic-internal-target",
            "E2_RUN_REF": "synthetic-run",
        }
        env.update(synthetic)
        # WSL only forwards explicitly listed variables to Windows executables.
        env["WSLENV"] = ":".join(filter(None, [env.get("WSLENV", ""), *synthetic]))
        result = subprocess.run(
            [docker, "compose", "-f", str(Path(__file__).with_name("compose.yaml")),
             "config", "--format", "json"], env=env, text=True,
            capture_output=True, timeout=15, check=False,
        )
        self.assertEqual(result.returncode, 0, "Compose render failed: " + result.stderr[:500])
        config = json.loads(result.stdout)
        tool = config["services"]["tool"]
        sender = config["services"]["sender"]
        self.assertEqual(tool["network_mode"], "none")
        self.assertEqual(tool["labels"]["org.abc2lab.e2.run"], "synthetic-run")
        self.assertEqual(sender["labels"]["org.abc2lab.e2.run"], "synthetic-run")
        self.assertNotIn("networks", tool)
        self.assertEqual(tool["cap_drop"], ["ALL"])
        self.assertTrue(tool["read_only"])
        self.assertFalse(tool.get("privileged", False))
        self.assertNotIn("ports", tool)
        self.assertEqual(len(tool["volumes"]), 1)
        self.assertEqual(tool["volumes"][0]["target"], "/run/e2-channel")
        self.assertTrue(tool["volumes"][0]["read_only"])
        self.assertEqual(set(sender["networks"]), {"registered_target"})
        self.assertNotIn("ports", sender)
        self.assertFalse(sender["volumes"][0].get("read_only", False))
        self.assertTrue(config["networks"]["registered_target"]["external"])
        self.assertTrue(config["volumes"]["channel"]["external"])


if __name__ == "__main__":
    unittest.main()
