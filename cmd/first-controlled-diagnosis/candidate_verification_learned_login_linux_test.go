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

// storedMethodReader는 ⑤-B 저장 방법 읽기 경계를 흉내 낸다(라벨별 방법·since 또는 고정 사유). since가 없으면 관찰 시각과 같다
// (처음 배운 방법).
type storedMethodReader struct {
	learned map[string]learnedlogin.Learned
	since   map[string]time.Time
	reason  learnedlogin.Reason
}

func (s storedMethodReader) learnedLoginMethod(_ string, label string, _ uint64, _ string) (learnedlogin.Learned, learnedlogin.Reason) {
	m, _, reason := s.learnedLoginMethodState("", label, 0, "")
	return m, reason
}

func (s storedMethodReader) learnedLoginMethodState(_ string, label string, _ uint64, _ string) (learnedlogin.Learned, time.Time, learnedlogin.Reason) {
	if s.reason != "" {
		return learnedlogin.Learned{}, time.Time{}, s.reason
	}
	m, ok := s.learned[label]
	if !ok {
		return learnedlogin.Learned{}, time.Time{}, learnedlogin.ReasonNone
	}
	since, ok := s.since[label]
	if !ok {
		since = m.Source.At
	}
	return m, since, ""
}

// methodOnlyReader는 since를 주지 않는 이전 읽기 경계다(방법만). verified를 판단할 수 없어 쓰지 않아야 한다.
type methodOnlyReader struct{ inner storedMethodReader }

func (s methodOnlyReader) learnedLoginMethod(targetID, label string, revision uint64, credential string) (learnedlogin.Learned, learnedlogin.Reason) {
	return s.inner.learnedLoginMethod(targetID, label, revision, credential)
}

// verifyStored는 저장된 방법을 E③ 다시 로그인으로 검증한 줄을 남긴다(제품 쓰기 경로 recordLoginMethodVerified 그대로).
func verifyStored(t *testing.T, r *productTargetRegistry, rec productTargetRecord, label string, m learnedlogin.Method, attempt string, at time.Time) {
	t.Helper()
	if _, err := r.recordLoginMethodVerified(rec.Target.ID, label, rec.Target.Revision, credentialOf(r, rec.Target.ID, label), m.Digest(), "run-e3",
		attempt, at); err != nil {
		t.Fatal("verified 줄 쓰기", err)
	}
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
		credential := credentialOf(r, rec.Target.ID, "tester-a")
		p.Auth.Accounts = []server.E2HTTPFileAuthAccount{{Label: "tester-a", Credential: server.E2HTTPFilePin{
			File: bindingCredentialName(rec.Target.ID, "tester-a", credential), SHA256: credential}}}
		for i := range p.Rules {
			p.Rules[i].Origin = rec.Target.Origin
		}
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

// G01·ST03: verified 방법만 쓴다. verified 방법이라도 원본 시도가 지금 원본 회차 보호 기록·탐색 경계 근거·감사 저널과 맞지
// 않으면(인계 실패 표지·기록 유실·근거 불일치·이전 회차·저장 줄과 회차 선택 불일치) 그 사유로 막고 9칸으로 넘어가지 않는다.
// 출처 재대조가 맞아도 verified가 없으면 쓰지 않는다(출처 수준은 verified를 대신하지 않음).
func TestSelectedCandidateLearnedLoginTrust(t *testing.T) {
	// 계정마다 verified 줄을 새로 쓰므로 하위 시험마다 대상을 새로 만든다. 9칸이 있어도 넘어가지 않는지 함께 본다.
	setup := func(t *testing.T) (*productTargetRegistry, productTargetRecord, string) {
		r, _, rec := learnedAccountTarget(t)
		if _, err := r.setTargetLogin("ARTEX", rec.Target.ID, diagnosisprep.TargetLogin{Method: "POST", Path: "/read/login", Format: "form",
			UsernameField: "email", PasswordField: "password", Cookies: []string{"session"}}); err != nil {
			t.Fatal(err)
		}
		return r, rec, server.CandidateAccountRevision("tester-a", credentialOf(r, rec.Target.ID, "tester-a"))
	}
	for _, tc := range []struct {
		name   string
		change func(*server.E2HTTPFilePolicy, *learnedlogin.Row, *learnedlogin.ExplorationEvidence)
		after  func(t *testing.T, runs string, stored *learnedlogin.Learned)
		want   string
		events []e2proxy.Event
	}{
		{name: "diagnosis stage audited journal", want: server.CandidateLoginTrustVerified, change: diagnosisSourceRow, events: []e2proxy.Event{diagnosisLoginResult(302)},
			after: func(_ *testing.T, _ string, stored *learnedlogin.Learned) { stored.Source.AttemptRef = "diagnosis-1" }},
		{name: "diagnosis stage journal other status", want: string(learnedlogin.ReasonTransportUnverified), change: diagnosisSourceRow,
			events: []e2proxy.Event{diagnosisLoginResult(401)}, after: func(_ *testing.T, _ string, stored *learnedlogin.Learned) { stored.Source.AttemptRef = "diagnosis-1" }},
		{name: "exploration observed", want: server.CandidateLoginTrustVerified},
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
		{name: "stored source time differs", want: string(learnedlogin.ReasonRecordInvalid), after: func(_ *testing.T, _ string, stored *learnedlogin.Learned) {
			stored.Source.At = stored.Source.At.Add(-time.Second)
		}},
		{name: "stored method digest differs", want: string(learnedlogin.ReasonRecordInvalid), after: func(_ *testing.T, _ string, stored *learnedlogin.Learned) {
			stored.Method.SessionNames = []string{"other"}
		}},
		{name: "account not bound in source run", want: string(learnedlogin.ReasonCredentialChanged), change: func(p *server.E2HTTPFilePolicy, _ *learnedlogin.Row, _ *learnedlogin.ExplorationEvidence) {
			p.Auth.Accounts[0].Credential.SHA256 = strings.Repeat("f", 64)
		}},
		{name: "source run belongs to other target", want: string(learnedlogin.ReasonCredentialChanged), change: func(p *server.E2HTTPFilePolicy, _ *learnedlogin.Row, _ *learnedlogin.ExplorationEvidence) {
			p.Auth.Accounts[0].Credential.File = "account-other-target.json"
		}},
		{name: "source run predates target approval", want: string(learnedlogin.ReasonRecordInvalid), change: func(p *server.E2HTTPFilePolicy, _ *learnedlogin.Row, _ *learnedlogin.ExplorationEvidence) {
			p.ValidFrom = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		}},
		{name: "source login uses other origin", want: string(learnedlogin.ReasonRecordInvalid), change: func(p *server.E2HTTPFilePolicy, _ *learnedlogin.Row, _ *learnedlogin.ExplorationEvidence) {
			p.Rules[1].Origin = "https://other.example.test"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, rec, ref := setup(t)
			runs, stored := selectedSourceRun(t, r, rec, tc.change, tc.events...)
			if tc.after != nil {
				tc.after(t, runs, &stored)
			}
			reader := storedMethodReader{learned: map[string]learnedlogin.Learned{"tester-a": stored}}
			if c := candidateLoginChoice(r, reader, runs, "ARTEX", rec.Target.ID, ref, ""); c.Source != diagnosisprep.CandidateLoginNone ||
				c.Reason != candidateReasonUnverified {
				t.Fatalf("verified 없이 출처 재대조만으로 씀: %+v", c)
			}
			verifyStored(t, r, rec, "tester-a", stored.Method, "confirmation-1", stored.Source.At.Add(time.Hour))
			choice := candidateLoginChoice(r, reader, runs, "ARTEX", rec.Target.ID, ref, "")
			got := choice.Trust
			if choice.Source == diagnosisprep.CandidateLoginNone {
				got = choice.Reason
			}
			if got != tc.want || choice.Source == diagnosisprep.CandidateLoginConfigured {
				t.Fatalf("선택 %+v, want %s", choice, tc.want)
			}
			if choice.Source == diagnosisprep.CandidateLoginLearned && (choice.Method == nil || choice.Method.Digest() != stored.Method.Digest() ||
				len(choice.VerifiedRef) != 64) {
				t.Fatal("다른 방법을 돌려주거나 검증 사건 참조가 없음")
			}
		})
	}
}

// G03·작업 P0 시각 결속: verified는 그 방법이 지금 것이 된 시각(since) 뒤의 같은 방법·지금 대상 판·자격 판 검증만이다.
// 같은 방법을 다시 배워 관찰 시각(at)만 늦어진 경우는 since 기준이라 그 사이의 검증도 인정하고, since 앞의 검증·다른 방법의
// 검증·같은 시각의 검증은 인정하지 않는다. since를 주지 않는 읽기 경계면 쓰지 않는다(at만으로 추정하지 않음).
func TestSelectedCandidateLearnedLoginVerifiedSince(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		// since·verified는 원본 관찰 시각(at) 기준 차이다.
		since, verified time.Duration
		legacy          bool
		want            string
	}{
		{name: "no verified", since: 0, want: candidateReasonUnverified},
		{name: "other method verified", method: "/other", verified: time.Hour, want: candidateReasonUnverified},
		{name: "verified before since", method: "/login", since: -time.Hour, verified: -2 * time.Hour, want: candidateReasonUnverified},
		{name: "verified at since", method: "/login", verified: 0, want: candidateReasonUnverified},
		{name: "relearned same method verified between since and at", method: "/login", since: -2 * time.Hour, verified: -time.Hour,
			want: server.CandidateLoginTrustVerified},
		{name: "verified after at", method: "/login", verified: time.Hour, want: server.CandidateLoginTrustVerified},
		{name: "reader without since", method: "/login", verified: time.Hour, legacy: true, want: candidateReasonVerificationUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, rec := learnedAccountTarget(t)
			runs, stored := selectedSourceRun(t, r, rec, nil)
			cred := credentialOf(r, rec.Target.ID, "tester-a")
			if tc.method != "" {
				verifyStored(t, r, rec, "tester-a", learnedMethod(tc.method), "confirmation-1", stored.Source.At.Add(tc.verified))
			}
			var reader productLearnedMethodReader = storedMethodReader{learned: map[string]learnedlogin.Learned{"tester-a": stored},
				since: map[string]time.Time{"tester-a": stored.Source.At.Add(tc.since)}}
			if tc.legacy {
				reader = methodOnlyReader{reader.(storedMethodReader)}
			}
			c := candidateLoginChoice(r, reader, runs, "ARTEX", rec.Target.ID, server.CandidateAccountRevision("tester-a", cred), "")
			got := c.Trust
			if c.Source == diagnosisprep.CandidateLoginNone {
				got = c.Reason
			}
			if got != tc.want || c.Source == diagnosisprep.CandidateLoginConfigured {
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
	if c := candidateLoginChoice(r, nil, runs, "ARTEX", rec.Target.ID, ref, ""); c.Source != diagnosisprep.CandidateLoginNone || c.Reason != string(learnedlogin.ReasonNone) {
		t.Fatalf("방법·설정 없음: %+v", c)
	}
	login := diagnosisprep.TargetLogin{Method: "POST", Path: "/read/login", Format: "form", UsernameField: "email", PasswordField: "password", Cookies: []string{"session"}}
	if _, err := r.setTargetLogin("ARTEX", rec.Target.ID, login); err != nil {
		t.Fatal(err)
	}
	if c := candidateLoginChoice(r, storedMethodReader{}, runs, "ARTEX", rec.Target.ID, ref, ""); c.Source != diagnosisprep.CandidateLoginConfigured || c.Login == nil || c.Login.Path != "/read/login" {
		t.Fatalf("학습 방법 없음 → 9칸: %+v", c)
	}
	for _, reason := range []learnedlogin.Reason{learnedlogin.ReasonTargetChanged, learnedlogin.ReasonCredentialChanged, learnedlogin.ReasonRecordInvalid} {
		if c := candidateLoginChoice(r, storedMethodReader{reason: reason}, runs, "ARTEX", rec.Target.ID, ref, ""); c.Source != diagnosisprep.CandidateLoginNone || c.Reason != string(reason) {
			t.Fatalf("%s에서 9칸으로 넘어감: %+v", reason, c)
		}
	}
	if c := candidateLoginChoice(r, nil, runs, "ARTEX", rec.Target.ID, strings.Repeat("0", 64), ""); c.Source != diagnosisprep.CandidateLoginNone || c.Reason != string(learnedlogin.ReasonCredentialChanged) {
		t.Fatalf("모르는 계정 참조: %+v", c)
	}
	if _, err := r.setAccountCredential("ARTEX", rec.Target.ID, "tester-a", diagnosisprep.AccountCredential{Username: "learned-user-a", Password: "rotated-pass-a"}); err != nil {
		t.Fatal(err)
	}
	if c := candidateLoginChoice(r, nil, runs, "ARTEX", rec.Target.ID, ref, ""); c.Source != diagnosisprep.CandidateLoginNone || c.Reason != string(learnedlogin.ReasonCredentialChanged) {
		t.Fatalf("자격 변경 뒤 옛 참조: %+v", c)
	}
	// G02: 학습 방법은 있지만 verified가 아니면(learned만) 9칸이 있어도 넘어가지 않는다.
	ref = server.CandidateAccountRevision("tester-a", credentialOf(r, rec.Target.ID, "tester-a"))
	learnedOnly := storedMethodReader{learned: map[string]learnedlogin.Learned{"tester-a": learnedAt("/login", "run-gone", time.Now().UTC())}}
	if c := candidateLoginChoice(r, learnedOnly, runs, "ARTEX", rec.Target.ID, ref, ""); c.Source != diagnosisprep.CandidateLoginNone || c.Reason != candidateReasonUnverified {
		t.Fatalf("learned만 있는데 9칸으로 넘어감: %+v", c)
	}
	if c := candidateLoginChoice(r, methodOnlyReader{learnedOnly}, runs, "ARTEX", rec.Target.ID, ref, ""); c.Source != diagnosisprep.CandidateLoginNone ||
		c.Reason != candidateReasonVerificationUnavailable {
		t.Fatalf("since 없는 읽기 경계에서 9칸으로 넘어감: %+v", c)
	}
}

// 실행 결속: 학습 로그인 계획은 그 계정 하나의 자격만 싣고(9칸 없는 대상 포함) 다른 계정 자격 사본은 지운다. 방법·출처
// 수준이 승인 뒤 바뀌면 싣지 않는다(AN15).
func TestSelectedCandidateLearnedAccountBinding(t *testing.T) {
	r, _, rec := learnedAccountTarget(t)
	cred := credentialOf(r, rec.Target.ID, "tester-a")
	ref := server.CandidateAccountRevision("tester-a", cred)
	runs, stored := selectedSourceRun(t, r, rec, nil)
	verifyStored(t, r, rec, "tester-a", stored.Method, "confirmation-1", stored.Source.At.Add(time.Hour))
	reader := storedMethodReader{learned: map[string]learnedlogin.Learned{"tester-a": stored}}
	b := &urlPreparationRuntime{product: &productRuntime{targets: r, learnedMethods: reader, config: urlPreparationInstallation{RunsDir: runs}}}
	prep := currentLearnedPreparation(t, b, rec.Target.ID, ref)
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
		l.policy.Auth.PasswordResponse != e2proxy.PasswordResponseModelVisible ||
		l.policy.SelectedAccountRef != ref || l.policy.HeaderMode != e2proxy.HeaderTestAccountPassthrough || len(l.productAccounts) != 1 {
		t.Fatalf("결속 결과 %+v", l.policy.Auth)
	}
	if _, ok := l.materials[otherFile]; ok || len(l.materials[pin]) == 0 || string(l.materials["ca.pem"]) != "ca" {
		t.Fatal("다른 계정 자격 사본이 남거나 선택 계정 자격을 싣지 않음")
	}
	changed := plan
	other := *prep
	other.VerifiedRef = strings.Repeat("e", 64)
	changed.AuthPreparation = &other
	if reason := b.bindCandidateLearnedAccount(&urlLoadedProfile{materials: map[string][]byte{}}, changed); reason != "candidate_auth_preparation_changed" {
		t.Fatalf("승인 뒤 바뀐 검증 사건을 실음: %q", reason)
	}
	// G03·ST12·ST13: 승인 뒤 verified가 해제되면 실행하지 않고, E③가 다시 검증해 검증 사건이 바뀌어도 옛 계획은 실행하지 않는다.
	if reason := r.reserveCandidateLogin(rec.Target.ID, "tester-a", stored.Method.Digest(), rec.Target.Revision, cred, stored.Source.At,
		plan.AuthPreparation.VerifiedRef, "candidate-90"); reason != "" {
		t.Fatalf("예약 실패: %s", reason)
	}
	if err := r.finishCandidateLogin(rec.Target.ID, "tester-a", "candidate-90", "", candidateLoginClearedFailed); err != nil {
		t.Fatal(err)
	}
	if reason := b.candidateLearnedLoginRefusal(plan, ""); reason != candidateReasonUnverified {
		t.Fatalf("verified 해제 뒤 옛 계획: %q", reason)
	}
	verifyStored(t, r, rec, "tester-a", stored.Method, "confirmation-2", stored.Source.At.Add(2*time.Hour))
	if b.candidateLearnedLoginCurrent(plan, "") {
		t.Fatal("다른 검증 사건으로 바뀐 뒤에도 옛 계획이 통과")
	}
	plan.AuthPreparation = currentLearnedPreparation(t, b, rec.Target.ID, ref)
	if err := os.WriteFile(filepath.Join(runs, liveJob, "state", learnedlogin.AttemptsUnavailableFile), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b.candidateLearnedLoginCurrent(plan, "") {
		t.Fatal("원본 회차 인계 실패 뒤에도 송신 직전 재검사를 통과")
	}
}

// currentLearnedPreparation은 지금 고른 verified 방법으로 만든 v2 인증 준비다(계획 생성과 같은 변환).
func currentLearnedPreparation(t *testing.T, b *urlPreparationRuntime, targetID, ref string) *server.CandidateLLMAuthPreparation {
	t.Helper()
	c := b.product.candidateLogin("ARTEX", targetID, ref, "")
	if c.Source != diagnosisprep.CandidateLoginLearned || c.Method == nil {
		t.Fatalf("verified 방법을 고르지 못함: %+v", c)
	}
	prep := server.CandidateLearnedAuthPreparation(server.CandidateLearnedLogin{Method: *c.Method, Trust: c.Trust, VerifiedRef: c.VerifiedRef})
	if prep == nil {
		t.Fatal("v2 인증 준비를 만들지 못함")
	}
	return prep
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

// AL10(단위): 로그인 설정 9칸이 없어 일반 결속에 계정이 없는 대상도, 학습 로그인 계획이면 그 계정 하나의 자격만 실어 실행한다.
func TestSelectedCandidateLearnedBindingWithoutConfiguredLogin(t *testing.T) {
	r, _, rec := learnedAccountTarget(t)
	cred := credentialOf(r, rec.Target.ID, "tester-a")
	ref := server.CandidateAccountRevision("tester-a", cred)
	runs, stored := selectedSourceRun(t, r, rec, nil)
	verifyStored(t, r, rec, "tester-a", stored.Method, "confirmation-1", stored.Source.At.Add(time.Hour))
	b := &urlPreparationRuntime{product: &productRuntime{targets: r, config: urlPreparationInstallation{RunsDir: runs},
		learnedMethods: storedMethodReader{learned: map[string]learnedlogin.Learned{"tester-a": stored}}}}
	if login, err := r.targetLogin("ARTEX", rec.Target.ID); err != nil || login != nil {
		t.Fatal("9칸 없는 대상 준비 실패")
	}
	prep := currentLearnedPreparation(t, b, rec.Target.ID, ref)
	l := &urlLoadedProfile{}
	if reason := b.bindCandidateLearnedAccount(l, server.CandidateLLMPlan{TargetID: rec.Target.ID, AccountRef: ref, AuthPreparation: prep}); reason != "" {
		t.Fatalf("9칸 없는 대상에서 학습 로그인 결속 실패: %s", reason)
	}
	if l.policy.Auth == nil || l.policy.Auth.LearnedMethod == nil || len(l.materials) != 1 || len(l.productAccounts) != 1 {
		t.Fatal("계정 하나의 자격만 싣지 않음")
	}
}
