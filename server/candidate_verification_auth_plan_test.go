package server

import (
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

func TestSelectedAuthPreparationUsesCurrentTargetApprovalOutsideOrdinaryReadScope(t *testing.T) {
	now := time.Now().UTC()
	approved := now.Add(-time.Minute)
	origin := "https://synthetic.test:9443"
	read := "=/rest/products/search"
	login := "=/login"
	target := diagnosisprep.ProductTarget{ID: "owned-target", Origin: origin, State: "approved", Revision: 2,
		PathPrefixes: []string{login, read}, Methods: []string{"GET", "HEAD", "POST"},
		ApprovedAt: approved, ExpiresAt: now.Add(time.Hour)}
	// The ordinary diagnosis scope intentionally excludes auth_prep. The
	// original E² policy handles login under a distinct purpose.
	scope := diagnosisprep.Scope{TargetRef: target.ID, Origin: origin, PathPrefixes: []string{read},
		Methods: []string{"GET", "HEAD"}, ExpiresAt: target.ExpiresAt}
	plan := CandidateLLMPlan{Version: "candidate-llm-plan/v1", TaskID: 1, CandidateID: 5,
		EvidenceDigest: strings.Repeat("a", 64), AnalysisDigest: strings.Repeat("b", 64),
		OriginalRunRef: "original-run", AccountRef: strings.Repeat("c", 64),
		TargetID: target.ID, TargetRevision: target.Revision, Origin: origin,
		ApprovedAt: approved, ExpiresAt: now.Add(10 * time.Minute), DurationSeconds: 90, MaxRequests: 2,
		AuthPreparation: &CandidateLLMAuthPreparation{Method: "POST", ExactPath: "/login", LoginDigest: strings.Repeat("d", 64)},
		Actions:         []CandidateLLMPlanAction{{Purpose: "Repeat approved HTML read", Method: "GET", ExactPath: "/rest/products/search"}}}
	original := diagnosisprep.Input{TargetURL: origin + "/rest/products/search"}
	if !candidateLLMPlanStillApproved(now, plan, original, scope, target) {
		t.Fatal("separately approved auth preparation was rejected by ordinary read scope")
	}
	withoutPost := target
	withoutPost.Methods = []string{"GET", "HEAD"}
	if candidateLLMPlanStillApproved(now, plan, original, scope, withoutPost) {
		t.Fatal("login POST accepted after current target approval removed it")
	}
	withoutLogin := target
	withoutLogin.PathPrefixes = []string{read}
	if candidateLLMPlanStillApproved(now, plan, original, scope, withoutLogin) {
		t.Fatal("login path accepted after current target approval removed it")
	}
	excluded := plan
	excluded.ExcludedPaths = []string{"/login"}
	original.ExcludedPaths = "/login"
	if candidateLLMPlanStillApproved(now, excluded, original, scope, target) {
		t.Fatal("excluded login path accepted")
	}
	unsafeWrite := plan
	unsafeWrite.Actions = []CandidateLLMPlanAction{{Purpose: "Unapproved state change", Method: "POST", ExactPath: "/rest/products/search"}}
	if candidateLLMPlanStillApproved(now, unsafeWrite, original, scope, target) {
		t.Fatal("candidate write accepted without a recovery contract")
	}
}
