package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const sentinel = "SENTINELVALUE"

var testReceiver = receiverInfo{"http://outside.test:9001", "ws://outside.test:9001", "outside.test:9002"}

func newTestRecorder(t *testing.T) (*recorder, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reach.jsonl")
	rec, err := newRecorder(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rec.file.Close() })
	return rec, path
}

func readRows(t *testing.T, path string) []row {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := []row{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var v row
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, v)
	}
	return rows
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestFirstScreenNeedsJavaScript(t *testing.T) {
	rec, _ := newTestRecorder(t)
	h := targetHandler(rec, testReceiver)
	index := get(t, h, "/")
	if index.Code != http.StatusOK || !strings.Contains(index.Body.String(), `<script src="/assets/app.js">`) || strings.Contains(index.Body.String(), "fixture-ready") || strings.Contains(index.Body.String(), "<a") {
		t.Fatal(index.Code, index.Body.String())
	}
	if app := get(t, h, "/assets/app.js"); !strings.Contains(app.Body.String(), "fixture-ready") || !strings.Contains(app.Body.String(), `fetch("/api/items")`) || app.Header().Get("Content-Type") != jsType {
		t.Fatal(app.Body.String(), app.Header())
	}
	var items struct{ Items []string }
	if err := json.Unmarshal(get(t, h, "/api/items").Body.Bytes(), &items); err != nil || len(items.Items) == 0 {
		t.Fatal(err, items)
	}
	for _, path := range []string{"/assets/app.css", "/assets/logo.svg"} {
		if got := get(t, h, path); got.Code != http.StatusOK || got.Body.Len() == 0 {
			t.Fatal(path, got.Code)
		}
	}
}

func TestScenarioPagesCarryAttempts(t *testing.T) {
	rec, _ := newTestRecorder(t)
	h := targetHandler(rec, testReceiver)
	want := map[string][]string{
		"B04": {"http://outside.test:9001/hit/B04-img", "http://outside.test:9001/hit/B04-script", "http://outside.test:9001/hit/B04-fetch"},
		"B05": {"/out/B05-frame", "/out/B05-fetch"},
		"B06": {`register("/sw.js")`, `"controllerchange",()=>fetch("/sw-probe")`},
		"B07": {"ws://outside.test:9001/hit/B07"},
		"B08": {`fetch("/api/items")`},
		"B09": {"stun:outside.test:9002"},
		"B12": {`rel="preconnect" href="http://outside.test:9001"`, `rel="dns-prefetch" href="http://outside.test:9001"`, `rel="prefetch" href="http://outside.test:9001/hit/B12-prefetch"`},
		"B13": {"http://outside.test:9001/hit/B13-blob", "http://outside.test:9001/hit/B13-data"},
		"B14": {`/write/B14-form`, `/write/B14-fetch`, `sendBeacon("/write/B14-beacon"`},
	}
	if len(want) != len(scenarios) {
		t.Fatal(len(scenarios))
	}
	for id, parts := range want {
		page := get(t, h, "/bypass/"+id)
		if page.Code != http.StatusOK || strings.Contains(page.Body.String(), "{{") {
			t.Fatal(id, page.Code, page.Body.String())
		}
		for _, part := range parts {
			if !strings.Contains(page.Body.String(), part) {
				t.Fatal(id, part, page.Body.String())
			}
		}
	}
	if got := get(t, h, "/bypass/B99"); got.Code != http.StatusNotFound {
		t.Fatal(got.Code)
	}
}

func TestEveryOutsideAttemptIsAKnownHit(t *testing.T) {
	for id, page := range scenarios {
		rest := page
		for {
			_, after, ok := strings.Cut(rest, "/hit/")
			if !ok {
				break
			}
			name := after[:strings.IndexAny(after, `"'`)]
			if !receiverHits[name] {
				t.Fatal(id, name)
			}
			rest = after
		}
	}
	for name := range redirectHits {
		if !receiverHits[name] {
			t.Fatal(name)
		}
	}
}

func TestAltSvcOnlyOnQUICScenario(t *testing.T) {
	rec, _ := newTestRecorder(t)
	h := targetHandler(rec, testReceiver)
	if got := get(t, h, "/bypass/B08").Header().Get("Alt-Svc"); got != `h3="outside.test:9002"; ma=60` {
		t.Fatal(got)
	}
	if got := get(t, h, "/bypass/B04").Header().Get("Alt-Svc"); got != "" {
		t.Fatal(got)
	}
}

func TestRedirectLeavesScope(t *testing.T) {
	rec, _ := newTestRecorder(t)
	h := targetHandler(rec, testReceiver)
	got := get(t, h, "/out/B05-frame")
	if got.Code != http.StatusFound || got.Header().Get("Location") != "http://outside.test:9001/hit/B05-frame" {
		t.Fatal(got.Code, got.Header())
	}
	if got := get(t, h, "/out/other"); got.Code != http.StatusNotFound {
		t.Fatal(got.Code)
	}
}

func TestServiceWorkerTriesOutsideOnInstallAndFetch(t *testing.T) {
	rec, _ := newTestRecorder(t)
	body := get(t, targetHandler(rec, testReceiver), "/sw.js").Body.String()
	for _, part := range []string{"http://outside.test:9001/hit/B06-install", "http://outside.test:9001/hit/B06-fetch", `"/sw-probe"`} {
		if !strings.Contains(body, part) {
			t.Fatal(part, body)
		}
	}
}

func TestTargetWritesAreRecorded(t *testing.T) {
	rec, path := newTestRecorder(t)
	h := targetHandler(rec, testReceiver)
	for _, id := range []string{"B14-fetch", sentinel} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/write/"+id+"?q="+sentinel, strings.NewReader(sentinel)))
		if w.Code != http.StatusNoContent {
			t.Fatal(w.Code)
		}
	}
	if got := get(t, h, "/write/B14-fetch"); got.Code != http.StatusMethodNotAllowed {
		t.Fatal(got.Code)
	}
	want := []row{{1, surfaceTargetWrite, "B14-fetch", "POST", false}, {2, surfaceTargetWrite, unknownScenario, "POST", false}}
	if got := readRows(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestReceiverRecordsEveryRequestWithoutValues(t *testing.T) {
	rec, path := newTestRecorder(t)
	h := receiverHandler(rec)
	req := httptest.NewRequest(http.MethodGet, "/hit/B04-img?token="+sentinel, nil)
	req.Header.Set("Cookie", "a="+sentinel)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal(w.Code, w.Header())
	}
	get(t, h, "/hit/"+sentinel)
	get(t, h, "/"+sentinel)
	upgrade := httptest.NewRequest(http.MethodGet, "/hit/B07", nil)
	upgrade.Header.Set("Upgrade", "websocket")
	h.ServeHTTP(httptest.NewRecorder(), upgrade)
	want := []row{
		{1, surfaceReceiver, "B04-img", "GET", false},
		{2, surfaceReceiver, unknownScenario, "GET", false},
		{3, surfaceReceiver, unknownScenario, "GET", false},
		{4, surfaceReceiver, "B07", "GET", true},
	}
	if got := readRows(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), sentinel) {
		t.Fatal(err, string(data))
	}
}

func TestUDPRecordsArrivalOnly(t *testing.T) {
	rec, path := newTestRecorder(t)
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serveUDP(ctx, conn, rec) }()
	client, err := net.Dial("udp", conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	packet := append(append([]byte{0, 1, 0, 0}, stunCookie...), sentinel...)
	if _, err := client.Write(packet); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(readRows(t, path)) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	conn.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got, want := readRows(t, path), []row{{Seq: 1, Surface: surfaceReceiverUDP, Scenario: scenarioSTUN}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	if data, _ := os.ReadFile(path); strings.Contains(string(data), sentinel) {
		t.Fatal(string(data))
	}
}

func TestClassifyUDP(t *testing.T) {
	stun := append([]byte{0, 1, 0, 0}, stunCookie...)
	for name, tc := range map[string]struct {
		packet []byte
		want   string
	}{
		"stun":           {stun, scenarioSTUN},
		"stun over quic": {append([]byte{0xC0, 1, 0, 0}, stunCookie...), scenarioSTUN},
		"quic long":      {[]byte{0xC0, 0, 0, 0, 1}, scenarioQUIC},
		"quic bit only":  {[]byte{0x80}, scenarioQUIC},
		"other":          {[]byte{0x40, 0, 0, 0, 1}, unknownScenario},
		"short cookie":   {[]byte{0, 1, 0, 0, 0x21, 0x12, 0xA4}, unknownScenario},
		"empty":          {nil, unknownScenario},
	} {
		if got := classifyUDP(tc.packet); got != tc.want {
			t.Errorf("%s got=%s want=%s", name, got, tc.want)
		}
	}
}

func TestRecorderCreatesPrivateNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reach.jsonl")
	rec, err := newRecorder(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rec.file.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal(err, info.Mode())
	}
	if _, err := newRecorder(path); !os.IsExist(err) {
		t.Fatal(err)
	}
}

func TestRecordFailureFailsRunAndRequest(t *testing.T) {
	rec, _ := newTestRecorder(t)
	rec.file.Close()
	w := httptest.NewRecorder()
	receiverHandler(rec).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/hit/B07", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatal(w.Code)
	}
	select {
	case <-rec.failed:
	default:
		t.Fatal("failure was not signalled")
	}
	second := httptest.NewRecorder()
	targetHandler(rec, testReceiver).ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/write/B14-fetch", nil))
	if second.Code != http.StatusInternalServerError {
		t.Fatal(second.Code)
	}
}

func TestParseReceiver(t *testing.T) {
	got, err := parseReceiver("https://outside.test:9001/", "0.0.0.0:9002")
	if want := (receiverInfo{"https://outside.test:9001", "wss://outside.test:9001", "outside.test:9002"}); err != nil || got != want {
		t.Fatalf("%+v %v", got, err)
	}
	bad := []string{"", "ftp://a.test", "http://", "http://a.test/x", "http://a.test?q=1", "http://a.test#f", "http://u:p@a.test", `http://a"b.test`}
	for _, raw := range bad {
		if _, err := parseReceiver(raw, "0.0.0.0:9002"); err == nil {
			t.Fatal(raw)
		}
	}
	if _, err := parseReceiver("http://a.test", "no-port"); err == nil {
		t.Fatal("udp address")
	}
	if plain, err := parseReceiver("http://a.test", "0.0.0.0:1"); err != nil || plain.websocket != "ws://a.test" {
		t.Fatalf("%+v %v", plain, err)
	}
}
