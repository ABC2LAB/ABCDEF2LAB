//go:build linux

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 제품 실행(평가 문맥 없음)은 실제로 보낸 시도마다 상태 디렉터리의 표시용 패턴 파일에 값 조각을 가린 경로를 남긴다.
// 원문 값은 남지 않는다(작업대 e2-flow path_pattern).
func TestE2HTTPFactoryRecordsProductAttemptPatterns(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "owned") }))
	defer target.Close()
	cfg, _, policy := e2HostPolicyFixture(t)
	if policy.Evaluation != nil {
		t.Fatal("fixture is not a product policy")
	}
	policy.Rules[0].Origin = target.URL
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0600) != nil {
		t.Fatal("합성 정책 준비 실패")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	options, err := LoadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: ctx}, Supervisor: &e2HTTPSupervisorFixture{}})
	if err != nil {
		t.Fatal("호스트 준비 실패", err)
	}
	lease, err := options.Acquire(ctx, E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 27, IntentID: 31})
	if err != nil {
		t.Fatal("실제 실행 획득 실패", err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if lease.Runner != nil {
			_ = lease.Runner.Close(cleanup)
		}
		if lease.Execution != nil {
			_ = lease.Execution.Stop(cleanup)
		}
		if lease.Release != nil {
			_ = lease.Release(cleanup)
		}
	})
	call, err := lease.Execution.BeginCall(ctx, "attempt-pattern-regression")
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest("GET", target.URL+"/allowed/48151623", nil)
	response, receipt, err := call.Do(ctx, request)
	if err != nil || response == nil || !receipt.ResponseComplete {
		t.Fatalf("양성 요청 실패: %+v %v", receipt, err)
	}
	response.Body.Close()
	closeCtx, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	_, _ = call.Close(closeCtx)
	data, err := os.ReadFile(filepath.Join(policy.StateDir, E2AttemptPatternsFile))
	if err != nil {
		t.Fatal("product attempt pattern file not written", err)
	}
	var row E2AttemptPatternRow
	if json.Unmarshal([]byte(strings.TrimSpace(string(data))), &row) != nil || row.AttemptID != receipt.AttemptID || row.PathPattern != "/allowed/{id}" || strings.Contains(string(data), "48151623") {
		t.Fatalf("pattern row: %q", data)
	}
}
