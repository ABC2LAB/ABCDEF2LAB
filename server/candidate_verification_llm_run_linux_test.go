//go:build linux

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"golang.org/x/crypto/bcrypt"
)

func TestSelectedCandidateActionsRealE2SyntheticRunnerPostgres(t *testing.T) {
	for _, test := range []struct {
		name, path string
		allowed    bool
	}{
		{"approved-exact-path", "/documents", true},
		{"outside-exact-path", "/blocked", false},
	} {
		t.Run(test.name, func(t *testing.T) { runSelectedCandidateActionFixture(t, test.path, test.allowed) })
	}
}

func runSelectedCandidateActionFixture(t *testing.T, actionPath string, wantAllowed bool) {
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
	var allowed, blocked atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/blocked" {
			blocked.Add(1)
		}
		if r.Method != http.MethodGet || r.URL.Path != "/documents" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		allowed.Add(1)
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
	policy.Rules = []E2HTTPFileRule{{Ref: "candidate-exact", Origin: target.URL, ConnectIP: "127.0.0.1", PathPrefix: "/documents", PathMatch: "exact", PathScopeRef: "candidate-exact", Methods: []string{"GET"}}}
	policy.MaxRequests, policy.Limits.MaxAttempts, policy.Limits.MaxCalls = 1, 1, 1
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
	plan := CandidateLLMPlan{Version: "candidate-llm-plan/v1", Origin: target.URL, Actions: []CandidateLLMPlanAction{{Purpose: "Check independently", Method: "GET", ExactPath: actionPath}}, MaxRequests: 1}
	observations, err := ExecuteSelectedCandidateActions(ctx, manager, FirstDiagnosisHTTPStartOptions{
		Host: cfg, SkillDir: skillDir, DataDir: dataDir, KeyDir: keyDir,
		LocalLLMManifestPath: manifest, Supervisor: &candidateGETTestSupervisor{taskID: task.ID}, CleanupTimeout: 5 * time.Second,
	}, mustFirstDiagnosisTaskNumber(task.ID), policy.RunRef, plan)
	if !wantAllowed {
		if err == nil || allowed.Load() != 0 || blocked.Load() != 0 || len(observations) != 0 {
			t.Fatalf("out-of-policy action sent or accepted: allowed=%d blocked=%d observations=%d err=%v", allowed.Load(), blocked.Load(), len(observations), err)
		}
		return
	}
	if err != nil || allowed.Load() != 1 || blocked.Load() != 0 || len(observations) != 1 {
		t.Fatalf("candidate E2 action: allowed=%d blocked=%d observations=%d err=%v", allowed.Load(), blocked.Load(), len(observations), err)
	}
	if observations[0].Source.Observation.Endpoint != "/documents" || !observations[0].Source.Observation.AuditComplete {
		t.Fatal("candidate E2 observation incomplete")
	}
}
