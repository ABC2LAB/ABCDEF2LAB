// 실제 Workbench + 소유 준비 API fixture. 모델·대상·진단 시작은 호출하지 않는다.
// Node 22+ native WebSocket, 별도 고정 Chromium의 loopback CDP를 사용한다. MCP를 켜지 않는다.
import { readFile, writeFile, mkdir } from "node:fs/promises";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const ui = new URL(process.env.JINDO_U01_UI_URL ?? "http://127.0.0.1:39201");
const cdp = new URL(
	process.env.JINDO_U01_CDP_URL ?? "http://127.0.0.1:39421/json/version",
);
for (const u of [ui, cdp])
	if (
		u.protocol !== "http:" ||
		u.hostname !== "127.0.0.1" ||
		!u.port ||
		u.username ||
		u.password
	)
		throw new Error("loopback_fixture_required");
const output = process.env.JINDO_U01_OUTPUT ?? "/tmp/jindo-u01-browser-results";
if (!output.startsWith("/tmp/") || output.includes(".."))
	throw new Error("private_output_required");
await mkdir(output, { recursive: true, mode: 0o700 });
const version = await (await fetch(cdp)).json();
const wsURL = new URL(version.webSocketDebuggerUrl);
if (wsURL.hostname !== "127.0.0.1" || wsURL.port !== cdp.port)
	throw new Error("unbound_cdp_endpoint");
const socket = new WebSocket(wsURL);
await new Promise((resolve, reject) => {
	socket.onopen = resolve;
	socket.onerror = reject;
});
let serial = 0;
const pending = new Map();
const sessions = new Map();
function send(method, params = {}, sessionId) {
	return new Promise((resolve, reject) => {
		const id = ++serial;
		const timeout = setTimeout(() => {
			pending.delete(id);
			reject(new Error("cdp_timeout"));
		}, 15000);
		pending.set(id, { resolve, reject, timeout });
		socket.send(
			JSON.stringify({
				id,
				method,
				params,
				...(sessionId ? { sessionId } : {}),
			}),
		);
	});
}
socket.onmessage = ({ data }) => {
	const message = JSON.parse(data);
	if (message.id) {
		const p = pending.get(message.id);
		if (p) {
			clearTimeout(p.timeout);
			pending.delete(message.id);
			message.error
				? p.reject(new Error("cdp_command_failed"))
				: p.resolve(message.result);
		}
		return;
	}
	if (message.method === "Fetch.requestPaused")
		void paused(message.params, message.sessionId).catch(() => {
			const state = sessions.get(message.sessionId);
			if (state) state.failure = "fixture_route_failed";
		});
};
const owner = "a".repeat(64),
	digest = "c".repeat(64);
const limits = {
	format: "abc2lab.workbench-flow/v1",
	max_pages: 4,
	max_endpoints: 32,
	max_response_bytes: 4096,
	allow_reversible_writes: true,
};
const fixtures = [
	{
		id: "auto_https",
		scheme: "https",
		mode: "ai_auto_v1",
		approval: "preauthorized_scope",
		writes: true,
	},
	{
		id: "manual_https",
		scheme: "https",
		mode: "user_approval_v1",
		approval: "workbench_selective_v1",
		writes: true,
	},
	{
		id: "readonly_https",
		scheme: "https",
		mode: "ai_auto_v1",
		approval: "preauthorized_scope",
		writes: false,
	},
	{
		id: "auto_http",
		scheme: "http",
		mode: "ai_auto_v1",
		approval: "preauthorized_scope",
		writes: true,
	},
	{
		id: "missing_scope",
		scheme: "https",
		mode: "ai_auto_v1",
		approval: "preauthorized_scope",
		writes: true,
		missing: true,
	},
	{
		id: "invalid_delete_scope",
		scheme: "https",
		mode: "ai_auto_v1",
		approval: "preauthorized_scope",
		writes: true,
		invalid: true,
	},
];
function makeFixture(c) {
	const origin = `${c.scheme}://u01-owned.example.test`;
	const methods = c.writes
		? ["GET", "HEAD", "POST", "PUT", "PATCH"]
		: ["GET", "HEAD"];
	if (c.invalid) methods.push("DELETE");
	const input = {
		target_url: origin + "/owned",
		profile_id: "profile-u01",
		workflow_mode: c.mode,
	};
	const scope = {
		target_ref: "target-u01",
		scope_ref: "scope-u01",
		origin,
		path_prefixes: ["/owned", "/api/safe"],
		methods,
		query_policy: "any",
		body_policy: "bounded",
		max_body_bytes: 4096,
		approval_mode: c.approval,
		attestation_ref: "attestation-u01",
		budget: {
			requests: 3,
			model_requests: 2,
			input_tokens: 1000,
			output_tokens: 1000,
			cost_usd_micros: 0,
			duration_seconds: 60,
		},
		expires_at: "2030-01-01T00:00:00Z",
	};
	const job = {
		id: "job-u01",
		revision: 2,
		source_digest: digest,
		input,
		state: c.missing ? "awaiting_approval" : "ready",
		phase: c.missing ? "draft" : "scope_check",
		preparation_started: false,
		authority_state: "not_issued",
		started: false,
		drain_complete: false,
		result_ready: false,
		...(c.missing ? { reason_code: "approval_required" } : { scope }),
	};
	const handle = {
		ownerContext: owner,
		creationKey: "create-u01",
		input,
		id: job.id,
		revision: job.revision,
		sourceDigest: digest,
	};
	const catalog = {
		version: "diagnosis-api.v2",
		owner_context: owner,
		profiles: [
			{
				id: "profile-u01",
				label: "소유 U01 fixture",
				model_profile_ref: "local-u01",
				model_label: "로컬 시험 프로필",
				workflow: limits,
				workflow_request_limit: 3,
			},
		],
	};
	const target = {
		id: "target-" + "a".repeat(32),
		origin,
		state: "approved",
		path_prefixes: scope.path_prefixes,
		methods,
		requests: 3,
		port: c.scheme === "http" ? 80 : 443,
		addresses: ["192.0.2.10"],
		tls_spki_sha256: c.scheme === "http" ? "" : "a".repeat(64),
		expires_at: scope.expires_at,
	};
	return { origin, methods, input, job, handle, catalog, target };
}
async function paused(event, session) {
	const state = sessions.get(session);
	if (!state) return;
	const url = new URL(event.request.url);
	if (url.origin !== ui.origin) {
		state.blockedExternal++;
		await send(
			"Fetch.failRequest",
			{ requestId: event.requestId, errorReason: "BlockedByClient" },
			session,
		);
		return;
	}
	if (!url.pathname.startsWith("/api/")) {
		await send(
			"Fetch.continueRequest",
			{ requestId: event.requestId },
			session,
		);
		return;
	}
	state.apiReads++;
	let code = 200,
		body = {};
	if (event.request.method !== "GET") {
		state.mutations++;
		code = 403;
		body = { error: "fixture_read_only" };
	} else if (url.pathname === "/api/auth/status") body = { initialized: true };
	else if (url.pathname === "/api/diagnoses/v2/catalog") body = state.f.catalog;
	else if (url.pathname === "/api/diagnoses/v2/drafts/job-u01/status") {
		state.receiptRead = true;
		body = {
			version: "diagnosis-api.v2",
			owner_context: owner,
			draft: state.f.job,
		};
	} else if (url.pathname === "/api/product/targets/resolve")
		body = { target: state.f.target };
	else {
		code = 404;
		body = { error: "fixture_not_available" };
	}
	await send(
		"Fetch.fulfillRequest",
		{
			requestId: event.requestId,
			responseCode: code,
			responseHeaders: [
				{ name: "Content-Type", value: "application/json" },
				{ name: "Cache-Control", value: "no-store" },
			],
			body: Buffer.from(JSON.stringify(body)).toString("base64"),
		},
		session,
	);
}
async function evaluate(session, expression) {
	const r = await send(
		"Runtime.evaluate",
		{ expression, returnByValue: true, awaitPromise: true },
		session,
	);
	if (r.exceptionDetails) throw new Error("browser_evaluation_failed");
	return r.result?.value;
}
async function waitFor(session, expression, timeout = 90000) {
	const deadline = Date.now() + timeout;
	while (Date.now() < deadline) {
		if (await evaluate(session, expression)) return;
		await new Promise((resolve) => setTimeout(resolve, 150));
	}
	throw new Error("render_condition_timeout");
}
const results = [];
try {
	for (const c of fixtures) {
		const f = makeFixture(c),
			state = {
				f,
				apiReads: 0,
				mutations: 0,
				blockedExternal: 0,
				receiptRead: false,
				failure: null,
			};
		const { targetId } = await send("Target.createTarget", {
			url: "about:blank",
		});
		const { sessionId } = await send("Target.attachToTarget", {
			targetId,
			flatten: true,
		});
		sessions.set(sessionId, state);
		try {
			await send("Page.enable", {}, sessionId);
			await send("Runtime.enable", {}, sessionId);
			await send(
				"Emulation.setDeviceMetricsOverride",
				{ width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false },
				sessionId,
			);
			await send(
				"Fetch.enable",
				{ patterns: [{ urlPattern: "*", requestStage: "Request" }] },
				sessionId,
			);
			await send(
				"Page.addScriptToEvaluateOnNewDocument",
				{
					source: `localStorage.clear();sessionStorage.clear();localStorage.setItem("abc2lab.devToken","synthetic-u01-session");sessionStorage.setItem("abc2lab.urlDiagnosisHandle.v2",${JSON.stringify(JSON.stringify(f.handle))});`,
				},
				sessionId,
			);
			await send("Page.navigate", { url: ui.origin + "/#new" }, sessionId);
			await waitFor(
				sessionId,
				`document.querySelector('[data-view="new"] h1')?.textContent?.includes('새 점검') && !!document.querySelector('[data-view="new"] #luPolicy')`,
			);
			const deadline = Date.now() + 15000;
			while (!state.receiptRead && Date.now() < deadline)
				await new Promise((resolve) => setTimeout(resolve, 100));
			if (!state.receiptRead) throw new Error("real_receipt_not_read");
			if (c.writes && !c.missing && !c.invalid)
				await waitFor(
					sessionId,
					`!!document.querySelector('[aria-label="쓰기 승인 안내"]')`,
				);
			else await new Promise((resolve) => setTimeout(resolve, 500));
			await evaluate(
				sessionId,
				`document.querySelector('[aria-label="쓰기 승인 안내"]')?.scrollIntoView({block:"center",behavior:"instant"})`,
			);
			const observed = await evaluate(
				sessionId,
				`(()=>{const el=document.querySelector('[aria-label="쓰기 승인 안내"]');const text=el?.innerText??"";const rect=el?.getBoundingClientRect();const start=[...document.querySelectorAll('button')].find(b=>b.textContent.trim()==="점검 시작");return {notice:!!el,noticeVisible:!!rect&&rect.width>0&&rect.top>=0&&rect.bottom<=innerHeight,origin:text.includes(${JSON.stringify(f.origin)}),paths:text.includes('/owned, /api/safe'),methods:text.includes(${JSON.stringify(f.methods.join(" / "))}),risk:text.includes('어떤 쓰기 요청이 나갈지 미리 모두 알 수 없으며, 대상 데이터나 상태가 바뀔 수 있습니다'),automatic:text.includes('요청마다 수동 확인하지 않고 보냅니다'),manual:text.includes('요청마다 사람이 내용을 확인하고 승인한 뒤'),plain:document.body.innerText.includes('암호화하지 않는 주소 (http)'),startPresent:!!start,startDisabled:!start||start.disabled};})()`,
			);
			const shouldShow = c.writes && !c.missing && !c.invalid;
			if (
				shouldShow &&
				(!observed.noticeVisible ||
					!observed.origin ||
					!observed.paths ||
					!observed.methods ||
					!observed.risk ||
					!observed.startDisabled)
			)
				throw new Error("approved_write_notice_failed");
			if (
				shouldShow &&
				(c.mode === "ai_auto_v1" ? !observed.automatic : !observed.manual)
			)
				throw new Error("approval_mode_copy_failed");
			if (!shouldShow && observed.notice)
				throw new Error("unsupported_write_grant_shown");
			if (
				(c.missing || c.invalid) &&
				observed.startPresent &&
				!observed.startDisabled
			)
				throw new Error("unapproved_start_enabled");
			if (c.scheme === "http" && !observed.plain)
				throw new Error("plaintext_warning_missing");
			if (state.mutations !== 0 || state.failure)
				throw new Error("unexpected_mutation_or_route_failure");
			const shot = await send(
				"Page.captureScreenshot",
				{ format: "png", captureBeyondViewport: false },
				sessionId,
			);
			const png = Buffer.from(shot.data, "base64");
			await writeFile(`${output}/${c.id}.png`, png, { mode: 0o600 });
			results.push({
				case: c.id,
				status: "PASS",
				...observed,
				api_reads: state.apiReads,
				mutation_requests: state.mutations,
				blocked_external_resources: state.blockedExternal,
				fixture_sha256: createHash("sha256")
					.update(JSON.stringify(f))
					.digest("hex"),
				screenshot_sha256: createHash("sha256").update(png).digest("hex"),
			});
		} catch (error) {
			await writeFile(
				`${output}/${c.id}-diagnostic.txt`,
				String(await evaluate(sessionId, "document.body.innerText")),
				{ mode: 0o600 },
			);
			results.push({ case: c.id, status: "FAIL", code: error.message });
			console.error(
				JSON.stringify({ case: c.id, status: "FAIL", code: error.message }),
			);
		} finally {
			sessions.delete(sessionId);
			await send("Target.closeTarget", { targetId });
		}
	}
} finally {
	socket.close();
}
const report = {
	app_source_sha: execFileSync("git", ["rev-parse", "HEAD"], {
		encoding: "utf8",
	}).trim(),
	driver_sha256: createHash("sha256")
		.update(await readFile(fileURLToPath(import.meta.url)))
		.digest("hex"),
	browser: version.Browser,
	protocol: version["Protocol-Version"],
	viewport: "1440x1000",
	browser_mode:
		"dedicated non-root read-only container; --no-sandbox; native CDP; MCP disabled",
	api: "owned deterministic preparation receipt fixture",
	engine: "NOT_RUN",
	paid_model: "NOT_RUN",
	results,
};
await writeFile(`${output}/report.json`, JSON.stringify(report, null, 2), {
	mode: 0o600,
});
console.log(JSON.stringify(report));
if (results.some((r) => r.status !== "PASS")) process.exitCode = 1;
