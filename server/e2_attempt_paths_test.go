//go:build !windows

package server

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/pathmask"
)

var attemptPathTestKey = bytes.Repeat([]byte{41}, pathmask.KeyBytes)

func readAttemptPathRows(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	return rows
}

// 완료 표식이 있고 행 수·digest가 기록과 같은지 본다. 표식이 없으면 수집기는 그 기록을 받지 않는다.
func requireAttemptPathsDone(t *testing.T, dir string, rows int) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, E2AttemptPathsFile))
	if err != nil {
		t.Fatal(err)
	}
	done, err := os.ReadFile(filepath.Join(dir, E2AttemptPathsDoneFile))
	if err != nil {
		t.Fatal("completion marker missing", err)
	}
	var marker map[string]any
	if err := json.Unmarshal(done, &marker); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if len(marker) != 3 || marker["format"] != E2AttemptPathsFormat || marker["rows"] != float64(rows) || marker["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("marker %v rows=%d", marker, rows)
	}
	if info, err := os.Stat(filepath.Join(dir, E2AttemptPathsDoneFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("marker mode", err)
	}
}

func requireNoAttemptPathsDone(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, E2AttemptPathsDoneFile)); !os.IsNotExist(err) {
		t.Fatal("completion marker written for a failed projection")
	}
}

func TestE2AttemptPathLogWritesMaskedRowsOnly(t *testing.T) {
	if newE2AttemptPathLog(attemptPathTestKey[:8]) != nil {
		t.Fatal("short key accepted")
	}
	dir := t.TempDir()
	l := newE2AttemptPathLog(attemptPathTestKey)
	if err := l.open(dir); err != nil {
		t.Fatal(err)
	}
	l.observe(e2proxy.Receipt{Sequence: 1, Decision: "allowed", Reason: e2proxy.Complete, Method: "GET"}, "/allowed/item")
	l.observe(e2proxy.Receipt{Sequence: 2, Decision: "denied", Reason: e2proxy.PolicyDenied, Method: "POST"}, "/outside/jimsmithexample/1234")
	l.observe(e2proxy.Receipt{Sequence: 3, Decision: "unsupported", Reason: e2proxy.Unsupported}, "")
	if err := l.close(); err != nil || l.close() != nil {
		t.Fatal("close", err)
	}
	requireAttemptPathsDone(t, dir, 3)
	path := filepath.Join(dir, E2AttemptPathsFile)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal(err, info.Mode())
	}
	rows := readAttemptPathRows(t, path)
	ref := pathmask.KeyRef(attemptPathTestKey)
	want := []struct {
		seq                    float64
		decision, reason, meth string
		escaped                string
	}{{1, "allowed", "complete", "GET", "/allowed/item"}, {2, "denied", "policy_denied", "POST", "/outside/jimsmithexample/1234"}, {3, "unsupported", "unsupported", "", ""}}
	if len(rows) != len(want) {
		t.Fatalf("%d rows", len(rows))
	}
	for i, w := range want {
		r := rows[i]
		if len(r) != 8 || r["sequence"] != w.seq || r["decision"] != w.decision || r["reason"] != w.reason || r["method"] != w.meth ||
			r["path_profile_ref"] != pathmask.Profile || r["path_key_ref"] != ref {
			t.Fatalf("%d: %v", i, r)
		}
		if w.escaped == "" {
			if r["path_pattern"] != nil || r["masked_segments"] != float64(0) {
				t.Fatalf("%d: missing path not null: %v", i, r)
			}
			continue
		}
		pattern, masked := pathmask.Mask(w.escaped, attemptPathTestKey)
		if r["path_pattern"] != pattern || r["masked_segments"] != float64(masked) {
			t.Fatalf("%d: %v want %s %d", i, r, pattern, masked)
		}
	}
	raw, _ := os.ReadFile(path)
	for _, secret := range []string{"jimsmithexample", "outside", "allowed/item", "1234", string(attemptPathTestKey)} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("raw value %q in projection", secret)
		}
	}
	if !bytes.Equal(l.key, make([]byte, len(l.key))) {
		t.Fatal("key kept after close")
	}
}

func TestE2AttemptPathLogFailuresStopWriting(t *testing.T) {
	// 열기 전 시도는 기록할 수 없어 실패로 남고, 열어도 그 뒤 행을 쓰지 않는다(빠진 행을 러너가 잡는다).
	dir := t.TempDir()
	early := newE2AttemptPathLog(attemptPathTestKey)
	early.observe(e2proxy.Receipt{Sequence: 1, Decision: "allowed"}, "/a")
	if err := early.open(dir); err != nil {
		t.Fatal(err)
	}
	early.observe(e2proxy.Receipt{Sequence: 2, Decision: "allowed"}, "/b")
	if err := early.close(); err == nil || early.close() == nil {
		t.Fatal("lost attempt closed as complete")
	}
	if rows := readAttemptPathRows(t, filepath.Join(dir, E2AttemptPathsFile)); len(rows) != 0 || !early.failed {
		t.Fatalf("rows after a lost attempt: %v", rows)
	}
	requireNoAttemptPathsDone(t, dir)
	// 한 번도 열지 못했는데 시도가 있었으면 마감도 실패다. 시도가 없었으면 기록·표식 없이 성공이다.
	never := newE2AttemptPathLog(attemptPathTestKey)
	never.observe(e2proxy.Receipt{Sequence: 1, Decision: "allowed"}, "/a")
	if never.close() == nil {
		t.Fatal("unopened projection with an attempt closed as complete")
	}
	if idle := newE2AttemptPathLog(attemptPathTestKey); idle.close() != nil || idle.failed {
		t.Fatal("unused projection failed")
	}
	// 이미 있는 파일·링크 자리에는 열지 않는다.
	again := newE2AttemptPathLog(attemptPathTestKey)
	if again.open(dir) == nil {
		t.Fatal("existing projection reopened")
	}
	linked := t.TempDir()
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), filepath.Join(linked, E2AttemptPathsFile)); err != nil {
		t.Fatal(err)
	}
	if newE2AttemptPathLog(attemptPathTestKey).open(linked) == nil {
		t.Fatal("projection followed a link")
	}
	// 쓰기 오류 뒤에는 쓰지 않는다.
	broken := newE2AttemptPathLog(attemptPathTestKey)
	brokenDir := t.TempDir()
	if err := broken.open(brokenDir); err != nil {
		t.Fatal(err)
	}
	_ = broken.file.Close()
	broken.observe(e2proxy.Receipt{Sequence: 1, Decision: "allowed"}, "/a")
	if !broken.failed || broken.close() == nil {
		t.Fatal("write failure not latched")
	}
	requireNoAttemptPathsDone(t, brokenDir)
	// 닫은 뒤의 시도도 실패로 남는다.
	closed := newE2AttemptPathLog(attemptPathTestKey)
	if err := closed.open(t.TempDir()); err != nil || closed.close() != nil {
		t.Fatal("close")
	}
	closed.observe(e2proxy.Receipt{Sequence: 1}, "/a")
	if !closed.failed || closed.open(t.TempDir()) == nil || closed.close() == nil {
		t.Fatal("closed projection accepted rows, reopened, or forgot the late attempt")
	}
}

// 행이 다 있어도 마감(Sync·Close)에 실패하면 완료 표식을 쓰지 않는다. 순번 대조만으로는 이 실패를 못 잡는다.
func TestE2AttemptPathLogCloseFailureLeavesNoMarker(t *testing.T) {
	dir := t.TempDir()
	l := newE2AttemptPathLog(attemptPathTestKey)
	if err := l.open(dir); err != nil {
		t.Fatal(err)
	}
	l.observe(e2proxy.Receipt{Sequence: 1, Decision: "allowed", Reason: e2proxy.Complete, Method: "GET"}, "/allowed")
	l.observe(e2proxy.Receipt{Sequence: 2, Decision: "denied", Reason: e2proxy.PolicyDenied, Method: "GET"}, "/outside")
	_ = l.file.Close() // Sync·Close가 실패하게 만든다.
	if err := l.close(); err == nil || l.close() == nil || !l.failed {
		t.Fatal("close failure not reported", err)
	}
	if rows := readAttemptPathRows(t, filepath.Join(dir, E2AttemptPathsFile)); len(rows) != 2 {
		t.Fatalf("fixture expected every row to exist: %d", len(rows))
	}
	requireNoAttemptPathsDone(t, dir)
	// 표식 자리를 이미 무엇인가 차지하고 있으면(쓸 수 없으면) 역시 실패다.
	taken := t.TempDir()
	other := newE2AttemptPathLog(attemptPathTestKey)
	if err := other.open(taken); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taken, E2AttemptPathsDoneFile), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if other.close() == nil || !other.failed {
		t.Fatal("existing marker accepted")
	}
}

func TestE2AttemptPathLogConcurrentRowsStayWhole(t *testing.T) {
	dir := t.TempDir()
	l := newE2AttemptPathLog(attemptPathTestKey)
	if err := l.open(dir); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 1; i <= 64; i++ {
		wg.Add(1)
		go func(seq uint64) {
			defer wg.Done()
			l.observe(e2proxy.Receipt{Sequence: seq, Decision: "denied", Reason: e2proxy.PolicyDenied, Method: "GET"}, "/x/"+strings.Repeat("a", int(seq)%20))
		}(uint64(i))
	}
	wg.Wait()
	if err := l.close(); err != nil {
		t.Fatal(err)
	}
	requireAttemptPathsDone(t, dir, 64)
	rows := readAttemptPathRows(t, filepath.Join(dir, E2AttemptPathsFile))
	seen := map[float64]bool{}
	for _, r := range rows {
		seen[r["sequence"].(float64)] = true
	}
	if len(rows) != 64 || len(seen) != 64 || l.failed {
		t.Fatalf("%d rows %d sequences failed=%t", len(rows), len(seen), l.failed)
	}
}
