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
// meter methods, is under test. The front binds port 0 and reports its own
// address, so no other listener can stand in for it.
func runEngine(t *testing.T, upstream string, extra ...string) (string, string, chan int) {
	t.Helper()
	bound := make(chan net.Addr, 1)
	listening = func(addr net.Addr) { bound <- addr }
	t.Cleanup(func() { listening = func(net.Addr) {} })
	dir, _ := privateTestDir(t)
	exit := make(chan int, 1)
	go func() {
		var stderr bytes.Buffer
		args := append([]string{"-role", "engine", "-listen", "127.0.0.1:0", "-upstream", upstream, "-log-dir", dir},
			extra...)
		exit <- run(args, strings.NewReader(""), io.Discard, &stderr)
	}()
	select {
	case addr := <-bound:
		return addr.String(), dir, exit
	case code := <-exit:
		t.Fatalf("front exited %d before listening", code)
	case <-time.After(5 * time.Second):
		t.Fatal("front never started listening")
	}
	return "", "", nil
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
	// A fresh transport per run: no idle connection from an earlier front can be reused.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	for _, sealFirst := range []bool{true, false} {
		addr, dir, exit := runEngine(t, upstream.URL)
		resp, err := client.Get("http://" + addr + "/x")
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

// The -drain-grace flag reaches the meter the command runs: a request left open
// after the seal is cancelled and counted, and the drain completes.
func TestRunEngineDrainGraceFlag(t *testing.T) {
	arrived := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		<-r.Context().Done()
	}))
	t.Cleanup(func() {
		upstream.CloseClientConnections()
		upstream.Close()
	})
	addr, dir, exit := runEngine(t, upstream.URL, "-drain-grace", "100ms")
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := client.Get("http://" + addr + "/hang"); err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached the upstream")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	waitFile(t, filepath.Join(dir, drainedName))
	<-done
	if got := readMarker[drainedMarker](t, filepath.Join(dir, drainedName)); got.ForcedCancelled != 1 ||
		got.InflightAtDrain != 0 {
		t.Fatalf("drained marker: %+v", got)
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
}

// A seal sent the moment the front is reachable must be recorded: the signal
// handlers are installed before the port opens.
func TestRunEngineSealAtFirstListenIsRecorded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	dir, _ := privateTestDir(t)
	listening = func(net.Addr) {
		if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { listening = func(net.Addr) {} })
	exit := make(chan int, 1)
	go func() {
		var stderr bytes.Buffer
		exit <- run([]string{"-role", "engine", "-listen", "127.0.0.1:0", "-upstream", upstream.URL, "-log-dir", dir},
			strings.NewReader(""), io.Discard, &stderr)
	}()
	waitFile(t, filepath.Join(dir, drainedName))
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
	if !got.Sealed || !got.Drained || got.Forwarded != 0 {
		t.Fatalf("terminated marker: %+v", got)
	}
}

func TestRunEnginePathLogFlagWritesPatterns(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	addr, dir, exit := runEngine(t, upstream.URL, "-path-log")
	resp, err := client.Get("http://" + addr + "/api/Users/7")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exit:
	case <-time.After(10 * time.Second):
		t.Fatal("front did not stop")
	}
	rows := readRows[pathRow](t, filepath.Join(dir, enginePathLogName))
	if len(rows) != 1 || rows[0].Pattern != "/api/Users/{id}" {
		t.Fatalf("%+v", rows)
	}
}
