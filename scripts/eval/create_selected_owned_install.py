#!/usr/bin/env python3
"""Create private, disposable inputs for the selected candidate Compose fixture."""

import json
import os
import pathlib
import secrets
import sys


def main():
    if len(sys.argv) not in (2, 3, 4):
        raise SystemExit("usage: create_selected_owned_install.py <new-private-directory> [network-prefix] [allow-login-post]")
    network_prefix = sys.argv[2] if len(sys.argv) == 3 else "172.31"
    if len(sys.argv) == 4:
        network_prefix = sys.argv[2]
    allow_login_post = len(sys.argv) == 4 and sys.argv[3] == "allow-login-post"
    if len(sys.argv) == 4 and not allow_login_post:
        raise SystemExit("last argument must be allow-login-post")
    if network_prefix not in ("172.31", "10.93"):
        raise SystemExit("network-prefix must be 172.31 or 10.93")
    root = pathlib.Path(sys.argv[1]).resolve()
    root.mkdir(mode=0o700)
    config = {
        "format": "abc2lab.product-install/v2",
        "database_host": f"{network_prefix}.252.10",
        "database_name": "abc2lab",
        "admin_password": secrets.token_urlsafe(28),
        "upstream_ca_file": "/target-tls/ca.pem",
        "target_policy": {
            "format": "abc2lab.product-target-policy/v1",
            "private_cidrs": [f"{network_prefix}.253.0/24"],
            "allowed_ports": [9443],
            "max_requests": 64,
            "max_targets": 2,
            "resolver_address": f"{network_prefix}.252.40:5353",
        },
        "valid_for_hours": 2,
        "max_runs": 8,
        "local_model": {
            "model": "owned-synthetic-selected",
            "base_url": f"http://{network_prefix}.252.30:8080/v1",
            "addresses": [f"{network_prefix}.252.30"],
            "context_window_k": 128,
            "max_tokens": 2048,
        },
    }
    if allow_login_post:
        config["target_policy"]["workflow"] = {
            "format": "abc2lab.workbench-flow/v1", "max_pages": 8,
            "max_endpoints": 32, "max_response_bytes": 65536,
            "allow_reversible_writes": True,
        }
    for name, contents in (("install.json", json.dumps(config)), ("postgres-password", secrets.token_urlsafe(32))):
        path = root / name
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as file:
            file.write(contents)


if __name__ == "__main__":
    main()
