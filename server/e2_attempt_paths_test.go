//go:build !windows

package server

import (
	"bufio"
	"bytes"
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
	if err := early.close(); err != nil {
		t.Fatal(err)
	}
	if rows := readAttemptPathRows(t, filepath.Join(dir, E2AttemptPathsFile)); len(rows) != 0 || !early.failed {
		t.Fatalf("rows after a lost attempt: %v", rows)
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
	if !broken.failed {
		t.Fatal("write failure not latched")
	}
	// 닫은 뒤의 시도도 실패로 남는다.
	closed := newE2AttemptPathLog(attemptPathTestKey)
	if err := closed.open(t.TempDir()); err != nil || closed.close() != nil {
		t.Fatal("close")
	}
	closed.observe(e2proxy.Receipt{Sequence: 1}, "/a")
	if !closed.failed || closed.open(t.TempDir()) == nil {
		t.Fatal("closed projection accepted rows or reopened")
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
	rows := readAttemptPathRows(t, filepath.Join(dir, E2AttemptPathsFile))
	seen := map[float64]bool{}
	for _, r := range rows {
		seen[r["sequence"].(float64)] = true
	}
	if len(rows) != 64 || len(seen) != 64 || l.failed {
		t.Fatalf("%d rows %d sequences failed=%t", len(rows), len(seen), l.failed)
	}
}
