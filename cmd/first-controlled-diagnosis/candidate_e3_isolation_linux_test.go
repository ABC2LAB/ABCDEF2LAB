//go:build linux

package main

import (
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/server"
)

func selectedCandidateE3Fixture(t *testing.T) (*urlPreparationRuntime, *urlLoadedProfile, string) {
	t.Helper()
	r, _, target := accountTestTarget(t, true)
	if _, err := r.putDeclaration("ARTEX", target.ID, target.Revision, diagnosisprep.DeclarationInput{
		Identity: e3contract.IdentitySpec{Path: "/read/me", SubjectIDPath: "id"},
		Types:    []e3contract.TypeDeclaration{{Type: "post", Sharing: e3contract.SharingShared}},
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := r.lookup("ARTEX", target.Origin+"/read", "")
	if err != nil {
		t.Fatal(err)
	}
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.20")}, nil
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		return strings.Repeat("b", 64), time.Now().Add(time.Hour), nil
	}
	l := connectionTestProfile(r, rec)
	l.policy.Rules[0].Ref = "candidate-exact"
	l.policy.Rules[0].PathMatch = "exact"
	l.policy.Rules[0].PathScopeRef = "candidate-exact"
	l.policy.Rules[0].ApprovalDisplay = server.RequestApprovalDisplay{QueryKeys: []string{}, JSONFields: []string{}}
	l.policy.HeaderMode = e2proxy.HeaderPreserve
	l.policy.MaxRequests = 1
	l.profile = urlPreparationProfile{ValidFrom: time.Now().Add(-time.Minute), ValidUntil: time.Now().Add(time.Hour), Budget: diagnosisprep.Budget{DurationSeconds: 30}}
	l.launch.ShutdownTimeoutSeconds = 1
	l.launch.E2HostSocket = "/run/e2-host/audit.sock"
	job := "candidate-e3-isolated"
	if err := l.prepareTargetConnection(t.Context(), nil, job); err != nil {
		t.Fatal(err)
	}
	runs := t.TempDir()
	if err := os.Chmod(runs, 0700); err != nil {
		t.Fatal(err)
	}
	return &urlPreparationRuntime{config: urlPreparationInstallation{RunsDir: runs}}, l, job
}

func TestIssueSelectedCandidateExcludesActiveE3Declaration(t *testing.T) {
	b, l, job := selectedCandidateE3Fixture(t)
	if _, active := l.productRegistry.activeDeclaration(*l.productTarget); !active {
		t.Fatal("fixture declaration not active")
	}
	launch, err := b.issueSelectedCandidate(l, job, server.FirstDiagnosisPreparedTask{TaskID: 91, ExplorationID: 92})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(launch.HTTP.PolicyFile)
	if err != nil {
		t.Fatal(err)
	}
	var issued server.E2HTTPFilePolicy
	if e2config.StrictJSON(raw, &issued) != nil || issued.RunRef != "run-"+job || len(issued.Rules) != 1 ||
		issued.Rules[0].PathMatch != "exact" || issued.Rules[0].Methods[0] != "GET" || issued.Rules[0].Purpose != "" ||
		issued.E3WriteConsent || issued.E3Resettable || issued.Auth != nil || issued.Limits.E3ConfirmationRequests == nil ||
		*issued.Limits.E3ConfirmationRequests != 0 {
		t.Fatal("candidate authority contains E3 capabilities or lost exact read scope")
	}
	for _, name := range []string{productFrozenDeclarationFile, productFrozenPolicyFile, productTargetRefsFile} {
		if _, err := os.Stat(filepath.Join(b.config.RunsDir, job, name)); !os.IsNotExist(err) {
			t.Fatalf("candidate run issued %s", name)
		}
	}
	a, err := os.ReadFile(launch.HTTP.AuthorityFile)
	if err != nil {
		t.Fatal(err)
	}
	var authority server.FirstDiagnosisHTTPApproval
	if json.Unmarshal(a, &authority) != nil || authority.TaskID != 91 || authority.ExplorationID != 92 || authority.PolicySHA256 != urlSHA(raw) {
		t.Fatal("candidate authority did not bind fresh task and exact policy")
	}
}

func TestCandidateLLMReadRuleStrictJSON(t *testing.T) {
	rule := candidateLLMReadRule("candidate-llm-1", "https://owned.example", "10.200.1.20", strings.Repeat("a", 64),
		server.CandidateLLMPlanAction{Method: "GET", ExactPath: "/read", Purpose: "compare observed public response"})
	raw, err := json.Marshal(rule)
	if err != nil {
		t.Fatal(err)
	}
	var decoded server.E2HTTPFileRule
	if e2config.StrictJSON(raw, &decoded) != nil || len(decoded.ApprovalDisplay.QueryKeys) != 0 ||
		decoded.ApprovalDisplay.QueryKeys == nil || decoded.ApprovalDisplay.JSONFields == nil ||
		decoded.PathMatch != "exact" || decoded.Purpose != "" || decoded.Credentials != nil {
		t.Fatal("candidate read rule cannot be strictly decoded as an isolated exact E2 rule")
	}
}

func TestIssueSelectedCandidateRejectsE3OrBroaderPolicy(t *testing.T) {
	for name, change := range map[string]func(*urlLoadedProfile){
		"e3 write":       func(l *urlLoadedProfile) { l.policy.E3WriteConsent = true },
		"e3 purpose":     func(l *urlLoadedProfile) { l.policy.Rules[0].Purpose = e2proxy.PurposeE3Read },
		"broad path":     func(l *urlLoadedProfile) { l.policy.Rules[0].PathMatch = "prefix" },
		"write method":   func(l *urlLoadedProfile) { l.policy.Rules[0].Methods = []string{"POST"} },
		"authentication": func(l *urlLoadedProfile) { l.policy.Auth = &server.E2HTTPFileAuth{} },
	} {
		t.Run(name, func(t *testing.T) {
			b, l, job := selectedCandidateE3Fixture(t)
			change(l)
			if _, err := b.issueSelectedCandidate(l, job, server.FirstDiagnosisPreparedTask{TaskID: 91, ExplorationID: 92}); err == nil {
				t.Fatal("unsafe candidate policy issued")
			}
			if _, err := os.Stat(filepath.Join(b.config.RunsDir, job)); !os.IsNotExist(err) {
				t.Fatal("unsafe candidate created authority directory")
			}
		})
	}
}
