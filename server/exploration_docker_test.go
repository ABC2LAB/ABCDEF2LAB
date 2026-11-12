package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/internal/explorefixture"
	"github.com/Autumn-27/norma/llm"
)

// 이 시험은 하네스가 만든 네 내부망에서만 실행한다. 운영 경계 수용·실제 모델 시험과 구분한다.
type dockerExploreConfig struct {
	Limits         exploration.Limits `json:"limits"`
	FixtureSocket  string             `json:"fixture_socket"`
	BrowserSocket  string             `json:"browser_socket"`
	MCPURL         string             `json:"mcp_url"`
	TargetOrigin   string             `json:"target_origin"`
	CDNOrigin      string             `json:"cdn_origin"`
	DirectProxy    string             `json:"direct_proxy"`
	CAFile         string             `json:"ca_file"`
	Mode           string             `json:"mode"`
	ResultFile     string             `json:"result_file"`
	MetadataScript string             `json:"metadata_script"`
}
type dockerPrivateControl struct{ socket string }

func (c dockerPrivateControl) call(ctx context.Context, path string, in, out any) error {
	tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.socket)
	}}
	defer tr.CloseIdleConnections()
	body, e := json.Marshal(in)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", "http://fixture"+path, bytes.NewReader(body))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := (&http.Client{Transport: tr, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if e != nil {
		return exploration.ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return exploration.ErrUnavailable
	}
	if out == nil {
		_, e = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return e
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

type dockerExploreBoundary struct {
	ctl dockerPrivateControl
	eb  exploration.Binding
	fb  explorefixture.Binding
}

func (b *dockerExploreBoundary) Open(ctx context.Context, eb exploration.Binding) (exploration.Lease, error) {
	if eb != b.eb {
		return nil, exploration.ErrBinding
	}
	var state explorefixture.State
	if e := b.ctl.call(ctx, "/open", map[string]any{"binding": b.fb, "limits": eb.Limits, "comm_mode": eb.CommMode, "write_mode": eb.WriteMode}, &state); e != nil {
		return nil, e
	}
	return b, nil
}
func (b *dockerExploreBoundary) state(ctx context.Context) (explorefixture.State, error) {
	var s explorefixture.State
	e := b.ctl.call(ctx, "/status", nil, &s)
	return s, e
}
func (b *dockerExploreBoundary) translate(s explorefixture.State) (exploration.State, error) {
	if s.Binding != b.fb || s.Limits != b.eb.Limits || s.CommMode != b.eb.CommMode || s.WriteMode != b.eb.WriteMode {
		return exploration.State{}, exploration.ErrBinding
	}
	return exploration.State{Binding: b.eb, Active: s.Active, ClosedReason: exploration.Reason(s.Reason), TargetRequests: s.Forwarded, ExternalRequests: s.External, ActiveConnections: s.ActiveConnections}, nil
}
func (b *dockerExploreBoundary) Status(ctx context.Context) (exploration.State, error) {
	s, e := b.state(ctx)
	if e != nil {
		return exploration.State{}, e
	}
	return b.translate(s)
}
func (b *dockerExploreBoundary) Close(ctx context.Context, r exploration.Reason) (exploration.State, error) {
	var s explorefixture.State
	e := b.ctl.call(ctx, "/close", map[string]string{"reason": string(r)}, &s)
	if e != nil {
		return exploration.State{}, e
	}
	return b.translate(s)
}
func (b *dockerExploreBoundary) Session(ctx context.Context) (diagnosisflow.ExplorationBinding, error) {
	s, e := b.state(ctx)
	if e != nil || !s.Active || s.Binding != b.fb {
		return diagnosisflow.ExplorationBinding{}, diagnosisflow.ErrExplorationClosed
	}
	return b.flowBinding(), nil
}
func (b *dockerExploreBoundary) flowBinding() diagnosisflow.ExplorationBinding {
	return diagnosisflow.ExplorationBinding{SessionRef: b.fb.SessionRef, RunRef: b.fb.RunRef, JobID: b.fb.JobID, TaskID: b.fb.TaskID, TargetRef: b.fb.TargetRef, TargetRevision: b.fb.TargetRevision, SourceDigest: b.fb.SourceDigest}
}
func (b *dockerExploreBoundary) ReadObservation(ctx context.Context, fb diagnosisflow.ExplorationBinding, seq uint64) (diagnosisflow.ExplorationObservation, error) {
	if fb != b.flowBinding() {
		return diagnosisflow.ExplorationObservation{}, diagnosisflow.ErrExplorationBinding
	}
	var o explorefixture.Observation
	if e := b.ctl.call(ctx, "/observation", map[string]any{"binding": b.fb, "seq": seq}, &o); e != nil {
		return diagnosisflow.ExplorationObservation{}, e
	}
	if o.Binding != b.fb {
		return diagnosisflow.ExplorationObservation{}, diagnosisflow.ErrExplorationBinding
	}
	return diagnosisflow.ExplorationObservation{Binding: fb, Seq: o.Seq, At: o.At, Method: o.Method, Path: o.Path, Source: o.Source, Kind: o.Kind, Transport: o.Transport, Status: o.Status, Forwarded: o.Forwarded, ResponseReceived: o.ResponseReceived, QueryKeys: o.QueryKeys, BodyKeys: o.BodyKeys, ContentType: o.ContentType, ResponseContentType: o.ResponseContentType, ResponseSHA256: o.ResponseSHA256}, nil
}

type dockerExploreBrowser struct {
	info ExplorationBrowserInfo
	ctl  dockerPrivateControl
}

func (b *dockerExploreBrowser) Info() ExplorationBrowserInfo { return b.info }
func (b *dockerExploreBrowser) Close(ctx context.Context) error {
	return b.ctl.call(ctx, "/close", nil, nil)
}

type dockerExploreStore struct {
	mu     sync.Mutex
	record diagnosisflow.Record
}

func (s *dockerExploreStore) Create(_ context.Context, r diagnosisflow.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.Snapshot.Revision != 0 {
		return diagnosisflow.ErrConflict
	}
	s.record = r
	return nil
}
func (s *dockerExploreStore) Save(_ context.Context, v uint64, r diagnosisflow.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.Snapshot.Revision != v {
		return diagnosisflow.ErrConflict
	}
	s.record = r
	return nil
}
func (s *dockerExploreStore) Read(_ context.Context, owner, id string) (diagnosisflow.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.Owner != owner || s.record.Snapshot.JobID != id {
		return diagnosisflow.Record{}, diagnosisflow.ErrUnavailable
	}
	return s.record, nil
}

type dockerToolStep struct {
	name  string
	input any
}
type dockerExploreProvider struct {
	toolErrors int
	refShape   map[string]bool
	failure    string
	steps      []dockerToolStep
	calls      int
	proof      map[string]bool
	invalid    bool
	flow       *diagnosisflow.Controller
	boundary   *dockerExploreBoundary
	last       uint64
	formRef    string
}

func (p *dockerExploreProvider) drain(ctx context.Context) error {
	state, e := p.boundary.state(ctx)
	if e != nil {
		return e
	}
	for p.last < state.RecordCount {
		seq := p.last + 1
		if _, e = p.flow.ReportObserved(ctx, p.boundary.flowBinding(), seq); e != nil {
			return e
		}
		p.last = seq
	}
	return nil
}
func (p *dockerExploreProvider) Complete(ctx context.Context, r llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls++
	allowed := map[string]bool{"report_discovery": true}
	for _, name := range exploration.BrowserToolNames() {
		allowed["mcp__explore_browser__"+name] = true
	}
	for _, tool := range r.Tools {
		if !allowed[tool.Name] {
			p.invalid = true
		}
	}

	// MCP가 실행 코드를 함께 돌려주므로 Result 절의 JSON 반환값만 판정한다.
	for _, m := range r.Messages {
		for _, block := range m.Content {
			if block.Type != llm.BlockToolResult {
				continue
			}
			if block.IsError {
				p.toolErrors++
				continue
			}
			for _, content := range block.Content {
				if block.ToolUseID == "15" {
					p.refShape = map[string]bool{"button": strings.Contains(content.Text, "button"), "save": strings.Contains(content.Text, "save"), "ref": strings.Contains(content.Text, "[ref="), "snapshot": strings.Contains(content.Text, "Snapshot")}
				}
				if matches := regexp.MustCompile(`button "save" \[ref=([A-Za-z0-9]+)\]`).FindStringSubmatch(content.Text); len(matches) == 2 {
					p.formRef = matches[1]
				}
				value, ok := dockerEvaluateValue(content.Text)
				if !ok {
					continue
				}
				for _, proof := range []string{"P1_SPA_READY", "P1_RUNTIME_HOOK_READY", "P1_RUNTIME_METADATA_READY", "P1_CDN_READY", "P1_CDN_BLOCKED", "P1_WEBMCP_REGISTERED", "P1_WEBMCP_NONE", "P1_FORM_READY", "P1_CSS_READY"} {
					if value == proof {
						p.proof[proof] = true
					}
				}
			}
		}
	}
	if e := p.drain(ctx); e != nil {
		p.failure = "observed_drain"
		return llm.Message{}, "", llm.Usage{}, e
	}
	if p.calls > len(p.steps) {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("탐색 fixture 완료")}}, "end_turn", llm.Usage{}, nil
	}
	step := p.steps[p.calls-1]
	if step.name == "mcp__explore_browser__browser_click" {
		if p.formRef == "" {
			p.failure = "form_ref_missing"
			return llm.Message{}, "", llm.Usage{}, errors.New("fixture_form_reference_missing")
		}
		step.input = map[string]string{"element": "save button", "ref": p.formRef}
	}
	input, _ := json.Marshal(step.input)
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: strconv.Itoa(p.calls), Name: step.name, Input: input}}}, "tool_use", llm.Usage{}, nil
}
func (p *dockerExploreProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("owned fixture uses Complete"))
	}
}

// Result의 첫 JSON 값만 허용하고 나머지 code/snapshot 절은 증거로 읽지 않는다.
func dockerEvaluateValue(text string) (string, bool) {
	start := strings.Index(text, "### Result\n")
	if start < 0 {
		return "", false
	}
	part := strings.TrimSpace(strings.SplitN(text[start+len("### Result\n"):], "\n### ", 2)[0])
	var value string
	if json.Unmarshal([]byte(part), &value) != nil {
		return "", false
	}
	return value, true
}
func TestDockerEvaluateProofIgnoresEchoedCode(t *testing.T) {
	for _, text := range []string{"### Ran Playwright code\nreturn 'P1_SPA_READY'", "### Result\n\"missing\"\n### Ran Playwright code\nreturn 'P1_SPA_READY'"} {
		if value, ok := dockerEvaluateValue(text); ok && value == "P1_SPA_READY" {
			t.Fatal("code echo accepted as runtime proof")
		}
	}
	if value, ok := dockerEvaluateValue("### Result\n\"P1_SPA_READY\"\n### Ran Playwright code\nreturn 'missing'"); !ok || value != "P1_SPA_READY" {
		t.Fatal("actual result rejected")
	}
}
func TestOwnedDockerExplorationMCP(t *testing.T) {
	path := os.Getenv("JINDO_P1_FIXTURE_CONTEXT")
	if path == "" {
		t.Skip("owned Docker harness not supplied")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal("fixture_context_read")
	}
	var cfg dockerExploreConfig
	if json.Unmarshal(raw, &cfg) != nil || cfg.ResultFile == "" {
		t.Fatal("fixture_context_invalid")
	}
	report := map[string]any{"format": "jindo.exploration-fixture-result/v1", "boundary_kind": "owned_contract_fixture", "production_acceptance": "NOT_RUN", "model_run": "NOT_RUN", "mode": cfg.Mode, "status": "FAIL"}
	defer func() {
		b, _ := json.MarshalIndent(report, "", "  ")
		if os.WriteFile(cfg.ResultFile, b, 0600) != nil {
			t.Error("fixture_summary_write")
		}
	}()
	eb := exploration.Binding{SessionRef: "fixture-session", RunRef: "fixture-run", JobID: "fixture-job", TaskID: "7", TargetRef: "fixture-target", TargetRevision: 1, WriteMode: exploration.ReadOnly, CommMode: cfg.Mode, Limits: cfg.Limits}
	if eb.CommMode == exploration.FullLocal {
		eb.Limits.MaxExternalRequests = 0
	}
	b := &dockerExploreBoundary{ctl: dockerPrivateControl{cfg.FixtureSocket}, eb: eb, fb: explorefixture.Binding{SessionRef: eb.SessionRef, RunRef: eb.RunRef, JobID: eb.JobID, TaskID: eb.TaskID, TargetRef: eb.TargetRef, TargetRevision: eb.TargetRevision, SourceDigest: strings.Repeat("a", 64)}}
	store := &dockerExploreStore{}
	flow, e := diagnosisflow.New(t.Context(), diagnosisflow.Binding{TargetRef: eb.TargetRef, TargetRevision: 1, JobID: eb.JobID, TaskID: eb.TaskID, RunRef: eb.RunRef, SourceDigest: b.fb.SourceDigest, Owner: "owned-fixture", Mode: diagnosisflow.Manual, TargetURL: cfg.TargetOrigin + "/", PathPrefixes: []string{"/"}, ReadonlyPrefixes: []string{"/"}, Methods: []string{"GET", "HEAD"}, Limits: diagnosisflow.Limits{Format: diagnosisflow.Version, MaxPages: 3, MaxEndpoints: 32, MaxResponseBytes: 1 << 20}, Current: func() error { return nil }}, store)
	if e != nil {
		t.Fatal("flow_create")
	}
	script, e := os.ReadFile(cfg.MetadataScript)
	if e != nil {
		t.Fatal("metadata_script_missing")
	}
	p := &dockerExploreProvider{proof: map[string]bool{}, flow: flow, boundary: b}
	nav := func(path string) dockerToolStep {
		return dockerToolStep{"mcp__explore_browser__browser_navigate", map[string]string{"url": cfg.TargetOrigin + path}}
	}
	eval := func(fn string) dockerToolStep {
		return dockerToolStep{"mcp__explore_browser__browser_evaluate", map[string]string{"function": fn}}
	}
	p.steps = []dockerToolStep{
		nav("/"),
		eval("() => document.getElementById('fixture-ready') ? 'P1_SPA_READY' : 'missing'"),
		eval("() => getComputedStyle(document.getElementById('app')).fontFamily==='sans-serif'?'P1_CSS_READY':'missing'"),
		eval("() => {" + string(script) + ";globalThis.__p1metadata=globalThis.__apiReconMetadata.install();return 'P1_RUNTIME_HOOK_READY'}"),
		eval("async () => {await fetch('/api/items?filter=fixture');return globalThis.__p1metadata.drain().discoveries.some(x=>x.query_keys.includes('filter'))?'P1_RUNTIME_METADATA_READY':'missing'}"),
		{"report_discovery", map[string]any{"discoveries": []map[string]string{{"method": "GET", "path": "/api/inferred-only", "source": "api_recon_static"}}}},
		nav("/crawl/P08"), eval("() => document.getElementById('p08-ready')?.textContent==='ok'?'P1_CDN_READY':'P1_CDN_BLOCKED'"),
		nav("/bypass/B04"), nav("/bypass/B05"), nav("/bypass/B12"), nav("/bypass/B13"),
		// B09: 페이지가 WebRTC STUN으로 수신기 UDP에 닿으려 한다. 브라우저 namespace에는 경계 입구 밖 경로가 없다.
		nav("/bypass/B09"),
		nav("/crawl/S13"), eval("() => document.getElementById('save') && location.pathname==='/crawl/S13'?'P1_FORM_READY':'missing'"),
		{"mcp__explore_browser__browser_snapshot", map[string]any{}},
		{"mcp__explore_browser__browser_click", nil},
		nav("/crawl/B18-webmcp"), eval("() => {const v=document.getElementById('webmcp-attempted')?.textContent;return v==='registered'?'P1_WEBMCP_REGISTERED':v==='none'||v==='failed'?'P1_WEBMCP_NONE':'missing'}"),
		// 자동 top-level form 이동은 다른 페이지의 양성 대조를 덮지 않도록 마지막에 시험한다.
		nav("/bypass/B14"),
	}
	worker := agent.NewWorker(p, "owned-deterministic-fixture", t.TempDir(), nil, 0, 32)
	worker.SetNonStreaming(func() bool { return true })
	browser := &dockerExploreBrowser{info: ExplorationBrowserInfo{Binding: eb, PackageVersion: exploration.MCPPackageVersion, MCP: db.MCPServer{Name: exploration.MCPServerName, Transport: "http", URL: cfg.MCPURL}}, ctl: dockerPrivateControl{cfg.BrowserSocket}}
	var pinned diagnosisflow.ExplorationBinding
	session, e := OpenExplorationSession(t.Context(), ExplorationSessionOptions{Worker: worker, Binding: eb, Boundary: b, StartBrowser: func(context.Context, exploration.Binding) (ExplorationBrowserLease, error) { return browser, nil }, Redact: func(raw []byte) []byte { return append([]byte(nil), raw...) }, CloseTimeout: 20 * time.Second, StatusInterval: 100 * time.Millisecond, Finalize: func(ctx context.Context, _ exploration.Binding) error {
		if e := p.drain(ctx); e != nil {
			return e
		}
		return flow.FinishExploration(ctx, pinned, "completed")
	}})
	if e != nil {
		report["failure"] = "session_open"
		t.Fatal("session_open_failed")
	}
	defer session.Close(context.Background(), exploration.Cancelled)
	pinned, e = flow.BindExploration(t.Context(), b)
	if e != nil {
		t.Fatal("flow_binding")
	}
	ca, e := os.ReadFile(cfg.CAFile)
	if e != nil {
		t.Fatal("public_ca_missing")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatal("public_ca_invalid")
	}
	proxy, e := url.Parse(cfg.DirectProxy)
	if e != nil {
		t.Fatal("proxy_invalid")
	}
	tr := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	handled, e := flow.CrawlExploration(t.Context(), pinned, func(ctx context.Context, target string) (diagnosisflow.ExplorationFetchResult, error) {
		req, e := http.NewRequestWithContext(ctx, "GET", target, nil)
		if e != nil {
			return diagnosisflow.ExplorationFetchResult{}, e
		}
		r, e := client.Do(req)
		if e != nil {
			return diagnosisflow.ExplorationFetchResult{}, e
		}
		defer r.Body.Close()
		body, e := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if e != nil {
			return diagnosisflow.ExplorationFetchResult{}, e
		}
		seq, e := strconv.ParseUint(r.Header.Get("X-Jindo-Fixture-Sequence"), 10, 64)
		return diagnosisflow.ExplorationFetchResult{Seq: seq, Body: body, Complete: e == nil}, e
	})
	if !handled || e != nil {
		report["failure"] = "static_crawl"
		t.Fatal("static_crawl_failed")
	}
	// 이미 static parser가 수집한 seq를 증분 reader의 시작점으로 쓴다.
	state, e := b.state(t.Context())
	if e != nil {
		t.Fatal("boundary_state")
	}
	p.last = state.RecordCount
	result, e := session.Run(t.Context(), worker, agent.ExplorationRunOptions{TargetURL: cfg.TargetOrigin + "/", Reporter: flow, MaxTurns: 24, MaxDuration: 150 * time.Second}, nil)
	if e != nil {
		report["failure"] = "worker_or_cleanup"
		report["model_calls"] = p.calls
		report["provider_failure"] = p.failure
		report["tool_error_count"] = p.toolErrors
		report["form_ref_shape"] = p.refShape
		report["proof"] = p.proof
		t.Fatal("worker_or_cleanup_failed")
	}
	_ = result
	state, e = b.state(t.Context())
	if e != nil {
		t.Fatal("final_state")
	}
	snap, e := flow.Snapshot()
	if e != nil {
		t.Fatal("flow_snapshot")
	}
	observed, inferred := 0, 0
	resources := map[string]bool{"/assets/app.js": false, "/assets/app.css": false, "/assets/logo.svg": false, "/api/items": false}
	dynamicKeys := false
	for _, v := range flow.DiscoveryInventory() {
		if v.Confidence == "observed" {
			observed++
			if _, ok := resources[v.Path]; ok && v.Status == 200 {
				resources[v.Path] = true
			}
			if v.Path == "/api/items" && v.Source == diagnosisflow.SourceBrowserXHR {
				for _, key := range v.QueryKeys {
					if key == "filter" {
						dynamicKeys = true
					}
				}
			}
		} else {
			inferred++
		}
	}
	report["proof"] = p.proof
	report["resource_observations"] = resources
	report["observed_query_keys"] = dynamicKeys
	for _, seen := range resources {
		if !seen {
			report["failure"] = "required_resource_not_observed"
			t.Fatal("required_resource_not_observed")
		}
	}
	report["forwarded"] = state.Forwarded
	report["external"] = state.External
	report["denied"] = state.Denied
	report["active_connections"] = state.ActiveConnections
	report["observed"] = observed
	report["inferred"] = inferred
	report["model_calls"] = p.calls
	report["diagnosis_attempts"] = snap.Counts.Attempted
	if p.invalid || !dynamicKeys || !p.proof["P1_CSS_READY"] || !p.proof["P1_SPA_READY"] || !p.proof["P1_RUNTIME_HOOK_READY"] || !p.proof["P1_RUNTIME_METADATA_READY"] || (!p.proof["P1_WEBMCP_REGISTERED"] && !p.proof["P1_WEBMCP_NONE"]) || !p.proof["P1_FORM_READY"] || p.formRef == "" || observed < 3 || inferred < 1 || state.Active || state.ActiveConnections != 0 || snap.Counts.Attempted != 0 || snap.Counts.Sent != 0 || snap.Stage != "awaiting_selection" {
		report["failure"] = "integration_invariant"
		t.Fatal("integration_invariant_failed")
	}
	if cfg.Mode == "external_allowed" && (!p.proof["P1_CDN_READY"] || state.External < 3) {
		report["failure"] = "cdn_render"
		t.Fatal("cdn_render_failed")
	}
	if cfg.Mode == "full_local" && (state.External != 0 || !p.proof["P1_CDN_BLOCKED"] || state.Denied["external_blocked_local"] < 1) {
		report["failure"] = "local_cdn_guard"
		t.Fatal("local_cdn_guard_failed")
	}
	if state.Denied["out_of_scope_origin"] < 1 || state.Denied["method_not_approved"] < 1 {
		report["failure"] = "negative_controls"
		t.Fatal("negative_controls_failed")
	}
	report["status"] = "PASS"
}
