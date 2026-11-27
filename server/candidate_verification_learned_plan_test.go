package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/learnedlogin"
	"github.com/Autumn-27/artex/report"
)

// 인증 준비 변경과 로그인 실패는 실행 사건으로 보존된다. 조회 검증기가 이 사유를 거부하면
// 정상 기록된 선택 이력도 제품 API에서 503이 된다(S04 회귀).
func TestSelectedCandidateHistoryAcceptsAuthFailureReasons(t *testing.T) {
	now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	plan, _, _, _ := learnedPlanFixture(now, CandidateLearnedAuthPreparation(CandidateLearnedLogin{
		Method: selectedTokenMethod(), Trust: CandidateLoginTrustVerified,
	}))
	digest, err := candidateLLMPlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	candidate := db.DiagnosticCandidate{ID: plan.CandidateID, TaskID: plan.TaskID, RunRef: plan.OriginalRunRef,
		EvidenceVersion: 1, EvidenceDigest: plan.EvidenceDigest}
	row := candidateVerificationAttempt{ID: "8", CandidateID: fmt.Sprint(candidate.ID), RequestKey: "learned-failure",
		Method: "llm/v1", OriginalDigest: candidate.EvidenceDigest, OriginalVersion: candidate.EvidenceVersion,
		OriginalRunRef: candidate.RunRef, RequestedAt: now.Add(-time.Second), LLMPlan: &plan, PlanDigest: digest,
		Latest: candidateVerificationEvent{State: "failed", RuleVersion: diagnosisVerificationSelected, Sources: json.RawMessage(`[]`),
			SourcesDigest: "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945", At: now}}
	binding := candidateVerificationBinding{Decision: sql.NullString{String: "approved", Valid: true},
		DecisionDigest: sql.NullString{String: digest, Valid: true}}
	for _, step := range []int{6, 7} {
		for _, reason := range []string{"candidate_auth_preparation_changed", "candidate_auth_failed", "candidate_auth_result_unknown", "candidate_auth_not_sent"} {
			row.Latest.Reason = reason
			if err := validateCandidateVerificationAttempt(row, step, binding, candidate, report.CandidateAssessment{}); err != nil {
				t.Errorf("step %d reason %s rejected: %v", step, reason, err)
			}
		}
	}
	row.Latest.Reason = "arbitrary_failure"
	if validateCandidateVerificationAttempt(row, 6, binding, candidate, report.CandidateAssessment{}) == nil {
		t.Fatal("unknown failure reason accepted")
	}
}

func learnedPlanFixture(now time.Time, a *CandidateLLMAuthPreparation) (CandidateLLMPlan, diagnosisprep.Input, diagnosisprep.Scope, diagnosisprep.ProductTarget) {
	approved := now.Add(-time.Minute)
	origin := "https://synthetic.test:9443"
	target := diagnosisprep.ProductTarget{ID: "owned-target", Origin: origin, State: "approved", Revision: 3,
		PathPrefixes: []string{"/api", "/session", "/signin", "=/rest/items"}, Methods: []string{"GET", "HEAD", "POST"},
		ApprovedAt: approved, ExpiresAt: now.Add(time.Hour)}
	scope := diagnosisprep.Scope{TargetRef: target.ID, Origin: origin, PathPrefixes: []string{"=/rest/items"},
		Methods: []string{"GET", "HEAD"}, ExpiresAt: target.ExpiresAt}
	plan := CandidateLLMPlan{Version: candidateLLMPlanV2, TaskID: 1, CandidateID: 5,
		EvidenceDigest: strings.Repeat("a", 64), AnalysisDigest: strings.Repeat("b", 64),
		OriginalRunRef: "original-run", AccountRef: strings.Repeat("c", 64),
		TargetID: target.ID, TargetRevision: target.Revision, Origin: origin,
		ApprovedAt: approved, ExpiresAt: now.Add(10 * time.Minute), DurationSeconds: 90, MaxRequests: 1 + CandidateLLMAuthRequests(a),
		AuthPreparation: a,
		Actions:         []CandidateLLMPlanAction{{Purpose: "Repeat authenticated read", Method: "GET", ExactPath: "/rest/items"}}}
	return plan, diagnosisprep.Input{TargetURL: origin + "/rest/items"}, scope, target
}

// 기존 v1 계획(9칸)의 JSON·digest는 새 칸이 생겨도 바뀌지 않는다(저장·승인된 계획이 재조회만으로 다른 뜻이 되지 않음).
func TestSelectedLLMPlanV1DigestUnchanged(t *testing.T) {
	type legacyAuth struct {
		Method      string `json:"method"`
		ExactPath   string `json:"exact_path"`
		LoginDigest string `json:"login_digest"`
	}
	type legacyPlan struct {
		Version         string                   `json:"version"`
		TaskID          int64                    `json:"task_id"`
		CandidateID     int64                    `json:"candidate_id"`
		EvidenceDigest  string                   `json:"evidence_digest"`
		OriginalRunRef  string                   `json:"original_run_ref"`
		AnalysisDigest  string                   `json:"analysis_digest"`
		AccountRef      string                   `json:"account_ref,omitempty"`
		AuthPreparation *legacyAuth              `json:"auth_preparation,omitempty"`
		TargetID        string                   `json:"target_id"`
		TargetRevision  uint64                   `json:"target_revision"`
		Origin          string                   `json:"origin"`
		ExcludedPaths   []string                 `json:"excluded_paths,omitempty"`
		Actions         []CandidateLLMPlanAction `json:"actions"`
		MaxRequests     int                      `json:"max_requests"`
		DurationSeconds int                      `json:"duration_seconds"`
		ApprovedAt      time.Time                `json:"target_approved_at"`
		ExpiresAt       time.Time                `json:"expires_at"`
	}
	now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	plan, _, _, _ := learnedPlanFixture(now, &CandidateLLMAuthPreparation{Method: "POST", ExactPath: "/signin", LoginDigest: strings.Repeat("d", 64)})
	plan.Version = candidateLLMPlanV1
	legacy := legacyPlan{plan.Version, plan.TaskID, plan.CandidateID, plan.EvidenceDigest, plan.OriginalRunRef, plan.AnalysisDigest, plan.AccountRef,
		&legacyAuth{"POST", "/signin", strings.Repeat("d", 64)}, plan.TargetID, plan.TargetRevision, plan.Origin, plan.ExcludedPaths, plan.Actions,
		plan.MaxRequests, plan.DurationSeconds, plan.ApprovedAt, plan.ExpiresAt}
	raw, _ := json.Marshal(legacy)
	sum := sha256.Sum256(raw)
	got, err := candidateLLMPlanDigest(plan)
	if err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatal("기존 v1 계획 digest가 바뀜")
	}
	var anonymous = plan
	anonymous.AccountRef, anonymous.AuthPreparation, anonymous.MaxRequests = "", nil, 1
	encoded, _ := json.Marshal(anonymous)
	for _, key := range []string{"kind", "prep_method", "prep_path", "requests", "trust", "auth_preparation"} {
		if strings.Contains(string(encoded), `"`+key+`"`) {
			t.Fatalf("익명 v1 계획에 새 칸 %s가 생김", key)
		}
	}
}

// 학습 방법마다 인증 준비 유형·준비 요청이 정해진다: 같은 경로 준비(v1), 별도 경로 CSRF(v2), 준비 없음(v1, POST 1).
// 방법 digest가 login_digest이고 값은 없다.
func TestSelectedLearnedAuthPreparationShapes(t *testing.T) {
	for _, tc := range []struct {
		name           string
		method         learnedlogin.Method
		kind, prepPath string
		requests       int
	}{
		{"same path form", selectedFormCSRFMethod(), CandidateAuthLearnedV1, "/signin", 2},
		{"separate JSON CSRF", selectedJSONCSRFMethod(), CandidateAuthLearnedV2, "/api/csrf", 2},
		{"separate hidden CSRF", selectedHiddenCSRFMethod(), CandidateAuthLearnedV2, "/session/new", 2},
		{"token", selectedTokenMethod(), CandidateAuthLearnedV1, "", 1},
	} {
		a := CandidateLearnedAuthPreparation(CandidateLearnedLogin{Method: tc.method, Trust: CandidateLoginTrustExplorationObserved})
		if a == nil || a.Kind != tc.kind || a.PrepPath != tc.prepPath || a.Requests != tc.requests || a.LoginDigest != tc.method.Digest() ||
			a.ExactPath != tc.method.Path || (a.PrepPath != "") != (a.PrepMethod == "GET") {
			t.Errorf("%s: %+v", tc.name, a)
		}
		now := time.Now().UTC()
		plan, original, scope, target := learnedPlanFixture(now, a)
		if !candidateLLMPlanStillApproved(now, plan, original, scope, target) {
			t.Errorf("%s: 승인 범위 안 계획을 거부", tc.name)
		}
		raw, _ := json.Marshal(plan)
		if strings.Contains(string(raw), "seeded") || strings.Contains(string(raw), "csrf-token") {
			t.Errorf("%s: 계획에 값이 들어감", tc.name)
		}
	}
	if CandidateLearnedAuthPreparation(CandidateLearnedLogin{Method: selectedTokenMethod(), Trust: "learned"}) != nil {
		t.Fatal("출처 수준이 정책 밖인 방법으로 계획을 만듦")
	}
	bad := selectedTokenMethod()
	bad.Method = "PUT"
	if CandidateLearnedAuthPreparation(CandidateLearnedLogin{Method: bad, Trust: CandidateLoginTrustVerified}) != nil {
		t.Fatal("재현할 수 없는 방법으로 계획을 만듦")
	}
}

// AN07·AN08·AN13: 송신 직전마다 보는 계획 재검사는 준비 GET·로그인 POST의 정확한 경로·메서드·제외 경로·대상 승인 경로·위험
// 단어·요청 수를 지금 대상 승인으로 다시 본다.
func TestSelectedLearnedPlanStillApprovedRechecksPreparation(t *testing.T) {
	now := time.Now().UTC()
	base := CandidateLearnedAuthPreparation(CandidateLearnedLogin{Method: selectedJSONCSRFMethod(), Trust: CandidateLoginTrustExplorationObserved})
	for name, change := range map[string]func(*CandidateLLMPlan, *diagnosisprep.Input, *diagnosisprep.ProductTarget){
		"prep excluded": func(p *CandidateLLMPlan, in *diagnosisprep.Input, _ *diagnosisprep.ProductTarget) {
			p.ExcludedPaths, in.ExcludedPaths = []string{"/api/csrf"}, "/api/csrf"
		},
		"login excluded": func(p *CandidateLLMPlan, in *diagnosisprep.Input, _ *diagnosisprep.ProductTarget) {
			p.ExcludedPaths, in.ExcludedPaths = []string{"/api/login"}, "/api/login"
		},
		"prep outside target": func(_ *CandidateLLMPlan, _ *diagnosisprep.Input, t *diagnosisprep.ProductTarget) {
			t.PathPrefixes = []string{"=/api/login", "=/rest/items"}
		},
		"target without POST": func(_ *CandidateLLMPlan, _ *diagnosisprep.Input, t *diagnosisprep.ProductTarget) {
			t.Methods = []string{"GET", "HEAD"}
		},
		"risky login path": func(p *CandidateLLMPlan, _ *diagnosisprep.Input, _ *diagnosisprep.ProductTarget) {
			p.AuthPreparation.ExactPath = "/api/account/delete"
		},
		"budget mismatch": func(p *CandidateLLMPlan, _ *diagnosisprep.Input, _ *diagnosisprep.ProductTarget) { p.MaxRequests-- },
		"unplanned prep": func(p *CandidateLLMPlan, _ *diagnosisprep.Input, _ *diagnosisprep.ProductTarget) {
			p.AuthPreparation.Requests, p.AuthPreparation.PrepMethod, p.AuthPreparation.PrepPath = 1, "", ""
			p.MaxRequests--
		},
		"v2 prep on login path": func(p *CandidateLLMPlan, _ *diagnosisprep.Input, _ *diagnosisprep.ProductTarget) {
			p.AuthPreparation.PrepPath = p.AuthPreparation.ExactPath
		},
		"untrusted": func(p *CandidateLLMPlan, _ *diagnosisprep.Input, _ *diagnosisprep.ProductTarget) {
			p.AuthPreparation.Trust = "learned"
		},
		"v1 version with learned": func(p *CandidateLLMPlan, _ *diagnosisprep.Input, _ *diagnosisprep.ProductTarget) {
			p.Version = candidateLLMPlanV1
		},
		"v2 version with configured": func(p *CandidateLLMPlan, _ *diagnosisprep.Input, _ *diagnosisprep.ProductTarget) {
			p.AuthPreparation = &CandidateLLMAuthPreparation{Method: "POST", ExactPath: "/api/login", LoginDigest: strings.Repeat("d", 64)}
			p.MaxRequests = 2
		},
		"target reapproved": func(_ *CandidateLLMPlan, _ *diagnosisprep.Input, t *diagnosisprep.ProductTarget) { t.Revision++ },
	} {
		a := *base
		plan, original, scope, target := learnedPlanFixture(now, &a)
		change(&plan, &original, &target)
		if candidateLLMPlanStillApproved(now, plan, original, scope, target) {
			t.Errorf("%s: 계획을 받음", name)
		}
	}
}

func TestSelectedCandidateConsecutiveAuthFailures(t *testing.T) {
	for _, tc := range []struct {
		reasons []string
		want    int
	}{
		{nil, 0},
		{[]string{"candidate_auth_failed", "candidate_auth_result_unknown", "candidate_auth_failed"}, 3},
		{[]string{"candidate_auth_failed", "execution_started", "candidate_auth_failed"}, 1},
		{[]string{"model_supported", "candidate_auth_failed"}, 0},
		{[]string{"candidate_auth_not_sent", "candidate_auth_failed"}, 0},
	} {
		if got := consecutiveCandidateAuthFailures(tc.reasons); got != tc.want {
			t.Errorf("%v: %d, want %d", tc.reasons, got, tc.want)
		}
	}
}

// 계획 API는 보호 준비 서비스의 인증 준비 원천을 그대로 옮긴다: 학습 방법(출처 수준 포함), 9칸, 막힘 사유. 계약에 맞지 않는
// 응답(정책 밖 출처 수준)은 계획하지 않는다.
func TestSelectedCandidatePlanAuthLookup(t *testing.T) {
	m := selectedJSONCSRFMethod()
	login := diagnosisprep.TargetLogin{Method: "POST", Path: "/login", Format: "form", UsernameField: "email", PasswordField: "password", Cookies: []string{"session"}}
	ref := strings.Repeat("c", 64)
	for _, tc := range []struct {
		name    string
		choice  diagnosisprep.ProductCandidateLogin
		learned bool
		config  bool
		reason  string
	}{
		{"learned", diagnosisprep.ProductCandidateLogin{Source: diagnosisprep.CandidateLoginLearned, Trust: CandidateLoginTrustExplorationObserved, Method: &m}, true, false, ""},
		{"configured", diagnosisprep.ProductCandidateLogin{Source: diagnosisprep.CandidateLoginConfigured, Login: &login}, false, true, ""},
		{"untrusted", diagnosisprep.ProductCandidateLogin{Source: diagnosisprep.CandidateLoginNone, Reason: "learned_login_transport_unverified"}, false, false, "learned_login_transport_unverified"},
		{"invalid trust", diagnosisprep.ProductCandidateLogin{Source: diagnosisprep.CandidateLoginLearned, Trust: "learned", Method: &m}, false, false, ""},
	} {
		endpoint := candidateLoginEndpoint{choice: tc.choice, ref: ref}
		auth, reason := readCandidatePlanAuth(t.Context(), endpoint, "ARTEX", "owned-target", ref)
		if (auth != nil && auth.Learned != nil) != tc.learned || (auth != nil && auth.Configured != nil) != tc.config || reason != tc.reason {
			t.Errorf("%s: %+v %q", tc.name, auth, reason)
		}
	}
}

type candidateLoginEndpoint struct {
	choice diagnosisprep.ProductCandidateLogin
	ref    string
}

func (e candidateLoginEndpoint) Call(_ context.Context, req diagnosisprep.Request) (diagnosisprep.Response, error) {
	if req.Product == nil || req.Product.Operation != "target_login_candidate" || req.Product.AccountRef != e.ref {
		return diagnosisprep.Response{Version: req.Version, RequestID: req.RequestID, Error: "invalid_request"}, nil
	}
	choice := e.choice
	return diagnosisprep.Response{Version: req.Version, RequestID: req.RequestID, Product: &diagnosisprep.ProductResponse{CandidateLogin: &choice}}, nil
}

// 연속 실패 집계 질의는 실제 스키마(계획 jsonb의 account_ref·실행·최근 사건)에서 돈다. 실행이 없으면 0이다.
func TestSelectedCandidateRecentAuthFailuresQueryPostgres(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	tx, err := s.m.pg.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if n, err := candidateRecentAuthFailures(t.Context(), tx, "owned-target", strings.Repeat("c", 64), candidateSelectedLoginMaxFailures); err != nil || n != 0 {
		t.Fatalf("집계 질의: %d %v", n, err)
	}
}

// 화면 인계용 합성 fixture(docs/contracts/fixtures/selected-candidate-learned-login.json)는 실제 코드로 만든 값과 같아야
// 한다. ARTEX_UPDATE_CONTRACT_FIXTURE=1이면 다시 쓴다.
func TestSelectedCandidateLearnedLoginFixture(t *testing.T) {
	now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	learned := CandidateLearnedAuthPreparation(CandidateLearnedLogin{Method: selectedJSONCSRFMethod(), Trust: CandidateLoginTrustExplorationObserved})
	plan, _, _, _ := learnedPlanFixture(now, learned)
	digest, err := candidateLLMPlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	token := CandidateLearnedAuthPreparation(CandidateLearnedLogin{Method: selectedTokenMethod(), Trust: CandidateLoginTrustVerified})
	fixture := map[string]any{
		"schema":      "jindo.selected-candidate-learned-login.fixture/v1",
		"note":        "합성 예시다. 값(아이디·비밀번호·세션·CSRF)은 계획·이력·모델 입력에 없다.",
		"plan_v2":     plan,
		"plan_digest": digest,
		"auth_preparation_examples": map[string]any{
			"learned_login_v2_separate_csrf": learned,
			"learned_login_v1_token":         token,
			"configured_login_v1":            CandidateLLMAuthPreparation{Method: "POST", ExactPath: "/login", LoginDigest: strings.Repeat("d", 64)},
		},
		"plan_errors": []map[string]string{
			{"error": "candidate_auth_preparation_unavailable"},
			{"error": "candidate_learned_login_unusable", "reason": "learned_login_transport_unverified"},
			{"error": "candidate_learned_login_unusable", "reason": "login_attempts_exhausted"},
			{"error": "verification_plan_changed"},
		},
		"execution_failure_reasons": []string{"candidate_auth_preparation_changed", "candidate_auth_failed", "candidate_auth_result_unknown", "candidate_auth_not_sent"},
	}
	raw, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	path := filepath.Join("..", "docs", "contracts", "fixtures", "selected-candidate-learned-login.json")
	if os.Getenv("ARTEX_UPDATE_CONTRACT_FIXTURE") == "1" {
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := os.ReadFile(path)
	if err != nil || string(stored) != string(raw) {
		t.Fatal("계약 fixture가 실제 코드 값과 다름(ARTEX_UPDATE_CONTRACT_FIXTURE=1로 다시 만들기)")
	}
}

// 계획 결속 트리거는 llm/v1 요청에 v1(9칸)과 v2(학습 로그인) 계획만 받는다. 다른 판본은 거부한다(제품 S11에서 v2가 막혔던
// 결함의 회귀 시험).
func TestCandidateLLMPlanBindAcceptsLearnedV2Postgres(t *testing.T) {
	s, f, _ := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	taskID := *stored.TaskID
	if err := saveDiagnosisSelectedVerificationPolicy(t.Context(), s.m.pg.DB, taskID, passiveRunRef); err != nil {
		t.Fatal(err)
	}
	storePassiveCandidate(t, s, taskID, "", "", "")
	rows, err := s.m.ResolveTask(fmt.Sprint(taskID)).Store.ListDiagnosticCandidates(taskID, passiveRunRef, 2)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	candidate := rows[0]
	const empty = "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"
	analysisDigest := strings.Repeat("b", 64)
	var requestID int64
	if err := s.m.pg.QueryRowContext(t.Context(), `INSERT INTO diagnostic_candidate_verification_requests(task_id,candidate_id,original_run_ref,original_evidence_version,original_evidence_digest,requester,request_key,method)
VALUES($1,$2,$3,$4,$5,'ARTEX','synthetic-llm-plan-v2','llm/v1') RETURNING id`, taskID, candidate.ID, candidate.RunRef, candidate.EvidenceVersion, candidate.EvidenceDigest).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources_digest,rule_version) VALUES($1,1,'requested','selected_by_user',$2,'selected/llm-v1')`, []any{requestID, empty}},
		{`INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources_digest,rule_version) VALUES($1,2,'checking_stored_evidence','analysis_started',$2,'selected/llm-v1')`, []any{requestID, empty}},
		{`INSERT INTO diagnostic_candidate_verification_analyses(request_id,proposal,proposal_digest,profile_id,model_requests,input_tokens,output_tokens,cost_usd_micros) VALUES($1,'{}'::jsonb,$2,1,1,0,0,0)`, []any{requestID, analysisDigest}},
		{`INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources_digest,rule_version) VALUES($1,3,'analysis_ready','analysis_available',$2,'selected/llm-v1')`, []any{requestID, empty}},
	} {
		if _, err := s.m.pg.ExecContext(t.Context(), step.sql, step.args...); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(version string) error {
		plan, _ := json.Marshal(map[string]any{"version": version, "task_id": taskID, "candidate_id": candidate.ID, "evidence_digest": candidate.EvidenceDigest,
			"original_run_ref": candidate.RunRef, "analysis_digest": analysisDigest, "target_id": "owned-target", "target_revision": 3})
		_, err := s.m.pg.ExecContext(t.Context(), `INSERT INTO diagnostic_candidate_verification_plans(request_id,plan,plan_digest,target_id,target_revision,expires_at)
VALUES($1,$2::jsonb,$3,'owned-target',3,$4)`, requestID, plan, strings.Repeat("c", 64), time.Now().Add(10*time.Minute))
		return err
	}
	if err := insert("candidate-llm-plan/v9"); err == nil {
		t.Fatal("알 수 없는 계획 판본을 받음")
	}
	if err := insert(candidateLLMPlanV2); err != nil {
		t.Fatalf("학습 로그인 계획 v2를 거부: %v", err)
	}
}
