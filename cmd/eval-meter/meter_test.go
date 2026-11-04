package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const sentinel = "eval-meter-synthetic-sentinel"

func privateTestDir(t *testing.T) (string, *os.File) {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, code := openPrivateDir(path)
	if code != "" {
		t.Fatalf("private dir: %s", code)
	}
	t.Cleanup(func() { _ = dir.Close() })
	return path, dir
}

func targetURL(t *testing.T, server *httptest.Server) *url.URL {
	t.Helper()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

type seenRequest struct {
	host, method, uri, body string
	header                  http.Header
}

func recordingUpstream(t *testing.T) (*httptest.Server, func() []seenRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []seenRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, seenRequest{host: r.Host, method: r.Method, uri: r.RequestURI, body: string(body), header: r.Header.Clone()})
		mu.Unlock()
		_, _ = io.WriteString(w, "upstream")
	}))
	t.Cleanup(server.Close)
	return server, func() []seenRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenRequest(nil), seen...)
	}
}

func newTestEngine(t *testing.T, upstream *httptest.Server) (*engineMeter, *httptest.Server, string) {
	t.Helper()
	path, dir := privateTestDir(t)
	m, code := newEngineMeter(dir, targetURL(t, upstream), time.Now, 0)
	if code != "" {
		t.Fatalf("engine meter: %s", code)
	}
	front := httptest.NewServer(m)
	t.Cleanup(func() {
		front.Close()
		_ = m.close()
	})
	return m, front, path
}

func readRows[T any](t *testing.T, path string) []T {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []T
	for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
		if line == "" {
			continue
		}
		var row T
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("row is not JSON: %v", err)
		}
		rows = append(rows, row)
	}
	return rows
}

func readMarker[T any](t *testing.T, path string) T {
	t.Helper()
	var marker T
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &marker); err != nil {
		t.Fatal(err)
	}
	return marker
}

func send(t *testing.T, req *http.Request) (int, string) {
	t.Helper()
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestEngineForwardsRequestUnchanged(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	_, front, _ := newTestEngine(t, upstream)

	req, _ := http.NewRequest(http.MethodPost, front.URL+"/a/b?x=1&y=two", strings.NewReader("payload-body"))
	req.Host = "eval-meter:8080"
	req.Header.Set("X-Custom", "kept")
	req.Header.Set("X-Forwarded-For", "10.0.0.7")
	req.Header.Set("Forwarded", "for=10.0.0.7")
	if status, body := send(t, req); status != http.StatusOK || body != "upstream" {
		t.Fatalf("forward failed: %d %q", status, body)
	}
	bare, _ := http.NewRequest(http.MethodGet, front.URL+"/plain", nil)
	if status, _ := send(t, bare); status != http.StatusOK {
		t.Fatalf("bare forward failed: %d", status)
	}

	got := seen()
	if len(got) != 2 {
		t.Fatalf("upstream saw %d requests, want 2", len(got))
	}
	first := got[0]
	if first.host != "eval-meter:8080" || first.method != http.MethodPost || first.uri != "/a/b?x=1&y=two" || first.body != "payload-body" {
		t.Fatalf("request changed in transit: %+v", first)
	}
	if first.header.Get("X-Custom") != "kept" || first.header.Values("X-Forwarded-For")[0] != "10.0.0.7" ||
		len(first.header.Values("X-Forwarded-For")) != 1 || first.header.Get("Forwarded") != "for=10.0.0.7" {
		t.Fatalf("headers changed in transit: %v", first.header)
	}
	if _, added := got[1].header["X-Forwarded-For"]; added {
		t.Fatal("meter added a forwarding header the engine did not send")
	}
	if got[1].host != strings.TrimPrefix(front.URL, "http://") {
		t.Fatalf("Host rewritten to %q", got[1].host)
	}
}

func TestEngineLogsCountsWithoutSensitiveFields(t *testing.T) {
	upstream, _ := recordingUpstream(t)
	m, front, path := newTestEngine(t, upstream)
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodPost, front.URL+"/"+sentinel+"?q="+sentinel, strings.NewReader(sentinel))
		req.Header.Set("Authorization", "Bearer "+sentinel)
		req.Header.Set("Cookie", "session="+sentinel)
		send(t, req)
	}
	m.seal()
	raw, err := os.ReadFile(filepath.Join(path, engineLogName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(sentinel)) {
		t.Fatal("engine log contains path, query, header or body content")
	}
	rows := readRows[engineRow](t, filepath.Join(path, engineLogName))
	if len(rows) != 3 {
		t.Fatalf("rows=%d, want 3", len(rows))
	}
	for i, row := range rows {
		if row.Seq != uint64(i+1) || row.Kind != "request" || row.Method != http.MethodPost || row.Status != http.StatusOK ||
			row.Outcome != "completed" || row.ResponseBytes != int64(len("upstream")) {
			t.Fatalf("row %d: %+v", i, row)
		}
	}
	drained := readMarker[drainedMarker](t, filepath.Join(path, drainedName))
	if drained.Forwarded != 3 || drained.InflightAtDrain != 0 || !drained.LogComplete || drained.RejectedAfterSeal != 0 {
		t.Fatalf("drained marker: %+v", drained)
	}
}

func TestEngineSealRejectsWithoutForwarding(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	m, front, path := newTestEngine(t, upstream)
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/before", nil)
	send(t, req)
	m.seal()
	m.seal() // idempotent: a second signal must not rewrite markers
	for i := 0; i < 2; i++ {
		late, _ := http.NewRequest(http.MethodGet, front.URL+"/after", nil)
		if status, _ := send(t, late); status != http.StatusServiceUnavailable {
			t.Fatalf("request after seal got %d", status)
		}
	}
	if n := len(seen()); n != 1 {
		t.Fatalf("upstream saw %d requests after seal, want only the 1 before", n)
	}
	rows := readRows[engineRow](t, filepath.Join(path, engineLogName))
	if len(rows) != 3 || rows[1].Outcome != "rejected_after_seal" || rows[2].Kind != "rejected" {
		t.Fatalf("rows: %+v", rows)
	}
	sealed := readMarker[sealedMarker](t, filepath.Join(path, sealedName))
	if sealed.ForwardedAtSeal != 1 || sealed.InflightAtSeal != 0 || !sealed.LogComplete {
		t.Fatalf("sealed marker: %+v", sealed)
	}
	m.mu.Lock()
	failed := m.markerFailed
	m.mu.Unlock()
	if failed {
		t.Fatal("repeated seal tried to rewrite its markers")
	}
}

func TestEngineDrainWaitsForInflightRequest(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseUpstream := func() { releaseOnce.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
		_, _ = io.WriteString(w, "slow")
	}))
	defer upstream.Close()
	// Runs before upstream.Close even when an assertion fails, so a blocked
	// handler cannot hang the test binary.
	defer releaseUpstream()
	m, front, path := newTestEngine(t, upstream)

	done := make(chan int, 1)
	go func() {
		resp, err := http.Get(front.URL + "/slow")
		if err != nil {
			done <- 0
			return
		}
		_ = resp.Body.Close()
		done <- resp.StatusCode
	}()
	<-arrived
	m.seal()
	select {
	case <-m.drained:
		t.Fatal("drained while a request was still in flight")
	default:
	}
	if _, err := os.Stat(filepath.Join(path, drainedName)); !os.IsNotExist(err) {
		t.Fatal("drained marker written before in-flight request finished")
	}
	sealed := readMarker[sealedMarker](t, filepath.Join(path, sealedName))
	if sealed.InflightAtSeal != 1 {
		t.Fatalf("sealed marker: %+v", sealed)
	}
	releaseUpstream()
	if status := <-done; status != http.StatusOK {
		t.Fatalf("in-flight request was cut off: %d", status)
	}
	select {
	case <-m.drained:
	case <-time.After(5 * time.Second):
		t.Fatal("never drained")
	}
	drained := readMarker[drainedMarker](t, filepath.Join(path, drainedName))
	if drained.InflightAtDrain != 0 || drained.Forwarded != 1 || drained.SealedAt > drained.DrainedAt {
		t.Fatalf("drained marker: %+v", drained)
	}
}

func TestEngineSealClosesUpgradedStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = rw.Flush()
		_, _ = io.Copy(conn, rw)
	}))
	defer upstream.Close()
	m, front, path := newTestEngine(t, upstream)

	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /socket HTTP/1.1\r\nHost: eval-meter\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade failed: %v %v", resp, err)
	}
	m.seal()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("stream stayed open after seal")
	}
	select {
	case <-m.drained:
	case <-time.After(5 * time.Second):
		t.Fatal("an open stream blocked the drain")
	}
	drained := readMarker[drainedMarker](t, filepath.Join(path, drainedName))
	if drained.StreamsTotal != 1 || drained.ForcedClosed != 1 || drained.InflightAtDrain != 0 || drained.Forwarded != 1 {
		t.Fatalf("drained marker: %+v", drained)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows := readRows[engineRow](t, filepath.Join(path, engineLogName))
		if len(rows) == 1 {
			if rows[0].Kind != "stream" || rows[0].Outcome != "forced_closed" || rows[0].Status != http.StatusSwitchingProtocols {
				t.Fatalf("stream row: %+v", rows[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stream row never written")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEngineLogFailureStopsForwarding(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	m, front, _ := newTestEngine(t, upstream)
	_ = m.log.file.Close()
	first, _ := http.NewRequest(http.MethodGet, front.URL+"/one", nil)
	send(t, first)
	second, _ := http.NewRequest(http.MethodGet, front.URL+"/two", nil)
	if status, _ := send(t, second); status != http.StatusServiceUnavailable {
		t.Fatalf("request after a lost log row was forwarded: %d", status)
	}
	if n := len(seen()); n != 1 {
		t.Fatalf("upstream saw %d requests, want 1", n)
	}
}

func TestEngineUpstreamFailureOutcome(t *testing.T) {
	upstream, _ := recordingUpstream(t)
	m, front, path := newTestEngine(t, upstream)
	upstream.Close()
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/gone", nil)
	if status, _ := send(t, req); status != http.StatusBadGateway {
		t.Fatalf("status=%d", status)
	}
	m.seal()
	rows := readRows[engineRow](t, filepath.Join(path, engineLogName))
	if len(rows) != 1 || rows[0].Outcome != "upstream_error" {
		t.Fatalf("rows: %+v", rows)
	}
}

func TestObserverAllowsOnlyRegisteredGet(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer upstream.Close()
	path, dir := privateTestDir(t)
	o, code := newObserver(dir, targetURL(t, upstream), "/api/registered", time.Now)
	if code != "" {
		t.Fatal(code)
	}
	front := httptest.NewServer(o)
	defer front.Close()

	cases := []struct {
		name   string
		build  func() *http.Request
		status int
	}{
		{"registered", func() *http.Request {
			r, _ := http.NewRequest(http.MethodGet, front.URL+"/api/registered", nil)
			return r
		}, http.StatusOK},
		{"post", func() *http.Request {
			r, _ := http.NewRequest(http.MethodPost, front.URL+"/api/registered", nil)
			return r
		}, http.StatusForbidden},
		{"other path", func() *http.Request { r, _ := http.NewRequest(http.MethodGet, front.URL+"/api/other", nil); return r }, http.StatusForbidden},
		{"query", func() *http.Request {
			r, _ := http.NewRequest(http.MethodGet, front.URL+"/api/registered?x=1", nil)
			return r
		}, http.StatusForbidden},
		{"encoded path", func() *http.Request {
			r, _ := http.NewRequest(http.MethodGet, front.URL+"/api/regist%65red", nil)
			return r
		}, http.StatusForbidden},
		{"body", func() *http.Request {
			r, _ := http.NewRequest(http.MethodGet, front.URL+"/api/registered", strings.NewReader("x"))
			return r
		}, http.StatusForbidden},
		{"upgrade", func() *http.Request {
			r, _ := http.NewRequest(http.MethodGet, front.URL+"/api/registered", nil)
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("Connection", "Upgrade")
			return r
		}, http.StatusForbidden},
	}
	for _, tc := range cases {
		if status, _ := send(t, tc.build()); status != tc.status {
			t.Fatalf("%s: status %d, want %d", tc.name, status, tc.status)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("target saw %d observer requests, want only the registered one", hits.Load())
	}
	if err := o.close(); err != nil {
		t.Fatal(err)
	}
	rows := readRows[observerRow](t, filepath.Join(path, observerLogName))
	if len(rows) != len(cases) || rows[0].Decision != "registered" || rows[0].Status != http.StatusOK {
		t.Fatalf("observer rows: %+v", rows)
	}
	for _, row := range rows[1:] {
		if row.Decision != "rejected" || row.Status != http.StatusForbidden {
			t.Fatalf("rejected row: %+v", row)
		}
	}
}

func TestPrivateOutputGuards(t *testing.T) {
	open := t.TempDir()
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, code := openPrivateDir(open); code != privateDirRequired {
		t.Fatalf("0755 directory accepted: %q", code)
	}
	link := filepath.Join(t.TempDir(), "link")
	path, dir := privateTestDir(t)
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, code := openPrivateDir(link); code != privateDirRequired {
		t.Fatalf("symlinked directory accepted: %q", code)
	}
	file, code := createPrivateFile(dir, "evidence.jsonl")
	if code != "" {
		t.Fatal(code)
	}
	info, _ := file.Stat()
	_ = file.Close()
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	if _, code := createPrivateFile(dir, "evidence.jsonl"); code != logExists {
		t.Fatalf("existing evidence reopened: %q", code)
	}
	if err := writeMarker(dir, "marker.json", map[string]int{"v": 1}); err != nil {
		t.Fatal(err)
	}
	if err := writeMarker(dir, "marker.json", map[string]int{"v": 2}); err == nil {
		t.Fatal("marker overwritten")
	}
	if got := readMarker[map[string]int](t, filepath.Join(path, "marker.json")); got["v"] != 1 {
		t.Fatalf("marker content changed: %v", got)
	}
}

func TestRunRejectsInvalidInvocation(t *testing.T) {
	path, _ := privateTestDir(t)
	for _, args := range [][]string{
		{},
		{"-role", "unknown"},
		{"-role", "engine", "-listen", "127.0.0.1:0", "-upstream", "http://target:3000", "-log-dir", path, "-allow-path", "/x"},
		{"-role", "observer", "-listen", "127.0.0.1:0", "-upstream", "http://target:3000", "-log-dir", path},
		{"-role", "observer", "-listen", "127.0.0.1:0", "-upstream", "http://target:3000", "-log-dir", path, "-allow-path", "/a?b"},
		{"-role", "engine", "-listen", "127.0.0.1:0", "-upstream", "http://target:3000/path", "-log-dir", path},
		{"-role", "engine", "-listen", "127.0.0.1:0", "-upstream", "https://target:3000", "-log-dir", path},
		{"-role", "engine", "-listen", "127.0.0.1:0", "-upstream", "http://user@target:3000", "-log-dir", path},
		{"-role", "engine", "-listen", "127.0.0.1:0", "-upstream", "http://target", "-log-dir", path},
		{"-role", "probe", "-upstream", "http://target:3000"},
		{"-role", "engine", "extra"},
	} {
		var stderr bytes.Buffer
		if code := run(args, strings.NewReader(""), io.Discard, &stderr); code != 2 || !strings.HasPrefix(stderr.String(), invalidInput) {
			t.Fatalf("%v: exit %d stderr %q", args, code, stderr.String())
		}
	}
	for _, path := range []string{"/api/../x", "/api/./x", "/a b", "//x", "x", "/a\x7f"} {
		if validAllowPath(path) {
			t.Fatalf("allow path %q accepted", path)
		}
	}
	if !validAllowPath("/api/Challenges") {
		t.Fatal("plain registered path rejected")
	}
}

func TestProbeReportsObservedPaths(t *testing.T) {
	open, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	closedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := closedListener.Addr().String()
	_ = closedListener.Close()
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/health", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	defer web.Close()

	plan := map[string]any{"timeout_ms": 2000, "checks": []map[string]string{
		{"id": "open", "kind": "tcp", "address": open.Addr().String()},
		{"id": "closed", "kind": "tcp", "address": closedAddr},
		{"id": "web", "kind": "http_get", "url": web.URL + "/health"},
		{"id": "redirect", "kind": "http_get", "url": web.URL + "/moved"},
		{"id": "name", "kind": "dns", "name": "eval-meter-missing.invalid"},
	}}
	input, _ := json.Marshal(plan)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-role", "probe"}, bytes.NewReader(input), &stdout, &stderr); code != 0 {
		t.Fatalf("probe exit %d: %s", code, stderr.String())
	}
	var report probeReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"open": {"success", "connected"}, "closed": {"fail", "refused"},
		"web": {"success", "http_418"}, "name": {"fail", "not_found"},
		// A redirect is reported, not followed: following would test another path.
		"redirect": {"success", "http_302"},
	}
	if len(report.Results) != len(want) {
		t.Fatalf("results: %+v", report.Results)
	}
	for _, result := range report.Results {
		if got := [2]string{result.Observed, result.Code}; got != want[result.ID] {
			t.Fatalf("%s: got %v want %v", result.ID, got, want[result.ID])
		}
	}
}

func TestProbeRejectsInvalidPlans(t *testing.T) {
	for name, plan := range map[string]string{
		"no timeout":   `{"checks":[{"id":"a","kind":"dns","name":"x.invalid"}]}`,
		"no checks":    `{"timeout_ms":10,"checks":[]}`,
		"duplicate id": `{"timeout_ms":10,"checks":[{"id":"a","kind":"dns","name":"x"},{"id":"a","kind":"dns","name":"y"}]}`,
		"unknown kind": `{"timeout_ms":10,"checks":[{"id":"a","kind":"udp","address":"x:1"}]}`,
		"extra field":  `{"timeout_ms":10,"expect":"fail","checks":[{"id":"a","kind":"dns","name":"x"}]}`,
		"mixed fields": `{"timeout_ms":10,"checks":[{"id":"a","kind":"tcp","address":"x:1","url":"http://x"}]}`,
		"https":        `{"timeout_ms":10,"checks":[{"id":"a","kind":"http_get","url":"https://x"}]}`,
		"bad port":     `{"timeout_ms":10,"checks":[{"id":"a","kind":"tcp","address":"x:0"}]}`,
		"trailing":     `{"timeout_ms":10,"checks":[{"id":"a","kind":"dns","name":"x"}]} {}`,
	} {
		var stderr bytes.Buffer
		if code := run([]string{"-role", "probe"}, strings.NewReader(plan), io.Discard, &stderr); code != 2 {
			t.Fatalf("%s: exit %d", name, code)
		}
	}
}

func TestEngineTerminateRecordsUndrainedStop(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseUpstream := func() { releaseOnce.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
	}))
	defer upstream.Close()
	defer releaseUpstream()
	m, front, path := newTestEngine(t, upstream)
	go func() {
		if resp, err := http.Get(front.URL + "/slow"); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-arrived
	m.terminate("signal")
	got := readMarker[terminatedMarker](t, filepath.Join(path, terminatedName))
	if got.Sealed || got.Drained || got.InflightAtTerminate != 1 || got.Forwarded != 1 || got.Reason != "signal" {
		t.Fatalf("undrained stop not visible: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(path, drainedName)); !os.IsNotExist(err) {
		t.Fatal("drained marker written for a stop without seal")
	}
}

func TestEngineSealRaceCountsEveryRequest(t *testing.T) {
	// total requests race the seal; whether any of them lose depends on scheduling, so the
	// rejection path is pinned by a separate batch that starts only after seal returns.
	const total, afterSeal = 200, 20
	var hits atomic.Int64
	quarter := make(chan struct{})
	var quarterOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == total/4 {
			quarterOnce.Do(func() { close(quarter) })
		}
	}))
	defer upstream.Close()
	m, front, path := newTestEngine(t, upstream)
	transport := &http.Transport{MaxConnsPerHost: 0, MaxIdleConnsPerHost: total}
	defer transport.CloseIdleConnections()

	start := make(chan struct{})
	statuses := make(chan int, total)
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req, _ := http.NewRequest(http.MethodGet, front.URL+"/race", nil)
			resp, err := transport.RoundTrip(req)
			if err != nil {
				statuses <- 0
				return
			}
			_ = resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	close(start)
	select {
	case <-quarter:
	case <-time.After(10 * time.Second):
		t.Fatal("requests never reached the target")
	}
	m.seal()
	for i := 0; i < afterSeal; i++ {
		req, _ := http.NewRequest(http.MethodGet, front.URL+"/after", nil)
		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatalf("request after seal failed: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("request after seal: status %d, want 503", resp.StatusCode)
		}
	}
	wg.Wait()
	close(statuses)
	var ok, rejected int
	for status := range statuses {
		switch status {
		case http.StatusOK:
			ok++
		case http.StatusServiceUnavailable:
			rejected++
		default:
			t.Fatalf("request lost or failed: status %d", status)
		}
	}
	select {
	case <-m.drained:
	case <-time.After(5 * time.Second):
		t.Fatal("never drained")
	}
	drained := readMarker[drainedMarker](t, filepath.Join(path, drainedName))
	sealed := readMarker[sealedMarker](t, filepath.Join(path, sealedName))
	// Nothing may be admitted after seal: the count at drain equals the count
	// at seal, and the requests still in flight then are the only stragglers.
	if drained.Forwarded != sealed.ForwardedAtSeal {
		t.Fatalf("admitted after seal: forwarded at seal %d, at drain %d", sealed.ForwardedAtSeal, drained.Forwarded)
	}
	rows := readRows[engineRow](t, filepath.Join(path, engineLogName))
	var forwardedRows, rejectedRows int
	for _, row := range rows {
		switch row.Kind {
		case "request":
			forwardedRows++
		case "rejected":
			rejectedRows++
		}
	}
	// Every request is either forwarded-and-counted or rejected-and-counted. The racing batch may
	// lose none or some; every request started after seal is rejected and counted.
	if ok+rejected != total || int64(ok) != hits.Load() || drained.Forwarded != uint64(ok) ||
		forwardedRows != ok || rejectedRows != rejected+afterSeal || len(rows) != total+afterSeal {
		t.Fatalf("ok=%d rejected=%d hits=%d forwarded=%d rows=%d/%d/%d", ok, rejected, hits.Load(), drained.Forwarded,
			forwardedRows, rejectedRows, len(rows))
	}
}

func TestRunFailsClosedBeforeListening(t *testing.T) {
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := free.Addr().String()
	_ = free.Close()

	open := t.TempDir()
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	used, _ := privateTestDir(t)
	if err := os.WriteFile(filepath.Join(used, engineLogName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ dir, code string }{{open, privateDirRequired}, {used, logExists}} {
		var stderr bytes.Buffer
		code := run([]string{"-role", "engine", "-listen", addr, "-upstream", "http://target.invalid:3000", "-log-dir", tc.dir},
			strings.NewReader(""), io.Discard, &stderr)
		if code != 1 || strings.TrimSpace(stderr.String()) != tc.code {
			t.Fatalf("exit %d stderr %q, want %s", code, stderr.String(), tc.code)
		}
		if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			_ = conn.Close()
			t.Fatal("front listened although its evidence log could not be opened")
		}
	}
}
