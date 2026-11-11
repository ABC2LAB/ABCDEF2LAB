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

	"github.com/Autumn-27/artex/pathmask"
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
	addr, dir, exit := runEngine(t, upstream.URL, "-path-log", "-path-key-file", writePathKey(t, testPathKey, 0o600))
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
	if len(rows) != 1 || rows[0].PathPattern != "/"+pathmask.Pseudonym("api", testPathKey)+"/"+pathmask.Pseudonym("Users", testPathKey)+"/{id}" {
		t.Fatalf("%+v", rows)
	}
}

// -explore-listen은 두 번째 리슨을 열고, 같은 기록에 입구를 나눠 남긴다. 종료 신호 하나로 둘 다 닫힌다.
func TestRunEngineExploreListenTagsIngress(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	explored := make(chan net.Addr, 1)
	listeningExplore = func(addr net.Addr) { explored <- addr }
	t.Cleanup(func() { listeningExplore = func(net.Addr) {} })
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	addr, dir, exit := runEngine(t, upstream.URL, "-explore-listen", "127.0.0.1:0", "-path-log", "-path-key-file", writePathKey(t, testPathKey, 0o600))
	var exploreAddr string
	select {
	case a := <-explored:
		exploreAddr = a.String()
	case <-time.After(5 * time.Second):
		// 실패해도 엔진을 멈춰서 같은 시험 프로세스의 뒤 시험이 매달리지 않게 한다.
		stopRun(t, exit)
		t.Fatal("explore front did not listen")
	}
	for _, target := range []string{"http://" + addr + "/a", "http://" + exploreAddr + "/b"} {
		resp, err := client.Get(target)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exit:
	case <-time.After(10 * time.Second):
		t.Fatal("fronts did not stop")
	}
	rows := readRows[pathRow](t, filepath.Join(dir, enginePathLogName))
	if len(rows) != 2 || rows[0].Ingress != ingressMain || rows[1].Ingress != ingressExplore {
		t.Fatalf("%+v", rows)
	}
	if _, err := net.DialTimeout("tcp", exploreAddr, 200*time.Millisecond); err == nil {
		t.Fatal("explore front still open after stop")
	}
}

func TestRunExploreListenOnlyForEngine(t *testing.T) {
	dir, _ := privateTestDir(t)
	for _, args := range [][]string{
		{"-role", "observer", "-listen", "127.0.0.1:0", "-explore-listen", "127.0.0.1:0", "-upstream", "http://127.0.0.1:9", "-log-dir", dir, "-allow-path", "/x"},
	} {
		// 받아들이면 그 역할이 신호를 기다리며 계속 돈다. 시험이 매달리지 않게 시간 안에 안 끝나면 멈춘다.
		var stderr bytes.Buffer
		exit := make(chan int, 1)
		go func() { exit <- run(args, strings.NewReader(""), io.Discard, &stderr) }()
		select {
		case code := <-exit:
			if code != 2 || !strings.Contains(stderr.String(), invalidInput) {
				t.Fatalf("%v: %d %s", args, code, stderr.String())
			}
		case <-time.After(2 * time.Second):
			stopRun(t, exit)
			t.Fatalf("%v was accepted", args)
		}
	}
	// 이미 쓰는 주소면 두 리슨 다 열지 않고 기록을 남기지 않는다.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	engineDir, _ := privateTestDir(t)
	var stderr bytes.Buffer
	args := []string{"-role", "engine", "-listen", "127.0.0.1:0", "-explore-listen", busy.Addr().String(), "-upstream", "http://127.0.0.1:9", "-log-dir", engineDir}
	exit := make(chan int, 1)
	go func() { exit <- run(args, strings.NewReader(""), io.Discard, &stderr) }()
	select {
	case code := <-exit:
		if code != 1 || !strings.Contains(stderr.String(), listenUnavailable) {
			t.Fatalf("%d %s", code, stderr.String())
		}
	case <-time.After(2 * time.Second):
		stopRun(t, exit)
		t.Fatal("engine started without its explore front")
	}
}

// stopRun은 신호를 기다리며 도는 run을 SIGTERM으로 끝내고 기다린다.
func stopRun(t *testing.T, exit chan int) {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Error(err)
		return
	}
	select {
	case <-exit:
	case <-time.After(10 * time.Second):
		t.Error("run did not stop on SIGTERM")
	}
}
