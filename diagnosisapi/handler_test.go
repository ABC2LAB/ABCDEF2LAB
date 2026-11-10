package diagnosisapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosis"
)

var testConfiguration = Configuration{TargetRef: "owned-loopback", ScopeRef: "read-only", ModelProfileRef: "synthetic", ApprovalMode: "confirm_every_request", Budget: Budget{Requests: 3, Tokens: 100, DurationSeconds: 60}}

type fakeRegistry struct {
	approved  bool
	invalid   bool
	removed   bool
	now       time.Time
	transform func(*Binding)
	profiles  []Profile
}

func (f *fakeRegistry) Profiles(context.Context, string) ([]Profile, error) {
	if f.removed {
		return nil, errors.New("unavailable")
	}
	if f.profiles != nil {
		return f.profiles, nil
	}
	return []Profile{{ID: "profile", Label: "합성 시험", Configuration: testConfiguration}}, nil
}
func (f *fakeRegistry) Approval(_ context.Context, owner string, d Draft) (*Binding, error) {
	if !f.approved {
		return nil, nil
	}
	b := &Binding{Principal: owner, DraftID: d.ID, Revision: d.Revision, SourceDigest: d.SourceDigest, TaskID: "42", PreparationRef: "operator-prepared", ExpiresAt: f.now.Add(time.Minute)}
	if f.invalid {
		b.SourceDigest = "changed"
	}
	if f.transform != nil {
		f.transform(b)
	}
	return b, nil
}

type fakeGateway struct {
	mu         sync.Mutex
	calls      []diagnosis.Command
	startErr   bool
	badBinding bool
	state      diagnosis.State
	snapshot   func(*diagnosis.Snapshot)
}

func (g *fakeGateway) Call(_ context.Context, r diagnosis.Request) (diagnosis.Response, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, r.Command)
	if r.Command == diagnosis.Start {
		g.state = diagnosis.Running
		if g.startErr {
			return diagnosis.Response{}, errors.New("lost response")
		}
	}
	if r.Command == diagnosis.Stop {
		g.state = diagnosis.Stopping
	}
	state := g.state
	if state == "" {
		state = diagnosis.Prepared
	}
	s := &diagnosis.Snapshot{TaskID: r.TaskID, PreparationRef: r.PreparationRef, State: state, Started: state != diagnosis.Prepared}
	if g.badBinding {
		s.TaskID = "43"
	}
	if g.snapshot != nil {
		g.snapshot(s)
	}
	return diagnosis.Response{Version: diagnosis.Version, RequestID: r.RequestID, Snapshot: s}, nil
}
func setup(t *testing.T) (*Handler, *fakeRegistry, *fakeGateway) {
	t.Helper()
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	reg := &fakeRegistry{now: now}
	gate := &fakeGateway{}
	h, err := New(Options{Authenticate: func(r *http.Request) (string, error) {
		switch r.Header.Get("Authorization") {
		case "Bearer alice":
			return "alice", nil
		case "Bearer bob":
			return "bob", nil
		}
		return "", errors.New("unauthorized")
	}, Registry: reg, Gateway: gate, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return h, reg, gate
}
func request(t *testing.T, h *Handler, method, path, owner, key string, body any) (int, map[string]json.RawMessage) {
	t.Helper()
	var data []byte
	if text, ok := body.(string); ok {
		data = []byte(text)
	} else if body != nil {
		data, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, BasePath+path, bytes.NewReader(data))
	if owner != "" {
		r.Header.Set("Authorization", "Bearer "+owner)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var result map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON %q", w.Body.String())
	}
	return w.Code, result
}
func resultDraft(t *testing.T, data map[string]json.RawMessage) Draft {
	t.Helper()
	var d Draft
	if err := json.Unmarshal(data["draft"], &d); err != nil {
		t.Fatalf("draft missing: %s", data)
	}
	return d
}
func create(t *testing.T, h *Handler) Draft {
	t.Helper()
	code, data := request(t, h, "POST", "/drafts", "alice", "create-1", requestBody{Version: Version, Configuration: &testConfiguration})
	if code != 201 {
		t.Fatalf("create %d %s", code, data)
	}
	return resultDraft(t, data)
}
func command(t *testing.T, h *Handler, d Draft, action, key string) (int, map[string]json.RawMessage) {
	return request(t, h, "POST", "/drafts/"+d.ID+"/"+action, "alice", key, requestBody{Version: Version, ExpectedRevision: d.Revision})
}
func TestDraftCreationHasNoEngineSideEffect(t *testing.T) {
	h, _, g := setup(t)
	d := create(t, h)
	if d.State != "draft" || d.TaskID != "" || d.Revision != 1 || len(d.SourceDigest) != 64 || len(g.calls) != 0 {
		t.Fatalf("draft side effect: %+v", d)
	}
}
func TestAuthenticationAndOwnership(t *testing.T) {
	h, _, _ := setup(t)
	d := create(t, h)
	for _, owner := range []string{"", "invalid"} {
		code, _ := request(t, h, "GET", "/catalog", owner, "", nil)
		if code != 401 {
			t.Fatalf("owner %s=%d", owner, code)
		}
	}
	code, _ := request(t, h, "GET", "/drafts/"+d.ID+"/status", "bob", "", nil)
	if code != 404 {
		t.Fatal(code)
	}
}
func TestUnconfiguredRegistryFailsClosed(t *testing.T) {
	h, _, _ := setup(t)
	h.options.Registry = nil
	code, _ := request(t, h, "GET", "/catalog", "alice", "", nil)
	if code != 503 {
		t.Fatal(code)
	}
	code, _ = request(t, h, "POST", "/drafts", "alice", "create", requestBody{Version: Version, Configuration: &testConfiguration})
	if code != 503 {
		t.Fatal(code)
	}
}
func TestDraftIdempotency(t *testing.T) {
	h, _, g := setup(t)
	d := create(t, h)
	code, data := request(t, h, "POST", "/drafts", "alice", "create-1", requestBody{Version: Version, Configuration: &testConfiguration})
	if code != 200 || resultDraft(t, data).ID != d.ID || len(g.calls) != 0 {
		t.Fatal("creation replay changed")
	}
	changed := testConfiguration
	changed.Budget.Requests++
	// 미등록 config는 승인 profile에서 우선 거부한다.
	code, _ = request(t, h, "POST", "/drafts", "alice", "create-1", requestBody{Version: Version, Configuration: &changed})
	if code != 403 {
		t.Fatal(code)
	}
}
func TestMissingApprovalDoesNotStart(t *testing.T) {
	h, _, g := setup(t)
	d := create(t, h)
	code, data := command(t, h, d, "validate", "")
	if code != 200 || resultDraft(t, data).State != "awaiting_approval" {
		t.Fatal(code, data)
	}
	code, _ = command(t, h, d, "start", "start")
	if code != 409 || len(g.calls) != 0 {
		t.Fatal(code, g.calls)
	}
}
func TestValidateThenStart(t *testing.T) {
	h, reg, g := setup(t)
	d := create(t, h)
	reg.approved = true
	code, data := command(t, h, d, "validate", "")
	if code != 200 || resultDraft(t, data).State != "prepared" {
		t.Fatal(code, data)
	}
	code, data = command(t, h, d, "start", "start")
	if code != 202 || resultDraft(t, data).State != "running" {
		t.Fatal(code, data)
	}
	code, _ = command(t, h, d, "start", "start")
	if code != 200 {
		t.Fatal(code)
	}
	code, _ = command(t, h, d, "start", "different")
	if code != 409 {
		t.Fatal(code)
	}
	if len(g.calls) != 2 || g.calls[0] != diagnosis.Validate || g.calls[1] != diagnosis.Start {
		t.Fatal(g.calls)
	}
}
func TestChangedApprovalBlocksStart(t *testing.T) {
	for _, invalid := range []bool{true, false} {
		t.Run(map[bool]string{true: "digest", false: "withdrawn"}[invalid], func(t *testing.T) {
			h, reg, g := setup(t)
			d := create(t, h)
			reg.approved = true
			command(t, h, d, "validate", "")
			reg.invalid = invalid
			reg.approved = invalid
			code, _ := command(t, h, d, "start", "start")
			if code != 409 || len(g.calls) != 1 {
				t.Fatal(code, g.calls)
			}
		})
	}
}
func TestReboundApprovalBlocksStartAndTaskAccess(t *testing.T) {
	for name, change := range map[string]func(*Binding){
		"task":        func(b *Binding) { b.TaskID = "43" },
		"preparation": func(b *Binding) { b.PreparationRef = "other-preparation" },
		"expiry":      func(b *Binding) { b.ExpiresAt = b.ExpiresAt.Add(time.Nanosecond) },
	} {
		t.Run(name, func(t *testing.T) {
			h, reg, g := setup(t)
			d := create(t, h)
			reg.approved = true
			code, data := command(t, h, d, "validate", "")
			if code != 200 || resultDraft(t, data).State != "prepared" || !h.BoundTask(t.Context(), "alice", "42") {
				t.Fatal("approval not prepared", code, data)
			}
			reg.transform = change
			if h.BoundTask(t.Context(), "alice", "42") {
				t.Fatal("changed approval still grants task access")
			}
			code, data = command(t, h, d, "start", "start")
			if code != 409 || string(data["error"]) != `"approval_changed"` {
				t.Fatal("changed approval accepted", code, data)
			}
			code, data = request(t, h, "GET", "/drafts/"+d.ID+"/status", "alice", "", nil)
			after := resultDraft(t, data)
			if code != 200 || after.State != "awaiting_approval" || after.TaskID != "" || after.Snapshot != nil || h.BoundTask(t.Context(), "alice", "42") {
				t.Fatal("stale preparation retained", code, after)
			}
			code, data = command(t, h, d, "start", "start")
			if code != 409 || string(data["error"]) != `"approval_required"` || len(g.calls) != 1 || g.calls[0] != diagnosis.Validate {
				t.Fatal("changed approval dispatched a command", code, data, g.calls)
			}
		})
	}
}
func TestApprovalExpiryTimezoneDoesNotChangeBinding(t *testing.T) {
	for name, offset := range map[string]int{"UTC": 0, "hours": 9 * 3600, "half_hour": 19800, "negative_half_hour": -12600, "quarter_hour": 20700} {
		t.Run(name, func(t *testing.T) {
			h, reg, g := setup(t)
			d := create(t, h)
			reg.approved = true
			expires := reg.now.Add(time.Minute).In(time.FixedZone("", offset)).Format(time.RFC3339)
			reg.transform = func(b *Binding) {
				// 보호 registry를 다시 읽는 경우처럼 매번 시각을 새로 파싱한다.
				var err error
				b.ExpiresAt, err = time.ParseInLocation(time.RFC3339, expires, time.UTC)
				if err != nil {
					t.Fatal(err)
				}
			}
			code, data := command(t, h, d, "validate", "")
			if code != 200 || resultDraft(t, data).State != "prepared" {
				t.Fatal("approval not prepared", code, data)
			}
			if !h.BoundTask(t.Context(), "alice", "42") || h.BoundTask(t.Context(), "bob", "42") || h.BoundTask(t.Context(), "alice", "43") {
				t.Fatal("same instant changed task access")
			}
			code, data = command(t, h, d, "start", "start")
			if code != 202 || resultDraft(t, data).State != "running" || len(g.calls) != 2 || g.calls[1] != diagnosis.Start {
				t.Fatal("same instant blocked start", code, data, g.calls)
			}
		})
	}
}
func TestExpiredApprovalBlocksValidate(t *testing.T) {
	h, reg, g := setup(t)
	d := create(t, h)
	reg.approved = true
	reg.now = reg.now.Add(-2 * time.Minute)
	code, _ := command(t, h, d, "validate", "")
	if code != 409 || len(g.calls) != 0 {
		t.Fatal(code, g.calls)
	}
}
func TestEditInvalidatesValidation(t *testing.T) {
	h, reg, g := setup(t)
	d := create(t, h)
	reg.approved = true
	command(t, h, d, "validate", "")
	code, data := request(t, h, "PATCH", "/drafts/"+d.ID, "alice", "", requestBody{Version: Version, ExpectedRevision: 1, Configuration: &testConfiguration})
	next := resultDraft(t, data)
	if code != 200 || next.Revision != 2 || next.State != "draft" {
		t.Fatal(code, next)
	}
	code, _ = command(t, h, next, "start", "start")
	if code != 409 || len(g.calls) != 1 {
		t.Fatal(code, g.calls)
	}
	code, _ = command(t, h, d, "validate", "")
	if code != 400 {
		t.Fatal(code)
	}
}
func TestUnknownStartIsNotAutomaticallyResent(t *testing.T) {
	h, reg, g := setup(t)
	d := create(t, h)
	reg.approved = true
	command(t, h, d, "validate", "")
	g.startErr = true
	code, _ := command(t, h, d, "start", "start")
	if code != 503 {
		t.Fatal(code)
	}
	code, data := command(t, h, d, "start", "start")
	if code != 200 || resultDraft(t, data).State != "unknown" || len(g.calls) != 2 {
		t.Fatal(code, g.calls)
	}
	code, data = request(t, h, "GET", "/drafts/"+d.ID+"/status", "alice", "", nil)
	if code != 200 || resultDraft(t, data).State != "running" || len(g.calls) != 3 || g.calls[2] != diagnosis.Status {
		t.Fatal(code, g.calls)
	}
}
func TestStopUsesGatewayNotTools(t *testing.T) {
	h, reg, g := setup(t)
	d := create(t, h)
	reg.approved = true
	command(t, h, d, "validate", "")
	command(t, h, d, "start", "start")
	code, data := command(t, h, d, "stop", "")
	if code != 202 || resultDraft(t, data).State != "stopping" {
		t.Fatal(code, data)
	}
	code, _ = command(t, h, d, "stop", "")
	if code != 409 {
		t.Fatal(code)
	}
	if g.calls[len(g.calls)-1] != diagnosis.Stop {
		t.Fatal(g.calls)
	}
}
func TestWrongGatewayBindingRejected(t *testing.T) {
	h, reg, g := setup(t)
	d := create(t, h)
	reg.approved = true
	g.badBinding = true
	code, _ := command(t, h, d, "validate", "")
	if code != 503 {
		t.Fatal(code)
	}
}
func TestStrictRequestParsing(t *testing.T) {
	h, _, g := setup(t)
	b, _ := json.Marshal(requestBody{Version: Version, Configuration: &testConfiguration})
	for name, body := range map[string]string{
		"unknown-field":     strings.TrimSuffix(string(b), "}") + `,"config_path":"/protected/launch.json"}`,
		"duplicate-version": strings.TrimSuffix(string(b), "}") + `,"version":"diagnosis-api.v1"}`,
		"duplicate-nested":  strings.Replace(string(b), `"requests":3`, `"requests":3,"requests":9`, 1),
		"trailing":          string(b) + " {}",
		"oversize":          strings.Repeat(" ", maxBodyBytes) + string(b),
		"wrong-version":     strings.Replace(string(b), Version, "future", 1),
	} {
		t.Run(name, func(t *testing.T) {
			code, _ := request(t, h, "POST", "/drafts", "alice", "key", body)
			if code != 400 {
				t.Fatal(code)
			}
		})
	}
	if len(g.calls) != 0 {
		t.Fatal(g.calls)
	}
}
func TestConcurrentSameStartDispatchesOnce(t *testing.T) {
	h, reg, g := setup(t)
	d := create(t, h)
	reg.approved = true
	command(t, h, d, "validate", "")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := command(t, h, d, "start", "same")
			if code != 200 && code != 202 {
				t.Errorf("code %d", code)
			}
		}()
	}
	wg.Wait()
	if len(g.calls) != 2 {
		t.Fatal(g.calls)
	}
}
func TestRealHTTPHandlerWithFakeEngine(t *testing.T) {
	h, _, g := setup(t)
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+BasePath+"/catalog", nil)
	req.Header.Set("Authorization", "Bearer alice")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || len(g.calls) != 0 {
		t.Fatal(res.StatusCode, g.calls)
	}
}

func TestCreateReceiptReadDoesNotReplay(t *testing.T) {
	h, _, g := setup(t)
	d := create(t, h)
	code, data := request(t, h, "GET", "/drafts", "alice", "create-1", nil)
	if code != 200 || resultDraft(t, data).ID != d.ID || len(g.calls) != 0 {
		t.Fatal(code, data, g.calls)
	}
	code, _ = request(t, h, "GET", "/drafts", "bob", "create-1", nil)
	if code != 404 {
		t.Fatal(code)
	}
}

type blockedBody struct {
	entered chan struct{}
	release chan struct{}
	reader  io.Reader
	once    sync.Once
}

func (b *blockedBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.reader.Read(p)
}
func (b *blockedBody) Close() error { return nil }
func TestSlowOtherDraftBodyDoesNotBlockStop(t *testing.T) {
	h, reg, g := setup(t)
	d := create(t, h)
	reg.approved = true
	command(t, h, d, "validate", "")
	command(t, h, d, "start", "start")
	_, data := request(t, h, "POST", "/drafts", "alice", "second", requestBody{Version: Version, Configuration: &testConfiguration})
	other := resultDraft(t, data)
	raw, _ := json.Marshal(requestBody{Version: Version, ExpectedRevision: other.Revision, Configuration: &testConfiguration})
	body := &blockedBody{entered: make(chan struct{}), release: make(chan struct{}), reader: bytes.NewReader(raw)}
	r := httptest.NewRequest("PATCH", BasePath+"/drafts/"+other.ID, body)
	r.Header.Set("Authorization", "Bearer alice")
	r.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), r) }()
	<-body.entered
	stopped := make(chan int, 1)
	go func() { code, _ := command(t, h, d, "stop", ""); stopped <- code }()
	select {
	case code := <-stopped:
		if code != 202 {
			t.Errorf("stop %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Error("slow unrelated body blocked stop")
	}
	close(body.release)
	<-done
	if g.calls[len(g.calls)-1] != diagnosis.Stop {
		t.Fatal(g.calls)
	}
}

type deadlineGateway struct{}

func (deadlineGateway) Call(ctx context.Context, _ diagnosis.Request) (diagnosis.Response, error) {
	<-ctx.Done()
	return diagnosis.Response{}, ctx.Err()
}
func TestGatewayTimeoutLeavesUnknownWithoutReplay(t *testing.T) {
	h, reg, _ := setup(t)
	d := create(t, h)
	reg.approved = true
	command(t, h, d, "validate", "")
	h.options.Gateway = deadlineGateway{}
	h.options.CallTimeout = 20 * time.Millisecond
	start := time.Now()
	code, _ := command(t, h, d, "start", "start")
	if code != 503 || time.Since(start) > time.Second {
		t.Fatal("unbounded gateway", code)
	}
	code, data := command(t, h, d, "start", "start")
	if code != 200 || resultDraft(t, data).State != "unknown" {
		t.Fatal(code, data)
	}
}

func TestCaseAliasesAreNotCanonicalFields(t *testing.T) {
	h, _, _ := setup(t)
	b, _ := json.Marshal(requestBody{Version: Version, Configuration: &testConfiguration})
	for name, raw := range map[string]string{
		"version":          strings.Replace(string(b), `version`, `Version`, 1),
		"nested":           strings.Replace(string(b), `requests`, `Requests`, 1),
		"duplicate":        strings.TrimSuffix(string(b), "}") + `,"Version":"diagnosis-api.v1"}`,
		"nested-duplicate": strings.Replace(string(b), `"requests":3`, `"requests":3,"Requests":9`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			code, _ := request(t, h, "POST", "/drafts", "alice", "alias-key", raw)
			if code != 400 {
				t.Fatal("alias accepted", code)
			}
		})
	}
}

type snapshotGateway struct{ snapshot diagnosis.Snapshot }

func (g snapshotGateway) Call(_ context.Context, r diagnosis.Request) (diagnosis.Response, error) {
	s := g.snapshot
	s.TaskID = r.TaskID
	s.PreparationRef = r.PreparationRef
	return diagnosis.Response{Version: diagnosis.Version, RequestID: r.RequestID, Snapshot: &s}, nil
}
func TestSnapshotContractMatchesGateway(t *testing.T) {
	cases := []struct {
		name  string
		s     diagnosis.Snapshot
		valid bool
	}{
		{"unknown_not_started", diagnosis.Snapshot{State: diagnosis.Unknown}, false},
		{"unknown_persisted_drain_failed", diagnosis.Snapshot{State: diagnosis.Unknown, Started: true, ResultReady: true}, true},
		{"unknown_drained_result_failed", diagnosis.Snapshot{State: diagnosis.Unknown, Started: true, DrainComplete: true}, true},
		{"prepared_with_result", diagnosis.Snapshot{State: diagnosis.Prepared, ResultReady: true}, false},
		{"terminal_without_result", diagnosis.Snapshot{State: diagnosis.Failed, Started: true, DrainComplete: true}, false},
		{"completed", diagnosis.Snapshot{State: diagnosis.Completed, Started: true, DrainComplete: true, ResultReady: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{options: Options{Gateway: snapshotGateway{tc.s}, CallTimeout: time.Second}}
			s := &storedDraft{}
			err := h.call(t.Context(), s, &Binding{TaskID: "42", PreparationRef: "prepared"}, diagnosis.Status, "")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}
