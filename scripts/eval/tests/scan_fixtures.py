"""Shared synthetic scan-record fixtures for parser tests."""

from __future__ import annotations


def record(oracle_value: str, resource_ref: str, **overrides: object) -> dict:
    """Contract-shaped synthetic record with no real scan data."""
    rec: dict = {
        "id": 1,
        "case_ref": f"case-{resource_ref}",
        "axis": "horizontal",
        "oracle_value": oracle_value,
        "vuln_verdict": None if oracle_value == "allowed" else oracle_value,
        "policy_check": "yes",
        "identity_check": "yes",
        "data_check": "yes",
        "reason_codes": ["policy_deny_resolved"],
        "canary_fields": ["email"],
        "owner_ref": "owner",
        "evaluated_ref": "evaluated",
        "vuln_class": "idor",
        "resource_ref": resource_ref,
        "model_tag": "test-model",
    }
    rec.update(overrides)
    return rec
