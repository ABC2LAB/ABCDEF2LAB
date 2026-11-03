package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hangingTarget answers /ok at once and holds /hang until the request is
// cancelled or release is closed, like a long poll the engine left open.
func hangingTarget(t *testing.T, release <-chan struct{}) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	arrived := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hang" {
			return
		}
		arrived <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() {
		target.CloseClientConnections()
		target.Close()
	})
	return target, arrived
}

func graceEngine(t *testing.T, target *httptest.Server, grace time.Duration) (*engineMeter, *httptest.Server, string) {
	t.Helper()
	path, dir := privateTestDir(t)
	m, code := newEngineMeter(dir, targetURL(t, target), time.Now, grace)
	if code != "" {
		t.Fatalf("engine meter: %s", code)
	}
	front := httptest.NewServer(m)
	t.Cleanup(func() {
		// A failing test can leave /hang open; drop the connections so Close does not wait for it.
		front.CloseClientConnections()
		front.Close()
		_ = m.close()
	})
	return m, front, path
}

// startHang sends /hang and returns the status the engine side eventually sees.
func startHang(t *testing.T, front *httptest.Server, arrived <-chan struct{}) <-chan int {
	t.Helper()
	status := make(chan int, 1)
	go func() {
		resp, err := http.Get(front.URL + "/hang")
		if err != nil {
			status <- 0
			return
		}
		_ = resp.Body.Close()
		status <- resp.StatusCode
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("hanging request never reached the target")
	}
	return status
}

func drainedWithin(m *engineMeter, wait time.Duration) bool {
	select {
	case <-m.drained:
		return true
	case <-time.After(wait):
		return false
	}
}

func TestDrainGraceCancelsRequestsStillOpenAfterSeal(t *testing.T) {
	target, arrived := hangingTarget(t, nil)
	m, front, path := graceEngine(t, target, 100*time.Millisecond)
	resp, err := http.Get(front.URL + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	status := startHang(t, front, arrived)
	m.seal()
	if drainedWithin(m, 30*time.Millisecond) {
		t.Fatal("drained before the grace period ended")
	}
	if !drainedWithin(m, 5*time.Second) {
		t.Fatal("open request was not cancelled after the grace period")
	}
	if got := <-status; got != http.StatusBadGateway {
		t.Fatalf("cancelled request answered %d to the engine", got)
	}
	marker := readMarker[drainedMarker](t, filepath.Join(path, drainedName))
	if marker.ForcedCancelled != 1 || marker.InflightAtDrain != 0 || marker.Forwarded != 2 || !marker.LogComplete {
		t.Fatalf("drained marker %+v", marker)
	}
	outcomes := map[string]int{}
	for _, row := range readRows[engineRow](t, filepath.Join(path, engineLogName)) {
		if row.Kind != "request" {
			t.Fatalf("unexpected row kind %q", row.Kind)
		}
		outcomes[row.Outcome]++
	}
	// The cancelled request was admitted before seal: it stays a counted request, marked as cut short.
	if outcomes["completed"] != 1 || outcomes["forced_cancelled"] != 1 || len(outcomes) != 2 {
		t.Fatalf("row outcomes %v", outcomes)
	}
	m.terminate("signal")
	terminated := readMarker[terminatedMarker](t, filepath.Join(path, terminatedName))
	if terminated.ForcedCancelled != 1 || !terminated.Drained {
		t.Fatalf("terminated marker %+v", terminated)
	}
}

func TestDrainWithoutGraceWaitsForTheRequest(t *testing.T) {
	release := make(chan struct{})
	target, arrived := hangingTarget(t, release)
	m, front, path := graceEngine(t, target, 0)
	status := startHang(t, front, arrived)
	m.seal()
	if drainedWithin(m, 150*time.Millisecond) {
		t.Fatal("drained while a request was still open and no grace was set")
	}
	close(release)
	if !drainedWithin(m, 5*time.Second) {
		t.Fatal("never drained after the request finished")
	}
	if got := <-status; got != http.StatusOK {
		t.Fatalf("status %d", got)
	}
	if marker := readMarker[drainedMarker](t, filepath.Join(path, drainedName)); marker.ForcedCancelled != 0 {
		t.Fatalf("drained marker %+v", marker)
	}
}

func TestDrainGraceDoesNotCountRequestsThatFinishInTime(t *testing.T) {
	release := make(chan struct{})
	target, arrived := hangingTarget(t, release)
	m, front, path := graceEngine(t, target, 400*time.Millisecond)
	status := startHang(t, front, arrived)
	m.seal()
	close(release)
	if !drainedWithin(m, 5*time.Second) {
		t.Fatal("never drained")
	}
	if got := <-status; got != http.StatusOK {
		t.Fatalf("status %d", got)
	}
	time.Sleep(500 * time.Millisecond) // the grace timer fires after the drain and must not count anything
	marker := readMarker[drainedMarker](t, filepath.Join(path, drainedName))
	if marker.ForcedCancelled != 0 || m.forcedCancel != 0 {
		t.Fatalf("finished request counted as cancelled: %+v", marker)
	}
	for _, row := range readRows[engineRow](t, filepath.Join(path, engineLogName)) {
		if row.Outcome != "completed" {
			t.Fatalf("row outcome %q", row.Outcome)
		}
	}
}

func TestHandshakeCancelledByGraceCountsOnceAsForcedStream(t *testing.T) {
	target, _ := hangingTarget(t, nil)
	m, _, path := graceEngine(t, target, time.Hour) // cancelInflight is called directly below
	m.mu.Lock()
	m.seq, m.inflight, m.forwarded, m.streamsTotal = 1, 1, 1, 1
	m.cancels[1] = func() {}
	m.mu.Unlock()
	m.seal()
	m.cancelInflight()
	client, server := net.Pipe()
	defer client.Close()
	if err := m.trackStream(1, server); err != errSealed {
		t.Fatalf("hijack after seal: %v", err)
	}
	marker := readMarker[drainedMarker](t, filepath.Join(path, drainedName))
	if marker.ForcedCancelled != 0 || marker.ForcedClosed != 1 || marker.InflightAtDrain != 0 {
		t.Fatalf("drained marker %+v", marker)
	}
}

func TestDrainGraceFlagBelongsToTheEngineAndIsBounded(t *testing.T) {
	cases := [][]string{
		{"-role", "engine", "-listen", "127.0.0.1:0", "-upstream", "http://target:3000", "-log-dir", "/x", "-drain-grace", "-1s"},
		{"-role", "engine", "-listen", "127.0.0.1:0", "-upstream", "http://target:3000", "-log-dir", "/x",
			"-drain-grace", (maxDrainGrace + time.Second).String()},
		{"-role", "observer", "-listen", "127.0.0.1:0", "-upstream", "http://target:3000", "-log-dir", "/x",
			"-allow-path", "/api/Challenges", "-drain-grace", "1s"},
		{"-role", "probe", "-drain-grace", "1s"},
	}
	for _, args := range cases {
		var stderr bytes.Buffer
		if code := run(args, strings.NewReader(""), &bytes.Buffer{}, &stderr); code != 2 ||
			!strings.HasPrefix(stderr.String(), invalidInput) {
			t.Fatalf("%v: exit %d stderr %q", args, code, stderr.String())
		}
	}
}
