//go:build linux

package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
)

// helper 프로세스 없이도 실제 factory가 E② 전송 지점까지 상위 위임 검사를 넘기는지 확인한다.
func TestE2HTTPFactoryForwardsParentAuthorityToTransmission(t *testing.T) {
	var hits atomic.Int64
	var revoked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); io.WriteString(w, "owned") }))
	defer target.Close()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.Rules[0].Origin = target.URL
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0600) != nil {
		t.Fatal("합성 정책 준비 실패")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	options, err := LoadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{PolicyFile: cfg.PolicyFile,
		Authority: &e2HTTPPolicyAuthorityFixture{ctx: ctx}, Supervisor: &e2HTTPSupervisorFixture{},
		CheckAuthority: func() error {
			if revoked.Load() {
				return errors.New("private parent revoked")
			}
			return nil
		},
	})
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
	call, err := lease.Execution.BeginCall(ctx, "parent-authority-regression")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		_, _ = call.Close(cleanup)
	})
	request, _ := http.NewRequest("GET", target.URL+"/allowed", nil)
	response, receipt, err := call.Do(ctx, request)
	if err != nil || response == nil || hits.Load() != 1 || !receipt.ResponseComplete {
		t.Fatalf("양성 수신=%d 결과=%+v 오류=%v", hits.Load(), receipt, err)
	}
	response.Body.Close()
	revoked.Store(true)
	response, receipt, err = call.Do(ctx, request)
	if err == nil || response != nil || !receipt.NotSent || receipt.Reason != e2proxy.Revoked || hits.Load() != 1 {
		t.Fatalf("철회 후 수신=%d 결과=%+v 오류=%v", hits.Load(), receipt, err)
	}
	if options.CheckAuthority(ctx) == nil {
		t.Fatal("호스트 권위 검사에서 상위 철회를 무시함")
	}
}
