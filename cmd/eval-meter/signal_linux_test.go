package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runEngine starts the real command loop so the signal wiring, not just the
// meter methods, is under test.
func runEngine(t *testing.T, upstream string) (string, string, chan int) {
	t.Helper()
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := free.Addr().String()
	_ = free.Close()
	dir, _ := privateTestDir(t)
	exit := make(chan int, 1)
	go func() {
		var stderr bytes.Buffer
		exit <- run([]string{"-role", "engine", "-listen", addr, "-upstream", upstream, "-log-dir", dir},
			strings.NewReader(""), io.Discard, &stderr)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if conn, err := net.Dial("tcp", addr); err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("front never started listening")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return addr, dir, exit
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", filepath.Base(path))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunEngineSignals(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	for _, sealFirst := range []bool{true, false} {
		addr, dir, exit := runEngine(t, upstream.URL)
		resp, err := http.Get("http://" + addr + "/x")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if sealFirst {
			if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
				t.Fatal(err)
			}
			waitFile(t, filepath.Join(dir, drainedName))
		}
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case code := <-exit:
			if code != 0 {
				t.Fatalf("exit %d", code)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("front did not stop on SIGTERM")
		}
		got := readMarker[terminatedMarker](t, filepath.Join(dir, terminatedName))
		if got.Sealed != sealFirst || got.Drained != sealFirst || got.Forwarded != 1 || got.Reason != "signal" {
			t.Fatalf("sealFirst=%v terminated marker: %+v", sealFirst, got)
		}
	}
}
