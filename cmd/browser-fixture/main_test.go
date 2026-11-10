package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
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

var testReceiver = receiverInfo{"http://outside.test:9001", "ws://outside.test:9001", "outside.test:9002", "9002"}

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
		"B06": {`register("/sw.js")`, `"controllerchange",()=>fetch("/sw-probe")`, `if(s.controller){fetch("/sw-probe")}`},
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
	if got := get(t, h, "/bypass/B08").Header().Get("Alt-Svc"); got != `h3=":9002"; ma=60` {
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
	if got := get(t, h, "/write/B14-fetch"); got.Code != http.StatusNotFound {
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

func testPacketConn(t *testing.T) net.PacketConn {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestServeStopsCleanlyAndClosesLog(t *testing.T) {
	rec, path := newTestRecorder(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, config{receiver: testReceiver}, rec, listenLocal(t), listenLocal(t), testPacketConn(t))
	}()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := rec.file.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	if rows := readRows(t, path); len(rows) != 0 {
		t.Fatal(rows)
	}
}

func TestServeReportsRecordFailureEvenAfterCancel(t *testing.T) {
	rec, _ := newTestRecorder(t)
	rec.file.Close()
	if err := rec.record(row{Surface: surfaceReceiver, Scenario: "B07"}); err == nil {
		t.Fatal("record must fail on a closed file")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := serve(ctx, config{receiver: testReceiver}, rec, listenLocal(t), listenLocal(t), testPacketConn(t))
	if !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
}

func TestServeEndsOnRecordFailure(t *testing.T) {
	rec, _ := newTestRecorder(t)
	done := make(chan error, 1)
	go func() {
		done <- serve(t.Context(), config{receiver: testReceiver}, rec, listenLocal(t), listenLocal(t), testPacketConn(t))
	}()
	rec.file.Close()
	if err := rec.record(row{Surface: surfaceReceiver, Scenario: "B07"}); err == nil {
		t.Fatal("record must fail on a closed file")
	}
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve kept running after a lost record")
	}
}

func TestRunReportsListenFailure(t *testing.T) {
	base := []string{"-receiver-udp", "127.0.0.1:0", "-receiver-origin", "http://outside.test:9001", "-log", filepath.Join(t.TempDir(), "reach.jsonl")}
	for _, args := range [][]string{
		append([]string{"-listen", "bad address", "-receiver-listen", "127.0.0.1:0"}, base...),
		append([]string{"-listen", "127.0.0.1:0", "-receiver-listen", "bad address"}, base...),
	} {
		if err := run(t.Context(), args); err == nil {
			t.Fatal(args)
		}
	}
}

func TestServeClosesListenersOnEveryReturn(t *testing.T) {
	certFile, keyFile := writeTestCertificate(t)
	cases := map[string]struct {
		cfg    config
		cancel bool
		fails  bool
	}{
		"missing certificate": {config{certFile: certFile + ".missing", keyFile: keyFile, receiver: testReceiver}, false, true},
		"already cancelled":   {config{receiver: testReceiver}, true, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec, _ := newTestRecorder(t)
			ctx, cancel := context.WithCancel(t.Context())
			if tc.cancel {
				cancel()
			}
			defer cancel()
			target, outside := listenLocal(t), listenLocal(t)
			err := serve(ctx, tc.cfg, rec, target, outside, testPacketConn(t))
			if (err != nil) != tc.fails {
				t.Fatal(err)
			}
			for _, l := range []net.Listener{target, outside} {
				if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
					t.Fatal(l.Addr(), err)
				}
			}
		})
	}
}

// 리스너를 연 채로 넘겨서, 주소를 고른 뒤 다시 여는 사이에 다른 시험이 포트를 가져가지 못하게 한다.
func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func writeTestCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func TestServeUsesTLSForTargetAndReceiver(t *testing.T) {
	rec, path := newTestRecorder(t)
	certFile, keyFile := writeTestCertificate(t)
	targetListener, outsideListener := listenLocal(t), listenLocal(t)
	target, outside := targetListener.Addr().String(), outsideListener.Addr().String()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	cfg := config{certFile: certFile, keyFile: keyFile, receiver: testReceiver}
	go func() { done <- serve(ctx, cfg, rec, targetListener, outsideListener, testPacketConn(t)) }()
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	reach := func(url string) *http.Response {
		deadline := time.Now().Add(5 * time.Second)
		for {
			response, err := client.Get(url)
			if err == nil {
				response.Body.Close()
				return response
			}
			if time.Now().After(deadline) {
				t.Fatal(err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if got := reach("https://" + target + "/"); got.StatusCode != http.StatusOK || got.TLS == nil {
		t.Fatal(got.StatusCode, got.TLS)
	}
	if got := reach("https://" + outside + "/hit/B07"); got.StatusCode != http.StatusNoContent || got.TLS == nil {
		t.Fatal(got.StatusCode, got.TLS)
	}
	client.CloseIdleConnections()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	hits := 0
	for _, v := range readRows(t, path) {
		if v.Surface == surfaceReceiver && v.Scenario == "B07" {
			hits++
		}
	}
	if hits != 1 {
		t.Fatal(hits)
	}
}

func startServe(t *testing.T, cfg config, rec *recorder, target, outside net.Listener) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	conn := testPacketConn(t)
	go func() { done <- serve(ctx, cfg, rec, target, outside, conn) }()
	return cancel, done
}

func waitForRows(t *testing.T, path string, want func([]row) bool) []row {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows := readRows(t, path)
		if want(rows) {
			return rows
		}
		if time.Now().After(deadline) {
			t.Fatalf("%+v", rows)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func dialUntilReady(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func onlyConnections(rows []row) bool {
	if len(rows) == 0 {
		return false
	}
	for _, v := range rows {
		if v.Surface != surfaceReceiverTCP || v.Scenario != unknownScenario || v.Method != "" {
			return false
		}
	}
	return true
}

func TestReceiverCountsConnectionWithoutRequest(t *testing.T) {
	rec, path := newTestRecorder(t)
	outside := listenLocal(t)
	cancel, done := startServe(t, config{receiver: testReceiver}, rec, listenLocal(t), outside)
	dialUntilReady(t, outside.Addr().String()).Close()
	waitForRows(t, path, onlyConnections)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if rows := readRows(t, path); len(rows) != 1 {
		t.Fatalf("one connection must be one row: %+v", rows)
	}
}

func TestReceiverCountsFailedTLSVerification(t *testing.T) {
	rec, path := newTestRecorder(t)
	certFile, keyFile := writeTestCertificate(t)
	outside := listenLocal(t)
	cancel, done := startServe(t, config{certFile: certFile, keyFile: keyFile, receiver: testReceiver}, rec, listenLocal(t), outside)
	raw := dialUntilReady(t, outside.Addr().String())
	defer raw.Close()
	// 신뢰하지 않는 인증서라 핸드셰이크가 끝나지 않고, HTTP 요청은 보내지 않는다.
	var verify x509.UnknownAuthorityError
	if err := tls.Client(raw, &tls.Config{ServerName: "localhost"}).Handshake(); !errors.As(err, &verify) {
		t.Fatal(err)
	}
	waitForRows(t, path, onlyConnections)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestConnectionRecordFailureFailsRun(t *testing.T) {
	rec, _ := newTestRecorder(t)
	outside := listenLocal(t)
	_, done := startServe(t, config{receiver: testReceiver}, rec, listenLocal(t), outside)
	rec.mu.Lock()
	rec.file.Close()
	rec.mu.Unlock()
	raw := dialUntilReady(t, outside.Addr().String())
	defer raw.Close()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve kept running after a lost connection record")
	}
}

func TestNonReadRequestsAreRecorded(t *testing.T) {
	rec, path := newTestRecorder(t)
	h := targetHandler(rec, testReceiver)
	for _, tc := range []struct{ method, path string }{{"POST", "/api/items"}, {"PUT", "/x"}, {"DELETE", "/assets/app.js"}, {"OPTIONS", "/"}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path+"?q="+sentinel, strings.NewReader(sentinel)))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatal(tc, w.Code)
		}
	}
	if got := get(t, h, "/nope"); got.Code != http.StatusNotFound {
		t.Fatal(got.Code)
	}
	head := httptest.NewRecorder()
	h.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/nope", nil))
	want := []row{{1, surfaceTargetWrite, unknownScenario, "POST", false}, {2, surfaceTargetWrite, unknownScenario, "PUT", false}, {3, surfaceTargetWrite, unknownScenario, "DELETE", false}, {4, surfaceTargetWrite, unknownScenario, "OPTIONS", false}}
	if got := readRows(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	if data, _ := os.ReadFile(path); strings.Contains(string(data), sentinel) {
		t.Fatal(string(data))
	}
}

func TestNonReadRecordFailureFailsRequest(t *testing.T) {
	rec, _ := newTestRecorder(t)
	rec.file.Close()
	w := httptest.NewRecorder()
	targetHandler(rec, testReceiver).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/items", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatal(w.Code)
	}
}

func TestRunAdvertisesActualUDPPort(t *testing.T) {
	bound := make(chan string, 1)
	announce = func(target, _ net.Addr) { bound <- target.String() }
	t.Cleanup(func() { announce = func(net.Addr, net.Addr) {} })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	args := []string{"-listen", "127.0.0.1:0", "-receiver-listen", "127.0.0.1:0", "-receiver-udp", "127.0.0.1:0", "-receiver-origin", "http://outside.test:9001", "-log", filepath.Join(t.TempDir(), "reach.jsonl")}
	go func() { done <- run(ctx, args) }()
	var target string
	select {
	case target = <-bound:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("run never bound its listeners")
	}
	response, err := http.Get("http://" + target + "/bypass/B08")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	header := response.Header.Get("Alt-Svc")
	if !strings.HasPrefix(header, `h3=":`) || strings.HasPrefix(header, `h3=":0"`) {
		t.Fatal(header)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsIncompleteFlags(t *testing.T) {
	if err := run(t.Context(), []string{"-listen", "127.0.0.1:0"}); err == nil || !strings.Contains(err.Error(), "are required") {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"-unknown"}); err == nil {
		t.Fatal("unknown flag must fail")
	}
}

func TestCloseReportsEarlierRecordFailure(t *testing.T) {
	rec, _ := newTestRecorder(t)
	earlier := errors.New("earlier failure")
	rec.err = earlier
	if err := rec.close(); !errors.Is(err, earlier) {
		t.Fatal(err)
	}
}

func TestRecordKeepsFirstFailure(t *testing.T) {
	rec, _ := newTestRecorder(t)
	rec.file.Close()
	first := rec.record(row{Surface: surfaceReceiver, Scenario: "B07"})
	if second := rec.record(row{Surface: surfaceReceiver, Scenario: "B07"}); first == nil || second != first {
		t.Fatal(first, second)
	}
}

func TestParseReceiver(t *testing.T) {
	got, err := parseReceiver("https://outside.test:9001/", "0.0.0.0:9002", true)
	if want := (receiverInfo{"https://outside.test:9001", "wss://outside.test:9001", "outside.test:9002", "9002"}); err != nil || got != want {
		t.Fatalf("%+v %v", got, err)
	}
	bad := []string{"", "ftp://a.test", "http://", "http://a.test/x", "http://a.test?q=1", "http://a.test#f", "http://u:p@a.test", `http://a"b.test`}
	for _, raw := range bad {
		if _, err := parseReceiver(raw, "0.0.0.0:9002", false); err == nil {
			t.Fatal(raw)
		}
	}
	if _, err := parseReceiver("http://a.test", "no-port", false); err == nil {
		t.Fatal("udp address")
	}
	if plain, err := parseReceiver("http://a.test", "0.0.0.0:1", false); err != nil || plain.websocket != "ws://a.test" {
		t.Fatalf("%+v %v", plain, err)
	}
	if _, err := parseReceiver("https://a.test", "0.0.0.0:1", false); err == nil {
		t.Fatal("https receiver without TLS")
	}
	if _, err := parseReceiver("http://a.test", "0.0.0.0:1", true); err == nil {
		t.Fatal("http receiver with TLS target")
	}
}
