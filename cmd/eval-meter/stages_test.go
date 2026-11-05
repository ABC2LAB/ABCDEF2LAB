package main

import (
	"bufio"
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rawUpstream accepts connections and lets act decide what the target does with
// each one, so the tests can produce writes and responses the stdlib server
// would never produce (dropping mid-request, closing without a response).
func rawUpstream(t *testing.T, act func(net.Conn)) *url.URL {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				act(conn)
			}()
		}
	}()
	return &url.URL{Scheme: "http", Host: ln.Addr().String()}
}

func newRawEngine(t *testing.T, target *url.URL) (*engineMeter, *httptest.Server, string) {
	t.Helper()
	path, dir := privateTestDir(t)
	m, code := newEngineMeter(dir, target, time.Now, 0)
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

func onlyRow(t *testing.T, path string) engineRow {
	t.Helper()
	rows := readRows[engineRow](t, filepath.Join(path, engineLogName))
	if len(rows) != 1 {
		t.Fatalf("want one row, got %+v", rows)
	}
	return rows[0]
}

func TestEngineStageCompletedRequest(t *testing.T) {
	upstream, _ := recordingUpstream(t)
	m, front, path := newTestEngine(t, upstream)
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/ok", nil)
	if status, _ := send(t, req); status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	m.seal()
	row := onlyRow(t, path)
	if row.Sent != sentYes || row.Response != responseComplete || row.Outcome != "completed" ||
		row.AttemptIDState != attemptAbsent || row.AttemptID != "" {
		t.Fatalf("row: %+v", row)
	}
}

func TestEngineStageNotSentWhenTargetIsDown(t *testing.T) {
	upstream, _ := recordingUpstream(t)
	m, front, path := newTestEngine(t, upstream)
	upstream.Close()
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/gone", nil)
	if status, _ := send(t, req); status != http.StatusBadGateway {
		t.Fatalf("status=%d", status)
	}
	m.seal()
	// The 502 is the meter's own answer, not a target response.
	if row := onlyRow(t, path); row.Sent != sentNo || row.Response != responseNone || row.Outcome != "upstream_error" {
		t.Fatalf("row: %+v", row)
	}
}

func TestEngineStageWrittenButNoResponse(t *testing.T) {
	target := rawUpstream(t, func(conn net.Conn) {
		// Read the whole request head, then close without answering.
		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil || line == "\r\n" {
				return
			}
		}
	})
	m, front, path := newRawEngine(t, target)
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/silent", nil)
	if status, _ := send(t, req); status != http.StatusBadGateway {
		t.Fatalf("status=%d", status)
	}
	m.seal()
	// Delivered but unanswered: the result is unknown, not "not sent".
	if row := onlyRow(t, path); row.Sent != sentYes || row.Response != responseNone {
		t.Fatalf("row: %+v", row)
	}
}

func TestEngineStageUncertainWhenTargetDropsMidRequest(t *testing.T) {
	target := rawUpstream(t, func(conn net.Conn) {
		buf := make([]byte, 512)
		_, _ = conn.Read(buf)
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0) // reset instead of a graceful close so the writer fails
		}
	})
	m, front, path := newRawEngine(t, target)
	body := bytes.Repeat([]byte("x"), 64<<20)
	req, _ := http.NewRequest(http.MethodPost, front.URL+"/upload", bytes.NewReader(body))
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err == nil {
		resp.Body.Close()
	}
	m.seal()
	if row := onlyRow(t, path); row.Sent != sentUncertain || row.Response != responseNone {
		t.Fatalf("row: %+v", row)
	}
}

func TestEngineStageInterruptedResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(upstream.Close)
	m, front, path := newTestEngine(t, upstream)
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/cut", nil)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err == nil {
		_, _ = bufio.NewReader(resp.Body).ReadString(0)
		resp.Body.Close()
	}
	m.seal()
	if row := onlyRow(t, path); row.Sent != sentYes || row.Response != responseInterrupted || row.Outcome != "aborted" {
		t.Fatalf("row: %+v", row)
	}
}

func TestEngineStageRejectedAfterSeal(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	m, front, path := newTestEngine(t, upstream)
	m.seal()
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/late", nil)
	req.Header.Set(attemptHeader, strings.Repeat("a", 32))
	if status, _ := send(t, req); status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", status)
	}
	row := onlyRow(t, path)
	if row.Kind != "rejected" || row.Sent != sentNo || row.Response != responseNone ||
		row.AttemptIDState != attemptValid || row.AttemptID != strings.Repeat("a", 32) || len(seen()) != 0 {
		t.Fatalf("row: %+v seen=%d", row, len(seen()))
	}
}

func TestEngineAttemptIDIsRecordedAndStripped(t *testing.T) {
	valid := "0123456789abcdef0123456789abcdef"
	cases := []struct {
		name   string
		values []string
		state  string
		id     string
	}{
		{"absent", nil, attemptAbsent, ""},
		{"valid", []string{valid}, attemptValid, valid},
		{"uppercase", []string{strings.ToUpper(valid)}, attemptInvalid, ""},
		{"uuid", []string{"01234567-89ab-cdef-0123-456789abcdef"}, attemptInvalid, ""},
		{"sentinel", []string{sentinel}, attemptInvalid, ""},
		{"comma", []string{valid + "," + valid}, attemptInvalid, ""},
		{"multiple", []string{valid, valid}, attemptMultiple, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream, seen := recordingUpstream(t)
			m, front, path := newTestEngine(t, upstream)
			req, _ := http.NewRequest(http.MethodGet, front.URL+"/item", nil)
			req.Header.Set("X-Other", "kept")
			for _, v := range tc.values {
				req.Header.Add(attemptHeader, v)
			}
			// A malformed ID must not block the request: the product defect stays visible.
			if status, _ := send(t, req); status != http.StatusOK {
				t.Fatalf("status=%d", status)
			}
			m.seal()
			row := onlyRow(t, path)
			if row.AttemptIDState != tc.state || row.AttemptID != tc.id || row.Sent != sentYes {
				t.Fatalf("row: %+v", row)
			}
			got := seen()
			if len(got) != 1 || len(got[0].header.Values(attemptHeader)) != 0 || got[0].header.Get("X-Other") != "kept" {
				t.Fatalf("target saw %+v", got)
			}
			log, err := os.ReadFile(filepath.Join(path, engineLogName))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(log, []byte(sentinel)) || bytes.Contains(log, []byte("01234567-89ab")) ||
				bytes.Contains(log, []byte(strings.ToUpper(valid))) {
				t.Fatalf("malformed attempt value leaked into the log")
			}
		})
	}
}
