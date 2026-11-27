package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/learnedlogin"
	"github.com/Autumn-27/artex/server"
)

// storedMethodReader는 ⑤-B 저장 방법 읽기 경계를 흉내 낸다(라벨별 방법 또는 고정 사유).
type storedMethodReader struct {
	learned map[string]learnedlogin.Learned
	reason  learnedlogin.Reason
}

func (s storedMethodReader) learnedLoginMethod(_ string, label string, _ uint64, _ string) (learnedlogin.Learned, learnedlogin.Reason) {
	if s.reason != "" {
		return learnedlogin.Learned{}, s.reason
	}
	m, ok := s.learned[label]
	if !ok {
		return learnedlogin.Learned{}, learnedlogin.ReasonNone
	}
	return m, ""
}

const selectedSourceRunRef = "run-" + liveJob

// selectedSourceRun은 학습 방법을 배운 원본 회차다: 계정 tester-a를 결속한 실행 정책, 탐색 경계 근거 v2 결속, 탐색 단계
// 로그인 성공 행(경계 순번 17), 같은 순번의 경계 관찰 근거. change로 각 요소를 바꾼다.
func selectedSourceRun(t *testing.T, r *productTargetRegistry, rec productTargetRecord, change func(*server.E2HTTPFilePolicy, *learnedlogin.Row, *learnedlogin.ExplorationEvidence), events ...e2proxy.Event) (string, learnedlogin.Learned) {
	t.Helper()
	row := modelSucceededRow("exploration-1", "tester-a", "/login")
	row.RunRef, row.Stage, row.TransportRef = selectedSourceRunRef, learnedlogin.StageExploration, "17"
	evidence := learnedlogin.ExplorationEvidence{Schema: learnedlogin.ExplorationEvidenceSchema, RunRef: selectedSourceRunRef, SessionRef: "explore-live",
		JobID: liveJob, TaskID: "9", TargetRef: rec.Target.ID, TargetRevision: rec.Target.Revision, SourceDigest: strings.Repeat("a", 64),
		Sends: []learnedlogin.ExplorationSend{{Seq: 17, Method: "POST", Path: "/login", Status: 302, BoundaryForwarded: true,
			ApprovedTarget: true, ObservationForwarded: true, ResponseReceived: true}}}
	var record func(func(e2proxy.Event))
	if len(events) > 0 {
		record = func(add func(e2proxy.Event)) {
			for _, ev := range events {
				add(ev)
			}
		}
	}
	runs, _ := liveRunFixtureWith(t, record, func(p *server.E2HTTPFilePolicy) {
		p.RunRef = selectedSourceRunRef
		p.Auth.Accounts = []server.E2HTTPFileAuthAccount{{Label: "tester-a", Credential: server.E2HTTPFilePin{File: "tester-a.json", SHA256: credentialOf(r, rec.Target.ID, "tester-a")}}}
		p.ExplorationLoginEvidence, p.ExplorationSourceDigest = "required/v2", strings.Repeat("a", 64)
		if change != nil {
			change(p, &row, &evidence)
		}
	})
	state := filepath.Join(runs, liveJob, "state")
	raw, _ := jsonLine(row)
	if err := os.WriteFile(filepath.Join(state, learnedlogin.AttemptsFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if evidence.Schema != "" {
		if err := learnedlogin.WriteExplorationEvidence(state, evidence); err != nil {
			t.Fatal(err)
		}
	}
	at, _ := time.Parse(time.RFC3339, row.At)
	return runs, learnedlogin.Learned{Method: learnedMethod("/login"), Source: learnedlogin.Source{RunRef: selectedSourceRunRef, AttemptRef: "exploration-1", At: at}}
}

func jsonLine(row learnedlogin.Row) ([]byte, error) {
	raw, err := json.Marshal(row)
	return append(raw, '\n'), err
}

// A·작업 A: 저장된 학습 방법은 원본 회차의 보호 기록·탐색 경계 근거로 다시 대조될 때만 선택 검증에 쓴다. 인계 실패 표지·
// 기록 유실·근거 불일치·진단 단계 출처·이전 회차·저장 줄과 회차 선택 불일치는 막고 9칸으로 넘어가지 않는다(AN04~AN06).
func TestSelectedCandidateLearnedLoginTrust(t *testing.T) {
	r, _, rec := learnedAccountTarget(t)
	if _, err := r.setTargetLogin("ARTEX", rec.Target.ID, diagnosisprep.TargetLogin{Method: "POST", Path: "/read/login", Format: "form",
		UsernameField: "email", PasswordField: "password", Cookies: []string{"session"}}); err != nil {
		t.Fatal(err)
	}
	ref := server.CandidateAccountRevision("tester-a", credentialOf(r, rec.Target.ID, "tester-a"))
	for _, tc := range []struct {
		name   string
		change func(*server.E2HTTPFilePolicy, *learnedlogin.Row, *learnedlogin.ExplorationEvidence)
		after  func(t *testing.T, runs string, stored *learnedlogin.Learned)
		want   string
		events []e2proxy.Event
	}{
		{name: "diagnosis stage audited journal", want: server.CandidateLoginTrustE2Audited, change: diagnosisSourceRow, events: []e2proxy.Event{diagnosisLoginResult(302)},
			after: func(_ *testing.T, _ string, stored *learnedlogin.Learned) { stored.Source.AttemptRef = "diagnosis-1" }},
		{name: "diagnosis stage journal other status", want: string(learnedlogin.ReasonTransportUnverified), change: diagnosisSourceRow,
			events: []e2proxy.Event{diagnosisLoginResult(401)}, after: func(_ *testing.T, _ string, stored *learnedlogin.Learned) { stored.Source.AttemptRef = "diagnosis-1" }},
		{name: "exploration observed", want: server.CandidateLoginTrustExplorationObserved},
		{name: "handoff marker", want: string(learnedlogin.ReasonRecordInvalid), after: func(t *testing.T, runs string, _ *learnedlogin.Learned) {
			if err := os.WriteFile(filepath.Join(runs, liveJob, "state", learnedlogin.AttemptsUnavailableFile), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "rows lost", want: string(learnedlogin.ReasonRecordInvalid), after: func(t *testing.T, runs string, _ *learnedlogin.Learned) {
			if err := os.Remove(filepath.Join(runs, liveJob, "state", learnedlogin.AttemptsFile)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "evidence lost", want: string(learnedlogin.ReasonTransportUnverified), change: func(_ *server.E2HTTPFilePolicy, _ *learnedlogin.Row, e *learnedlogin.ExplorationEvidence) {
			e.Schema = ""
		}},
		{name: "evidence other path", want: string(learnedlogin.ReasonTransportUnverified), change: func(_ *server.E2HTTPFilePolicy, _ *learnedlogin.Row, e *learnedlogin.ExplorationEvidence) {
			e.Sends[0].Path = "/other"
		}},
		{name: "legacy run without evidence policy", want: string(learnedlogin.ReasonTransportUnverified), change: func(p *server.E2HTTPFilePolicy, _ *learnedlogin.Row, _ *learnedlogin.ExplorationEvidence) {
			p.ExplorationLoginEvidence, p.ExplorationSourceDigest = "", ""
		}},
		{name: "diagnosis stage without journal", want: string(learnedlogin.ReasonTransportUnverified), change: diagnosisSourceRow,
			after: func(_ *testing.T, _ string, stored *learnedlogin.Learned) { stored.Source.AttemptRef = "diagnosis-1" }},
		{name: "stored attempt differs", want: string(learnedlogin.ReasonRecordInvalid), after: func(_ *testing.T, _ string, stored *learnedlogin.Learned) {
			stored.Source.AttemptRef = "exploration-2"
		}},
		{name: "stored method digest differs", want: string(learnedlogin.ReasonRecordInvalid), after: func(_ *testing.T, _ string, stored *learnedlogin.Learned) {
			stored.Method.SessionNames = []string{"other"}
		}},
		{name: "account not bound in source run", want: string(learnedlogin.ReasonCredentialChanged), change: func(p *server.E2HTTPFilePolicy, _ *learnedlogin.Row, _ *learnedlogin.ExplorationEvidence) {
			p.Auth.Accounts[0].Credential.SHA256 = strings.Repeat("f", 64)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runs, stored := selectedSourceRun(t, r, rec, tc.change, tc.events...)
			if tc.after != nil {
				tc.after(t, runs, &stored)
			}
			choice := candidateLoginChoice(r, storedMethodReader{learned: map[string]learnedlogin.Learned{"tester-a": stored}}, runs, "ARTEX", rec.Target.ID, ref)
			got := choice.Trust
			if choice.Source == diagnosisprep.CandidateLoginNone {
				got = choice.Reason
			}
			if got != tc.want || choice.Source == diagnosisprep.CandidateLoginConfigured {
				t.Fatalf("선택 %+v, want %s", choice, tc.want)
			}
			if choice.Source == diagnosisprep.CandidateLoginLearned && (choice.Method == nil || choice.Method.Digest() != stored.Method.Digest()) {
				t.Fatal("다른 방법을 돌려줌")
			}
		})
	}
}

// 진단 단계에서 배운 방법도 같은 방법·지금 대상 판·자격 판의 E③ verified가 학습 시각 뒤에 있으면 verified로 쓴다. 다른
// 방법의 검증이나 학습 시각보다 앞선 검증은 인정하지 않는다(저장 경계가 '방법이 지금 것이 된 시각'을 주지 않아 보수적으로 본다).
func TestSelectedCandidateLearnedLoginVerifiedTrust(t *testing.T) {
	learnedTime := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	stored := learnedlogin.Learned{Method: learnedMethod("/login"), Source: learnedlogin.Source{RunRef: "run-gone", AttemptRef: "diagnosis-1", At: learnedTime}}
	reader := storedMethodReader{learned: map[string]learnedlogin.Learned{"tester-a": stored}}
	for _, tc := range []struct {
		name   string
		method string
		at     time.Duration
		want   string
	}{
		{"no verified", "", 0, string(learnedlogin.ReasonRecordInvalid)},
		{"other method verified", "/other", time.Hour, string(learnedlogin.ReasonRecordInvalid)},
		{"verified before learned", "/login", -time.Hour, string(learnedlogin.ReasonRecordInvalid)},
		{"verified after learned", "/login", 2 * time.Hour, server.CandidateLoginTrustVerified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, rec := learnedAccountTarget(t)
			cred := credentialOf(r, rec.Target.ID, "tester-a")
			if tc.method != "" {
				if ok, err := r.recordLoginMethodVerified(rec.Target.ID, "tester-a", rec.Target.Revision, cred, learnedMethod(tc.method).Digest(), "run-e3",
					"confirmation-1", learnedTime.Add(tc.at)); err != nil || !ok {
					t.Fatal("verified 줄 쓰기", err)
				}
			}
			c := candidateLoginChoice(r, reader, t.TempDir(), "ARTEX", rec.Target.ID, server.CandidateAccountRevision("tester-a", cred))
			got := c.Trust
			if c.Source == diagnosisprep.CandidateLoginNone {
				got = c.Reason
			}
			if got != tc.want {
				t.Fatalf("선택 %+v, want %s", c, tc.want)
			}
		})
	}
}

// 우선순위: 학습 방법이 없을 때만 9칸, 둘 다 없으면 없음. 저장 방법이 대상 재승인·자격 변경으로 쓸 수 없으면 그 사유로 막고
// 9칸으로 넘어가지 않는다(AN02·AN03). 지워진 계정·바뀐 자격의 참조는 계정을 찾지 못한다(AN01·AN02).
func TestSelectedCandidateLoginPriority(t *testing.T) {
	r, _, rec := learnedAccountTarget(t)
	ref := server.CandidateAccountRevision("tester-a", credentialOf(r, rec.Target.ID, "tester-a"))
	runs := t.TempDir()
	if c := candidateLoginChoice(r, nil, runs, "ARTEX", rec.Target.ID, ref); c.Source != diagnosisprep.CandidateLoginNone || c.Reason != string(learnedlogin.ReasonNone) {
		t.Fatalf("방법·설정 없음: %+v", c)
	}
	login := diagnosisprep.TargetLogin{Method: "POST", Path: "/read/login", Format: "form", UsernameField: "email", PasswordField: "password", Cookies: []string{"session"}}
	if _, err := r.setTargetLogin("ARTEX", rec.Target.ID, login); err != nil {
		t.Fatal(err)
	}
	if c := candidateLoginChoice(r, storedMethodReader{}, runs, "ARTEX", rec.Target.ID, ref); c.Source != diagnosisprep.CandidateLoginConfigured || c.Login == nil || c.Login.Path != "/read/login" {
		t.Fatalf("학습 방법 없음 → 9칸: %+v", c)
	}
	for _, reason := range []learnedlogin.Reason{learnedlogin.ReasonTargetChanged, learnedlogin.ReasonCredentialChanged, learnedlogin.ReasonRecordInvalid} {
		if c := candidateLoginChoice(r, storedMethodReader{reason: reason}, runs, "ARTEX", rec.Target.ID, ref); c.Source != diagnosisprep.CandidateLoginNone || c.Reason != string(reason) {
			t.Fatalf("%s에서 9칸으로 넘어감: %+v", reason, c)
		}
	}
	if c := candidateLoginChoice(r, nil, runs, "ARTEX", rec.Target.ID, strings.Repeat("0", 64)); c.Source != diagnosisprep.CandidateLoginNone || c.Reason != string(learnedlogin.ReasonCredentialChanged) {
		t.Fatalf("모르는 계정 참조: %+v", c)
	}
	if _, err := r.setAccountCredential("ARTEX", rec.Target.ID, "tester-a", diagnosisprep.AccountCredential{Username: "learned-user-a", Password: "rotated-pass-a"}); err != nil {
		t.Fatal(err)
	}
	if c := candidateLoginChoice(r, nil, runs, "ARTEX", rec.Target.ID, ref); c.Source != diagnosisprep.CandidateLoginNone || c.Reason != string(learnedlogin.ReasonCredentialChanged) {
		t.Fatalf("자격 변경 뒤 옛 참조: %+v", c)
	}
}

// 실행 결속: 학습 로그인 계획은 그 계정 하나의 자격만 싣고(9칸 없는 대상 포함) 다른 계정 자격 사본은 지운다. 방법·출처
// 수준이 승인 뒤 바뀌면 싣지 않는다(AN15).
func TestSelectedCandidateLearnedAccountBinding(t *testing.T) {
	r, _, rec := learnedAccountTarget(t)
	cred := credentialOf(r, rec.Target.ID, "tester-a")
	ref := server.CandidateAccountRevision("tester-a", cred)
	runs, stored := selectedSourceRun(t, r, rec, nil)
	reader := storedMethodReader{learned: map[string]learnedlogin.Learned{"tester-a": stored}}
	b := &urlPreparationRuntime{product: &productRuntime{targets: r, learnedMethods: reader, config: urlPreparationInstallation{RunsDir: runs}}}
	prep := server.CandidateLearnedAuthPreparation(server.CandidateLearnedLogin{Method: stored.Method, Trust: server.CandidateLoginTrustExplorationObserved})
	plan := server.CandidateLLMPlan{TargetID: rec.Target.ID, AccountRef: ref, AuthPreparation: prep}
	otherFile := bindingCredentialName(rec.Target.ID, "tester-b", credentialOf(r, rec.Target.ID, "tester-b"))
	l := &urlLoadedProfile{materials: map[string][]byte{otherFile: []byte("other-account-secret"), "ca.pem": []byte("ca")}}
	l.policy.Auth = &server.E2HTTPFileAuth{Accounts: []server.E2HTTPFileAuthAccount{{Label: "tester-b", Credential: server.E2HTTPFilePin{File: otherFile}}}}
	if reason := b.bindCandidateLearnedAccount(l, plan); reason != "" {
		t.Fatalf("결속 실패: %s", reason)
	}
	pin := bindingCredentialName(rec.Target.ID, "tester-a", cred)
	if l.policy.Auth.LearnedMethod == nil || l.policy.Auth.LearnedMethod.Digest() != stored.Method.Digest() || len(l.policy.Auth.Accounts) != 1 ||
		l.policy.Auth.Accounts[0].Label != "tester-a" || l.policy.Auth.Accounts[0].Credential.File != pin || l.policy.Auth.PrepRuleRef != "auth-login-prep" ||
		l.policy.SelectedAccountRef != ref || l.policy.HeaderMode != e2proxy.HeaderTestAccountPassthrough || len(l.productAccounts) != 1 {
		t.Fatalf("결속 결과 %+v", l.policy.Auth)
	}
	if _, ok := l.materials[otherFile]; ok || len(l.materials[pin]) == 0 || string(l.materials["ca.pem"]) != "ca" {
		t.Fatal("다른 계정 자격 사본이 남거나 선택 계정 자격을 싣지 않음")
	}
	changed := plan
	other := *prep
	other.Trust = server.CandidateLoginTrustVerified
	changed.AuthPreparation = &other
	if reason := b.bindCandidateLearnedAccount(&urlLoadedProfile{materials: map[string][]byte{}}, changed); reason != "candidate_auth_preparation_changed" {
		t.Fatalf("승인 뒤 바뀐 준비를 실음: %q", reason)
	}
	if err := os.WriteFile(filepath.Join(runs, liveJob, "state", learnedlogin.AttemptsUnavailableFile), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b.candidateLearnedLoginCurrent(plan) {
		t.Fatal("원본 회차 인계 실패 뒤에도 송신 직전 재검사를 통과")
	}
}

func diagnosisSourceRow(_ *server.E2HTTPFilePolicy, row *learnedlogin.Row, _ *learnedlogin.ExplorationEvidence) {
	row.Stage, row.AttemptRef, row.TransportRef = learnedlogin.StageDiagnosis, "diagnosis-1", "call_m/"+diagnosisAttemptID
}

// diagnosisAttemptID는 E② 시도 참조 모양(16바이트 16진수)이다.
var diagnosisAttemptID = strings.Repeat("ab", 16)

// diagnosisLoginResult는 원본 회차 E② 감사 저널의 모델 로그인 POST 결과 사건이다.
func diagnosisLoginResult(status int) e2proxy.Event {
	e := liveEvent("result", "call_m", diagnosisAttemptID, 1, "allowed", e2proxy.Complete)
	e.Scope.RunRef, e.StatusCode, e.AuditComplete = selectedSourceRunRef, status, true
	return e
}
