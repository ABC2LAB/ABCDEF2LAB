//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e3contract"
)

// 회차 상태(Status)에 붙는 확정 단계 상태는 판정 목록의 policy_source와 같은 판독기에서 온다. 같은 회차·같은 결과 파일이면
// 두 응답의 상태·사유가 늘 같다. 결과 파일이 없으면 absent(꺼짐으로 추정하지 않음), 못 읽으면 unreadable이다.
func TestProductStatusConfirmationMatchesPolicySource(t *testing.T) {
	runs := policySourceRuns(t)
	path := filepath.Join(runs, liveJob, productE3ConfirmationFile)
	version := e3contract.RunResultVersion
	status := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "status", Principal: "ARTEX", Command: diagnosisprep.Status, JobID: liveJob}
	source := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "verdict-policy-source", Principal: "ARTEX",
		Command: diagnosisprep.ProductControl, Product: &diagnosisprep.ProductRequest{Operation: "policy_source", JobID: liveJob}}
	for name, tc := range map[string]struct {
		raw  string
		want diagnosisprep.ProductConfirmationState
	}{
		"absent":      {"", diagnosisprep.ProductConfirmationState{}},
		"completed":   {`{"version":"` + version + `","status":"completed","verdicts":[]}`, diagnosisprep.ProductConfirmationState{Present: true, Status: "completed"}},
		"disabled":    {`{"version":"` + version + `","status":"disabled","reason":"test_accounts_unavailable","verdicts":[]}`, diagnosisprep.ProductConfirmationState{Present: true, Status: "disabled", Reason: "test_accounts_unavailable"}},
		"refused":     {`{"version":"` + version + `","status":"start_refused","reason":"confirmation_share_unavailable","verdicts":[]}`, diagnosisprep.ProductConfirmationState{Present: true, Status: "start_refused", Reason: "confirmation_share_unavailable"}},
		"interrupted": {`{"version":"` + version + `","status":"interrupted","reason":"run_deadline","verdicts":[]}`, diagnosisprep.ProductConfirmationState{Present: true, Status: "interrupted", Reason: "run_deadline"}},
		"integrity":   {`{"version":"` + version + `","status":"confirmation_integrity_failed","verdicts":[]}`, diagnosisprep.ProductConfirmationState{Present: true, Status: "confirmation_integrity_failed"}},
		"unreadable":  {`{"version":"other/v9","status":"completed"}`, diagnosisprep.ProductConfirmationState{Present: true, Status: diagnosisprep.ConfirmationUnreadable}},
	} {
		t.Run(name, func(t *testing.T) {
			_ = os.Remove(path)
			if tc.raw != "" && os.WriteFile(path, []byte(tc.raw), 0o600) != nil {
				t.Fatal("결과 파일 쓰기")
			}
			base := &policySourceBase{job: &diagnosisprep.Job{ID: liveJob}}
			endpoint := productPreparationEndpoint{base: base, product: &productRuntime{config: urlPreparationInstallation{RunsDir: runs}}}
			got, err := endpoint.Call(t.Context(), status)
			if err != nil || got.Confirmation == nil || !reflect.DeepEqual(*got.Confirmation, tc.want) {
				t.Fatalf("상태 응답 확정 단계: %+v %v", got.Confirmation, err)
			}
			list, err := endpoint.Call(t.Context(), source)
			if err != nil || list.Product == nil || list.Product.Confirmation == nil || !reflect.DeepEqual(*list.Product.Confirmation, *got.Confirmation) {
				t.Fatalf("판정 목록과 다름: %+v", list.Product)
			}
			if base.calls[0].Command != diagnosisprep.Status || base.calls[0].Principal != "ARTEX" || base.calls[0].JobID != liveJob {
				t.Fatalf("회차 소유 확인을 건너뜀: %+v", base.calls)
			}
		})
	}
}

// 확정 단계 상태는 운영자의 자기 회차 Status 성공 응답에만 붙는다. 다른 주체·오류·다른 회차·기본 서비스 장애에는 없다.
func TestProductStatusConfirmationOnlyOnOwnedStatus(t *testing.T) {
	runs := policySourceRuns(t)
	if os.WriteFile(filepath.Join(runs, liveJob, productE3ConfirmationFile), []byte(`{"version":"`+e3contract.RunResultVersion+`","status":"completed"}`), 0o600) != nil {
		t.Fatal("결과 파일 쓰기")
	}
	request := func(principal string) diagnosisprep.Request {
		return diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "status", Principal: principal, Command: diagnosisprep.Status, JobID: liveJob}
	}
	for name, tc := range map[string]struct {
		base      *policySourceBase
		principal string
	}{
		"other principal": {&policySourceBase{job: &diagnosisprep.Job{ID: liveJob}}, "someone-else"},
		"not found":       {&policySourceBase{error: "not_found"}, "ARTEX"},
		"other job":       {&policySourceBase{job: &diagnosisprep.Job{ID: "job-other"}}, "ARTEX"},
		"service down":    {&policySourceBase{err: errors.New("closed")}, "ARTEX"},
	} {
		t.Run(name, func(t *testing.T) {
			endpoint := productPreparationEndpoint{base: tc.base, product: &productRuntime{config: urlPreparationInstallation{RunsDir: runs}}}
			got, _ := endpoint.Call(t.Context(), request(tc.principal))
			if got.Confirmation != nil {
				t.Fatalf("확정 단계 상태가 붙음: %+v", got.Confirmation)
			}
		})
	}
}
