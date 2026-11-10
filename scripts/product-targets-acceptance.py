#!/usr/bin/env python3
"""Owned two-URL Docker acceptance, using real authenticated v2 and report APIs.

Never calls OpenRouter. Only the fixed local fixture DNS control may change DNS.
Pass the protected install JSON, DNS token file and two completed browser draft IDs.
Prints bounded counts/status only; credentials, API bodies and reports stay in memory.
"""
import argparse
import json
import secrets
import time
import urllib.error
import urllib.request
from pathlib import Path


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *_args, **_kwargs):
        return None


opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
checks = 0


def check(ok, code):
    global checks
    if not ok:
        raise RuntimeError(code)
    checks += 1


def main():
    p = argparse.ArgumentParser()
    p.add_argument("phase", choices=("negative", "recover"))
    p.add_argument("install", type=Path)
    p.add_argument("dns_token", type=Path)
    p.add_argument("draft_a")
    p.add_argument("draft_b")
    args = p.parse_args()
    config = json.loads(args.install.read_text())
    check(config["format"] == "abc2lab.product-install/v2", "install_v2_required")
    check(not any(k in config for k in ("target_origin", "target_ip", "path_prefixes")), "fixed_target_install")
    token = ""
    version = "diagnosis-api.v2"
    base = "http://127.0.0.1:49087"

    def request(path, body=None, key=None, expected=200):
        headers = {"Content-Type": "application/json"}
        if token:
            headers["Authorization"] = "Bearer " + token
        if key:
            headers["Idempotency-Key"] = key
        req = urllib.request.Request(base + path, data=None if body is None else json.dumps(body).encode(), headers=headers)
        try:
            with opener.open(req, timeout=15) as r:
                status, raw = r.status, r.read(2 << 20)
        except urllib.error.HTTPError as e:
            status, raw = e.code, e.read(2 << 20)
        check(status == expected, "unexpected_api_status:" + path.split("?")[0] + ":" + str(status))
        return json.loads(raw)

    token = request("/api/auth/login", {"username": "ABC2LAB", "password": config["admin_password"]})["token"]
    check(bool(token), "authenticated_operator_required")
    prefix = "/api/diagnoses/v2/drafts"

    def snapshot():
        out = []
        for port in (49081, 49082, 49084):
            with opener.open(f"http://127.0.0.1:{port}/stats", timeout=5) as r:
                row = json.load(r)
            out.append((row["model_calls"], row["allowed_received"], row["outside_received"]))
        return out

    before = snapshot()
    check(before == [(0, 1, 0), (18, 0, 0), (0, 1, 0)], "two_real_round_counts")
    jobs = [request(prefix + "/" + id + "/status")["draft"] for id in (args.draft_a, args.draft_b)]
    target_ids = []
    for expected_origin, j in zip(("https://juice-a.test:9443", "https://juice-b.test:9443"), jobs):
        check(j["state"] == "completed" and j["result_ready"] and j["drain_complete"], "completed_saved_round")
        scope = j["scope"]
        check(scope["origin"] == expected_origin and scope["path_prefixes"] == ["/rest/products"] and scope["methods"] == ["GET", "HEAD"], "round_scope_isolation")
        target_ids.append(scope["target_ref"])
        task = j["task_id"]
        check(task.isdecimal(), "bound_task_required")
        run = request(f"/api/tasks/{task}/diagnosis-run")["run"]
        check(run["phase"] == "closed" and run["drain_complete"] and run["outcome"] == "completed", "stored_run_closed")
        execution = run["execution"]
        check(execution["audit_complete"], "audit_complete")
        counts = execution["counts"]
        check(all(counts[k] == n for k, n in {"attempts": 2, "allowed": 1, "denied": 1, "forward_attempted": 1, "responses": 1, "unknown": 0, "calls": 2}.items()), "round_egress_counts")
        entries = request("/api/report/diagnostic/remediation?task=" + task)["entries"]
        check(len(entries) == 1, "one_saved_candidate")
    check(target_ids[0] != target_ids[1], "distinct_target_refs")
    if args.phase == "recover":
        check(snapshot() == before, "recovery_caused_execution")
        print(json.dumps({"phase": args.phase, "status": "PASS", "checks": checks, "tasks": len(jobs), "model_calls": 18, "target_received": [1, 1], "qwen": "NOT_RUN"}))
        return

    def create(url, original=None):
        value = original or {"target_url": url, "profile_id": "product-model-local_only"}
        return request(prefix, {"version": version, "input": value}, secrets.token_hex(16), 201)["draft"]

    def start(j, expected=200):
        return request(prefix + "/" + j["id"] + "/start", {"version": version, "expected_revision": j["revision"]}, secrets.token_hex(16), expected)

    for url in ("https://juice.test:9443/rest/products", "https://juice-a.test:9443/outside-approved", "https://169.254.169.254/latest/meta-data"):
        denied = create(url)
        check(denied["state"] == "awaiting_approval" and not denied.get("scope") and not denied.get("task_id") and denied["authority_state"] == "not_issued", "unapproved_url_gained_authority")
        start(denied, 409)
        check(snapshot() == before, "unapproved_url_caused_traffic")

    def dns(ip):
        data = json.dumps({"host": "juice-a.test", "ip": ip}).encode()
        req = urllib.request.Request("http://127.0.0.1:49083/control", data=data, headers={"Content-Type": "application/json", "X-Fixture-Control": args.dns_token.read_text().strip()})
        with opener.open(req, timeout=5) as r:
            check(r.status == 204, "owned_dns_control")

    rebound = create("https://juice-a.test:9443/rest/products")
    check(rebound["state"] == "ready", "approved_scope_not_ready")
    try:
        dns("169.254.169.254")
        start(rebound)
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            failed = request(prefix + "/" + rebound["id"] + "/status")["draft"]
            if failed["state"] in ("failed", "unknown"):
                break
            time.sleep(.1)
        check(failed["state"] == "failed" and failed["reason_code"] == "target_dns_mismatch" and not failed.get("task_id") and failed["authority_state"] == "not_issued", "rebinding_not_fenced_before_preparation")
        check(snapshot() == before, "rebinding_caused_traffic")
    finally:
        dns("172.31.253.20")
    request("/api/product/targets/" + target_ids[0] + "/revoke", {})
    denied = create("https://juice-a.test:9443/rest/products")
    check(denied["state"] == "awaiting_approval" and not denied.get("scope"), "revocation_admitted_new_round")
    # B's exact input and approved record are independent from A's revocation.
    independent = create("", jobs[1]["input"])
    check(independent["state"] == "ready" and independent["source_digest"] == jobs[1]["source_digest"], "revocation_changed_other_target_binding")
    request("/api/tasks/" + jobs[0]["task_id"] + "/diagnosis-run")
    check(snapshot() == before, "negative_checks_caused_traffic")
    print(json.dumps({"phase": args.phase, "status": "PASS", "checks": checks, "tasks": 2, "model_calls": 18, "target_received": [1, 1], "unapproved_start": "BLOCKED", "dns_rebinding": "BLOCKED", "revoked_report": "READABLE", "qwen": "NOT_RUN"}))


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        # Fixed assertion codes are safe; library exceptions may contain credentials.
        print(json.dumps({"status": "FAIL", "reason": str(e) if type(e) is RuntimeError else type(e).__name__}))
        raise SystemExit(1) from None
