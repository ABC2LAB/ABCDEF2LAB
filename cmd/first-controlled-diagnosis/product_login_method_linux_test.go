package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/learnedlogin"
)

func loginMethodOf(t *testing.T, r *productTargetRegistry, id, label string) *diagnosisprep.ProductLoginMethod {
	t.Helper()
	list, err := r.listAccounts("ARTEX", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.Label == label {
			return a.LoginMethod
		}
	}
	t.Fatalf("계정 %s 없음", label)
	return nil
}

// 배운 로그인 방법(계약 §6-1): 결속한 대상 판·자격 판이 지금도 같을 때만 남기고, 같은 시도는 다시 쓰지 않는다. 계정 목록은
// none → learned(근거·방법) → verified(그 방법이 지금 것이 된 뒤의 검증) 순으로 보이고, 방법이 바뀌면 예전 검증은 버린다.
// 저널을 다시 읽어도 같고, 자격이 바뀌면 none·credential_changed다. 확정 단계 재사용은 판·자격이 같을 때만 저장된 방법을 받는다.
func TestLoginMethodLearnedJournalAndView(t *testing.T) {
	r, root, rec := learnedAccountTarget(t)
	id, rev := rec.Target.ID, rec.Target.Revision
	credA := credentialOf(r, id, "tester-a")
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	if m := loginMethodOf(t, r, id, "tester-a"); m == nil || m.State != productLoginMethodNone || m.ClearedReason != "" || m.Learned != nil {
		t.Fatalf("처음 상태: %+v", m)
	}
	if ok, _ := r.recordLoginMethodLearned(id, "tester-a", rev+1, credA, learnedAt("/login", "run-job1", t0), productLoginEvidenceObserved); ok {
		t.Fatal("결속 판이 다른 결과를 남김")
	}
	if ok, err := r.recordLoginMethodLearned(id, "tester-a", rev, credA, learnedAt("/login", "run-job1", t0), productLoginEvidenceObserved); !ok || err != nil {
		t.Fatalf("배운 방법을 남기지 못함: %v", err)
	}
	if ok, _ := r.recordLoginMethodLearned(id, "tester-a", rev, credA, learnedAt("/login", "run-job1", t0), productLoginEvidenceObserved); ok {
		t.Fatal("같은 시도를 다시 남김")
	}
	m := loginMethodOf(t, r, id, "tester-a")
	if m.State != productLoginMethodLearned || m.Learned == nil || m.Learned.RunRef != "run-job1" || m.Learned.Stage != "diagnosis" || m.Learned.Evidence != productLoginEvidenceObserved ||
		m.Method == nil || m.Method.Path != "/login" || m.Method.HTTPMethod != "POST" || m.Verified != nil {
		t.Fatalf("배움 상태: %+v", m)
	}
	if got, reason := r.learnedLoginMethod(id, "tester-a", rev, credA); reason != "" || got.Method.Path != "/login" || got.Source.RunRef != "run-job1" {
		t.Fatalf("저장된 방법 읽기: %v %+v", reason, got)
	}
	if _, reason := r.learnedLoginMethod(id, "tester-a", rev+1, credA); reason != learnedlogin.ReasonTargetChanged {
		t.Fatalf("다른 판: %v", reason)
	}
	if _, reason := r.learnedLoginMethod(id, "tester-a", rev, strings.Repeat("0", 64)); reason != learnedlogin.ReasonCredentialChanged {
		t.Fatalf("다른 자격: %v", reason)
	}
	if _, reason := r.learnedLoginMethod(id, "tester-b", rev, credentialOf(r, id, "tester-b")); reason != learnedlogin.ReasonNone {
		t.Fatalf("배운 것 없는 계정: %v", reason)
	}
	digest := learnedMethod("/login").Digest()
	if ok, _ := r.recordLoginMethodVerified(id, "tester-a", rev, credA, digest, "run-job2", "confirmation-1", t0.Add(time.Hour)); !ok {
		t.Fatal("verified를 남기지 못함")
	}
	if m := loginMethodOf(t, r, id, "tester-a"); m.State != productLoginMethodVerified || m.Verified == nil || m.Verified.RunRef != "run-job2" || m.Verified.Evidence != productLoginVerifiedEvidence {
		t.Fatalf("검증 상태: %+v", m)
	}
	// 다른 방법을 배우면 learned로 돌아가고, 예전 방법을 다시 배워도 그 방법이 지금 것이 된 뒤의 검증이 없어 learned다.
	other := learnedAt("/signin", "run-job3", t0.Add(2*time.Hour))
	other.Source.AttemptRef = "exploration-4"
	if ok, _ := r.recordLoginMethodLearned(id, "tester-a", rev, credA, other, productLoginEvidenceProbe); !ok {
		t.Fatal("다른 방법을 남기지 못함")
	}
	if m := loginMethodOf(t, r, id, "tester-a"); m.State != productLoginMethodLearned || m.Method.Path != "/signin" || m.Learned.Stage != "exploration" || m.Learned.Evidence != productLoginEvidenceProbe {
		t.Fatalf("방법 바뀜: %+v", m)
	}
	back := learnedAt("/login", "run-job4", t0.Add(3*time.Hour))
	if ok, _ := r.recordLoginMethodLearned(id, "tester-a", rev, credA, back, productLoginEvidenceObserved); !ok {
		t.Fatal("예전 방법을 다시 남기지 못함")
	}
	if m := loginMethodOf(t, r, id, "tester-a"); m.State != productLoginMethodLearned {
		t.Fatalf("예전 방법의 옛 검증이 다시 보임: %+v", m)
	}
	// 저널을 다시 읽어도 같다.
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if m := loginMethodOf(t, reopened, id, "tester-a"); m.State != productLoginMethodLearned || m.Method.Path != "/login" || m.Learned.RunRef != "run-job4" {
		t.Fatalf("재시작 뒤: %+v", m)
	}
	// 자격을 다시 등록하면 none·credential_changed이고 저장된 방법은 쓰지 않는다.
	if _, err := reopened.setAccountCredential("ARTEX", id, "tester-a", diagnosisprep.AccountCredential{Username: "changed-user", Password: "changed-pass-1"}); err != nil {
		t.Fatal(err)
	}
	if m := loginMethodOf(t, reopened, id, "tester-a"); m.State != productLoginMethodNone || m.ClearedReason != "credential_changed" || m.Method != nil {
		t.Fatalf("자격 변경 뒤: %+v", m)
	}
}

// 회차 기록 행에서 결속 계정마다 적격 성공의 마지막을 배우고, 근거는 그 시도의 contrast_source다. 적격이 아닌 성공(대조 없음)과
// 결속하지 않은 계정은 배우지 않는다.
func TestLearnFromRunRows(t *testing.T) {
	r, _, rec := learnedAccountTarget(t)
	id := rec.Target.ID
	credentials := map[string]string{"tester-a": credentialOf(r, id, "tester-a"), "tester-b": credentialOf(r, id, "tester-b")}
	first := modelSucceededRow("diagnosis-1", "tester-a", "/login")
	first.ContrastSource = "observed"
	later := modelSucceededRow("diagnosis-2", "tester-a", "/signin")
	later.ContrastSource = "probe"
	notEligible := modelSucceededRow("diagnosis-3", "tester-b", "/login")
	notEligible.Followup, notEligible.Outcome = learnedlogin.FollowupNone, "unconfirmed"
	if err := r.learnFromRows(rec, "run-job1", credentials, []string{"tester-a", "tester-b"}, []learnedlogin.Row{first, later, notEligible}); err != nil {
		t.Fatal(err)
	}
	if m := loginMethodOf(t, r, id, "tester-a"); m.State != productLoginMethodLearned || m.Method.Path != "/signin" || m.Learned.Evidence != productLoginEvidenceProbe || m.Learned.AttemptRef != "diagnosis-2" {
		t.Fatalf("배운 방법: %+v", m)
	}
	if m := loginMethodOf(t, r, id, "tester-b"); m.State != productLoginMethodNone {
		t.Fatalf("적격 아닌 성공을 배움: %+v", m)
	}
}
