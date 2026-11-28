//go:build linux

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"golang.org/x/crypto/bcrypt"
)

func TestSelectedCandidateActionsRealE2SyntheticRunnerPostgres(t *testing.T) {
	for _, test := range []struct {
		name    string
		action  CandidateLLMPlanAction
		allowed bool
	}{
		{"approved-exact-path", CandidateLLMPlanAction{Purpose: "Check independently", Method: "GET", ExactPath: "/documents"}, true},
		{"outside-exact-path", CandidateLLMPlanAction{Purpose: "Check independently", Method: "GET", ExactPath: "/blocked"}, false},
		{"approved-query-header", CandidateLLMPlanAction{Purpose: "Check structured input", Method: "GET", ExactPath: "/documents",
			Query: map[string]string{"mode": "review"}, Headers: map[string]string{"Accept": "application/json"}}, true},
	} {
		t.Run(test.name, func(t *testing.T) { runSelectedCandidateActionFixture(t, test.action, test.allowed, nil) })
	}
}

// selectedFixtureAccount는 선택 계정 회차(#950)의 합성 시험 계정이다. 대상 /documents는 로그인한 사용자의 비밀번호를
// 프로필처럼 되돌려 준다.
type selectedFixtureAccount struct {
	passwordResponse, password string
}

func runSelectedCandidateActionFixture(t *testing.T, action CandidateLLMPlanAction, wantAllowed bool, account *selectedFixtureAccount) []candidateverify.StoredObservationV2 {
	helper := firstDiagnosisNativeHelper(t)
	f := newControlledLocalDatabaseFixture(t)
	installer := &db.DB{DB: f.Installer}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	manager, err := NewFirstDiagnosisManager(ctx, t.TempDir(), f.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	task, err := manager.CreateTask("synthetic selected action", "one bounded read", nil, 60, 60)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installer.ExecContext(ctx, `UPDATE tasks SET paused=true,queued=false WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	task.updateLifecycle(func(s *taskLifecycleState) { s.Paused = true; s.Queued = false })
	hash, err := bcrypt.GenerateFromPassword([]byte("synthetic-candidate-login"), bcrypt.MinCost)
	if err != nil || installer.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("synthetic login setting unavailable")
	}
	var allowed, blocked, logins atomic.Int64
	const session = "synthetic-selected-fixture-session"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if account != nil && r.URL.Path == "/login" && r.Method == http.MethodPost {
			logins.Add(1)
			if r.ParseForm() != nil || r.Form.Get("email") != "tester@owned.test" || r.Form.Get("password") != account.password {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: session})
			_, _ = io.WriteString(w, "signed in")
			return
		}
		if account != nil && r.Header.Get("Cookie") != "session="+session {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path == "/blocked" {
			blocked.Add(1)
		}
		if r.Method != http.MethodGet || r.URL.Path != "/documents" ||
			action.Query != nil && r.URL.RawQuery != selectedActionQuery(action.Query) ||
			action.Headers != nil && r.Header.Get("Accept") != action.Headers["Accept"] {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		allowed.Add(1)
		if account != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"email":"tester@owned.test","password":"`+account.password+`"}`)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>synthetic fixture</body></html>"))
	}))
	defer target.Close()
	cfg, grant, policy := e2HostPolicyFixture(t)
	grant.TaskID, grant.ExplorationID = mustFirstDiagnosisTaskNumber(task.ID), task.ExpID
	binary, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	policy.Helper, policy.HelperSHA256 = helper, hex.EncodeToString(digest[:])
	policy.StateDir = filepath.Join(firstDiagnosisPrivateTemp(t), "state")
	if os.Mkdir(policy.StateDir, 0700) != nil {
		t.Fatal("synthetic E2 state unavailable")
	}
	policy.OwnerRef = "ARTEX"
	policy.ApprovalMode = e2proxy.ApprovalPreauthorizedScope
	policy.Rules = []E2HTTPFileRule{{Ref: "candidate-exact", Origin: target.URL, ConnectIP: "127.0.0.1", PathPrefix: "/documents", PathMatch: "exact",
		ExactQuery: selectedActionQuery(action.Query), RequestHeaders: action.Headers, StrictHeaders: true, PathScopeRef: "candidate-exact", Methods: []string{"GET"}}}
	policy.MaxRequests, policy.Limits.MaxAttempts, policy.Limits.MaxCalls = 1, 1, 1
	plan := CandidateLLMPlan{Version: "candidate-llm-plan/v1", Origin: target.URL, Actions: []CandidateLLMPlanAction{action}, MaxRequests: 1}
	if account != nil {
		// 제품 선택 후보 정책 모양(cmd bindCandidateLLMAccount): 선택 계정 하나, 로그인 경로 하나의 인증 준비 규칙, 시험 계정
		// 헤더 모드, 모델 로그인 표시와 판정 설정은 원래 제품 결속 그대로. 자격은 합성 값이다.
		login := cookieAuth().Login
		policy.Rules = append(policy.Rules, E2HTTPFileRule{Ref: "auth-login", Origin: target.URL, ConnectIP: "127.0.0.1", PathPrefix: login.Path,
			PathScopeRef: "auth-login-path", Methods: []string{login.Method}, Purpose: e2proxy.PurposeAuthPrep})
		pin := authCredentialPin(t, cfg.AuthorityRoot, "selected-fixture-account.json", "tester@owned.test", account.password)
		auth := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
		auth.PasswordResponse, auth.ModelLogin = account.passwordResponse, true
		policy.Auth, policy.HeaderMode = &auth, e2proxy.HeaderTestAccountPassthrough
		policy.LoginAttempts = &E2HTTPLoginAttemptLimits{MaxConsecutiveFailures: 3, MaxContrastProbes: 3, TokenMinBytes: 16}
		policy.SelectedAccountRef = CandidateAccountRevision("tester-a", pin.SHA256)
		policy.MaxRequests, policy.Limits.MaxAttempts, policy.Limits.MaxCalls = 2, 2, 2
		plan.AccountRef, plan.MaxRequests = policy.SelectedAccountRef, 2
		plan.AuthPreparation = &CandidateLLMAuthPreparation{Method: login.Method, ExactPath: login.Path, LoginDigest: CandidateLoginDigest(login)}
	}
	policy.Limits.MaxConcurrent = 1
	policy.Limits.CallTimeoutMS, policy.Limits.RequestTimeoutMS, policy.Proxy.RequestTimeoutMS = 20000, 15000, 15000
	policy.Runtime.WorkerTimeoutMS = 30000
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0600) != nil {
		t.Fatal("synthetic E2 policy unavailable")
	}
	if _, _, err := ValidateE2HTTPRunPolicyDocument(raw); err != nil {
		t.Fatal("synthetic E2 policy invalid", err)
	}
	digest = sha256.Sum256(raw)
	grant.PolicySHA256 = hex.EncodeToString(digest[:])
	e2HostWriteApproval(t, cfg, grant)
	skillDir, dataDir, keyDir := firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t)
	if os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), []byte("synthetic-candidate-jwt-material"), 0600) != nil {
		t.Fatal("synthetic JWT unavailable")
	}
	manifest := firstDiagnosisSupervisorModelManifest(t, installer)
	_ = firstDiagnosisPinFixtureTaskProfile(t, installer, task)
	observations, err := ExecuteSelectedCandidateActions(ctx, manager, FirstDiagnosisHTTPStartOptions{
		Host: cfg, SkillDir: skillDir, DataDir: dataDir, KeyDir: keyDir,
		LocalLLMManifestPath: manifest, Supervisor: &candidateGETTestSupervisor{taskID: task.ID}, CleanupTimeout: 5 * time.Second,
	}, mustFirstDiagnosisTaskNumber(task.ID), policy.RunRef, plan)
	if account != nil && logins.Load() != 1 {
		t.Fatalf("selected account login count=%d", logins.Load())
	}
	if !wantAllowed {
		// 계정 변형의 실패 사례는 승인 범위 안 요청이 실제로 나간 뒤 관찰을 잃는 경우다(보낸 건수 1).
		if err == nil || blocked.Load() != 0 || len(observations) != 0 || account == nil && allowed.Load() != 0 || account != nil && allowed.Load() != 1 {
			t.Fatalf("out-of-policy action sent or accepted: allowed=%d blocked=%d observations=%d err=%v", allowed.Load(), blocked.Load(), len(observations), err)
		}
		return nil
	}
	if err != nil || allowed.Load() != 1 || blocked.Load() != 0 || len(observations) != 1 {
		t.Fatalf("candidate E2 action: allowed=%d blocked=%d observations=%d err=%v", allowed.Load(), blocked.Load(), len(observations), err)
	}
	if observations[0].Source.Observation.Endpoint != "/documents" || !observations[0].Source.Observation.AuditComplete ||
		!selectedActionMatchesObservation(action, observations[0].Source.Observation) {
		t.Fatal("candidate E2 observation incomplete")
	}
	if account != nil && (observations[0].Source.Observation.RequestAccountRef != policy.SelectedAccountRef ||
		observations[0].Source.Observation.RequestAuthContext != "cookie" || observations[0].HeaderMode != e2proxy.HeaderTestAccountPassthrough) {
		t.Fatalf("selected account not bound to the actual send: %+v", observations[0])
	}
	return observations
}

// 선택 계정 후보 검증 회차를 실제 러너·E²·PostgreSQL로 끝까지 돌린다(합성 자격·소유 httptest 대상). 일반 제품 진단과 같은
// model_visible_v1이면 짧은 비밀번호 계정도 로그인하고, 비밀번호를 비춘 응답도 완료 관찰로 남아 최종 판정 모델 입력에
// 들어간다. 이전 mask_spans_v1은 같은 응답을 보류로 바꿔 관찰 투영에서 빠지므로 검증 회차 전체가 실패했다(DLP 폐기와 어긋남).
// 관찰은 응답 본문을 담지 않으므로 어느 정책에서도 최종 판정 모델 입력에 비밀번호가 새로 들어가지 않는다.
func TestSelectedCandidateAccountPasswordPolicyRealE2SyntheticRunnerPostgres(t *testing.T) {
	action := CandidateLLMPlanAction{Purpose: "Check independently", Method: "GET", ExactPath: "/documents"}
	for _, test := range []struct {
		name    string
		account selectedFixtureAccount
		allowed bool
	}{
		{"model-visible", selectedFixtureAccount{e2proxy.PasswordResponseModelVisible, "seeded-pass-88ff"}, true},
		{"model-visible-short-password", selectedFixtureAccount{e2proxy.PasswordResponseModelVisible, "abc12"}, true},
		{"legacy-mask-spans-loses-observation", selectedFixtureAccount{e2proxy.PasswordResponseMaskSpans, "seeded-pass-88ff"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			observations := runSelectedCandidateActionFixture(t, action, test.allowed, &test.account)
			if !test.allowed {
				return
			}
			c := syntheticCandidateAssessmentContext(t)
			c.Followup = observations
			request, err := BuildCandidateFinalRequest(c, candidateFinalMaxTokens)
			if err != nil || len(request.Messages) != 1 {
				t.Fatalf("final model input unavailable: %v", err)
			}
			input, _ := json.Marshal(request)
			if !strings.Contains(string(input), observations[0].Source.Observation.RequestAccountRef) {
				t.Fatal("selected account revision missing from final model input")
			}
			if strings.Contains(string(input), test.account.password) || strings.Contains(string(input), "synthetic-selected-fixture-session") {
				t.Fatal("final model input gained a credential that its observation never carries")
			}
		})
	}
}
