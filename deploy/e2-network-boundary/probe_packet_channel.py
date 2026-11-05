"""Bounded A-wire probe for a disposable tool process; it grants no authority."""

from __future__ import annotations

import argparse
import json
import re
import socket
import sys

CHANNEL = "/run/e2-channel/registered.sock"
READY = re.compile(rb"E2NB/1 ([0-9a-f]{32})\Z")
ACK_KEYS = {"version", "state", "reason", "admitted", "response_received"}
STATES = {"blocked_pre_send", "send_partial_or_unknown", "response_observed", "audit_unknown"}
REASONS = {"", "unsupported_path", "approval_unavailable", "revoked", "expired",
           "binding_mismatch", "budget_unavailable", "budget_exhausted",
           "audit_unavailable", "transport_unknown"}


def request(account_ref: str, resource_ref: str) -> dict[str, object]:
    for ref in (account_ref, resource_ref):
        if not ref or len(ref.encode("utf-8")) > 256 or any(ord(char) < 32 or ord(char) == 127 for char in ref):
            raise ValueError("invalid_reference")
    with socket.socket(socket.AF_UNIX, socket.SOCK_SEQPACKET) as conn:
        conn.settimeout(5)
        conn.connect(CHANNEL)
        ready = conn.recv(64)
        match = READY.fullmatch(ready)
        if match is None:
            raise ValueError("invalid_ready_packet")
        payload = json.dumps({"version": 1, "account_ref": account_ref,
                              "resource_ref": resource_ref,
                              "nonce": match.group(1).decode("ascii")},
                             separators=(",", ":")).encode("utf-8")
        if len(payload) > 4096 or conn.send(payload) != len(payload):
            raise ValueError("request_packet_failed")
        ack = json.loads(conn.recv(4096))
    if (not isinstance(ack, dict) or set(ack) != ACK_KEYS or
            type(ack["version"]) is not int or ack["version"] != 1 or
            not isinstance(ack["state"], str) or ack["state"] not in STATES or
            not isinstance(ack["reason"], str) or ack["reason"] not in REASONS or
            type(ack["admitted"]) is not bool or type(ack["response_received"]) is not bool):
        raise ValueError("invalid_ack_packet")
    # Only the bounded A ACK fields are returned. Never return a URL, payload,
    # credential, or raw socket error.
    return {"status": "PASS", "scope": "packet_ack_only", "ack": ack}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--account-ref", required=True)
    parser.add_argument("--resource-ref", required=True)
    args = parser.parse_args()
    try:
        result = request(args.account_ref, args.resource_ref)
    except (OSError, ValueError, UnicodeError, json.JSONDecodeError):
        print(json.dumps({"status": "FAIL", "scope": "packet_ack_only",
                          "issue": "packet_channel_unavailable"}))
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
