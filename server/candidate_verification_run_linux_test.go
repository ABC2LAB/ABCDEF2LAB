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
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
	"golang.org/x/crypto/bcrypt"
)

// This fixture authorizes only the test's synthetic task. Production uses
// CandidateExecutionSupervisor, whose DB and current-target checks are
// exercised separately; the E² runner, audit and drain are real. This fixture
// does not supply a product host socket or create a verdict seal.
type candidateGETTestSupervisor struct {
	taskID string
	server *Server
}

func (s *candidateGETTestSupervisor) Attach(server *Server) error {
	if s.server != nil || server == nil {
		return ErrCandidateExecutionBinding
	}
	s.server = server
	return nil
}
func (s *candidateGETTestSupervisor) AuthorizeExecution(_ context.Context, scope transmit.Scope, intentID int64) error {
	if s.server == nil || scope.TaskRef != s.taskID || intentID <= 0 {
		return ErrCandidateExecutionBinding
	}
	return nil
}
func (s *candidateGETTestSupervisor) CheckTaskQuiescent(_ context.Context, taskID int64) error {
	if strconv.FormatInt(taskID, 10) != s.taskID {
		return ErrCandidateExecutionBinding
	}
	return nil
}

func TestSelectedCandidateGETRealE2SyntheticRunnerPostgres(t *testing.T) {
	helper := firstDiagnosisNativeHelper(t)
	f := newControlledLocalDatabaseFixture(t)
	installer := &db.DB{DB: f.Installer}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	manager, err := NewFirstDiagnosisManager(ctx, t.TempDir(), f.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	task, err := manager.CreateTask("synthetic candidate E2", "two exact GETs", nil, 60, 60)
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
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/documents" {
			t.Errorf("candidate request escaped exact scope: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		hits.Add(1)
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
	policy.MaxRequests, policy.Limits.MaxAttempts, policy.Limits.MaxCalls = 2, 2, 2
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
	supervisor := &candidateGETTestSupervisor{taskID: task.ID}
	observations, err := ExecuteSelectedCandidateGET(ctx, manager, FirstDiagnosisHTTPStartOptions{
		Host: cfg, SkillDir: skillDir, DataDir: dataDir, KeyDir: keyDir,
		LocalLLMManifestPath: manifest, Supervisor: supervisor, CleanupTimeout: 5 * time.Second,
	}, mustFirstDiagnosisTaskNumber(task.ID), policy.RunRef, target.URL+"/documents")
	if err != nil || hits.Load() != 2 || len(observations) != 2 {
		t.Fatalf("candidate E2 GET/drain: hits=%d observations=%d attached=%t error=%v", hits.Load(), len(observations), supervisor.server != nil, err)
	}
	var status string
	var paused bool
	if err := f.Installer.QueryRowContext(ctx, `SELECT status,paused FROM tasks WHERE id=$1`, task.ID).Scan(&status, &paused); err != nil || status != "done" || !paused {
		t.Fatal("candidate E2 task not drained", err)
	}
}
