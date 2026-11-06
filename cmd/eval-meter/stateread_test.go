package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

const stateReadBody = `{"status":"success","data":[{"secret":"value-one"},{"secret":"value-two"}]}`

func stateReadServer(t *testing.T, handler http.HandlerFunc) int {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func stateReadRun(t *testing.T, plan stateReadPlan) ([]stateReadResult, string) {
	t.Helper()
	input, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-role=state-read"}, bytes.NewReader(input), &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var report stateReadReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	return report.Results, stdout.String()
}

func stateReadPlanFor(port int, paths ...string) stateReadPlan {
	return stateReadPlan{Port: port, TimeoutMS: 5000, MaxBodyBytes: 1 << 16, CountField: "data", Paths: paths}
}

func TestStateReadReportsCountAndDigestOnlyInOrder(t *testing.T) {
	var methods atomic.Value
	port := stateReadServer(t, func(w http.ResponseWriter, r *http.Request) {
		methods.Store(r.Method)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("state read must not carry credentials")
		}
		if r.URL.Path == "/empty" {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		_, _ = w.Write([]byte(stateReadBody))
	})
	results, raw := stateReadRun(t, stateReadPlanFor(port, "/two", "/empty"))
	if len(results) != 2 || results[0].Code != stateReadOK || *results[0].Count != 2 ||
		results[1].Code != stateReadOK || *results[1].Count != 0 || results[0].Status != http.StatusOK {
		t.Fatalf("results = %+v", results)
	}
	digest := sha256.Sum256([]byte(stateReadBody))
	if results[0].SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("digest is not the body digest")
	}
	if methods.Load() != http.MethodGet {
		t.Fatalf("method = %v", methods.Load())
	}
	for _, leaked := range []string{"value-one", "/two", "/empty", "secret"} {
		if strings.Contains(raw, leaked) {
			t.Fatalf("output leaks %q", leaked)
		}
	}
}

func TestStateReadClassifiesEveryOutcome(t *testing.T) {
	port := stateReadServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/large":
			_, _ = w.Write(bytes.Repeat([]byte("x"), 65))
		case "/exact":
			_, _ = w.Write([]byte(`{"data":[]}` + strings.Repeat(" ", 53)))
		case "/not-json":
			_, _ = w.Write([]byte("plain"))
		case "/no-field":
			_, _ = w.Write([]byte(`{"other":[1]}`))
		case "/not-array":
			_, _ = w.Write([]byte(`{"data":{"a":1}}`))
		case "/null":
			_, _ = w.Write([]byte(`{"data":null}`))
		default:
			_, _ = w.Write([]byte(`{"data":[1]}`))
		}
	})
	plan := stateReadPlanFor(port, "/missing", "/redirect", "/large", "/exact", "/not-json", "/no-field", "/not-array", "/null")
	plan.MaxBodyBytes = 64
	results, _ := stateReadRun(t, plan)
	want := []struct {
		code   string
		status int
		digest bool
	}{
		{stateReadStatus, http.StatusNotFound, false},
		{stateReadStatus, http.StatusFound, false},
		{stateReadTooLarge, http.StatusOK, false},
		{stateReadOK, http.StatusOK, true},
		{stateReadNotCounted, http.StatusOK, true},
		{stateReadNotCounted, http.StatusOK, true},
		{stateReadNotCounted, http.StatusOK, true},
		{stateReadNotCounted, http.StatusOK, true},
	}
	for i, expected := range want {
		got := results[i]
		if got.Code != expected.code || got.Status != expected.status || (got.SHA256 != "") != expected.digest {
			t.Errorf("%s: %+v", plan.Paths[i], got)
		}
		if (got.Count != nil) != (expected.code == stateReadOK) {
			t.Errorf("%s: count = %v", plan.Paths[i], got.Count)
		}
	}
}

func TestStateReadTransportFailureIsReportedNotFatal(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	results, _ := stateReadRun(t, stateReadPlanFor(port, "/a"))
	if results[0].Code != stateReadTransport || results[0].Status != 0 || results[0].SHA256 != "" {
		t.Fatalf("result = %+v", results[0])
	}
}

func TestStateReadRefusesBadPlansAndFlags(t *testing.T) {
	good := func() stateReadPlan { return stateReadPlanFor(3000, "/a") }
	cases := map[string]func(*stateReadPlan){
		"port zero":   func(p *stateReadPlan) { p.Port = 0 },
		"port high":   func(p *stateReadPlan) { p.Port = 65536 },
		"timeout":     func(p *stateReadPlan) { p.TimeoutMS = 0 },
		"count field": func(p *stateReadPlan) { p.CountField = "" },
		"body zero":   func(p *stateReadPlan) { p.MaxBodyBytes = 0 },
		"body high":   func(p *stateReadPlan) { p.MaxBodyBytes = maxStateReadBodyBytes + 1 },
		"no paths":    func(p *stateReadPlan) { p.Paths = nil },
		"too many paths": func(p *stateReadPlan) {
			p.Paths = strings.Split(strings.Repeat("/a ", maxStateReadPaths+1), " ")[:maxStateReadPaths+1]
		},
		"relative path":   func(p *stateReadPlan) { p.Paths = []string{"a"} },
		"query":           func(p *stateReadPlan) { p.Paths = []string{"/a?x=1"} },
		"traversal":       func(p *stateReadPlan) { p.Paths = []string{"/a/../b"} },
		"absolute target": func(p *stateReadPlan) { p.Paths = []string{"//evil.example/a"} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			plan := good()
			change(&plan)
			input, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if code := run([]string{"-role=state-read"}, bytes.NewReader(input), &stdout, &stderr); code == 0 || stdout.Len() != 0 {
				t.Fatalf("code = %d stdout = %q", code, stdout.String())
			}
		})
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-role=state-read"}, strings.NewReader(`{"port":3000,"timeout_ms":1,"max_body_bytes":1,"count_field":"d","paths":["/a"],"url":"http://x"}`), &stdout, &stderr); code == 0 {
		t.Fatal("unknown plan field accepted")
	}
	stdout.Reset()
	if code := run([]string{"-role=state-read"}, strings.NewReader(`{"port":3000,"timeout_ms":1,"max_body_bytes":1,"count_field":"d","paths":["/a"]} {}`), &stdout, &stderr); code == 0 {
		t.Fatal("trailing plan value accepted")
	}
	// A valid plan, so that only the flag can be the reason for refusal.
	valid, err := json.Marshal(good())
	if err != nil {
		t.Fatal(err)
	}
	for _, flagValue := range []string{"-listen=:1", "-upstream=http://x", "-log-dir=/x", "-allow-path=/a"} {
		stdout.Reset()
		if code := run([]string{"-role=state-read", flagValue}, bytes.NewReader(valid), &stdout, &stderr); code == 0 || stdout.Len() != 0 {
			t.Fatalf("flag %s accepted", flagValue)
		}
	}
}
