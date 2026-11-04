package main

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAPIFrontForwardsUnchanged(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	front := httptest.NewServer(newAPIFront(targetURL(t, upstream)))
	defer front.Close()

	req, err := http.NewRequest(http.MethodPost, front.URL+"/api/tasks?x=1&y=%2F", strings.NewReader(`{"goal":"g"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "127.0.0.1:18787"
	req.Header.Set("Authorization", "Bearer operator-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "10.9.9.9")
	req.Header.Set("Forwarded", "for=10.9.9.9")
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "upstream" {
		t.Fatalf("response %d %q", resp.StatusCode, body)
	}
	got := seen()
	if len(got) != 1 {
		t.Fatalf("upstream calls %d", len(got))
	}
	call := got[0]
	if call.method != http.MethodPost || call.uri != "/api/tasks?x=1&y=%2F" || call.body != `{"goal":"g"}` ||
		call.host != "127.0.0.1:18787" || call.header.Get("Authorization") != "Bearer operator-token" {
		t.Fatalf("forwarded %+v", call)
	}
	// A client must not be able to pose as another address to the engine.
	for _, name := range []string{"X-Forwarded-For", "Forwarded", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if call.header.Get(name) != "" {
			t.Fatalf("%s reached the engine: %q", name, call.header.Get(name))
		}
	}
}

func TestAPIFrontStreamsWithoutBuffering(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: last\n\n")
	}))
	defer upstream.Close()
	defer close(release)
	front := httptest.NewServer(newAPIFront(targetURL(t, upstream)))
	defer front.Close()

	resp, err := front.Client().Get(front.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line := make(chan string, 1)
	go func() {
		got, _ := bufio.NewReader(resp.Body).ReadString('\n')
		line <- got
	}()
	select {
	case got := <-line:
		if got != "data: first\n" {
			t.Fatalf("first line %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event held back until the stream ended")
	}
}

func TestAPIFrontUpgradeReachesEngine(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = rw.Flush()
		line, _ := rw.ReadString('\n')
		_, _ = rw.WriteString("echo:" + line)
		_ = rw.Flush()
	}))
	defer upstream.Close()
	front := httptest.NewServer(newAPIFront(targetURL(t, upstream)))
	defer front.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "GET /api/ws HTTP/1.1\r\nHost: front\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(status, "101") {
		t.Fatalf("status %q %v", status, err)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	_, _ = io.WriteString(conn, "ping\n")
	echo, err := reader.ReadString('\n')
	if err != nil || echo != "echo:ping\n" {
		t.Fatalf("echo %q %v", echo, err)
	}
}

func TestAPIFrontUpstreamDownGivesBareBadGateway(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	target := targetURL(t, upstream)
	upstream.Close()
	front := httptest.NewServer(newAPIFront(target))
	defer front.Close()
	resp, err := front.Client().Get(front.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || len(bytes.TrimSpace(body)) != 0 {
		t.Fatalf("response %d %q (upstream error text must not leak)", resp.StatusCode, body)
	}
}

func TestRunAPIFrontFlagRules(t *testing.T) {
	path, _ := privateTestDir(t)
	base := []string{"-role", "api-front", "-listen", "127.0.0.1:0", "-upstream", "http://engine:8787"}
	cases := []struct {
		name string
		args []string
		key  string
		code string
	}{
		{"log dir", append(append([]string(nil), base...), "-log-dir", path), "", invalidInput},
		{"allow path", append(append([]string(nil), base...), "-allow-path", "/api"), "", invalidInput},
		{"gateway flag", append(append([]string(nil), base...), "-backend", "api"), "", invalidInput},
		{"https upstream", []string{"-role", "api-front", "-listen", ":0", "-upstream", "https://engine:8787"}, "", invalidInput},
		{"upstream path", []string{"-role", "api-front", "-listen", ":0", "-upstream", "http://engine:8787/api"}, "", invalidInput},
		{"no listen", []string{"-role", "api-front", "-upstream", "http://engine:8787"}, "", invalidInput},
		{"key present", base, canaryKey, keyNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(llmKeyEnv, tc.key)
			var stdout, stderr bytes.Buffer
			if code := run(tc.args, strings.NewReader(""), &stdout, &stderr); code == 0 {
				t.Fatal("accepted")
			}
			if !strings.HasPrefix(stderr.String(), tc.code+"\n") {
				t.Fatalf("stderr %q", stderr.String())
			}
		})
	}
}
