"""Deterministic property checks for the snapshot endpoint setting boundary.

These tests deliberately stop at ``_endpoint``: validating a setting must not
open a socket, consult DNS, or touch the snapshot output directory.
"""

from __future__ import annotations

import builtins
import math
import os
import socket
import subprocess
import sys
import unittest
from dataclasses import replace
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import snapshot_collector as collector
from snapshot_collector import SnapshotCollectionError, SnapshotEndpoint


MARKER = "SYNTHETIC-ENDPOINT-CREDENTIAL-MARKER"


VALID_ORIGINS = (
    ("http://127.0.0.1", ("127.0.0.1", 80, "127.0.0.1:80")),
    ("http://127.0.0.1/", ("127.0.0.1", 80, "127.0.0.1:80")),
    ("http://127.0.0.1:1", ("127.0.0.1", 1, "127.0.0.1:1")),
    ("http://127.0.0.1:65535", ("127.0.0.1", 65535, "127.0.0.1:65535")),
    ("http://127.0.0.2:1234", ("127.0.0.2", 1234, "127.0.0.2:1234")),
    ("http://LOCALHOST", ("127.0.0.1", 80, "localhost:80")),
    ("http://[::1]", ("::1", 80, "[::1]:80")),
    ("http://[::1]:4567", ("::1", 4567, "[::1]:4567")),
    ("http://[0:0:0:0:0:0:0:1]", ("::1", 80, "[0:0:0:0:0:0:0:1]:80")),
)
VALID_PATHS = ("/api/state", "/instrumentation/state", "/api/v1/state")
VALID_TIMEOUTS = (0.125, 1, 5.5)
VALID_MAX_BYTES = (1, 1024, sys.maxsize - 1)


class SnapshotEndpointPropertiesTest(unittest.TestCase):
    def setUp(self) -> None:
        self._guards = [
            patch.object(socket, "socket", side_effect=AssertionError("network access")),
            patch.object(socket, "create_connection", side_effect=AssertionError("network access")),
            patch.object(socket, "getaddrinfo", side_effect=AssertionError("DNS access")),
            patch.object(subprocess, "Popen", side_effect=AssertionError("process access")),
            patch.object(subprocess, "run", side_effect=AssertionError("process access")),
            patch.object(subprocess, "call", side_effect=AssertionError("process access")),
            patch.object(subprocess, "check_call", side_effect=AssertionError("process access")),
            patch.object(subprocess, "check_output", side_effect=AssertionError("process access")),
            patch.object(collector.os, "open", side_effect=AssertionError("filesystem access")),
            patch.object(builtins, "open", side_effect=AssertionError("filesystem access")),
        ]
        for guard in self._guards:
            guard.start()
            self.addCleanup(guard.stop)

    def assert_valid(self, config: SnapshotEndpoint, expected: tuple[str, int, str]) -> None:
        before = replace(config)
        self.assertEqual(collector._endpoint(config), expected)
        self.assertEqual(config, before)

    def assert_rejected(self, config: object) -> None:
        before = replace(config) if isinstance(config, SnapshotEndpoint) else config
        with self.assertRaises(SnapshotCollectionError) as raised:
            collector._endpoint(config)  # type: ignore[arg-type]
        self.assertNotIn(MARKER, str(raised.exception))
        if isinstance(config, SnapshotEndpoint):
            self.assertEqual(config.origin, before.origin)
            self.assertEqual(config.path, before.path)
            self.assertEqual(config.max_bytes, before.max_bytes)
            if isinstance(config.timeout_seconds, float) and math.isnan(config.timeout_seconds):
                self.assertTrue(math.isnan(before.timeout_seconds))
            else:
                self.assertEqual(config.timeout_seconds, before.timeout_seconds)

    def test_valid_loopback_origins_paths_and_limits_form_explicit_tuples(self) -> None:
        cases = 0
        for origin, expected in VALID_ORIGINS:
            for path in VALID_PATHS:
                for timeout in VALID_TIMEOUTS:
                    for max_bytes in VALID_MAX_BYTES:
                        with self.subTest(origin=origin, path=path, timeout=timeout, max_bytes=max_bytes):
                            self.assert_valid(
                                SnapshotEndpoint(origin, path, timeout, max_bytes), expected
                            )
                            cases += 1
        self.assertEqual(cases, 243)

    def test_invalid_origin_matrix_rejects_without_echoing_sensitive_values(self) -> None:
        invalid_origins = (
            f"http://user:{MARKER}@127.0.0.1",
            f"http://127.0.0.1?token={MARKER}",
            f"http://127.0.0.1#{MARKER}",
            "http://127.0.0.1/registered-base",
            "https://127.0.0.1",
            "http://192.0.2.1",
            "http://example.test",
            "http://127.0.0.%31",
            "http://127.0.0.1:0",
            "http://127.0.0.1:65536",
            "http://127.0.0.1:not-a-port",
            "http://[::1",
            "http://127.0.0.1\nX-Synthetic: marker",
            "http://로컬호스트",
            "file:///tmp/synthetic",
            "http:///missing-host",
        )
        for origin in invalid_origins:
            with self.subTest(origin=origin):
                self.assert_rejected(SnapshotEndpoint(origin, "/api/state", 1.0, 1024))
        self.assertEqual(len(invalid_origins), 16)

    def test_invalid_path_matrix_rejects_without_echoing_sensitive_values(self) -> None:
        invalid_paths = (
            "",
            "relative/path",
            "//other-origin/path",
            "http://other-origin/path",
            "https://other-origin/path",
            f"/api/state?token={MARKER}",
            f"/api/state#{MARKER}",
            "/api\\state",
            "/경로",
            "/api\nX-Synthetic: marker",
            "/api\x00state",
            "/api state",
        )
        for path in invalid_paths:
            with self.subTest(path=path):
                self.assert_rejected(SnapshotEndpoint("http://127.0.0.1", path, 1.0, 1024))
        self.assertEqual(len(invalid_paths), 12)

    def test_control_characters_and_nonstring_fields_are_rejected(self) -> None:
        origin_base = "http://127.0.0.1"
        path_base = "/api/state"
        controls = tuple(range(33)) + (127,)
        for codepoint in controls:
            char = chr(codepoint)
            variants = (
                ("origin-prefix", SnapshotEndpoint(char + origin_base, path_base, 1.0, 1024)),
                ("origin-middle", SnapshotEndpoint(origin_base[:10] + char + origin_base[10:], path_base, 1.0, 1024)),
                ("origin-suffix", SnapshotEndpoint(origin_base + char, path_base, 1.0, 1024)),
                ("path-prefix", SnapshotEndpoint(origin_base, char + path_base, 1.0, 1024)),
                ("path-middle", SnapshotEndpoint(origin_base, path_base[:4] + char + path_base[4:], 1.0, 1024)),
                ("path-suffix", SnapshotEndpoint(origin_base, path_base + char, 1.0, 1024)),
            )
            for position, config in variants:
                with self.subTest(codepoint=codepoint, position=position):
                    self.assert_rejected(config)

        for field, config in (
            ("origin-none", SnapshotEndpoint(None, path_base, 1.0, 1024)),
            ("origin-number", SnapshotEndpoint(127001, path_base, 1.0, 1024)),
            ("path-none", SnapshotEndpoint(origin_base, None, 1.0, 1024)),
            ("path-number", SnapshotEndpoint(origin_base, 123, 1.0, 1024)),
        ):
            with self.subTest(field=field):
                self.assert_rejected(config)

    def test_invalid_limits_and_config_types_are_rejected(self) -> None:
        invalid_timeouts = (0, -1, -0.1, math.nan, math.inf, -math.inf, True, "1", None)
        for timeout in invalid_timeouts:
            with self.subTest(timeout=timeout):
                self.assert_rejected(SnapshotEndpoint("http://127.0.0.1", "/api/state", timeout, 1024))

        invalid_max_bytes = (0, -1, True, 1.0, "1024", None, sys.maxsize)
        for max_bytes in invalid_max_bytes:
            with self.subTest(max_bytes=max_bytes):
                self.assert_rejected(SnapshotEndpoint("http://127.0.0.1", "/api/state", 1.0, max_bytes))

        for config in (None, {}, object(), MARKER):
            with self.subTest(config_type=type(config).__name__):
                self.assert_rejected(config)

    def test_private_validation_has_no_network_or_filesystem_side_effect(self) -> None:
        config = SnapshotEndpoint("http://LOCALHOST:8080", "/api/state", 1.0, 1024)
        with patch.object(collector.http.client, "HTTPConnection") as connection, patch.object(
            collector.os, "open"
        ) as open_directory:
            self.assert_valid(config, ("127.0.0.1", 8080, "localhost:8080"))
            self.assert_rejected(SnapshotEndpoint("http://example.test", "/api/state", 1.0, 1024))
        connection.assert_not_called()
        open_directory.assert_not_called()

    def test_mutation_guards_require_rejection_to_be_real(self) -> None:
        valid_result = ("127.0.0.1", 80, "127.0.0.1:80")
        invalid_origin = SnapshotEndpoint(f"http://example.test?token={MARKER}", "/api/state", 1.0, 1024)
        invalid_path = SnapshotEndpoint("http://127.0.0.1", f"/api?token={MARKER}", 1.0, 1024)
        invalid_limit = SnapshotEndpoint("http://127.0.0.1", "/api/state", 0, 1024)
        original = collector._endpoint

        def origin_mutant(config: SnapshotEndpoint) -> tuple[str, int, str]:
            return original(replace(config, origin="http://127.0.0.1"))

        def path_mutant(config: SnapshotEndpoint) -> tuple[str, int, str]:
            return original(replace(config, path="/api/state"))

        def limit_mutant(config: SnapshotEndpoint) -> tuple[str, int, str]:
            return original(replace(config, timeout_seconds=1.0))

        for name, invalid, mutant in (
            ("origin", invalid_origin, origin_mutant),
            ("path", invalid_path, path_mutant),
            ("limits", invalid_limit, limit_mutant),
        ):
            with self.subTest(mutant=name):
                self.assert_rejected(invalid)
                # Each weakened validator changes only its named field. The
                # mutated path must genuinely produce the known valid tuple
                # before the original rejection oracle is expected to fail.
                self.assertEqual(mutant(invalid), valid_result)
                with patch.object(collector, "_endpoint", side_effect=mutant):
                    self.assertEqual(collector._endpoint(invalid), valid_result)
                    with self.assertRaises(AssertionError):
                        self.assert_rejected(invalid)


if __name__ == "__main__":
    unittest.main()
