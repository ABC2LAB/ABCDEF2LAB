#!/usr/bin/env python3
"""Exercise one owned selected-candidate product run through the actual APIs.

Only a loopback product, DNS controller, model and target fixture are allowed.
Credentials, target responses and report bodies remain in memory.
"""

import argparse
import datetime
import io
import json
import os
import re
import secrets
import subprocess
import time
import urllib.error
import urllib.request
import zipfile
from pathlib import Path


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *_args, **_kwargs):
        return None


OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())


def request(base, path, token="", body=None, key="", control="", expected=200, binary=False, method=None,
            return_status=False):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    if key:
        headers["Idempotency-Key"] = key
    if control:
        headers["X-Fixture-Control"] = control
    req = urllib.request.Request(base + path, data=None if body is None else json.dumps(body).encode(),
                                 headers=headers, method=method)
    try:
        with OPENER.open(req, timeout=20) as response:
            status, raw = response.status, response.read(4 << 20)
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read(4 << 20)
    if status not in (expected if isinstance(expected, tuple) else (expected,)):
        try:
            reason = json.loads(raw).get("error", "")
        except (ValueError, AttributeError):
            reason = ""
        raise RuntimeError("unexpected_status:" + path.split("?")[0] + ":" + str(status) + ":" + str(reason)[:80])
    result = raw if binary else json.loads(raw) if raw else {}
    return (status, result) if return_status else result


def await_fixture_count(base, field, minimum, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if request(base, "/stats")[field] >= minimum:
            return
        time.sleep(.1)
    raise RuntimeError("fixture_event_not_observed:" + field)


def kill_and_restart_owned_preparation(project, install, head, allow_open_head=False):
    name = f"{project}-preparation-1"
    for command in (["docker", "kill", "--signal=KILL", name], ["docker", "start", name]):
        done = subprocess.run(command, capture_output=True, text=True, check=False, timeout=40)
        if done.returncode != 0:
            raise RuntimeError("owned_preparation_restart_failed")
    # API, E2 Host and workbench share preparation's network namespace. A plain
    # docker restart leaves them bound to the old namespace after preparation
    # is recreated, so the owned Compose sidecars must be recreated as well.
    directory = Path(__file__).resolve().parents[2] / "deploy" / "product"
    files = [directory / f".compose.pr950-{part}-local.yaml"
             for part in ("base", "acceptance", "selected", "scenario")]
    if not all(path.is_file() for path in files):
        raise RuntimeError("owned_compose_files_unavailable")
    image = subprocess.run(["docker", "inspect", "--format", "{{.Config.Image}}", name],
                           capture_output=True, text=True, check=False, timeout=10)
    if image.returncode != 0 or not image.stdout.strip().startswith("abc2lab-product:"):
        raise RuntimeError("owned_product_image_unavailable")
    environment = os.environ.copy()
    environment.update({"PRODUCT_SOURCE_SHA": head, "PRODUCT_TAG": image.stdout.strip().split(":", 1)[1],
                        "PRODUCT_INSTALL_FILE": str(install),
                        "PRODUCT_POSTGRES_PASSWORD_FILE": str(install.parent / "postgres-password")})
    command = ["docker", "compose", "-p", project]
    for path in files:
        command += ["-f", str(path)]
    command += ["up", "-d", "--no-deps", "--force-recreate", "--wait", "e2-host", "api", "workbench"]
    done = subprocess.run(command, capture_output=True, text=True, check=False, timeout=120, env=environment)
    if done.returncode != 0:
        if allow_open_head:
            host_log = subprocess.run(["docker", "logs", "--tail", "20", f"{project}-e2-host-1"],
                                      capture_output=True, text=True, check=False, timeout=10)
            if host_log.returncode == 0 and "E② 현재 epoch를 확인할 수 없습니다" in host_log.stderr:
                return False  # Unverified in-flight intent keeps Host admission closed.
        raise RuntimeError("owned_sidecar_rebind_failed")
    return True


def verify_new_selection_after_restart(product, token, route, evidence_digest, previous_id, target_stats, model_stats,
                                       expected_requests, host_recovered=True):
    before_target = request(target_stats, "/stats")["allowed_received"]
    before_model = request(model_stats, "/stats")["model_calls"]
    selected = request(product, route, token, body={"request_id": "recovery-" + secrets.token_hex(8),
                                              "evidence_digest": evidence_digest, "method": "llm/v1"})
    item = selected["verifications"][0]
    if item["id"] == previous_id:
        raise RuntimeError("recovery_selection_reused_interrupted_request")
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        item = request(product, route, token)["verifications"][0]
        if item["latest"]["process_state"] in ("analysis_ready", "completed", "failed", "cancelled", "expired"):
            break
        time.sleep(.5)
    if item["latest"]["process_state"] != "analysis_ready":
        raise RuntimeError("recovery_analysis_not_ready")
    planned = request(product, route + "/" + item["id"] + "/plan", token,
                      body={"analysis_digest": item["analysis"]["digest"]})
    if len(planned["plan"]["actions"]) != expected_requests:
        raise RuntimeError("recovery_plan_changed")
    request(product, route + "/" + item["id"] + "/decision", token,
            body={"plan_digest": planned["plan_digest"], "decision": "approved"})
    deadline = time.monotonic() + 150
    while time.monotonic() < deadline:
        item = request(product, route, token)["verifications"][0]
        if item["latest"]["process_state"] in ("completed", "failed", "cancelled", "expired"):
            break
        time.sleep(.5)
    if not host_recovered:
        if item["latest"]["process_state"] != "failed" or item["latest"].get("reason") != "candidate_execution_unavailable":
            raise RuntimeError("open_head_did_not_block_new_execution")
        target_sends = request(target_stats, "/stats")["allowed_received"] - before_target
        model_calls = request(model_stats, "/stats")["model_calls"] - before_model
        if target_sends != 0 or model_calls != 1 or item["latest"].get("verdict") in ("model_supported", "model_refuted"):
            raise RuntimeError("open_head_failure_sent_or_promoted")
        return {"new_verification_id": item["id"], "approved_target_sends": 0,
                "model_calls": model_calls, "reason": item["latest"]["reason"], "host_recovered": False}
    if item["latest"]["process_state"] != "completed" or item["latest"].get("verdict") != "model_supported":
        raise RuntimeError("recovery_followup_not_completed:" + item["latest"]["process_state"])
    target_sends = request(target_stats, "/stats")["allowed_received"] - before_target
    model_calls = request(model_stats, "/stats")["model_calls"] - before_model
    if target_sends != expected_requests or model_calls != 2 or len(item["latest"].get("sources", [])) == 0:
        raise RuntimeError("recovery_followup_counts_invalid")
    return {"new_verification_id": item["id"], "approved_target_sends": target_sends, "model_calls": model_calls,
            "verdict": item["latest"]["verdict"]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("install", type=Path)
    parser.add_argument("dns_token", type=Path)
    parser.add_argument("--product-port", type=int, default=49587)
    parser.add_argument("--dns-port", type=int, default=49583)
    parser.add_argument("--target-stats-port", type=int, default=49581)
    parser.add_argument("--model-stats-port", type=int, default=49582)
    parser.add_argument("--head", required=True, help="Full source commit used for the product image")
    parser.add_argument("--project", default="pr950-selected93", help="Owned Compose project name")
    parser.add_argument("--scenario", choices=("S01", "S02", "S03", "S04", "S05", "S06", "S07", "S07B", "S07P", "S07R", "S08", "S09", "S09C", "S09R", "S09M"), default="S02")
    parser.add_argument("--resume-task", type=int, help="Continue an owned fixture task after a harness failure")
    parser.add_argument("--reuse-target", action="store_true", help="Use the previously approved owned fixture target")
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9a-f]{40}", args.head) or not re.fullmatch(r"[a-z0-9-]{3,40}", args.project):
        raise RuntimeError("invalid_owned_image_identity")
    for service in ("preparation", "synthetic-model"):
        result = subprocess.run(["docker", "inspect", "--format",
                                 '{{ index .Config.Labels "org.opencontainers.image.revision" }}',
                                 f"{args.project}-{service}-1"], capture_output=True, text=True, check=False)
        if result.returncode != 0 or result.stdout.strip() != args.head:
            raise RuntimeError("owned_image_revision_mismatch:" + service)
    config = json.loads(args.install.read_text(encoding="utf-8"))
    if config.get("format") != "abc2lab.product-install/v2" or "openrouter_manifest" in config:
        raise RuntimeError("owned_local_install_required")
    network_prefix = ".".join(config["database_host"].split(".")[:2])
    product = f"http://127.0.0.1:{args.product_port}"
    dns = f"http://127.0.0.1:{args.dns_port}"
    target_stats = f"http://127.0.0.1:{args.target_stats_port}"
    model_stats = f"http://127.0.0.1:{args.model_stats_port}"
    token = request(product, "/api/auth/login", body={"username": "ABC2LAB", "password": config["admin_password"]})["token"]
    before_target = request(target_stats, "/stats")["allowed_received"]
    before_logins = request(target_stats, "/stats")["auth_logins"]
    before_model = request(model_stats, "/stats")["model_calls"]
    if args.resume_task:
        task = str(args.resume_task)
    else:
        if not args.reuse_target:
            target = request(product, "/api/product/targets", token, body={
                "url": "https://synthetic.test:9443/rest/products/search",
                "path_prefixes": ["=/rest/products/search"] + (["=/login"] if args.scenario in ("S03", "S04") else []),
                "methods": ["GET", "HEAD"] + (["POST"] if args.scenario in ("S03", "S04") else []),
                "readonly_basis": "Owned isolated synthetic target; this read path has no state changes.",
                "requests": 64, "valid_hours": 2, "reversible_writes": args.scenario in ("S03", "S04"),
            })["target"]
            control = args.dns_token.read_text(encoding="utf-8").strip()
            request(dns, "/control", body={"name": target["challenge_name"], "values": [target["challenge_value"]],
                    "host": "synthetic.test", "ip": network_prefix + ".253.20"}, control=control, expected=204)
            request(product, "/api/product/targets/" + target["id"] + "/approve", token, body={})
            if args.scenario in ("S03", "S04"):
                account_base = "/api/product/targets/" + target["id"]
                request(product, account_base + "/login", token, body={"method": "POST", "path": "/login",
                    "format": "json", "username_field": "username", "password_field": "password",
                    "json_field": "auth_token", "header": "Authorization", "prefix": "Bearer "}, method="PUT")
                request(product, account_base + "/accounts", token, body={"label": "owned-a"})
                # 계정 분류 폐지(2026-11-26): 자격이 저장된 등록 계정을 결속한다. 시험 계정 표시(mark-test)는 없다
                request(product, account_base + "/accounts/owned-a/credential", token,
                        body={"username": "owned-a", "password": "synthetic-secret-a"}, method="PUT")
        draft = request(product, "/api/diagnoses/v2/drafts", token, body={"version": "diagnosis-api.v2", "input": {
            "target_url": "https://synthetic.test:9443/rest/products/search", "profile_id": "product-model-local_only",
            "display_name": "Owned selected candidate acceptance"}}, key=secrets.token_hex(16), expected=201)["draft"]
        prefix = "/api/diagnoses/v2/drafts/" + draft["id"]
        draft = request(product, prefix + "/validate", token, body={"version": "diagnosis-api.v2", "expected_revision": draft["revision"]})["draft"]
        if draft["state"] != "ready":
            raise RuntimeError("draft_not_ready:" + str(draft.get("reason_code")))
        request(product, prefix + "/start", token, body={"version": "diagnosis-api.v2", "expected_revision": draft["revision"]}, key=secrets.token_hex(16))
        deadline = time.monotonic() + 240
        while time.monotonic() < deadline:
            draft = request(product, prefix + "/status", token)["draft"]
            if draft["state"] in ("completed", "failed", "cancelled", "unknown"):
                break
            time.sleep(.5)
        if draft["state"] != "completed" or not draft.get("drain_complete"):
            raise RuntimeError("diagnosis_not_completed:" + draft["state"] + ":" + str(draft.get("reason_code")))
        task = draft["task_id"]
    run = request(product, "/api/tasks/" + task + "/diagnosis-run", token)["run"]
    if run["outcome"] != "completed" or not run["execution"]["audit_complete"]:
        raise RuntimeError("diagnosis_audit_incomplete")
    after_diagnosis_target = request(target_stats, "/stats")["allowed_received"]
    after_diagnosis_logins = request(target_stats, "/stats")["auth_logins"]
    after_diagnosis_model = request(model_stats, "/stats")["model_calls"]
    remediation = request(product, "/api/report/diagnostic/remediation?task=" + task + "&audience=internal", token)
    entries = remediation["entries"]
    candidates = [row for row in entries if str(row.get("entry_id", "")).startswith("candidate-")]
    if len(candidates) != 1:
        raise RuntimeError("expected_one_candidate")
    candidate_id = str(candidates[0]["entry_id"]).removeprefix("candidate-")
    route = "/api/product/tasks/" + task + "/candidates/" + candidate_id + "/verifications"
    history = request(product, route, token)
    selection_body = {"request_id": "owned-" + secrets.token_hex(8),
        "evidence_digest": history["evidence_digest"], "method": "llm/v1"}
    selection = request(product, route, token, body=selection_body)
    verification = selection["verifications"][0]["id"]
    if args.scenario == "S09":
        repeated = request(product, route, token, body=selection_body)
        if repeated["verifications"][0]["id"] != verification:
            raise RuntimeError("selection_idempotency_failed")
        request(product, route, token, body={**selection_body, "request_id": "other-" + secrets.token_hex(8)}, expected=409)
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        history = request(product, route, token)
        latest = history["verifications"][0]["latest"]
        if latest["process_state"] in ("analysis_ready", "completed", "failed", "cancelled", "expired"):
            break
        time.sleep(.5)
    if args.scenario == "S07B" and latest["process_state"] == "failed" and latest.get("reason") == "budget_unavailable":
        pass
    elif args.scenario == "S01" and latest["process_state"] == "completed":
        pass
    elif latest["process_state"] != "analysis_ready":
        raise RuntimeError("analysis_not_ready:" + latest["process_state"])
    pending_target = request(target_stats, "/stats")["allowed_received"]
    pending_model = request(model_stats, "/stats")["model_calls"]
    decision_status = None
    if args.scenario in ("S02", "S03", "S04", "S05", "S06", "S07", "S07P", "S07R", "S08", "S09", "S09C", "S09R", "S09M"):
        analysis = history["verifications"][0]["analysis"]
        plan_route = route + "/" + verification + "/plan"
        if args.scenario == "S06":
            request(product, plan_route, token, body={"analysis_digest": analysis["digest"], "body": "synthetic-write"}, expected=400)
            decision_status, denied = request(product, plan_route, token, body={"analysis_digest": analysis["digest"]},
                                              expected=409, return_status=True)
            if denied.get("error") != "candidate_action_method_unsupported":
                raise RuntimeError("unsupported_write_not_rejected_at_plan")
        else:
            plan_response = request(product, plan_route, token, body={"analysis_digest": analysis["digest"]})
        if args.scenario != "S06":
            plan = plan_response["plan"]
        expected_actions = 0 if args.scenario == "S06" else 2 if args.scenario in ("S05", "S07R", "S09C", "S09R") else 1
        expected_requests = 2 if args.scenario in ("S03", "S04", "S05", "S07R", "S09C", "S09R") else 1
        if args.scenario != "S06" and (plan["max_requests"] != expected_requests or len(plan["actions"]) != expected_actions):
            raise RuntimeError("unreviewable_plan")
        if args.scenario in ("S05", "S07R", "S09C", "S09R") and (plan["actions"][1].get("kind") != "normal_control" or
                                         plan["actions"][1].get("query") != {"view": "summary"} or
                                         plan["actions"][1].get("headers") != {"Accept-Language": "en-US"}):
            raise RuntimeError("structured_plan_not_bound")
        if args.scenario == "S06":
            pass
        elif args.scenario == "S09":
            restarted = subprocess.run(["docker", "restart", f"{args.project}-api-1"],
                                      capture_output=True, text=True, check=False, timeout=40)
            if restarted.returncode != 0:
                raise RuntimeError("owned_api_restart_failed")
            ready = False
            for _ in range(60):
                try:
                    persisted = request(product, route, token)
                    ready = persisted["verifications"][0]["id"] == verification and \
                        persisted["verifications"][0]["latest"]["process_state"] == "awaiting_approval"
                    if ready:
                        break
                except (OSError, RuntimeError):
                    pass
                time.sleep(.5)
            if not ready:
                raise RuntimeError("restart_history_unavailable")
            cancel_route = route + "/" + verification + "/cancel"
            first = request(product, cancel_route, token, body={"plan_digest": plan_response["plan_digest"]})
            second = request(product, cancel_route, token, body={"plan_digest": plan_response["plan_digest"]})
            if first.get("process_state") != "cancelled" or second.get("process_state") != "cancelled":
                raise RuntimeError("restart_cancel_not_idempotent")
            decision_status = 200
        elif args.scenario == "S07":
            request(product, route + "/" + verification + "/decision", token,
                    body={"plan_digest": "0" * 64, "decision": "approved"}, expected=409)
            decision_status, _ = request(product, route + "/" + verification + "/decision", token,
                    body={"plan_digest": plan_response["plan_digest"], "decision": "rejected"}, return_status=True)
        elif args.scenario == "S07P":
            expires = datetime.datetime.fromisoformat(plan["expires_at"].replace("Z", "+00:00"))
            if (expires - datetime.datetime.now(datetime.timezone.utc)).total_seconds() > 660:
                raise RuntimeError("unexpected_plan_expiry")
            while datetime.datetime.now(datetime.timezone.utc) <= expires:
                time.sleep(min(5, max(.1, (expires - datetime.datetime.now(datetime.timezone.utc)).total_seconds())))
            decision_status, denied = request(product, route + "/" + verification + "/decision", token,
                                              body={"plan_digest": plan_response["plan_digest"], "decision": "approved"},
                                              expected=409, return_status=True)
            if denied.get("error") != "verification_plan_expired":
                raise RuntimeError("expired_plan_was_not_rejected")
        else:
            if args.scenario in ("S07R", "S09C", "S09R"):
                request(target_stats, "/hold-next-read", body={}, expected=204)
            if args.scenario == "S09M":
                request(model_stats, "/hold-next-final", body={}, expected=204)
            if args.scenario == "S04":
                account_base = "/api/product/targets/" + target["id"]
                request(product, account_base + "/accounts", token, body={"label": "owned-b"})
                request(product, account_base + "/accounts/owned-b/credential", token,
                        body={"username": "owned-b", "password": "synthetic-secret-b"}, method="PUT")
                # 계획 뒤 결속 계정 바꾸기: 예전에는 owned-a 표시 해제(unmark)였다. 해제가 없어졌으므로 owned-a 자격을 바꿔
                # 결속 요약을 바꾼다(제품 제어 account_credential_set이 결속 사본을 지움)
                request(product, account_base + "/accounts/owned-a/credential", token,
                        body={"username": "owned-a", "password": "synthetic-secret-a-replaced"}, method="PUT")
            decision_status, _ = request(product, route + "/" + verification + "/decision", token,
                    body={"plan_digest": plan_response["plan_digest"], "decision": "approved"},
                    expected=(200, 409) if args.scenario == "S04" else 200, return_status=True)
            if args.scenario in ("S07R", "S09C", "S09R"):
                await_fixture_count(target_stats, "held_target_reads", 1)
            if args.scenario == "S07R":
                request(product, "/api/product/targets/" + target["id"] + "/revoke", token, body={})
            elif args.scenario == "S09C":
                cancelled = request(product, route + "/" + verification + "/cancel", token,
                                    body={"plan_digest": plan_response["plan_digest"]})
                if cancelled.get("process_state") != "cancelled":
                    raise RuntimeError("active_cancel_not_recorded")
            elif args.scenario == "S09R":
                host_recovered = kill_and_restart_owned_preparation(args.project, args.install, args.head, allow_open_head=True)
            elif args.scenario == "S09M":
                await_fixture_count(model_stats, "held_final_models", 1, timeout=90)
                host_recovered = kill_and_restart_owned_preparation(args.project, args.install, args.head)
    deadline = time.monotonic() + 180
    while args.scenario not in ("S06", "S07", "S07B", "S07P", "S09") and (args.scenario != "S04" or decision_status == 200) and latest["process_state"] != "completed" and time.monotonic() < deadline:
        try:
            history = request(product, route, token)
        except (OSError, RuntimeError):
            if args.scenario not in ("S09R", "S09M"):
                raise
            time.sleep(.5)
            continue
        latest = history["verifications"][0]["latest"]
        if latest["process_state"] in ("failed", "cancelled", "expired"):
            break
        time.sleep(.5)
    if args.scenario == "S07B":
        history = request(product, route, token)
        latest = history["verifications"][0]["latest"]
        if latest["process_state"] != "failed" or latest.get("reason") != "budget_unavailable":
            raise RuntimeError("exhausted_installation_budget_advanced")
    elif args.scenario == "S06":
        history = request(product, route, token)
        latest = history["verifications"][0]["latest"]
        if latest["process_state"] != "analysis_ready" or request(model_stats, "/stats")["model_calls"] != pending_model:
            raise RuntimeError("unsupported_write_advanced_or_called_model")
    elif args.scenario == "S09":
        history = request(product, route, token)
        latest = history["verifications"][0]["latest"]
        if latest["process_state"] != "cancelled" or latest.get("reason") != "user_cancelled":
            raise RuntimeError("restart_cancel_not_preserved")
    elif args.scenario == "S07P":
        history = request(product, route, token)
        latest = history["verifications"][0]["latest"]
        if latest["process_state"] != "expired" or latest.get("reason") != "plan_expired":
            raise RuntimeError("expired_plan_history_missing")
    elif args.scenario == "S07":
        history = request(product, route, token)
        latest = history["verifications"][0]["latest"]
        if latest["process_state"] != "rejected" or latest.get("reason") != "user_rejected":
            raise RuntimeError("rejection_not_preserved")
    elif args.scenario == "S04" and decision_status == 409:
        history = request(product, route, token)
        latest = history["verifications"][0]["latest"]
    elif args.scenario in ("S07R", "S09C", "S09R", "S09M"):
        expected_reason = {"S07R": "candidate_execution_unavailable", "S09C": "candidate_execution_cancelled",
                           "S09R": "candidate_execution_unavailable", "S09M": "final_model_interrupted"}[args.scenario]
        if latest["process_state"] not in ("failed", "cancelled", "expired") or latest.get("reason") != expected_reason:
            raise RuntimeError("interruption_reason_not_preserved")
    elif args.scenario == "S04" and latest["process_state"] in ("failed", "cancelled", "expired"):
        pass
    elif args.scenario == "S08" and latest["process_state"] == "failed" and latest.get("reason") == "final_model_unavailable":
        pass
    elif latest["process_state"] != "completed":
        raise RuntimeError("verification_not_completed:" + latest["process_state"] + ":" + latest.get("reason", ""))
    report = "/api/report/diagnostic?task=" + task
    for audience in ("internal", "shared"):
        for fmt in ("", "&format=docx"):
            rendered = request(product, report + "&audience=" + audience + fmt, token, binary=True)
            content = zipfile.ZipFile(io.BytesIO(rendered)).read("word/document.xml") if fmt else rendered
            if b"synthetic-secret-a" in content or b"synthetic-session-a-" in content:
                raise RuntimeError("report_secret_exposure")
    current = request(product, "/api/report/diagnostic/remediation?task=" + task + "&audience=internal", token)
    current_candidate = next((row for row in current["entries"] if row.get("entry_id") == "candidate-" + candidate_id), None)
    if current_candidate is None:
        raise RuntimeError("candidate_missing_from_updated_report")
    if args.scenario in ("S01", "S02", "S03", "S05") and current_candidate.get("outcome") != "model_supported":
        raise RuntimeError("selected_verdict_missing_from_report")
    if args.scenario in ("S04", "S06", "S07", "S07B", "S07P", "S07R", "S08", "S09", "S09C", "S09R", "S09M") and current_candidate.get("outcome") in ("model_supported", "model_refuted"):
        raise RuntimeError("unverified_candidate_promoted_in_report")
    if args.scenario in ("S07R", "S09C", "S09R", "S09M"):
        time.sleep(16)  # Let the deliberately held fixture response arrive; no late send may follow.
    after_target = request(target_stats, "/stats")["allowed_received"]
    after_logins = request(target_stats, "/stats")["auth_logins"]
    after_authenticated_reads = request(target_stats, "/stats")["authenticated_reads"]
    after_structured_reads = request(target_stats, "/stats")["structured_reads"]
    after_model = request(model_stats, "/stats")["model_calls"]
    expected_new_sends = 0 if args.scenario in ("S01", "S04", "S06", "S07", "S07B", "S07P", "S09") else 2 if args.scenario == "S05" else 1
    if pending_target != after_diagnosis_target or after_target != pending_target + expected_new_sends:
        raise RuntimeError("unexpected_target_send_count")
    if args.scenario == "S03" and (after_diagnosis_logins - before_logins != 1 or
                                    after_logins - after_diagnosis_logins != 1 or after_authenticated_reads != after_target):
        raise RuntimeError("authenticated_send_binding_failed")
    if args.scenario == "S05" and after_structured_reads != 1:
        raise RuntimeError("structured_send_missing")
    if args.scenario == "S04" and (after_logins != after_diagnosis_logins or
                                    latest.get("verdict") in ("model_supported", "model_refuted")):
        raise RuntimeError("changed_account_gained_decisive_verdict")
    if args.scenario == "S08" and latest.get("verdict") in ("model_supported", "model_refuted"):
        raise RuntimeError("unrelated_model_source_gained_decisive_verdict")
    if args.scenario == "S06" and (pending_model - after_diagnosis_model != 1 or after_model != pending_model):
        raise RuntimeError("unsupported_write_triggered_extra_model_call")
    if args.scenario == "S07B" and after_model != after_diagnosis_model:
        raise RuntimeError("exhausted_installation_budget_triggered_model")
    if args.scenario in ("S07R", "S09C", "S09R") and after_model != pending_model:
        raise RuntimeError("interrupted_target_work_triggered_final_model")
    if args.scenario == "S09M" and after_model != pending_model + 1:
        raise RuntimeError("unknown_final_model_was_replayed")
    recovery = None
    if args.scenario in ("S09R", "S09M"):
        recovery = verify_new_selection_after_restart(product, token, route, history["evidence_digest"], verification,
                                                      target_stats, model_stats, 2 if args.scenario == "S09R" else 1,
                                                      host_recovered=host_recovered)
        updated = request(product, "/api/report/diagnostic/remediation?task=" + task + "&audience=internal", token)
        recovered_candidate = next((row for row in updated["entries"] if row.get("entry_id") == "candidate-" + candidate_id), None)
        if recovered_candidate is None or (host_recovered and recovered_candidate.get("outcome") != "model_supported") or (
                not host_recovered and recovered_candidate.get("outcome") in ("model_supported", "model_refuted")):
            raise RuntimeError("recovery_verdict_missing_from_report")
    print(json.dumps({"scenario": args.scenario, "head": args.head, "status": "PASS", "initial_candidates": len(candidates),
        "before_approval_target_sends": pending_target - after_diagnosis_target, "initial_target_sends": None if args.resume_task else after_diagnosis_target - before_target,
        "approved_followup_sends": after_target - pending_target, "out_of_scope_sends": request(target_stats, "/stats")["outside_received"],
        "model_calls": after_model - before_model, "verdict": latest.get("verdict"), "reason": latest.get("reason"),
        "auth_logins": after_logins - before_logins, "authenticated_reads": after_authenticated_reads,
        "structured_reads": after_structured_reads,
        "decision_status": decision_status,
        "audit_complete": True, "html_docx_variants": 4, "paid_model_calls": 0,
        "recovery": recovery}, sort_keys=True))


if __name__ == "__main__":
    try:
        main()
    except RuntimeError as error:
        print(json.dumps({"status": "FAIL", "reason": str(error)}, sort_keys=True))
        raise SystemExit(1) from None
