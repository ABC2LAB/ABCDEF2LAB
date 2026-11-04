package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	canaryKey  = "sk-or-v1-synthetic-canary-0000"
	chatPath   = "/api/v1/chat/completions"
	genPath    = "/api/v1/generation"
	pinnedSlug = "example-provider/fp8"
	// The official profile's sampling (model card, thinking mode, general tasks) plus seed.
	pinnedSampling = `{"temperature":1.0,"top_p":0.95,"top_k":20,"min_p":0.0,"presence_penalty":1.5,` +
		`"repetition_penalty":1.0,"seed":42}`
	// How the gateway re-encodes it ahead of the provider pin.
	pinnedSamplingFields = `"temperature":1,"top_p":0.95,"top_k":20,"min_p":0,"presence_penalty":1.5,` +
		`"repetition_penalty":1,"seed":42`
)

type upstreamCall struct {
	method, path, query, body string
	header                    http.Header
}

// fakeProvider stands in for the LLM API: chat completions answer with a
// generation id (JSON or SSE), generation lookups answer from a script.
type fakeProvider struct {
	mu          sync.Mutex
	calls       []upstreamCall
	next        int
	omitID      bool
	fixedID     string // answer every request with this id
	padBytes    int    // put this much JSON before the id
	slowStarted chan struct{}
	lookups     map[string][]int // per id: status codes to return in order (200 = provider data)
}

// lookupEmptyProvider scripts a 200 whose provider_name is empty: stats exist
// but do not say who served, so the id must not count as verified.
const lookupEmptyProvider = 1

func (p *fakeProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.calls = append(p.calls, upstreamCall{r.Method, r.URL.Path, r.URL.RawQuery, string(body), r.Header.Clone()})
	p.mu.Unlock()
	switch r.URL.Path {
	case chatPath:
		if bytes.Contains(body, []byte(`"slow"`)) {
			p.slowStarted <- struct{}{}
			<-r.Context().Done()
			return
		}
		if bytes.Contains(body, []byte(`"partial"`)) {
			// One event with an id, then the provider keeps the stream open.
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"gen-partial\",\"choices\":[]}\n\n")
			w.(http.Flusher).Flush()
			p.slowStarted <- struct{}{}
			<-r.Context().Done()
			return
		}
		if bytes.Contains(body, []byte(`"broken"`)) {
			panic(http.ErrAbortHandler) // drop the connection without an answer
		}
		p.mu.Lock()
		p.next++
		id := fmt.Sprintf("gen-%d", p.next)
		if p.fixedID != "" {
			id = p.fixedID
		}
		omit, pad := p.omitID, p.padBytes
		p.mu.Unlock()
		if bytes.Contains(body, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, ": PROCESSING\n\n")
			_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n", id)
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if omit {
			_, _ = io.WriteString(w, `{"choices":[]}`)
			return
		}
		if pad > 0 {
			_, _ = fmt.Fprintf(w, `{"pad":%q,"id":%q}`, strings.Repeat("a", pad), id)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":%q,"choices":[{"message":{"content":"hi"}}]}`, id)
	case genPath:
		id := r.URL.Query().Get("id")
		p.mu.Lock()
		script := p.lookups[id]
		status := http.StatusNotFound
		if len(script) > 0 {
			status, p.lookups[id] = script[0], script[1:]
		}
		p.mu.Unlock()
		if status == lookupEmptyProvider {
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"provider_name":"","provider_responses":[]}}`, id)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"provider_name":"Example","total_cost":1,"provider_responses":[`+
			`{"provider_name":"Example","endpoint_id":"ep-1","status":200,"latency":5}]}}`, id)
	default:
		w.WriteHeader(http.StatusTeapot)
	}
}

func (p *fakeProvider) chatCalls() []upstreamCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []upstreamCall
	for _, c := range p.calls {
		if c.path == chatPath {
			out = append(out, c)
		}
	}
	return out
}

type gatewayHarness struct {
	g        *llmGateway
	provider *fakeProvider
	front    *httptest.Server
	dirPath  string
	sleeps   []time.Duration
}

func newHarness(t *testing.T, backend string) *gatewayHarness {
	t.Helper()
	h := &gatewayHarness{provider: &fakeProvider{slowStarted: make(chan struct{}, 1), lookups: map[string][]int{}}}
	var upstream *httptest.Server
	cfg := gatewayConfig{backend: backend, allowPath: chatPath}
	if backend == backendAPI {
		upstream = httptest.NewTLSServer(h.provider)
		cfg.provider, cfg.generationPath, cfg.key = pinnedSlug, genPath, canaryKey
		sampling, ok := parseSampling(pinnedSampling)
		if !ok {
			t.Fatal("test sampling rejected")
		}
		cfg.sampling = sampling
		cfg.verifyAttempts, cfg.verifyInterval = 3, time.Millisecond
	} else {
		upstream = httptest.NewServer(h.provider)
	}
	t.Cleanup(upstream.Close)
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.upstream = u
	path, dir := privateTestDir(t)
	h.dirPath = path
	g, code := newLLMGateway(dir, cfg, upstream.Client().Transport, time.Now,
		func(d time.Duration) { h.sleeps = append(h.sleeps, d) })
	if code != "" {
		t.Fatal(code)
	}
	t.Cleanup(func() { _ = g.close() })
	h.g = g
	h.front = httptest.NewServer(g)
	t.Cleanup(h.front.Close)
	return h
}

func (h *gatewayHarness) post(t *testing.T, path, contentType, body string, header map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.front.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for name, value := range header {
		req.Header.Set(name, value)
	}
	resp, err := h.front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	return resp, string(got)
}

func (h *gatewayHarness) rows(t *testing.T) []llmRow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.dirPath, llmLogName))
	if err != nil {
		t.Fatal(err)
	}
	var rows []llmRow
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var row llmRow
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	return rows
}

// waitRows waits for n rows: a streamed response can reach the client before
// the gateway finishes its bookkeeping for that request.
func (h *gatewayHarness) waitRows(t *testing.T, n int) []llmRow {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows := h.rows(t)
		if len(rows) >= n || time.Now().After(deadline) {
			return rows
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *gatewayHarness) sealAndWait(t *testing.T) verifiedMarker {
	t.Helper()
	h.g.seal()
	select {
	case err := <-h.g.verifyErr:
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("verification did not finish")
	}
	data, err := os.ReadFile(filepath.Join(h.dirPath, llmVerifiedName))
	if err != nil {
		t.Fatal(err)
	}
	var marker verifiedMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		t.Fatal(err)
	}
	return marker
}

const engineBody = `{"model":"example/model","messages":[{"role":"user","content":"hi"}]}`

func TestGatewayPinsProviderAndUsesOwnKey(t *testing.T) {
	h := newHarness(t, backendAPI)
	resp, body := h.post(t, chatPath, "application/json", engineBody, map[string]string{
		"Authorization": "Bearer engine-placeholder", "X-Session-Id": "s-1", "Cookie": "c=1",
		"Accept-Encoding": "gzip", "Accept": "application/json", "HTTP-Referer": "https://engine.example",
	})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"gen-1"`) {
		t.Fatalf("response %d %q", resp.StatusCode, body)
	}
	calls := h.provider.chatCalls()
	if len(calls) != 1 {
		t.Fatalf("calls %d", len(calls))
	}
	want := `{` + pinnedSamplingFields + `,"provider":{"order":["` + pinnedSlug + `"],"allow_fallbacks":false},` +
		engineBody[1:]
	if calls[0].body != want {
		t.Fatalf("upstream body %q, want %q", calls[0].body, want)
	}
	header := calls[0].header
	if got := header.Get("Authorization"); got != "Bearer "+canaryKey {
		t.Fatalf("authorization not swapped")
	}
	for _, name := range []string{"X-Session-Id", "Cookie", "Http-Referer", "X-Forwarded-For", "Forwarded"} {
		if header.Get(name) != "" {
			t.Fatalf("engine header %s leaked upstream", name)
		}
	}
	if header.Get("Accept-Encoding") != "identity" || header.Get("Accept") != "application/json" {
		t.Fatalf("accept headers %q %q", header.Get("Accept-Encoding"), header.Get("Accept"))
	}
	rows := h.waitRows(t, 1)
	if len(rows) != 1 || rows[0].Decision != "forwarded" || rows[0].GenerationID != "gen-1" ||
		rows[0].RequestBytes != int64(len(want)) || rows[0].Status != http.StatusOK {
		t.Fatalf("rows %+v", rows)
	}
}

func TestGatewayStreamsAndFindsEventID(t *testing.T) {
	h := newHarness(t, backendAPI)
	resp, body := h.post(t, chatPath, "application/json; charset=utf-8",
		`{"model":"m","stream":true,"messages":[]}`, nil)
	if resp.StatusCode != http.StatusOK || !strings.HasSuffix(body, "data: [DONE]\n\n") ||
		!strings.Contains(body, `"gen-1"`) {
		t.Fatalf("stream %d %q", resp.StatusCode, body)
	}
	if rows := h.waitRows(t, 1); len(rows) != 1 || rows[0].GenerationID != "gen-1" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestGatewayPinsEmptyAndSpacedObjects(t *testing.T) {
	h := newHarness(t, backendAPI)
	for _, body := range []string{"{}", " \n{ }", "\t{\"model\":\"m\"}\n"} {
		if resp, _ := h.post(t, chatPath, "application/json", body, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("%q: %d", body, resp.StatusCode)
		}
	}
	for _, call := range h.provider.chatCalls() {
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal([]byte(call.body), &decoded); err != nil {
			t.Fatalf("upstream body %q is not JSON: %v", call.body, err)
		}
		if string(decoded["provider"]) != `{"order":["`+pinnedSlug+`"],"allow_fallbacks":false}` {
			t.Fatalf("provider %s", decoded["provider"])
		}
		if string(decoded["temperature"]) != "1" || string(decoded["top_k"]) != "20" || string(decoded["seed"]) != "42" {
			t.Fatalf("sampling not pinned in %q", call.body)
		}
	}
}

func TestGatewayRejectsWithoutForwarding(t *testing.T) {
	cases := []struct {
		name, method, path, contentType, body string
		header                                map[string]string
		status                                int
		decision                              string
	}{
		{"get", http.MethodGet, chatPath, "application/json", "", nil, 403, "rejected_method"},
		{"models", http.MethodPost, "/api/v1/models", "application/json", "{}", nil, 403, "rejected_path"},
		{"query", http.MethodPost, chatPath + "?x=1", "application/json", "{}", nil, 403, "rejected_path"},
		{"encoded", http.MethodPost, "/api/v1/chat%2Fcompletions", "application/json", "{}", nil, 403, "rejected_path"},
		{"upgrade", http.MethodPost, chatPath, "application/json", "{}", map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"}, 403, "rejected_path"},
		{"text", http.MethodPost, chatPath, "text/plain", "{}", nil, 415, "rejected_content_type"},
		{"no type", http.MethodPost, chatPath, "", "{}", nil, 415, "rejected_content_type"},
		{"invalid", http.MethodPost, chatPath, "application/json", `{"a":`, nil, 400, "rejected_body"},
		{"array", http.MethodPost, chatPath, "application/json", `[{}]`, nil, 400, "rejected_body"},
		{"trailing", http.MethodPost, chatPath, "application/json", `{} {}`, nil, 400, "rejected_body"},
		{"duplicate", http.MethodPost, chatPath, "application/json", `{"model":"a","model":"b"}`, nil, 400, "rejected_body"},
		{"provider", http.MethodPost, chatPath, "application/json", `{"provider":{"order":["other"]}}`, nil, 400, "rejected_body"},
		{"escaped provider", http.MethodPost, chatPath, "application/json", `{"provider":{}}`, nil, 400, "rejected_body"},
		// An engine-chosen sampling value would mix with the pinned one: refused, never overwritten.
		{"temperature", http.MethodPost, chatPath, "application/json", `{"temperature":0}`, nil, 400, "rejected_body"},
		{"top_p", http.MethodPost, chatPath, "application/json", `{"top_p":1}`, nil, 400, "rejected_body"},
		{"top_k", http.MethodPost, chatPath, "application/json", `{"top_k":1}`, nil, 400, "rejected_body"},
		{"min_p", http.MethodPost, chatPath, "application/json", `{"min_p":0}`, nil, 400, "rejected_body"},
		{"presence_penalty", http.MethodPost, chatPath, "application/json", `{"presence_penalty":0}`, nil, 400, "rejected_body"},
		{"repetition_penalty", http.MethodPost, chatPath, "application/json", `{"repetition_penalty":1}`, nil, 400, "rejected_body"},
		{"seed", http.MethodPost, chatPath, "application/json", `{"seed":1}`, nil, 400, "rejected_body"},
		{"frequency_penalty", http.MethodPost, chatPath, "application/json", `{"frequency_penalty":0}`, nil, 400, "rejected_body"},
		{"too large", http.MethodPost, chatPath, "application/json", `{"x":"` + strings.Repeat("a", maxLLMRequestBytes) + `"}`, nil, 413, "rejected_too_large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, backendAPI)
			req, err := http.NewRequest(tc.method, h.front.URL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			for name, value := range tc.header {
				req.Header.Set(name, value)
			}
			resp, err := h.front.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.status)
			}
			if calls := h.provider.chatCalls(); len(calls) != 0 {
				t.Fatalf("forwarded %d", len(calls))
			}
			if rows := h.waitRows(t, 1); len(rows) != 1 || rows[0].Decision != tc.decision || rows[0].GenerationID != "" {
				t.Fatalf("rows %+v", rows)
			}
		})
	}
}

func TestGatewayLocalForwardsUnchangedWithoutKey(t *testing.T) {
	h := newHarness(t, backendLocal)
	resp, _ := h.post(t, chatPath, "application/json", engineBody, map[string]string{
		"Authorization": "Bearer engine-placeholder", "X-Session-Id": "s-1"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	calls := h.provider.chatCalls()
	if len(calls) != 1 || calls[0].body != engineBody {
		t.Fatalf("local body changed: %+v", calls)
	}
	if calls[0].header.Get("Authorization") != "" || calls[0].header.Get("X-Session-Id") != "" {
		t.Fatalf("local upstream got engine headers")
	}
	h.waitRows(t, 1)
	marker := h.sealAndWait(t)
	if marker.Backend != backendLocal || marker.Sampling != nil || marker.Forwarded != 1 || len(marker.Generations) != 0 ||
		!marker.LogComplete {
		t.Fatalf("marker %+v", marker)
	}
	h.provider.mu.Lock()
	defer h.provider.mu.Unlock()
	for _, call := range h.provider.calls {
		if call.path == genPath {
			t.Fatal("local backend looked up generations")
		}
	}
}

func TestGatewaySealCancelsVerifiesAndNeverWritesKey(t *testing.T) {
	h := newHarness(t, backendAPI)
	h.post(t, chatPath, "application/json", engineBody, nil) // gen-1
	h.post(t, chatPath, "application/json", engineBody, nil) // gen-2
	h.provider.lookups["gen-1"] = []int{http.StatusNotFound, http.StatusOK}
	h.provider.lookups["gen-2"] = []int{http.StatusUnauthorized}
	h.waitRows(t, 2)

	slow := make(chan int, 1)
	go func() {
		resp, _ := h.post(t, chatPath, "application/json", `{"model":"slow"}`, nil)
		slow <- resp.StatusCode
	}()
	<-h.provider.slowStarted
	marker := h.sealAndWait(t)
	if status := <-slow; status != http.StatusBadGateway {
		t.Fatalf("in-flight request after seal: %d", status)
	}
	if resp, _ := h.post(t, chatPath, "application/json", engineBody, nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("after seal: %d", resp.StatusCode)
	}

	want, _ := parseSampling(pinnedSampling)
	if marker.Role != roleGateway || marker.Backend != backendAPI || marker.Provider != pinnedSlug ||
		marker.Sampling == nil || *marker.Sampling != *want || marker.Forwarded != 2 || marker.CancelledBySeal != 1 || marker.MissingIDs != 0 || !marker.LogComplete {
		t.Fatalf("marker %+v", marker)
	}
	if len(marker.Generations) != 2 {
		t.Fatalf("generations %+v", marker.Generations)
	}
	first, second := marker.Generations[0], marker.Generations[1]
	if first.ID != "gen-1" || first.Status != "verified" || first.ProviderName != "Example" ||
		len(first.Attempts) != 1 || first.Attempts[0] != (providerAttempt{"Example", "ep-1", 200}) {
		t.Fatalf("gen-1 %+v", first)
	}
	if second.ID != "gen-2" || second.Status != "unverified" || second.ProviderName != "" {
		t.Fatalf("gen-2 %+v", second)
	}
	if len(h.sleeps) != 1 || h.sleeps[0] != time.Millisecond {
		t.Fatalf("retry waits %v (401 must not retry, 404 must)", h.sleeps)
	}
	h.provider.mu.Lock()
	lookups := 0
	for _, call := range h.provider.calls {
		if call.path == genPath {
			lookups++
			if call.header.Get("Authorization") != "Bearer "+canaryKey || call.method != http.MethodGet {
				t.Fatalf("lookup without gateway key")
			}
		}
	}
	h.provider.mu.Unlock()
	if lookups != 3 {
		t.Fatalf("lookups %d", lookups)
	}

	rows := h.waitRows(t, 4)
	decisions := map[string]int{}
	for _, row := range rows {
		decisions[row.Decision]++
	}
	if decisions["forwarded"] != 2 || decisions["cancelled_by_seal"] != 1 || decisions["rejected_after_seal"] != 1 {
		t.Fatalf("decisions %v", decisions)
	}
	entries, err := os.ReadDir(h.dirPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(h.dirPath, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{canaryKey, "engine-placeholder", `"model"`, chatPath} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatalf("%s contains %q", entry.Name(), secret)
			}
		}
	}
}

func TestGatewayCountsResponsesWithoutID(t *testing.T) {
	for name, set := range map[string]func(*fakeProvider){
		"no id":            func(p *fakeProvider) { p.omitID = true },
		"id past the scan": func(p *fakeProvider) { p.padBytes = maxIDScanBytes },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, backendAPI)
			set(h.provider)
			if resp, _ := h.post(t, chatPath, "application/json", engineBody, nil); resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			h.waitRows(t, 1)
			marker := h.sealAndWait(t)
			if marker.MissingIDs != 1 || marker.Forwarded != 1 || len(marker.Generations) != 0 {
				t.Fatalf("marker %+v", marker)
			}
		})
	}
}

func TestGatewayEmptyProviderNameStaysUnverified(t *testing.T) {
	h := newHarness(t, backendAPI)
	h.post(t, chatPath, "application/json", engineBody, nil)
	h.provider.lookups["gen-1"] = []int{lookupEmptyProvider, lookupEmptyProvider, lookupEmptyProvider}
	h.waitRows(t, 1)
	marker := h.sealAndWait(t)
	if len(marker.Generations) != 1 || marker.Generations[0].Status != "unverified" || len(h.sleeps) != 2 {
		t.Fatalf("marker %+v sleeps %v", marker.Generations, h.sleeps)
	}
}

func TestGatewayLooksUpEachGenerationOnce(t *testing.T) {
	h := newHarness(t, backendAPI)
	h.provider.fixedID = "gen-same"
	h.provider.lookups["gen-same"] = []int{http.StatusOK}
	h.post(t, chatPath, "application/json", engineBody, nil)
	h.post(t, chatPath, "application/json", engineBody, nil)
	h.waitRows(t, 2)
	marker := h.sealAndWait(t)
	if marker.Forwarded != 2 || len(marker.Generations) != 1 || marker.Generations[0].Status != "verified" {
		t.Fatalf("marker %+v", marker)
	}
}

func TestGatewayOutcomeFollowsCompletionNotSealTiming(t *testing.T) {
	cases := []struct {
		complete, sealed, cancelled bool
		want                        string
	}{
		{true, true, true, "forwarded"}, // finished just before seal: still a served generation
		{true, false, false, "forwarded"},
		{false, true, true, "cancelled_by_seal"},
		{false, true, false, "upstream_error"},
		{false, false, true, "upstream_error"},
		{false, false, false, "upstream_error"},
	}
	for _, tc := range cases {
		if got := outcome(tc.complete, tc.sealed, tc.cancelled); got != tc.want {
			t.Fatalf("outcome(%v,%v,%v) = %s, want %s", tc.complete, tc.sealed, tc.cancelled, got, tc.want)
		}
	}
}

func TestGatewayAbortedStreamStillDrainsAndVerifies(t *testing.T) {
	h := newHarness(t, backendAPI)
	h.provider.lookups["gen-partial"] = []int{http.StatusOK}
	req, err := http.NewRequest(http.MethodPost, h.front.URL+chatPath, strings.NewReader(`{"model":"partial","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-h.provider.slowStarted
	first := make([]byte, 16)
	if _, err := io.ReadFull(resp.Body, first); err != nil || !strings.HasPrefix(string(first), "data: ") {
		t.Fatalf("first event %q %v", first, err)
	}
	// Seal cuts the stream mid-copy; the proxy aborts with ErrAbortHandler.
	marker := h.sealAndWait(t)
	if marker.CancelledBySeal != 1 || marker.Forwarded != 0 || len(marker.Generations) != 1 ||
		marker.Generations[0].ID != "gen-partial" || marker.Generations[0].Status != "verified" {
		t.Fatalf("marker %+v", marker)
	}
	rows := h.waitRows(t, 1)
	if len(rows) != 1 || rows[0].Decision != "cancelled_by_seal" || rows[0].GenerationID != "gen-partial" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestGatewayUpstreamFailureIsCountedSeparately(t *testing.T) {
	h := newHarness(t, backendAPI)
	if resp, _ := h.post(t, chatPath, "application/json", `{"model":"broken"}`, nil); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d", resp.StatusCode)
	}
	h.waitRows(t, 1)
	marker := h.sealAndWait(t)
	if marker.UpstreamErrors != 1 || marker.Forwarded != 0 || marker.CancelledBySeal != 0 || marker.MissingIDs != 0 {
		t.Fatalf("marker %+v", marker)
	}
}

func TestGenerationIDScanning(t *testing.T) {
	jsonCases := map[string]string{
		`{"id":"gen-1","x":1}`:                        "gen-1",
		`{"object":"chat","choices":[],"id":"gen-2"}`: "gen-2",
		`{"choices":[{"id":"nested"}]}`:               "",
		`{"id":"gen-3"`:                               "gen-3",
		`{"x":"` + strings.Repeat("a", 10) + ``:       "",
		`[{"id":"gen-4"}]`:                            "",
	}
	for head, want := range jsonCases {
		if got := jsonID([]byte(head)); got != want {
			t.Fatalf("jsonID(%q) = %q, want %q", head, got, want)
		}
	}
	sse := ": c\n\ndata: {\"choices\":[]}\n\ndata: {\"id\":\"gen-5\"}\n\ndata: [DONE]\n\n"
	if got := sseID([]byte(sse)); got != "gen-5" {
		t.Fatalf("sseID = %q", got)
	}
	if got := sseID([]byte("data: {\"id\":\"gen-6\"}")); got != "" {
		t.Fatalf("incomplete event line must not count: %q", got)
	}
	for _, id := range []string{"gen 7", "gen/7", strings.Repeat("a", 129), ""} {
		scan := &idScanner{}
		scan.head.WriteString(`{"id":` + fmt.Sprintf("%q", id) + `}`)
		if got := scan.id(); got != "" {
			t.Fatalf("id %q accepted", id)
		}
	}
}

func TestRunGatewayFlagRules(t *testing.T) {
	path, _ := privateTestDir(t)
	api := []string{"-role", "llm-gateway", "-backend", "api", "-listen", "127.0.0.1:0", "-upstream", "https://llm.example",
		"-allow-path", chatPath, "-log-dir", path, "-provider", pinnedSlug, "-sampling", pinnedSampling,
		"-generation-path", genPath, "-verify-attempts", "3", "-verify-interval", "2s"}
	local := []string{"-role", "llm-gateway", "-backend", "local", "-listen", "127.0.0.1:0", "-upstream",
		"http://llm-host:11434", "-allow-path", "/v1/chat/completions", "-log-dir", path}
	replace := func(args []string, flag, value string) []string {
		out := append([]string(nil), args...)
		for i := range out {
			if out[i] == flag {
				out[i+1] = value
				return out
			}
		}
		return append(out, flag, value)
	}
	drop := func(args []string, flag string) []string {
		var out []string
		for i := 0; i < len(args); i++ {
			if args[i] == flag {
				i++
				continue
			}
			out = append(out, args[i])
		}
		return out
	}
	cases := []struct {
		name string
		args []string
		key  string
		code string
	}{
		{"api http upstream", replace(api, "-upstream", "http://llm.example"), canaryKey, invalidInput},
		{"api upstream path", replace(api, "-upstream", "https://llm.example/api"), canaryKey, invalidInput},
		{"api no provider", drop(api, "-provider"), canaryKey, invalidInput},
		{"api bad provider", replace(api, "-provider", "Other Provider"), canaryKey, invalidInput},
		{"api no sampling", drop(api, "-sampling"), canaryKey, invalidInput},
		{"api sampling missing seed", replace(api, "-sampling", strings.Replace(pinnedSampling, `,"seed":42`, "", 1)), canaryKey, invalidInput},
		{"api sampling unknown key", replace(api, "-sampling", strings.Replace(pinnedSampling, `"seed"`, `"seeds"`, 1)), canaryKey, invalidInput},
		{"api sampling trailing", replace(api, "-sampling", pinnedSampling+" {}"), canaryKey, invalidInput},
		{"local with sampling", append(append([]string(nil), local...), "-sampling", pinnedSampling), "", invalidInput},
		{"api no generation path", drop(api, "-generation-path"), canaryKey, invalidInput},
		{"api zero attempts", replace(api, "-verify-attempts", "0"), canaryKey, invalidInput},
		{"api too many attempts", replace(api, "-verify-attempts", "101"), canaryKey, invalidInput},
		{"api long interval", replace(api, "-verify-interval", "11m"), canaryKey, invalidInput},
		{"api no key", api, "", invalidInput},
		{"api key with space", api, "sk bad", invalidInput},
		{"local https", replace(local, "-upstream", "https://llm-host:11434"), "", invalidInput},
		{"local with key", local, canaryKey, invalidInput},
		{"local with provider", append(append([]string(nil), local...), "-provider", pinnedSlug), "", invalidInput},
		{"local with verify", append(append([]string(nil), local...), "-verify-attempts", "1"), "", invalidInput},
		{"bad allow path", replace(local, "-allow-path", "chat"), "", invalidInput},
		{"no backend", drop(local, "-backend"), "", invalidInput},
		{"engine with key", []string{"-role", "engine", "-listen", ":0", "-upstream", "http://t:1", "-log-dir", path}, canaryKey, keyNotAllowed},
		{"probe with key", []string{"-role", "probe"}, canaryKey, keyNotAllowed},
		{"engine with gateway flag", []string{"-role", "engine", "-listen", ":0", "-upstream", "http://t:1", "-log-dir", path, "-backend", "api"}, "", invalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(llmKeyEnv, tc.key)
			var stdout, stderr bytes.Buffer
			if code := run(tc.args, strings.NewReader(""), &stdout, &stderr); code == 0 {
				t.Fatalf("accepted")
			}
			if !strings.HasPrefix(stderr.String(), tc.code+"\n") {
				t.Fatalf("stderr %q", stderr.String())
			}
			if strings.Contains(stderr.String(), canaryKey) || strings.Contains(stdout.String(), canaryKey) {
				t.Fatal("key echoed")
			}
		})
	}
	if _, ok := gatewayFromFlags("api", "https://llm.example", chatPath, pinnedSlug, pinnedSampling, genPath, 3,
		time.Second, canaryKey); !ok {
		t.Fatal("valid api config rejected")
	}
	if _, ok := gatewayFromFlags("local", "http://llm-host:11434", "/v1/chat/completions", "", "", "", 0, 0, ""); !ok {
		t.Fatal("valid local config rejected")
	}
}

func TestParseSamplingRanges(t *testing.T) {
	base := map[string]string{"temperature": "1.0", "top_p": "0.95", "top_k": "20", "min_p": "0.0",
		"presence_penalty": "1.5", "repetition_penalty": "1.0", "seed": "42"}
	render := func(change map[string]string) string {
		parts := []string{}
		for _, name := range []string{"temperature", "top_p", "top_k", "min_p", "presence_penalty", "repetition_penalty", "seed"} {
			value, ok := base[name]
			if v, changed := change[name]; changed {
				value, ok = v, v != ""
			}
			if ok {
				parts = append(parts, `"`+name+`":`+value)
			}
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	if _, ok := parseSampling(render(nil)); !ok {
		t.Fatal("official sampling rejected")
	}
	accepted := []map[string]string{
		{"temperature": "0"}, {"temperature": "2"}, {"top_p": "1"}, {"top_k": "1"}, {"top_k": "1000"},
		{"min_p": "1"}, {"presence_penalty": "-2"}, {"presence_penalty": "2"}, {"repetition_penalty": "2"},
		{"seed": "0"}, {"seed": "9223372036854775807"},
	}
	for _, change := range accepted {
		if _, ok := parseSampling(render(change)); !ok {
			t.Fatalf("%v rejected", change)
		}
	}
	rejected := []map[string]string{
		{"temperature": "-0.1"}, {"temperature": "2.01"}, {"top_p": "0"}, {"top_p": "1.01"}, {"top_k": "0"},
		{"top_k": "1001"}, {"top_k": "20.5"}, {"min_p": "-0.1"}, {"min_p": "1.1"}, {"presence_penalty": "-2.1"},
		{"presence_penalty": "2.1"}, {"repetition_penalty": "0"}, {"repetition_penalty": "2.1"}, {"seed": "-1"},
		{"seed": "1.5"}, {"seed": "9223372036854775808"}, {"temperature": `"1"`}, {"temperature": "null"},
		{"temperature": ""}, {"top_p": ""}, {"top_k": ""}, {"min_p": ""}, {"presence_penalty": ""},
		{"repetition_penalty": ""}, {"seed": ""},
	}
	for _, change := range rejected {
		if _, ok := parseSampling(render(change)); ok {
			t.Fatalf("%v accepted", change)
		}
	}
	// All seven keys present plus one more: an unknown key is refused on its own, not because another is missing.
	extra := strings.TrimSuffix(render(nil), "}") + `,"frequency_penalty":0}`
	for _, raw := range []string{"", "{}", "[]", `{"temperature":1} x`, extra,
		strings.Repeat(" ", maxSamplingFlagBytes+1) + render(nil)} {
		if _, ok := parseSampling(raw); ok {
			t.Fatalf("%q accepted", raw)
		}
	}
}
