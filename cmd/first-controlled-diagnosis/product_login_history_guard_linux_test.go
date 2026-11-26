package main

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/learnedlogin"
	"github.com/Autumn-27/artex/server"
)

func TestProductReloginHistoryGuard(t *testing.T) {
	const label = "tester-a"
	const runRef = "run-job1"
	r, _, rec := learnedAccountTarget(t)
	creds := map[string]string{label: credentialOf(r, rec.Target.ID, label)}
	failed := modelSucceededRow("diagnosis-1", label, "/login")
	failed.Outcome = learnedlogin.OutcomeFailed
	failed.ConsecutiveFailures = productLearnedLoginMaxFailures
	unknown := failed
	unknown.Outcome, unknown.Transport, unknown.ConsecutiveFailures = learnedlogin.OutcomeUnknown, learnedlogin.TransportUnknown, 0
	success := modelSucceededRow("diagnosis-2", label, "/login")
	unsupported := success
	unsupported.QueryKeys = []string{"input"}
	foreign := failed
	foreign.RunRef = "run-other"
	for _, tc := range []struct {
		name        string
		rows        []learnedlogin.Row
		file        string
		body        string
		reason      learnedlogin.Reason
		allowLegacy bool
	}{
		{name: "기존 회차 이력 파일 없음", reason: learnedlogin.ReasonNone, allowLegacy: true},
		{name: "보호 파일에서 유효한 이력 0건", file: learnedlogin.AttemptsFile, body: "\n", reason: learnedlogin.ReasonNone, allowLegacy: true},
		{name: "다른 회차 실패 이력", rows: []learnedlogin.Row{foreign}, reason: learnedlogin.ReasonNone, allowLegacy: true},
		{name: "0바이트 이력은 정상 인계로 보지 않음", file: learnedlogin.AttemptsFile, reason: learnedlogin.ReasonRecordInvalid},
		{name: "이력 손상", file: learnedlogin.AttemptsFile, body: "{", reason: learnedlogin.ReasonRecordInvalid},
		{name: "이력 파일 유실 및 실패 표지", file: learnedlogin.AttemptsUnavailableFile, body: "unavailable", reason: learnedlogin.ReasonRecordInvalid},
		{name: "읽을 수 있는 이력 및 실패 표지", rows: []learnedlogin.Row{success}, file: learnedlogin.AttemptsUnavailableFile, reason: learnedlogin.ReasonRecordInvalid},
		{name: "실패 상한의 기존 설정 우회", rows: []learnedlogin.Row{failed}, reason: learnedlogin.ReasonNone},
		{name: "결과 불명 뒤 기존 설정 전송 금지", rows: []learnedlogin.Row{unknown}, reason: learnedlogin.ReasonNone},
		{name: "결과 불명 뒤 학습 로그인 전송 금지", rows: []learnedlogin.Row{unknown, success}},
		{name: "입력 재구성 불가", rows: []learnedlogin.Row{unsupported}, reason: learnedlogin.ReasonInputUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runs, job := learnedRunDir(t, tc.rows)
			if tc.file != "" {
				if err := os.WriteFile(filepath.Join(runs, job, "state", tc.file), []byte(tc.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ll := newProductLearnedLogin(r, runs, job, rec, runRef, creds, "/identity")
			fake := &fakeLearnedAccounts{result: server.E2HTTPLearnedLoginResult{Result: server.E2HTTPAuthAuthenticated}}
			_, _, planReason := ll.plan(label)
			if planReason != tc.reason {
				t.Fatalf("선택 사유 %q, 기대 %q", planReason, tc.reason)
			}
			if tc.allowLegacy {
				if ll.relogin(t.Context(), label, fake, fake) != 1 || len(fake.legacy) != 1 || len(fake.requests) != 0 {
					t.Fatal("정상 이력에서 기존 인증 경로를 잃음")
				}
				return
			}
			if got := ll.relogin(t.Context(), label, fake, fake); got != 0 || len(fake.requests) != 0 || len(fake.legacy) != 0 || len(fake.contrasts) != 0 {
				t.Fatalf("차단해야 할 이력에서 다시 로그인 호출: 전송 %d", got)
			}
		})
	}
}

func TestProductE3WiredReloginRequiresHistoryAndBinding(t *testing.T) {
	for _, name := range []string{"인계 실패 표지", "보호된 실행 결속 없음"} {
		t.Run(name, func(t *testing.T) {
			f := newE3HookFixture(t, true, nil)
			c := f.confirmation(nil, e3HookAccounts(), nil)
			state := filepath.Join(c.runsDir, c.jobID)
			if name == "인계 실패 표지" {
				if err := os.MkdirAll(filepath.Join(state, "state"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(state, "state", learnedlogin.AttemptsUnavailableFile), []byte("unavailable"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(filepath.Join(state, "authority.json")); err != nil {
				t.Fatal(err)
			}
			exec, _ := e3TraceExecution(t)
			legacy := &e3ReloginAccounts{result: server.E2HTTPAuthAuthenticated}
			var created atomic.Int64
			wireE3(c, exec, legacy, countingBridge(&created))
			deps, release, _ := c.wiredDeps("9")
			defer release()
			if deps.Relogin != nil && deps.Relogin(t.Context(), "tester-a") != 0 {
				t.Fatal("이력이나 결속을 확인할 수 없는데 다시 로그인함")
			}
			if len(legacy.labels) != 0 || created.Load() != 0 {
				t.Fatal("기존 로그인 설정이나 전송 브리지를 호출함")
			}
		})
	}
}
