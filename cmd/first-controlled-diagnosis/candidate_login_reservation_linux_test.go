package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/learnedlogin"
	"github.com/Autumn-27/artex/server"
)

// reservationFixture는 두 계정(tester-a·tester-b)이 같은 방법을 verified로 가진 대상이다.
type reservationFixture struct {
	// actions는 고정 준비(검증 줄) 뒤에 쓴 계정 저널 action이다.
	actions *[]string
	r       *productTargetRegistry
	root    string
	rec     productTargetRecord
	method  learnedlogin.Method
	since   time.Time
}

func newReservationFixture(t *testing.T) reservationFixture {
	t.Helper()
	r, root, rec := learnedAccountTarget(t)
	f := reservationFixture{r: r, root: root, rec: rec, method: learnedMethod("/login"), since: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)}
	for _, label := range []string{"tester-a", "tester-b"} {
		verifyStored(t, r, rec, label, f.method, "confirmation-1", f.since.Add(time.Hour))
	}
	f.actions = &[]string{}
	r.accountJournal = recordingJournal{productContactJournal: r.accountJournal, actions: f.actions}
	return f
}

func (f reservationFixture) gate(label, holder string) (string, string) {
	return f.r.candidateLoginGate(f.rec.Target.ID, label, f.method.Digest(), f.rec.Target.Revision, credentialOf(f.r, f.rec.Target.ID, label), f.since, holder)
}

func (f reservationFixture) reserve(t *testing.T, label, ref string) string {
	t.Helper()
	verified, reason := f.gate(label, "")
	if reason != "" {
		return reason
	}
	return f.r.reserveCandidateLogin(f.rec.Target.ID, label, f.method.Digest(), f.rec.Target.Revision, credentialOf(f.r, f.rec.Target.ID, label), f.since,
		verified, ref)
}

// recordingJournal은 계정 저널에 쓴 줄의 action을 모은다(쓰기는 그대로 보호 저널로 간다).
type recordingJournal struct {
	productContactJournal
	actions *[]string
}

func (j recordingJournal) Append(raw json.RawMessage) error {
	if err := j.productContactJournal.Append(raw); err != nil {
		return err
	}
	var e struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(raw, &e) == nil {
		*j.actions = append(*j.actions, e.Action)
	}
	return nil
}

func countAction(actions []string, action string) int {
	n := 0
	for _, a := range actions {
		if a == action {
			n++
		}
	}
	return n
}

// ST09·ST14·G08: 같은 대상·계정의 선택 로그인은 한 번에 하나다. 다른 계정은 따로 돈다. 예약을 잡은 실행만 송신 직전 확인을 통과한다.
func TestCandidateLoginReservationOnePerAccount(t *testing.T) {
	f := newReservationFixture(t)
	if reason := f.reserve(t, "tester-a", "candidate-1"); reason != "" {
		t.Fatalf("첫 예약: %s", reason)
	}
	if reason := f.reserve(t, "tester-a", "candidate-2"); reason != candidateReasonInProgress {
		t.Fatalf("같은 계정 두 번째 예약: %q", reason)
	}
	if _, reason := f.gate("tester-a", ""); reason != candidateReasonInProgress {
		t.Fatalf("진행 중 계획·실행 전 확인: %q", reason)
	}
	if _, reason := f.gate("tester-a", "candidate-2"); reason != candidateReasonInProgress {
		t.Fatalf("다른 실행의 송신 직전 확인: %q", reason)
	}
	if ref, reason := f.gate("tester-a", "candidate-1"); reason != "" || len(ref) != 64 {
		t.Fatalf("예약한 실행의 송신 직전 확인: %q", reason)
	}
	if reason := f.reserve(t, "tester-b", "candidate-3"); reason != "" {
		t.Fatalf("다른 계정 예약: %s", reason)
	}
	if err := f.r.finishCandidateLogin(f.rec.Target.ID, "tester-a", "candidate-1", candidateLoginFinishedAuthenticated, ""); err != nil {
		t.Fatal(err)
	}
	if _, reason := f.gate("tester-a", "candidate-1"); reason != candidateReasonUnverified {
		t.Fatalf("닫은 예약으로 계속 송신: %q", reason)
	}
	if reason := f.reserve(t, "tester-a", "candidate-4"); reason != "" {
		t.Fatalf("앞 실행이 끝난 뒤 예약: %s", reason)
	}
}

// ST04~ST06·G04·G05: 미전송이면 verified를 유지하고, 실패·결과 불명이면 현재 유효성만 해제한다(과거 verified 줄은 남김). 같은
// 해제는 두 번 쓰지 않는다. 선택 로그인 성공이 verified를 새로 쓰지 않는다. 해제 뒤 E③ 다시 검증은 다시 verified를 만든다.
func TestCandidateLoginClearLifecycle(t *testing.T) {
	f := newReservationFixture(t)
	if reason := f.reserve(t, "tester-a", "candidate-1"); reason != "" {
		t.Fatal(reason)
	}
	if err := f.r.finishCandidateLogin(f.rec.Target.ID, "tester-a", "candidate-1", candidateLoginFinishedNotSent, ""); err != nil {
		t.Fatal(err)
	}
	if _, reason := f.gate("tester-a", ""); reason != "" {
		t.Fatalf("미전송 뒤 verified가 사라짐: %q", reason)
	}
	if reason := f.reserve(t, "tester-a", "candidate-2"); reason != "" {
		t.Fatal(reason)
	}
	if err := f.r.finishCandidateLogin(f.rec.Target.ID, "tester-a", "candidate-2", candidateLoginFinishedAuthenticated, ""); err != nil {
		t.Fatal(err)
	}
	before := countAction(*f.actions, accountLoginMethodVerified)
	for i, cleared := range []string{candidateLoginClearedFailed, candidateLoginClearedUnknown} {
		ref := "candidate-" + string(rune('5'+i))
		if reason := f.reserve(t, "tester-a", ref); reason != "" {
			t.Fatalf("%s 예약: %s", cleared, reason)
		}
		for range 2 { // 같은 해제를 두 번 불러도 한 번만 쓴다.
			if err := f.r.finishCandidateLogin(f.rec.Target.ID, "tester-a", ref, "", cleared); err != nil {
				t.Fatal(err)
			}
		}
		if _, reason := f.gate("tester-a", ""); reason != candidateReasonUnverified {
			t.Fatalf("%s 뒤 verified 유지: %q", cleared, reason)
		}
		if _, reason := f.gate("tester-b", ""); reason != "" {
			t.Fatalf("다른 계정 verified가 해제됨: %q", reason)
		}
		verifyStored(t, f.r, f.rec, "tester-a", f.method, "confirmation-"+string(rune('2'+i)), f.since.Add(time.Duration(2+i)*time.Hour))
		if _, reason := f.gate("tester-a", ""); reason != "" {
			t.Fatalf("해제 뒤 E③ 다시 검증이 반영되지 않음: %q", reason)
		}
	}
	actions := *f.actions
	if countAction(actions, accountLoginVerifiedCleared) != 2 || countAction(actions, accountLoginMethodVerified) != before+2 ||
		countAction(actions, accountCandidateLoginReserved) != 4 || countAction(actions, accountCandidateLoginFinished) != 2 {
		t.Fatalf("저널 사건 수: %v", actions)
	}
	st := f.r.accounts[f.rec.Target.ID]["tester-a"]
	if view := loginMethodVerifiedFor(f.rec, st, f.method.Digest(), f.since); view == nil || view.attemptRef != "confirmation-3" {
		t.Fatalf("마지막 검증: %+v", view)
	}
}

// ST19: 예약 뒤 같은 계정의 검증 사건이 바뀌면(자격 변경 뒤 새 E③ 검증) 늦게 온 실패는 새 검증을 해제하지 않고 superseded로 닫는다.
func TestCandidateLoginLateFailureKeepsNewerVerified(t *testing.T) {
	f := newReservationFixture(t)
	if reason := f.reserve(t, "tester-a", "candidate-1"); reason != "" {
		t.Fatal(reason)
	}
	if _, err := f.r.setAccountCredential("ARTEX", f.rec.Target.ID, "tester-a", diagnosisprep.AccountCredential{Username: "learned-user-a", Password: "rotated-pass-a"}); err != nil {
		t.Fatal(err)
	}
	if _, reason := f.gate("tester-a", "candidate-1"); reason != candidateReasonUnverified {
		t.Fatalf("실행 중 자격 변경 뒤 송신 직전 확인 통과: %q", reason)
	}
	verifyStored(t, f.r, f.rec, "tester-a", f.method, "confirmation-2", f.since.Add(2*time.Hour))
	if err := f.r.finishCandidateLogin(f.rec.Target.ID, "tester-a", "candidate-1", "", candidateLoginClearedFailed); err != nil {
		t.Fatal(err)
	}
	if _, reason := f.gate("tester-a", ""); reason != "" {
		t.Fatalf("늦은 실패가 새 검증을 해제함: %q", reason)
	}
	if actions := *f.actions; countAction(actions, accountLoginVerifiedCleared) != 0 || countAction(actions, accountCandidateLoginFinished) != 1 {
		t.Fatalf("저널: %v", actions)
	}
}

// failingAppendJournal은 쓰기만 실패하는 계정 저널이다(확인은 통과: 일시 저장 장애).
type failingAppendJournal struct{ productContactJournal }

func (failingAppendJournal) Append(json.RawMessage) error { return errors.New("disk full") }

// ST10·ST11·G06·G07: 해제 기록을 남기지 못하면 그 계정의 다음 계획·실행을 막고(다른 계정은 그대로), 재시작 뒤에도 막힌다. 열린
// 예약은 재시작만으로 풀리지 않고 결과 불명 해제(candidate_login_interrupted)를 남긴 뒤에만 닫힌다. 그 기록도 못 남기면 계속 막는다.
func TestCandidateLoginClearFailureFailsClosedAcrossRestart(t *testing.T) {
	f := newReservationFixture(t)
	if reason := f.reserve(t, "tester-a", "candidate-1"); reason != "" {
		t.Fatal(reason)
	}
	good := f.r.accountJournal
	f.r.accountJournal = failingAppendJournal{good}
	if err := f.r.finishCandidateLogin(f.rec.Target.ID, "tester-a", "candidate-1", "", candidateLoginClearedFailed); err == nil {
		t.Fatal("해제 기록 실패를 성공으로 봄")
	}
	if _, reason := f.gate("tester-a", ""); reason != candidateReasonUnresolved {
		t.Fatalf("해제 기록 실패 뒤 계획 허용: %q", reason)
	}
	if reason := f.reserve(t, "tester-a", "candidate-2"); reason != candidateReasonUnresolved {
		t.Fatalf("해제 기록 실패 뒤 실행 허용: %q", reason)
	}
	if _, reason := f.gate("tester-b", ""); reason != "" {
		t.Fatalf("다른 계정까지 막힘: %q", reason)
	}
	f.r.accountJournal = good
	if err := f.r.Close(); err != nil {
		t.Fatal(err)
	}
	// 재시작: 저장된 예약이 열린 채 남아 있다. 복구 기록을 못 남기면 계속 막는다.
	reopened, err := newProductTargetRegistry(f.root, f.r.policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	f.r = reopened
	defer func() { _ = f.r.Close() }()
	if st := f.r.accounts[f.rec.Target.ID]["tester-a"]; st.candidateLogin == nil || st.candidateLogin.ref != "candidate-1" {
		t.Fatal("재시작 뒤 열린 예약이 사라짐")
	}
	good = f.r.accountJournal
	f.r.accountJournal = failingAppendJournal{good}
	if _, reason := f.gate("tester-a", ""); reason != candidateReasonUnresolved {
		t.Fatalf("재시작 뒤 복구 기록 실패에도 허용: %q", reason)
	}
	f.r.accountJournal = recordingJournal{productContactJournal: good, actions: f.actions}
	if _, reason := f.gate("tester-a", ""); reason != candidateReasonUnverified {
		t.Fatalf("결과 불명 예약을 안전하게 닫지 않음: %q", reason)
	}
	actions := *f.actions
	if countAction(actions, accountLoginVerifiedCleared) != 1 || countAction(actions, accountCandidateLoginFinished) != 0 {
		t.Fatalf("복구 사건: %v", actions)
	}
	if _, reason := f.gate("tester-b", ""); reason != "" {
		t.Fatalf("다른 계정 verified가 해제됨: %q", reason)
	}
}

// 재시작(실행 중 프로세스 종료)도 같은 경로다: 예약을 잡은 프로세스가 사라지면 새 프로세스는 결과 불명으로 보고 해제한다.
func TestCandidateLoginCrashedReservationClearsOnRestart(t *testing.T) {
	f := newReservationFixture(t)
	if reason := f.reserve(t, "tester-a", "candidate-1"); reason != "" {
		t.Fatal(reason)
	}
	if err := f.r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(f.root, f.r.policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	f.r = reopened
	defer func() { _ = f.r.Close() }()
	if reason := f.reserve(t, "tester-a", "candidate-2"); reason != candidateReasonUnverified {
		t.Fatalf("재시작 뒤 결과 불명 예약 위에 새 실행: %q", reason)
	}
	if err := f.r.finishCandidateLogin(f.rec.Target.ID, "tester-a", "candidate-1", candidateLoginFinishedAuthenticated, ""); err != nil {
		t.Fatal(err)
	}
	if _, reason := f.gate("tester-a", ""); reason != candidateReasonUnverified {
		t.Fatalf("이미 닫은 예약의 늦은 결과가 verified를 되살림: %q", reason)
	}
}

// 쓰는 쪽이 만들지 않는 예약·해제 줄은 저널 손상이다(다시 읽지 않음).
func TestCandidateLoginJournalRejectsUnpairedEvents(t *testing.T) {
	for _, e := range []productAccountEvent{
		{Action: accountCandidateLoginFinished, Evidence: candidateLoginFinishedNotSent, ReservationRef: "candidate-1"},
		{Action: accountLoginVerifiedCleared, Evidence: candidateLoginClearedFailed, ReservationRef: "candidate-1", RunRef: "run-e3",
			AttemptRef: "confirmation-1", Stage: productLoginVerifiedStage},
		{Action: accountLoginMethodVerified, ReservationRef: "candidate-1"},
	} {
		f := newReservationFixture(t)
		e.TargetID, e.Label, e.Actor, e.At, e.TargetRevision = f.rec.Target.ID, "tester-a", f.rec.Principal, time.Now().UTC(), f.rec.Target.Revision
		e.MethodSHA256 = f.method.Digest()
		if e.Action == accountLoginVerifiedCleared {
			e.CredentialSHA256 = credentialOf(f.r, f.rec.Target.ID, "tester-a")
		}
		if f.r.appendAccount(e) == nil {
			t.Fatalf("짝 없는 %s 줄을 받음", e.Action)
		}
		if err := f.r.Close(); err != nil {
			t.Fatal(err)
		}
		if reopened, err := newProductTargetRegistry(f.root, f.r.policy, func() error { return nil }); err == nil {
			reopened.Close()
			t.Fatalf("짝 없는 %s 줄이 있는 저널을 다시 읽음", e.Action)
		}
	}
}

func selectedObservation(seq uint64, account string, status int, complete bool) candidateverify.StoredObservationV2 {
	var o candidateverify.StoredObservationV2
	o.Source.Observation = e2proxy.HTTPObservation{Sequence: seq, RequestAccountRef: account, StatusCode: status, ResponseComplete: complete}
	return o
}

// ST04~ST08·G11: 예약을 닫을 결과. 학습 로그인을 시작하지 않았거나 POST를 전달하지 않았으면 유지, 보낸 뒤 실패·결과 불명·첫
// 인증 읽기 막힘(401·403)·첫 인증 읽기 미완료면 해제한다. 기록 칸이 not_sent여도 전달을 시작했으면 보낸 것으로 본다.
func TestCandidateSelectedLoginClassify(t *testing.T) {
	account := strings.Repeat("a", 64)
	sent := server.E2HTTPSelectedLoginOutcome{Result: server.E2HTTPAuthAuthenticated, Transport: learnedlogin.TransportSent, LoginForwardAttempted: true}
	ok := []candidateverify.StoredObservationV2{selectedObservation(3, account, 200, true), selectedObservation(4, account, 403, true)}
	for _, tc := range []struct {
		name              string
		reported          bool
		outcome           server.E2HTTPSelectedLoginOutcome
		observations      []candidateverify.StoredObservationV2
		finished, cleared string
	}{
		{name: "login never started", finished: candidateLoginFinishedNotSent},
		{name: "not sent (approval, budget, revoked)", reported: true, outcome: server.E2HTTPSelectedLoginOutcome{Result: server.E2HTTPAuthNotSent,
			Transport: learnedlogin.TransportNotSent}, finished: candidateLoginFinishedNotSent},
		{name: "not_sent record but forwarded", reported: true, outcome: server.E2HTTPSelectedLoginOutcome{Result: server.E2HTTPAuthNotSent,
			Transport: learnedlogin.TransportNotSent, LoginForwardAttempted: true}, cleared: candidateLoginClearedUnknown},
		{name: "rejected 4xx or login form", reported: true, outcome: server.E2HTTPSelectedLoginOutcome{Result: server.E2HTTPAuthLoginFailed,
			Transport: learnedlogin.TransportSent, LoginForwardAttempted: true}, cleared: candidateLoginClearedFailed},
		{name: "session not found", reported: true, outcome: server.E2HTTPSelectedLoginOutcome{Result: server.E2HTTPAuthSessionNotFound,
			Transport: learnedlogin.TransportSent, LoginForwardAttempted: true}, cleared: candidateLoginClearedFailed},
		{name: "result unknown", reported: true, outcome: server.E2HTTPSelectedLoginOutcome{Result: server.E2HTTPAuthResultUnknown,
			Transport: learnedlogin.TransportUnknown, LoginForwardAttempted: true}, cleared: candidateLoginClearedUnknown},
		{name: "first read 401", reported: true, outcome: sent, observations: []candidateverify.StoredObservationV2{selectedObservation(2, account, 401, true)},
			cleared: candidateLoginClearedReadDenied},
		{name: "first read 403", reported: true, outcome: sent, observations: []candidateverify.StoredObservationV2{selectedObservation(5, account, 200, true),
			selectedObservation(2, account, 403, true)}, cleared: candidateLoginClearedReadDenied},
		{name: "first read incomplete", reported: true, outcome: sent, observations: []candidateverify.StoredObservationV2{selectedObservation(2, account, 200, false)},
			cleared: candidateLoginClearedUnconfirmed},
		{name: "no authenticated read", reported: true, outcome: sent, observations: []candidateverify.StoredObservationV2{selectedObservation(2, "", 200, true)},
			cleared: candidateLoginClearedUnconfirmed},
		{name: "authenticated and first read ok", reported: true, outcome: sent, observations: ok, finished: candidateLoginFinishedAuthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c candidateSelectedLoginCapture
			if tc.reported {
				c.record(tc.outcome)
			}
			c.done(tc.observations)
			finished, cleared := c.classify(account)
			if finished != tc.finished || cleared != tc.cleared {
				t.Fatalf("got %q/%q, want %q/%q", finished, cleared, tc.finished, tc.cleared)
			}
		})
	}
	var twice candidateSelectedLoginCapture
	twice.record(sent)
	twice.record(sent)
	twice.done(ok)
	if _, cleared := twice.classify(account); cleared != candidateLoginClearedUnknown {
		t.Fatalf("학습 로그인 두 번 보고를 확신함: %q", cleared)
	}
}
