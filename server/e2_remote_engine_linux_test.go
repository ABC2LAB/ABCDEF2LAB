//go:build linux

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func e2RemoteDescribedFixture() e2RemoteDescribe {
	return e2RemoteDescribe{PolicyFingerprintSHA256: "fingerprint", PolicyConfigSHA256: "config", HeaderMode: "preserve", RunRef: "run-1", PolicyRef: "policy-1", CaseRef: "case-1", PolicyRevision: "rev-1", BudgetRef: "budget-1",
		MaxWorkerTurns: 3, MaxToolCalls: 7, WorkerTimeoutMS: 9000, MaxPlannerRounds: 2, MaxIntentRuns: 4, DrainTimeoutMS: 5000}
}

func TestNewE2RemoteRuntimeUsesTheDescribedLimits(t *testing.T) {
	var asked atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 첫 호출은 eval-e2가 아직 뜨는 중인 상황이다.
		if asked.Add(1) == 1 {
			http.Error(w, "not yet", http.StatusServiceUnavailable)
			return
		}
		var request e2RemoteRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Op != e2RemoteDescribeOp {
			t.Error("describe가 아닌 호출")
		}
		described := e2RemoteDescribedFixture()
		e2RemoteWrite(w, http.StatusOK, e2RemoteResponse{Describe: &described})
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime, err := newE2RemoteRuntime(ctx, E2RemoteEngineOptions{URL: server.URL, TokenFile: writeE2RemoteToken(t, e2RemoteTestToken), RequestTimeout: time.Second, DescribeTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	o := runtime.options
	if o.MaxWorkerTurns != 3 || o.MaxToolCalls != 7 || o.WorkerTimeout != 9*time.Second || o.MaxPlannerRounds != 2 || o.MaxIntentRuns != 4 || o.DrainTimeout != 5*time.Second {
		t.Fatalf("한도가 전달되지 않음: %+v", o)
	}
	if o.Expected.RunRef != "run-1" || o.Expected.PolicyRef != "policy-1" || o.Expected.CaseRef != "case-1" || o.Expected.PolicyRevision != "rev-1" || o.Expected.BudgetRef != "budget-1" || o.PolicyFingerprintSHA256 != "fingerprint" {
		t.Fatalf("결속 값이 전달되지 않음: %+v", o.Expected)
	}
	if !o.PromptProjection || !o.ResetBoundRemotely || o.Acquire == nil || o.AuthorizeIntent == nil || o.CheckAuthority == nil {
		t.Fatal("원격 조립에 필요한 옵션이 빠짐")
	}
	if asked.Load() < 2 {
		t.Fatal("기동 중 실패 뒤 다시 묻지 않음")
	}
}

func TestNewE2RemoteRuntimeGivesUpAtTheLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	defer server.Close()
	if _, err := newE2RemoteRuntime(context.Background(), E2RemoteEngineOptions{URL: server.URL, TokenFile: writeE2RemoteToken(t, e2RemoteTestToken), RequestTimeout: 100 * time.Millisecond, DescribeTimeout: 300 * time.Millisecond}); err == nil {
		t.Fatal("eval-e2 없이 엔진이 시작됨")
	}
	if _, err := newE2RemoteRuntime(context.Background(), E2RemoteEngineOptions{URL: server.URL, TokenFile: writeE2RemoteToken(t, e2RemoteTestToken), RequestTimeout: time.Second}); err == nil {
		t.Fatal("기동 시한 없는 설정 수용")
	}
}

func TestE2HTTPRuntimeResetMatching(t *testing.T) {
	local := &E2HTTPRuntime{options: E2HTTPRuntimeOptions{Expected: E2HTTPExpectedBinding{ResetRef: "reset-1"}}}
	if !local.resetMatches("reset-1") || local.resetMatches("reset-2") || local.resetMatches("") {
		t.Fatal("로컬 실행의 reset 대조가 틀림")
	}
	remote := &E2HTTPRuntime{options: E2HTTPRuntimeOptions{Expected: E2HTTPExpectedBinding{ResetRef: e2RemoteResetPlaceholder}, ResetBoundRemotely: true}}
	if !remote.resetMatches(strings64a) || remote.resetMatches("") {
		t.Fatal("원격 실행의 reset 대조가 틀림")
	}
}

const strings64a = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
