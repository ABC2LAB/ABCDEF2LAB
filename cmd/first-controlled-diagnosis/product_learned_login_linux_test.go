package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/learnedlogin"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/server"
)

// learnedAccountTarget은 승인 대상에 시험 계정 둘(자격 있음)을 둔 레지스트리다.
func learnedAccountTarget(t *testing.T) (*productTargetRegistry, string, productTargetRecord) {
	t.Helper()
	r, root, target := accountTestTarget(t, true)
	for i, label := range []string{"tester-a", "tester-b"} {
		if _, err := r.registerAccount("ARTEX", target.ID, label); err != nil {
			t.Fatal(err)
		}
		if _, err := r.markTestAccount("ARTEX", target.ID, label, true); err != nil {
			t.Fatal(err)
		}
		cred := diagnosisprep.AccountCredential{Username: "learned-user-" + string(rune('a'+i)), Password: "learned-pass-" + string(rune('a'+i))}
		if _, err := r.setAccountCredential("ARTEX", target.ID, label, cred); err != nil {
			t.Fatal(err)
		}
	}
	r.mu.Lock()
	rec := r.records[target.ID]
	r.mu.Unlock()
	return r, root, rec
}

func credentialOf(r *productTargetRegistry, targetID, label string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.accounts[targetID][label].credentialSHA256
}

func learnedMethod(path string) learnedlogin.Method {
	return learnedlogin.Method{Method: "POST", Path: path, Format: learnedlogin.FormatForm, IdentifierField: "userName", PasswordField: "password",
		OtherFields: []string{"_csrf"}, FieldsFromResponse: []string{"_csrf"}, SessionSource: learnedlogin.SourceSetCookie,
		SessionNames: []string{"sid"}, SessionSentAs: learnedlogin.SentAsCookie}
}

func learnedAt(path, run string, at time.Time) learnedlogin.Learned {
	return learnedlogin.Learned{Method: learnedMethod(path), Source: learnedlogin.Source{RunRef: run, AttemptRef: "diagnosis-1", At: at}}
}

// verifiedOf는 계정의 지금 verified 줄이 그 방법(path)과 지금 대상 판·자격 판에 맞는지다(방법이 지금 것이 된 시각 since 뒤).
func verifiedOf(r *productTargetRegistry, id, label, path string, since ...time.Time) *productLoginVerified {
	r.mu.Lock()
	defer r.mu.Unlock()
	var from time.Time
	if len(since) > 0 {
		from = since[0]
	}
	return loginMethodVerifiedFor(r.records[id], r.accounts[id][label], learnedMethod(path).Digest(), from)
}

// verified 줄(계약 §6-1-2)은 결속한 대상 판·자격 판이 지금도 같을 때만 쓰고, 같은 방법·판이면 다시 쓰지 않으며, 더 앞선
// 시각의 결과로 되돌리지 않는다. 상태 반영은 지금 배운 방법의 해시·대상 판·자격 판이 모두 같을 때만이다. 재시작(저널 재생)
// 뒤에도 같다. 자격 변경·표시 해제·대상 철회 뒤에는 반영하지 않는다.
func TestLoginMethodVerifiedJournal(t *testing.T) {
	r, root, rec := learnedAccountTarget(t)
	id, rev := rec.Target.ID, rec.Target.Revision
	credA := credentialOf(r, id, "tester-a")
	digest := learnedMethod("/login").Digest()
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	if ok, _ := r.recordLoginMethodVerified(id, "tester-a", rev, strings.Repeat("0", 64), digest, "run-job1", "confirmation-3", t0); ok {
		t.Fatal("다른 자격 판의 결과를 남김")
	}
	if ok, _ := r.recordLoginMethodVerified(id, "tester-a", rev+1, credA, digest, "run-job1", "confirmation-3", t0); ok {
		t.Fatal("다른 대상 판의 결과를 남김")
	}
	if _, err := r.recordLoginMethodVerified(id, "tester-a", rev, credA, digest, "run-job1", "diagnosis-3", t0); err == nil {
		t.Fatal("확정 단계가 아닌 시도 참조를 받음")
	}
	if ok, err := r.recordLoginMethodVerified(id, "tester-a", rev, credA, digest, "run-job1", "confirmation-3", t0); !ok || err != nil {
		t.Fatalf("verified 저장 %v %v", ok, err)
	}
	lines := accountJournalLines(t, root)
	if ok, _ := r.recordLoginMethodVerified(id, "tester-a", rev, credA, digest, "run-job2", "confirmation-4", t0.Add(time.Hour)); ok || accountJournalLines(t, root) != lines {
		t.Fatal("같은 방법·판의 verified를 다시 씀")
	}
	if ok, _ := r.recordLoginMethodVerified(id, "tester-a", rev, credA, digest, "run-job0", "confirmation-1", t0.Add(-time.Hour)); ok {
		t.Fatal("같은 방법의 더 앞선 결과가 verified를 덮음")
	}
	// 다른 방법(A)의 더 늦은 결과는 따로 남고 지금 방법(/login)의 검증을 지우지 않는다(Codex 리뷰).
	if ok, _ := r.recordLoginMethodVerified(id, "tester-a", rev, credA, learnedMethod("/old").Digest(), "run-job3", "confirmation-1", t0.Add(30*time.Minute)); !ok {
		t.Fatal("다른 방법의 verified를 남기지 못함")
	}
	if verifiedOf(r, id, "tester-a", "/login") == nil || verifiedOf(r, id, "tester-a", "/old") == nil {
		t.Fatal("방법별 verified가 서로를 덮음")
	}
	// 방법이 지금 것이 된 시각보다 앞선 검증은 반영하지 않는다(A 검증 → B 배움 → A 다시 배움에서 옛 A 검증이 다시 보이지 않음).
	if verifiedOf(r, id, "tester-a", "/old", t0.Add(time.Hour)) != nil {
		t.Fatal("방법이 바뀌기 전 검증이 다시 보임")
	}
	if v := verifiedOf(r, id, "tester-a", "/login"); v == nil || v.runRef != "run-job1" || v.attemptRef != "confirmation-3" || !v.at.Equal(t0) {
		t.Fatalf("verified 반영 %+v", v)
	}
	if verifiedOf(r, id, "tester-a", "/other") != nil {
		t.Fatal("다른 방법(learned 해시)에 verified가 반영됨")
	}
	if verifiedOf(r, id, "tester-b", "/login") != nil {
		t.Fatal("다른 계정에 섞임")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if v := verifiedOf(reopened, id, "tester-a", "/login"); v == nil || v.runRef != "run-job1" {
		t.Fatalf("재시작 뒤 %+v", v)
	}
	// 자격 변경 → 반영 안 함(줄은 남지만 지금 자격 판과 다름).
	if _, err := reopened.setAccountCredential("ARTEX", id, "tester-a", diagnosisprep.AccountCredential{Username: "changed-user", Password: "changed-pass-1"}); err != nil {
		t.Fatal(err)
	}
	if verifiedOf(reopened, id, "tester-a", "/login") != nil {
		t.Fatal("자격 변경 뒤 verified가 남음")
	}
	credA2 := credentialOf(reopened, id, "tester-a")
	if ok, _ := reopened.recordLoginMethodVerified(id, "tester-a", rev, credA2, digest, "run-job5", "confirmation-2", t0.Add(2*time.Hour)); !ok {
		t.Fatal("새 자격 판의 verified를 남기지 못함")
	}
	// 표시 해제(자격 삭제) → 반영 안 함.
	credB := credentialOf(reopened, id, "tester-b")
	if ok, _ := reopened.recordLoginMethodVerified(id, "tester-b", rev, credB, digest, "run-job5", "confirmation-3", t0.Add(2*time.Hour)); !ok {
		t.Fatal("B verified")
	}
	if _, err := reopened.unmarkAccount("ARTEX", id, "tester-b"); err != nil {
		t.Fatal(err)
	}
	if verifiedOf(reopened, id, "tester-b", "/login") != nil {
		t.Fatal("해제 뒤 verified가 남음")
	}
	// 대상 철회(판 변경) → 반영 안 함.
	if _, err := reopened.revoke("ARTEX", id); err != nil {
		t.Fatal(err)
	}
	if verifiedOf(reopened, id, "tester-a", "/login") != nil {
		t.Fatal("철회 뒤 verified가 남음")
	}
}

// login_method_verified 줄은 다시 열 때 검증한다. 계약 모양이 아니거나, 그때 자격 판과 다르거나, 더 앞선 판·시각으로 되돌리거나,
// 다른 동작 줄에 verified 칸이 붙으면 저널 손상으로 본다.
func TestLoginMethodVerifiedReplayRejectsForged(t *testing.T) {
	for name, forge := range map[string]func(*productAccountEvent){
		"stage":         func(e *productAccountEvent) { e.Stage = "diagnosis" },
		"evidence":      func(e *productAccountEvent) { e.Evidence = "observed_contrast" },
		"attempt":       func(e *productAccountEvent) { e.AttemptRef = "diagnosis-1" },
		"method":        func(e *productAccountEvent) { e.MethodSHA256 = "nothex" },
		"credential":    func(e *productAccountEvent) { e.CredentialSHA256 = strings.Repeat("1", 64) },
		"run":           func(e *productAccountEvent) { e.RunRef = "../run" },
		"statement":     func(e *productAccountEvent) { e.StatementSHA256 = strings.Repeat("0", 64) },
		"unknown label": func(e *productAccountEvent) { e.Label = "tester-z" },
		"fields on mark": func(e *productAccountEvent) {
			e.Action, e.StatementSHA256 = accountMarkTest, productAccountStatementSHA256()
		},
		"back in time":      func(e *productAccountEvent) { e.At = e.At.Add(-time.Hour) },
		"future revision":   func(e *productAccountEvent) { e.TargetRevision++ },
		"older revision":    func(e *productAccountEvent) { e.TargetRevision-- },
		"job on verified":   func(e *productAccountEvent) { e.JobRef = "job-1" },
		"login on verified": func(e *productAccountEvent) { e.Login = &diagnosisprep.TargetLogin{} },
	} {
		t.Run(name, func(t *testing.T) {
			r, root, rec := learnedAccountTarget(t)
			cred := credentialOf(r, rec.Target.ID, "tester-a")
			at := time.Now().UTC()
			if ok, err := r.recordLoginMethodVerified(rec.Target.ID, "tester-a", rec.Target.Revision, cred, learnedMethod("/login").Digest(), "run-job1", "confirmation-2", at); !ok || err != nil {
				t.Fatal("정상 verified 준비", err)
			}
			e := productAccountEvent{TargetID: rec.Target.ID, Label: "tester-a", Action: accountLoginMethodVerified, Actor: "ARTEX", At: at.Add(time.Minute),
				TargetRevision: rec.Target.Revision, CredentialSHA256: cred, MethodSHA256: learnedMethod("/login").Digest(), RunRef: "run-job2",
				Stage: productLoginVerifiedStage, AttemptRef: "confirmation-3", Evidence: productLoginVerifiedEvidence}
			forge(&e)
			raw, _ := json.Marshal(e)
			if err := r.accountJournal.Append(raw); err != nil {
				t.Fatal(err)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil }); err == nil {
				reopened.Close()
				t.Fatal("위조한 login_method_verified 줄을 재생함")
			}
		})
	}
}

// 여러 회차가 동시에 verified를 남겨도 저널이 깨지지 않는다(다시 열림).
func TestLoginMethodVerifiedConcurrentRecords(t *testing.T) {
	r, root, rec := learnedAccountTarget(t)
	id, rev := rec.Target.ID, rec.Target.Revision
	cred := credentialOf(r, id, "tester-a")
	base := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := "/login"
			if i%2 == 1 {
				path = "/session"
			}
			_, _ = r.recordLoginMethodVerified(id, "tester-a", rev, cred, learnedMethod(path).Digest(), "run-c"+strconv.Itoa(i), "confirmation-"+strconv.Itoa(i+1),
				base.Add(time.Duration(i)*time.Minute))
		}(i)
	}
	wg.Wait()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatalf("동시 기록 뒤 저널 재생 실패: %v", err)
	}
	defer reopened.Close()
	if verifiedOf(reopened, id, "tester-a", "/login") == nil && verifiedOf(reopened, id, "tester-a", "/session") == nil {
		t.Fatal("동시 기록 뒤 verified가 없음")
	}
}

// fakeStoredMethods는 ⑤-B가 구현할 저장된 방법 읽기 경계를 흉내 낸다.
type fakeStoredMethods map[string]learnedlogin.Learned

func (f fakeStoredMethods) learnedLoginMethod(_ string, label string, _ uint64, _ string) (learnedlogin.Learned, learnedlogin.Reason) {
	m, ok := f[label]
	if !ok {
		return learnedlogin.Learned{}, learnedlogin.ReasonNone
	}
	return m, ""
}

// fakeLearnedAccounts는 서버 세션 저장소 대신 학습 로그인 재사용·대조·결속 참조·로그인 설정 다시 로그인을 흉내 낸다.
type fakeLearnedAccounts struct {
	mu        sync.Mutex
	requests  []server.E2HTTPLearnedLoginRequest
	result    server.E2HTTPLearnedLoginResult
	contrast  server.E2HTTPLoginContrastResult
	contrasts []string
	legacy    []string
	refs      map[string]string
}

func (f *fakeLearnedAccounts) LearnedRelogin(_ context.Context, req server.E2HTTPLearnedLoginRequest) server.E2HTTPLearnedLoginResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	out := f.result
	out.BindingRef = f.refs[req.Label]
	out.Identifier = "learned-user-" + req.Label
	return out
}
func (f *fakeLearnedAccounts) LoginContrast(_ context.Context, path string) server.E2HTTPLoginContrastResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contrasts = append(f.contrasts, path)
	return f.contrast
}
func (f *fakeLearnedAccounts) AccountBindingRef(_, label string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refs[label], f.refs[label] != ""
}
func (f *fakeLearnedAccounts) BoundSession(string, string, string) (http.Header, error) {
	return nil, server.ErrE2HTTPAccountBinding
}
func (f *fakeLearnedAccounts) Relogin(_ context.Context, label string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.legacy = append(f.legacy, label)
	return server.E2HTTPAuthAuthenticated
}

func authenticatedResult() server.E2HTTPLearnedLoginResult {
	return server.E2HTTPLearnedLoginResult{Result: server.E2HTTPAuthAuthenticated, Requests: 2, Transport: learnedlogin.TransportSent,
		TransportRef: "call_x/attempt_y", Status: 302, At: time.Now().UTC(), LoginPaths: []string{"/login"}}
}

// learnedRunDir은 회차 상태 디렉터리에 로그인 시도 기록(rows)을 0600으로 둔다.
func learnedRunDir(t *testing.T, rows []learnedlogin.Row) (string, string) {
	t.Helper()
	runs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runs, "job1", "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{runs, filepath.Join(runs, "job1"), filepath.Join(runs, "job1", "state")} {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if rows != nil {
		var b strings.Builder
		for _, row := range rows {
			raw, _ := json.Marshal(row)
			b.Write(raw)
			b.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(runs, "job1", "state", learnedlogin.AttemptsFile), []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return runs, "job1"
}

func modelSucceededRow(ref, label, path string) learnedlogin.Row {
	return learnedlogin.Row{Schema: learnedlogin.RowSchema, AttemptRef: ref, RunRef: "run-job1", Stage: learnedlogin.StageDiagnosis,
		Actor: learnedlogin.ActorModel, TransportRef: "call_m/attempt_1", At: "2026-11-26T10:00:00Z", AccountLabel: label,
		Identifier: "learned-user-" + label, PasswordRegistered: true, Method: "POST", Path: path, QueryKeys: []string{}, Format: learnedlogin.FormatForm,
		IdentifierField: "userName", PasswordField: "password", OtherFields: []string{"_csrf"}, FieldsFromResponse: []string{"_csrf"},
		Status: 302, Transport: learnedlogin.TransportSent, SessionSource: learnedlogin.SourceSetCookie, SessionNames: []string{"sid"},
		SessionSeenAnonymous: []string{}, SessionSentAs: learnedlogin.SentAsCookie, Followup: learnedlogin.FollowupContrast, Outcome: learnedlogin.OutcomeSucceeded}
}

func readLearnedRows(t *testing.T, runs string) []learnedlogin.Row {
	t.Helper()
	rows, _, err := learnedlogin.ReadRows(filepath.Join(runs, "job1", "state"))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func identityObs(label, digest string) e3contract.Observation {
	return e3contract.Observation{Purpose: e3contract.PurposeIdentity, AccountLabel: label, Sent: true, ResponseComplete: true, StatusCode: 200,
		Normal: oracle.StateYes, SubjectDigest: digest, Ref: e3contract.CallRef{CallRef: "call_" + label, AttemptID: "attempt_" + label}}
}

// acceptedOf는 확정 단계가 신원 근거로 받아들인 관찰 집합이다.
func acceptedOf(obs map[string]e3contract.Observation, except ...string) map[productEvidenceKey]bool {
	out := map[productEvidenceKey]bool{}
	for label, o := range obs {
		if !slices.Contains(except, label) {
			out[evidenceKey(o.Ref)] = true
		}
	}
	return out
}

// 이 회차 적격 성공이 먼저이고, 없으면 저장된 방법, 둘 다 없으면 로그인 설정 다시 로그인이다. 배운 방법이 실패해도 로그인
// 설정으로 대신하지 않는다. 대조 요청은 회차에 한 번이다.
func TestLearnedLoginReloginPriority(t *testing.T) {
	r, _, rec := learnedAccountTarget(t)
	id := rec.Target.ID
	creds := map[string]string{"tester-a": credentialOf(r, id, "tester-a"), "tester-b": credentialOf(r, id, "tester-b")}
	runs, job := learnedRunDir(t, []learnedlogin.Row{modelSucceededRow("diagnosis-1", "tester-a", "/current")})
	ll := auditedLearnedLogin(r, runs, job, rec, "run-job1", creds, "/api/me")
	if ll.stored != nil {
		t.Fatal("learned 저장(⑤-B)이 없는데 저장 방법 읽기가 연결됨")
	}
	// ⑤-B가 레지스트리에 구현할 저장 방법 읽기를 흉내 낸다. A는 이 회차 성공이 먼저다.
	stored := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	ll.stored = fakeStoredMethods{"tester-a": learnedAt("/stored", "run-job0", stored), "tester-b": learnedAt("/stored", "run-job0", stored)}
	fake := &fakeLearnedAccounts{result: authenticatedResult(), contrast: server.E2HTTPLoginContrastResult{Sent: true, Status: 401}, refs: map[string]string{"tester-a": "acct-1", "tester-b": "acct-2"}}
	if n := ll.relogin(t.Context(), "tester-a", fake, fake); n != 3 {
		t.Fatalf("A 요청 수 %d(대조 1 + 로그인 2)", n)
	}
	if n := ll.relogin(t.Context(), "tester-b", fake, fake); n != 2 {
		t.Fatalf("B 요청 수 %d(대조는 회차에 한 번)", n)
	}
	if len(fake.requests) != 2 || fake.requests[0].Method.Path != "/current" || fake.requests[1].Method.Path != "/stored" || len(fake.legacy) != 0 || len(fake.contrasts) != 1 || fake.contrasts[0] != "/api/me" {
		t.Fatalf("우선순위: %+v legacy=%v contrasts=%v", fake.requests, fake.legacy, fake.contrasts)
	}
	if fake.requests[0].MaxFailures != productLearnedLoginMaxFailures {
		t.Fatalf("연속 실패 상한 %d", fake.requests[0].MaxFailures)
	}

	// 배운 방법이 없는 계정은 로그인 설정으로 다시 로그인한다.
	r2, _, rec2 := learnedAccountTarget(t)
	creds2 := map[string]string{"tester-a": credentialOf(r2, rec2.Target.ID, "tester-a")}
	runs2, job2 := learnedRunDir(t, nil)
	ll2 := auditedLearnedLogin(r2, runs2, job2, rec2, "run-job1", creds2, "/api/me")
	fake2 := &fakeLearnedAccounts{result: authenticatedResult()}
	if n := ll2.relogin(t.Context(), "tester-a", fake2, fake2); n != 1 || len(fake2.legacy) != 1 || len(fake2.requests) != 0 {
		t.Fatalf("배운 방법 없음: n=%d legacy=%v learned=%v", n, fake2.legacy, fake2.requests)
	}

	// 배운 방법이 실패해도 로그인 설정으로 대신하지 않는다.
	runs3, job3 := learnedRunDir(t, []learnedlogin.Row{modelSucceededRow("diagnosis-1", "tester-a", "/current")})
	ll3 := auditedLearnedLogin(r2, runs3, job3, rec2, "run-job1", creds2, "/api/me")
	fake3 := &fakeLearnedAccounts{result: server.E2HTTPLearnedLoginResult{Result: server.E2HTTPAuthLoginFailed, Reason: learnedlogin.ReasonRejected,
		Requests: 1, Transport: learnedlogin.TransportSent, TransportRef: "call_a/attempt_b", Status: 401}}
	if n := ll3.relogin(t.Context(), "tester-a", fake3, fake3); n != 1 || len(fake3.legacy) != 0 {
		t.Fatalf("실패를 로그인 설정으로 숨김: n=%d legacy=%v", n, fake3.legacy)
	}
	rows := readLearnedRows(t, runs3)
	last := rows[len(rows)-1]
	if last.Actor != learnedlogin.ActorProgram || last.Stage != learnedlogin.StageConfirmation || last.Outcome != learnedlogin.OutcomeFailed ||
		last.AttemptRef != "confirmation-2" || last.ConsecutiveFailures != 1 || last.TransportRef != "call_a/attempt_b" {
		t.Fatalf("프로그램 로그인 행 %+v", last)
	}
}

// 연속 실패 수(§5)는 회차 기록에서 세어 서버에 넘긴다. 프로그램 시도 실패도 다음 시도에 더한다.
func TestLearnedLoginPassesPriorFailures(t *testing.T) {
	r, _, rec := learnedAccountTarget(t)
	creds := map[string]string{"tester-a": credentialOf(r, rec.Target.ID, "tester-a")}
	failed := func(ref string) learnedlogin.Row {
		row := modelSucceededRow(ref, "tester-a", "/login")
		row.Outcome, row.Followup = learnedlogin.OutcomeFailed, learnedlogin.FollowupNone
		return row
	}
	runs, job := learnedRunDir(t, []learnedlogin.Row{modelSucceededRow("diagnosis-1", "tester-a", "/login"), failed("diagnosis-2"), failed("diagnosis-3")})
	ll := auditedLearnedLogin(r, runs, job, rec, "run-job1", creds, "")
	fake := &fakeLearnedAccounts{result: server.E2HTTPLearnedLoginResult{Result: server.E2HTTPAuthNotSent, Reason: learnedlogin.ReasonAttemptsExhausted, Transport: learnedlogin.TransportNotSent}}
	ll.relogin(t.Context(), "tester-a", fake, nil)
	if got := fake.requests[0].PriorFailures["learned-user-tester-a"]; got != 2 {
		t.Fatalf("이전 연속 실패 %d", got)
	}
	rows := readLearnedRows(t, runs)
	if last := rows[len(rows)-1]; last.Outcome != learnedlogin.OutcomeNotSent || last.Refusal != string(learnedlogin.ReasonAttemptsExhausted) {
		t.Fatalf("미전송 행 %+v", last)
	}
}

func TestLearnedLoginPreservesPriorBlockAfterDelayedSuccess(t *testing.T) {
	r, _, rec := learnedAccountTarget(t)
	const label = "tester-a"
	creds := map[string]string{label: credentialOf(r, rec.Target.ID, label)}
	succeeded := modelSucceededRow("diagnosis-1", label, "/login")
	pending := succeeded
	pending.Outcome, pending.Followup = learnedlogin.OutcomeUnconfirmed, learnedlogin.FollowupNone
	rows := []learnedlogin.Row{pending}
	for _, ref := range []string{"diagnosis-2", "diagnosis-3", "diagnosis-4"} {
		failed := modelSucceededRow(ref, label, "/login")
		failed.Outcome, failed.Followup = learnedlogin.OutcomeFailed, learnedlogin.FollowupNone
		rows = append(rows, failed)
	}
	rows = append(rows, succeeded)
	runs, job := learnedRunDir(t, rows)
	ll := auditedLearnedLogin(r, runs, job, rec, "run-job1", creds, "")
	fake := &fakeLearnedAccounts{result: server.E2HTTPLearnedLoginResult{
		Identifier: succeeded.Identifier, Result: server.E2HTTPAuthNotSent, Reason: learnedlogin.ReasonAttemptsExhausted,
		Transport: learnedlogin.TransportNotSent,
	}}
	if sent := ll.relogin(t.Context(), label, fake, nil); sent != 0 || len(fake.requests) != 1 {
		t.Fatalf("전송 %d건, 차단 확인 요청 %d건", sent, len(fake.requests))
	}
	if got := fake.requests[0].PriorFailures[succeeded.Identifier]; got < productLearnedLoginMaxFailures {
		t.Fatalf("이미 적용된 차단을 잃음: %d", got)
	}
	stored := readLearnedRows(t, runs)
	if last := stored[len(stored)-1]; last.ConsecutiveFailures != 0 || last.Refusal != string(learnedlogin.ReasonAttemptsExhausted) {
		t.Fatalf("기록 집계 %d, 차단 사유 %s", last.ConsecutiveFailures, last.Refusal)
	}
}

// verified는 세션 없는 대조가 막힘이고, 그 세션으로 연 신원 응답이 열림이며, 두 계정 신원이 다르고, 다시 로그인 뒤 세션이
// 바뀌지 않았을 때만이다. 하나라도 아니면 learned로 남는다(성공 근거 부족). 200·쿠키·모델 성공 기록만으로는 verified가 아니다.
func TestLearnedLoginVerifiedRequiresContrast(t *testing.T) {
	for name, tc := range map[string]struct {
		contrast server.E2HTTPLoginContrastResult
		identity map[string]e3contract.Observation
		rebound  bool
		verified bool
	}{
		"verified":                  {server.E2HTTPLoginContrastResult{Sent: true, Status: 401}, map[string]e3contract.Observation{"tester-a": identityObs("tester-a", strings.Repeat("a", 64)), "tester-b": identityObs("tester-b", strings.Repeat("b", 64))}, false, true},
		"redirect-to-login-blocked": {server.E2HTTPLoginContrastResult{Sent: true, Status: 302, RedirectPath: "/login"}, map[string]e3contract.Observation{"tester-a": identityObs("tester-a", strings.Repeat("a", 64)), "tester-b": identityObs("tester-b", strings.Repeat("b", 64))}, false, true},
		"public-identity":           {server.E2HTTPLoginContrastResult{Sent: true, Status: 200}, map[string]e3contract.Observation{"tester-a": identityObs("tester-a", strings.Repeat("a", 64)), "tester-b": identityObs("tester-b", strings.Repeat("b", 64))}, false, false},
		"contrast-not-sent":         {server.E2HTTPLoginContrastResult{Reason: learnedlogin.ReasonBudgetExhausted}, map[string]e3contract.Observation{"tester-a": identityObs("tester-a", strings.Repeat("a", 64)), "tester-b": identityObs("tester-b", strings.Repeat("b", 64))}, false, false},
		"same-subject":              {server.E2HTTPLoginContrastResult{Sent: true, Status: 401}, map[string]e3contract.Observation{"tester-a": identityObs("tester-a", strings.Repeat("a", 64)), "tester-b": identityObs("tester-b", strings.Repeat("a", 64))}, false, false},
		"identity-not-normal": {server.E2HTTPLoginContrastResult{Sent: true, Status: 401}, map[string]e3contract.Observation{"tester-a": func() e3contract.Observation {
			o := identityObs("tester-a", "")
			o.Normal, o.StatusCode = oracle.StateNo, 401
			return o
		}(), "tester-b": identityObs("tester-b", strings.Repeat("b", 64))}, false, false},
		// 상대 계정 신원 관찰이 없으면 A 값이 하나뿐이어도 두 신원이 다르다는 근거가 아니다(Codex 리뷰 지적).
		"other-identity-missing": {server.E2HTTPLoginContrastResult{Sent: true, Status: 401}, map[string]e3contract.Observation{"tester-a": identityObs("tester-a", strings.Repeat("a", 64))}, false, false},
		"other-identity-not-normal": {server.E2HTTPLoginContrastResult{Sent: true, Status: 401}, map[string]e3contract.Observation{"tester-a": identityObs("tester-a", strings.Repeat("a", 64)), "tester-b": func() e3contract.Observation {
			o := identityObs("tester-b", strings.Repeat("b", 64))
			o.Normal = oracle.StateNo
			return o
		}()}, false, false},
		// 확정 단계가 completed로 끝나지 않으면(진위 실패·중단) 봉인 대조 전 관찰로 성공을 세우지 않는다(Codex 2차 리뷰).
		"e3-not-completed": {contrast: server.E2HTTPLoginContrastResult{Sent: true, Status: 401}, identity: map[string]e3contract.Observation{"tester-a": identityObs("tester-a", strings.Repeat("a", 64)), "tester-b": identityObs("tester-b", strings.Repeat("b", 64))}, verified: false},
		// 신원 응답이 3xx면(리다이렉트 목적지를 모름) 열림으로 보지 않는다.
		"identity-redirect": {contrast: server.E2HTTPLoginContrastResult{Sent: true, Status: 401}, identity: map[string]e3contract.Observation{"tester-a": func() e3contract.Observation {
			o := identityObs("tester-a", strings.Repeat("a", 64))
			o.StatusCode = 302
			return o
		}(), "tester-b": identityObs("tester-b", strings.Repeat("b", 64))}, verified: false},
		// 봉인 조회가 확정 시한에 걸려 E③가 버린 신원 관찰(판정 근거에 없음)로는 성공을 세우지 않는다(Codex 3차 리뷰).
		"identity-not-accepted": {server.E2HTTPLoginContrastResult{Sent: true, Status: 401}, map[string]e3contract.Observation{"tester-a": identityObs("tester-a", strings.Repeat("a", 64)), "tester-b": identityObs("tester-b", strings.Repeat("b", 64))}, false, false},
		"session-replaced":      {server.E2HTTPLoginContrastResult{Sent: true, Status: 401}, map[string]e3contract.Observation{"tester-a": identityObs("tester-a", strings.Repeat("a", 64)), "tester-b": identityObs("tester-b", strings.Repeat("b", 64))}, true, false},
	} {
		t.Run(name, func(t *testing.T) {
			r, _, rec := learnedAccountTarget(t)
			id := rec.Target.ID
			creds := map[string]string{"tester-a": credentialOf(r, id, "tester-a"), "tester-b": credentialOf(r, id, "tester-b")}
			runs, job := learnedRunDir(t, []learnedlogin.Row{modelSucceededRow("diagnosis-1", "tester-a", "/login")})
			ll := auditedLearnedLogin(r, runs, job, rec, "run-job1", creds, "/api/me")
			fake := &fakeLearnedAccounts{result: authenticatedResult(), contrast: tc.contrast, refs: map[string]string{"tester-a": "acct-1"}}
			ll.relogin(t.Context(), "tester-a", fake, fake)
			for label, obs := range tc.identity {
				ll.observeIdentity(label, obs)
			}
			if tc.rebound {
				fake.refs["tester-a"] = "acct-9"
			}
			accepted := acceptedOf(tc.identity)
			switch name {
			case "e3-not-completed":
				accepted = acceptedIdentityEvidence(e3contract.RunResult{Status: e3contract.StatusInterrupted})
			case "identity-not-accepted":
				accepted = acceptedOf(tc.identity, "tester-a")
			}
			ll.finish(func(label string) string { ref, _ := fake.AccountBindingRef("", label); return ref }, accepted)
			if got := verifiedOf(r, id, "tester-a", "/login") != nil; got != tc.verified {
				t.Fatalf("verified=%v, want %v", got, tc.verified)
			}
			rows := readLearnedRows(t, runs)
			last := rows[len(rows)-1]
			wantOutcome := learnedlogin.OutcomeUnconfirmed
			if tc.verified {
				wantOutcome = learnedlogin.OutcomeSucceeded
			}
			if last.Actor != learnedlogin.ActorProgram || last.Outcome != wantOutcome || last.AttemptRef != "confirmation-2" {
				t.Fatalf("마지막 행 %+v", last)
			}
			if wantOutcome == learnedlogin.OutcomeSucceeded && (last.Followup != learnedlogin.FollowupContrast || last.ContrastSource != learnedlogin.ContrastProbe || last.ContrastProbes != 1) {
				t.Fatalf("성공 행 대조 칸 %+v", last)
			}
			// 프로그램 성공 행은 다음 회차 학습 대상이 아니다(모델 시도만 배운다).
			if sel := learnedlogin.Select(rows, "run-job1", []string{"tester-a"}); sel["tester-a"].Learned.Source.AttemptRef != "diagnosis-1" {
				t.Fatalf("프로그램 행을 학습함: %+v", sel)
			}
		})
	}
}

// 회차 결속(자격 판)이 확정 단계 도중 바뀌면 그 회차의 검증은 지금 계정에 남지 않는다(성공 근거가 모두 있어도).
func TestLearnedLoginFinishSkipsStaleBinding(t *testing.T) {
	r, root, rec := learnedAccountTarget(t)
	id := rec.Target.ID
	creds := map[string]string{"tester-a": credentialOf(r, id, "tester-a"), "tester-b": credentialOf(r, id, "tester-b")}
	runs, job := learnedRunDir(t, []learnedlogin.Row{modelSucceededRow("diagnosis-1", "tester-a", "/login")})
	ll := auditedLearnedLogin(r, runs, job, rec, "run-job1", creds, "/api/me")
	fake := &fakeLearnedAccounts{result: authenticatedResult(), contrast: server.E2HTTPLoginContrastResult{Sent: true, Status: 401}, refs: map[string]string{"tester-a": "acct-1"}}
	ll.relogin(t.Context(), "tester-a", fake, fake)
	obs := map[string]e3contract.Observation{"tester-a": identityObs("tester-a", strings.Repeat("a", 64)), "tester-b": identityObs("tester-b", strings.Repeat("b", 64))}
	for label, o := range obs {
		ll.observeIdentity(label, o)
	}
	if _, err := r.setAccountCredential("ARTEX", id, "tester-a", diagnosisprep.AccountCredential{Username: "rotated", Password: "rotated-pass-1"}); err != nil {
		t.Fatal(err)
	}
	lines := accountJournalLines(t, root)
	ll.finish(func(label string) string { ref, _ := fake.AccountBindingRef("", label); return ref }, acceptedOf(obs))
	if accountJournalLines(t, root) != lines || verifiedOf(r, id, "tester-a", "/login") != nil {
		t.Fatal("자격이 바뀐 뒤 옛 회차 검증이 남음")
	}
}

// 회차 기록이 없거나 읽을 수 없으면(권한) 이 회차 방법은 없다. 읽을 수 없는 기록은 record_invalid다.
func TestLearnedLoginUnreadableRecord(t *testing.T) {
	r, _, rec := learnedAccountTarget(t)
	creds := map[string]string{"tester-a": credentialOf(r, rec.Target.ID, "tester-a")}
	runs, job := learnedRunDir(t, []learnedlogin.Row{modelSucceededRow("diagnosis-1", "tester-a", "/login")})
	if err := os.Chmod(filepath.Join(runs, job, "state", learnedlogin.AttemptsFile), 0o644); err != nil {
		t.Fatal(err)
	}
	ll := auditedLearnedLogin(r, runs, job, rec, "run-job1", creds, "")
	if _, _, reason := ll.plan("tester-a"); reason != learnedlogin.ReasonRecordInvalid {
		t.Fatalf("기록 오류가 사유에 남지 않음: %v", reason)
	}
	if len(ll.rows) != 0 {
		t.Fatal("보호 파일이 아닌 기록을 읽음")
	}
	// 보호 파일이 아닌 기록에는 프로그램 행도 덧붙이지 않는다.
	if err := appendLoginAttemptRow(runs, job, learnedlogin.Row{Schema: learnedlogin.RowSchema, AttemptRef: "confirmation-1"}); err == nil {
		t.Fatal("0644 기록 파일에 덧붙임")
	}
}

// 확정 단계 계정 역할: 이 회차 인증 결과가 없어도 쓸 배운 방법이 있으면 확정 단계에 쓴다. 둘 다 없으면 그대로 미인증이다.
func TestUsableLearnedAccounts(t *testing.T) {
	usable := func(label string) bool { return label == "tester-b" }
	owner, cross := usableLearnedAccounts(e3contract.Account{Label: "tester-a"}, e3contract.Account{Label: "tester-b"}, usable)
	if owner.Authenticated || !cross.Authenticated {
		t.Fatalf("역할 %+v %+v", owner, cross)
	}
	owner, _ = usableLearnedAccounts(e3contract.Account{Label: "tester-a", Authenticated: true}, e3contract.Account{}, nil)
	if !owner.Authenticated {
		t.Fatal("이미 인증된 계정을 바꿈")
	}
	// 저장된 방법 읽기(⑤-B)가 붙으면 이 회차 성공이 없는 계정도 쓸 수 있다.
	r, _, rec := learnedAccountTarget(t)
	creds := map[string]string{"tester-a": credentialOf(r, rec.Target.ID, "tester-a"), "tester-b": credentialOf(r, rec.Target.ID, "tester-b")}
	runs, job := learnedRunDir(t, nil)
	ll := auditedLearnedLogin(r, runs, job, rec, "run-job1", creds, "/api/me")
	if ll.usable("tester-a") {
		t.Fatal("배운 방법이 없는데 쓸 수 있다고 봄")
	}
	ll.stored = fakeStoredMethods{"tester-a": learnedAt("/stored", "run-job0", time.Now().UTC())}
	if !ll.usable("tester-a") || ll.usable("tester-b") {
		t.Fatal("저장된 방법 반영")
	}
}
