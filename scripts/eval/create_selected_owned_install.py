#!/usr/bin/env python3
"""Create private, disposable inputs for the selected candidate Compose fixture."""

import json
import os
import pathlib
import secrets
import sys


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: create_selected_owned_install.py <new-private-directory>")
    root = pathlib.Path(sys.argv[1]).resolve()
    root.mkdir(mode=0o700)
    config = {
        "format": "abc2lab.product-install/v2",
        "database_host": "172.31.252.10",
        "database_name": "abc2lab",
        "admin_password": secrets.token_urlsafe(28),
        "target_policy": {
            "format": "abc2lab.product-target-policy/v1",
            "private_cidrs": ["172.31.253.0/24"],
            "allowed_ports": [9443],
            "max_requests": 64,
            "max_targets": 2,
            "resolver_address": "172.31.252.40:5353",
            "upstream_ca_file": "/target-tls/ca.pem",
        },
        "valid_for_hours": 2,
        "max_runs": 8,
        "local_model": {
            "model": "owned-synthetic-selected",
            "base_url": "http://172.31.252.30:8080/v1",
            "addresses": ["172.31.252.30"],
            "context_window_k": 128,
            "max_tokens": 2048,
        },
    }
    for name, contents in (("install.json", json.dumps(config)), ("postgres-password", secrets.token_urlsafe(32))):
        path = root / name
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as file:
            file.write(contents)


if __name__ == "__main__":
    main()
