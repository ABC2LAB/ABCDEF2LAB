//go:build linux

package llmpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// 호출 기록은 승인 장부와 같은 보호 디렉터리의 0600 파일에 한 줄씩 남는다. 순번은 재시작한 다른 정책 인스턴스와 합쳐도
// 1부터 이어진다. 파일이 보호 조건을 잃으면 기록만 실패로 세고 호출은 그대로 끝난다.
func TestOpenRouterCallRecordFileIsProtectedAndSequenced(t *testing.T) {
	p := openRouterFixture(t)
	m := p.manifest
	m.MaxRequests, m.MaxInputTokens, m.MaxOutputTokens = 5, 5*openRouterContext, 500
	raw, _ := json.Marshal(m)
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	path := filepath.Join(dir, "approval.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	upstream := openRouterRT(func(*http.Request) (*http.Response, error) { return openRouterResponse(openRouterReplyWithID), nil })
	call := func(p *OpenRouterPolicy) {
		t.Helper()
		resp, err := openRouterClient(p, upstream).Do(openRouterRequest())
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	first, err := LoadOpenRouterManifestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	call(first)
	call(first)
	second, err := LoadOpenRouterManifestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	call(second)
	name := filepath.Join(dir, fmt.Sprintf(".openrouter-%x.calls", sha256.Sum256([]byte(p.manifest.ApprovalID))))
	info, err := os.Stat(name)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("call record file missing or not 0600", err)
	}
	data, _ := os.ReadFile(name)
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) != 3 {
		t.Fatalf("%d call record lines", len(lines))
	}
	for i, line := range lines {
		var r OpenRouterCallRecord
		if err := json.Unmarshal(line, &r); err != nil || r.Seq != uint64(i+1) || r.Decision != OpenRouterCallForwarded || r.GenerationID != "gen-1700000000-AbCd_12" {
			t.Fatalf("line %d: %+v %v", i, r, err)
		}
	}
	if first.CallRecordFailures() != 0 || second.CallRecordFailures() != 0 {
		t.Fatal("record failures on a protected file")
	}
	// 공유 읽기로 바뀐 기록 파일에는 더하지 않는다. 호출은 그대로 끝나고 실패 수만 는다.
	if err := os.Chmod(name, 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := LoadOpenRouterManifestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	call(third)
	after, _ := os.ReadFile(name)
	if third.CallRecordFailures() != 1 || !bytes.Equal(after, data) || third.Snapshot().Requests != 1 || third.Snapshot().Blocked {
		t.Fatalf("unprotected record file: failures %d usage %+v", third.CallRecordFailures(), third.Snapshot())
	}
}
