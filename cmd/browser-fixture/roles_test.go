package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func withRoles(metadata, cdn string) receiverInfo {
	r := testReceiver
	if metadata != "" {
		r.metadata, r.metadataWS = metadata, wsOrigin(metadata)
	}
	r.cdn = cdn
	return r
}

const (
	testMetadata = "http://md.test:9101"
	testCDN      = "http://cdn.test:9201"
)

// 외부 CDN 역할이 있어야 뜻이 있는 페이지는 CDN 수신기가 없으면 404다(NOT_RUN으로 남게).
func TestCDNPagesNeedCDNReceiver(t *testing.T) {
	rec, _ := newTestRecorder(t)
	without := targetHandler(rec, testReceiver)
	for _, path := range []string{"/bypass/B15", "/crawl/P08"} {
		if got := get(t, without, path); got.Code != http.StatusNotFound {
			t.Fatal(path, got.Code)
		}
	}
	with := targetHandler(rec, withRoles("", testCDN))
	want := map[string][]string{
		"/bypass/B15": {testCDN + "/hit/B15-link", testCDN + "/hit/B15-form", testCDN + "/hit/B15-nav"},
		"/crawl/P08":  {testCDN + "/cdn/P08.js", testCDN + "/cdn/P08.css", testCDN + "/cdn/P08.svg", `id="p08-ready"`},
	}
	for path, parts := range want {
		page := get(t, with, path)
		if page.Code != http.StatusOK || strings.Contains(page.Body.String(), "{{") {
			t.Fatal(path, page.Code, page.Body.String())
		}
		for _, part := range parts {
			if !strings.Contains(page.Body.String(), part) {
				t.Fatal(path, part)
			}
		}
	}
}

// 메타데이터 역할 시도는 메타데이터 수신기가 있을 때만 같은 시험 ID 페이지에 붙는다.
func TestMetadataPartsOnlyWhenConfigured(t *testing.T) {
	rec, _ := newTestRecorder(t)
	without := targetHandler(rec, testReceiver)
	with := targetHandler(rec, withRoles(testMetadata, ""))
	for id := range metadataParts {
		plain := get(t, without, "/bypass/"+id).Body.String()
		if strings.Contains(plain, "-md") || strings.Contains(plain, "{{") {
			t.Fatal(id, "메타데이터 수신기 없이 메타데이터 시도가 붙음", plain)
		}
		full := get(t, with, "/bypass/"+id).Body.String()
		if strings.Contains(full, "{{") || !strings.HasPrefix(full, plain) {
			t.Fatal(id, "내부망 시도 뒤에 붙어야 함", full)
		}
	}
	body := get(t, with, "/bypass/B04").Body.String()
	for _, part := range []string{testMetadata + "/hit/B04-md-img", testMetadata + "/hit/B04-md-script", testMetadata + "/hit/B04-md-fetch"} {
		if !strings.Contains(body, part) {
			t.Fatal(part, body)
		}
	}
	if b07 := get(t, with, "/bypass/B07").Body.String(); !strings.Contains(b07, "ws://md.test:9101/hit/B07-md") {
		t.Fatal(b07)
	}
}

func TestMetadataRedirectNeedsMetadataReceiver(t *testing.T) {
	rec, _ := newTestRecorder(t)
	if got := get(t, targetHandler(rec, testReceiver), "/out/B05-md-frame"); got.Code != http.StatusNotFound {
		t.Fatal(got.Code)
	}
	got := get(t, targetHandler(rec, withRoles(testMetadata, "")), "/out/B05-md-fetch")
	if got.Code != http.StatusFound || got.Header().Get("Location") != testMetadata+"/hit/B05-md-fetch" {
		t.Fatal(got.Code, got.Header())
	}
}

// 역할 페이지가 부르는 /hit/ 이름은 전부 그 역할 수신기가 이름으로 세는 것이어야 한다.
func TestEveryRoleAttemptIsAKnownHit(t *testing.T) {
	check := func(pages map[string]string, hits map[string]bool) {
		t.Helper()
		for id, page := range pages {
			rest := page
			for {
				_, after, ok := strings.Cut(rest, "/hit/")
				if !ok {
					break
				}
				name := after[:strings.IndexAny(after, `"'`)]
				if !hits[name] {
					t.Fatal(id, name)
				}
				rest = after
			}
		}
	}
	check(metadataParts, metadataHits)
	check(cdnPages, cdnHits)
	for name := range metadataRedirectHits {
		if !metadataHits[name] {
			t.Fatal(name)
		}
	}
	for _, file := range []string{"P08.js", "P08.css", "P08.svg"} {
		if _, ok := cdnAssets[file]; !ok || !strings.Contains(p08Page, "/cdn/"+file) {
			t.Fatal(file)
		}
	}
}

func TestCDNHandlerServesAssetsAndRecordsWithoutValues(t *testing.T) {
	rec, path := newTestRecorder(t)
	h := cdnHandler(rec)
	js := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/cdn/P08.js?token="+sentinel, nil)
	req.Header.Set("Cookie", "a="+sentinel)
	h.ServeHTTP(js, req)
	if js.Code != http.StatusOK || js.Header().Get("Content-Type") != jsType || !strings.Contains(js.Body.String(), "p08-ready") {
		t.Fatal(js.Code, js.Header(), js.Body.String())
	}
	get(t, h, "/hit/B17-node")
	get(t, h, "/hit/"+sentinel)
	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/cdn/P08.js", strings.NewReader(sentinel)))
	if post.Code != http.StatusNoContent {
		t.Fatal(post.Code)
	}
	want := []row{
		{Seq: 1, Surface: surfaceReceiverCDN, Scenario: "P08-js", Method: "GET"},
		{Seq: 2, Surface: surfaceReceiverCDN, Scenario: "B17-node", Method: "GET"},
		{Seq: 3, Surface: surfaceReceiverCDN, Scenario: unknownScenario, Method: "GET"},
		{Seq: 4, Surface: surfaceReceiverCDN, Scenario: unknownScenario, Method: "POST"},
	}
	if got := readRows(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), sentinel) {
		t.Fatal(err, string(data))
	}
}

func TestMetadataReceiverRecordsItsRole(t *testing.T) {
	rec, path := newTestRecorder(t)
	h := roleReceiverHandler(rec, surfaceReceiverMetadata, metadataHits)
	get(t, h, "/hit/B04-md-img")
	get(t, h, "/hit/B04-img")
	want := []row{
		{Seq: 1, Surface: surfaceReceiverMetadata, Scenario: "B04-md-img", Method: "GET"},
		{Seq: 2, Surface: surfaceReceiverMetadata, Scenario: unknownScenario, Method: "GET"},
	}
	if got := readRows(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

// 선택 역할 수신기도 HTTP 요청 전에 끊긴 연결을 그 역할의 연결로 남긴다.
func TestServeRecordsRoleConnections(t *testing.T) {
	rec, path := newTestRecorder(t)
	md, cdn := listenLocal(t), listenLocal(t)
	cfg := config{receiver: withRoles(testMetadata, testCDN), extra: []extraReceiver{
		{listener: md, handler: roleReceiverHandler(rec, surfaceReceiverMetadata, metadataHits), connSurface: surfaceConnMetadata},
		{listener: cdn, handler: cdnHandler(rec), connSurface: surfaceConnCDN},
	}}
	cancel, done := startServe(t, cfg, rec, listenLocal(t), listenLocal(t))
	for _, addr := range []string{md.Addr().String(), cdn.Addr().String()} {
		dialUntilReady(t, addr).Close()
	}
	rows := waitForRows(t, path, func(rows []row) bool { return len(rows) >= 2 })
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Surface] = true
	}
	if !seen[surfaceConnMetadata] || !seen[surfaceConnCDN] {
		t.Fatalf("%+v", rows)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsUnpairedRoleFlags(t *testing.T) {
	base := []string{"-listen", "127.0.0.1:0", "-receiver-listen", "127.0.0.1:0", "-receiver-udp", "127.0.0.1:0",
		"-receiver-origin", "http://outside.test:9001", "-log", filepath.Join(t.TempDir(), "reach.jsonl")}
	for _, extra := range [][]string{{"-metadata-listen", "127.0.0.1:0"}, {"-cdn-origin", testCDN}} {
		err := run(t.Context(), append(append([]string{}, base...), extra...))
		if err == nil || !strings.Contains(err.Error(), "go together") {
			t.Fatal(extra, err)
		}
	}
}

// 역할 origin의 scheme이 TLS 설정과 다르면 혼합 콘텐츠로 시도가 생기지 않으므로 거부하고, 연 주소를 모두 닫는다.
func TestRunRejectsRoleOriginSchemeMismatch(t *testing.T) {
	args := []string{"-listen", "127.0.0.1:0", "-receiver-listen", "127.0.0.1:0", "-receiver-udp", "127.0.0.1:0",
		"-receiver-origin", "http://outside.test:9001", "-log", filepath.Join(t.TempDir(), "reach.jsonl"),
		"-cdn-listen", "127.0.0.1:0", "-cdn-origin", "https://cdn.test:9201"}
	if err := run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Fatal(err)
	}
}

func TestB18WebMCPPageTriesBothShapes(t *testing.T) {
	rec, _ := newTestRecorder(t)
	page := get(t, targetHandler(rec, testReceiver), "/crawl/B18-webmcp")
	if page.Code != http.StatusOK {
		t.Fatal(page.Code)
	}
	for _, part := range []string{"navigator.modelContext", "provideContext", "registerTool", `tool("browser_navigate")`, `id="webmcp-attempted"`} {
		if !strings.Contains(page.Body.String(), part) {
			t.Fatal(part)
		}
	}
}
