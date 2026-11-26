package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
)

func TestSelectedActionInputsAreCanonicalAndNoncredential(t *testing.T) {
	query := map[string]string{"mode": "review", "page": "2"}
	headers := map[string]string{"Accept": "application/json", "X-Requested-With": "XMLHttpRequest"}
	if !validSelectedActionInputs(query, headers) || selectedActionQuery(query) != "mode=review&page=2" ||
		!validSelectedPolicyQuery("mode=review&page=2") {
		t.Fatal("bounded structured read inputs were rejected")
	}
	for _, bad := range []map[string]string{
		{"auth_token": "abc"}, {"session": "abc"}, {"password": "abc"},
		{"mode": strings.Repeat("x", 33)}, {"q": "a b"},
	} {
		if validSelectedActionInputs(bad, nil) {
			t.Fatalf("credential-like query was accepted: %v", bad)
		}
	}
	for _, bad := range []map[string]string{
		{"Authorization": "Bearer synthetic"}, {"Cookie": "sid=synthetic"},
		{"X-Requested-With": "Other"}, {"Accept": "text/plain"},
	} {
		if validSelectedActionInputs(nil, bad) {
			t.Fatalf("unapproved header was accepted: %v", bad)
		}
	}
	for _, bad := range []string{"page=2&mode=review", "auth_token=x", "mode=a%20b", "mode=one&mode=two"} {
		if validSelectedPolicyQuery(bad) {
			t.Fatalf("noncanonical or credential query accepted: %q", bad)
		}
	}
}

func TestSelectedPlanRejectsExpiredRevokedOrChangedApproval(t *testing.T) {
	now := time.Now().UTC()
	const origin = "https://owned.example"
	plan := CandidateLLMPlan{Version: "candidate-llm-plan/v1", TaskID: 1, CandidateID: 2,
		EvidenceDigest: strings.Repeat("a", 64), AnalysisDigest: strings.Repeat("b", 64), OriginalRunRef: "run-original",
		TargetID: "target-owned", TargetRevision: 3, Origin: origin, ApprovedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
		Actions:     []CandidateLLMPlanAction{{Purpose: "Compare exact read", Method: "GET", ExactPath: "/documents"}},
		MaxRequests: 1, DurationSeconds: 90}
	input := diagnosisprep.Input{TargetURL: origin + "/documents"}
	scope := diagnosisprep.Scope{TargetRef: plan.TargetID, Origin: origin, PathPrefixes: []string{"=/documents"}, Methods: []string{"GET"}, ExpiresAt: now.Add(time.Hour)}
	target := diagnosisprep.ProductTarget{ID: plan.TargetID, Origin: origin, State: "approved", Revision: plan.TargetRevision,
		PathPrefixes: []string{"=/documents"}, Methods: []string{"GET"}, ApprovedAt: plan.ApprovedAt, ExpiresAt: now.Add(time.Hour)}
	if !candidateLLMPlanStillApproved(now, plan, input, scope, target) {
		t.Fatal("valid exact read plan rejected")
	}
	for _, tc := range []struct {
		name string
		edit func(*CandidateLLMPlan, *diagnosisprep.Input, *diagnosisprep.Scope, *diagnosisprep.ProductTarget)
	}{
		{"plan-expired", func(p *CandidateLLMPlan, _ *diagnosisprep.Input, _ *diagnosisprep.Scope, _ *diagnosisprep.ProductTarget) {
			p.ExpiresAt = now
		}},
		{"target-expired", func(_ *CandidateLLMPlan, _ *diagnosisprep.Input, _ *diagnosisprep.Scope, tg *diagnosisprep.ProductTarget) {
			tg.ExpiresAt = now
		}},
		{"scope-expired", func(_ *CandidateLLMPlan, _ *diagnosisprep.Input, sc *diagnosisprep.Scope, _ *diagnosisprep.ProductTarget) {
			sc.ExpiresAt = now
		}},
		{"revoked", func(_ *CandidateLLMPlan, _ *diagnosisprep.Input, _ *diagnosisprep.Scope, tg *diagnosisprep.ProductTarget) {
			tg.State = "revoked"
		}},
		{"revision", func(_ *CandidateLLMPlan, _ *diagnosisprep.Input, _ *diagnosisprep.Scope, tg *diagnosisprep.ProductTarget) {
			tg.Revision++
		}},
		{"excluded", func(p *CandidateLLMPlan, i *diagnosisprep.Input, _ *diagnosisprep.Scope, _ *diagnosisprep.ProductTarget) {
			i.ExcludedPaths = "/documents"
			p.ExcludedPaths = []string{"/documents"}
		}},
		{"path", func(_ *CandidateLLMPlan, _ *diagnosisprep.Input, sc *diagnosisprep.Scope, _ *diagnosisprep.ProductTarget) {
			sc.PathPrefixes = []string{"=/other"}
		}},
		{"method", func(_ *CandidateLLMPlan, _ *diagnosisprep.Input, _ *diagnosisprep.Scope, tg *diagnosisprep.ProductTarget) {
			tg.Methods = []string{"HEAD"}
		}},
		{"write", func(p *CandidateLLMPlan, _ *diagnosisprep.Input, _ *diagnosisprep.Scope, _ *diagnosisprep.ProductTarget) {
			p.Actions[0].Method = "POST"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, i, sc, tg := plan, input, scope, target
			p.Actions = append([]CandidateLLMPlanAction(nil), plan.Actions...)
			tc.edit(&p, &i, &sc, &tg)
			if candidateLLMPlanStillApproved(now, p, i, sc, tg) {
				t.Fatal("stale or out-of-scope plan remained approved")
			}
		})
	}
}

func TestSelectedActionObservationBindsQueryAndHeaders(t *testing.T) {
	action := CandidateLLMPlanAction{Method: "GET", ExactPath: "/documents", Query: map[string]string{"mode": "review"},
		Headers: map[string]string{"Accept": "application/json"}}
	query := sha256.Sum256([]byte(selectedActionQuery(action.Query)))
	raw, err := json.Marshal(selectedActionHeaders(action.Headers))
	if err != nil {
		t.Fatal(err)
	}
	header := sha256.Sum256(raw)
	obs := e2proxy.HTTPObservation{Method: "GET", Endpoint: "/documents", ExactFunction: true,
		RequestQueryDigest: hex.EncodeToString(query[:]), RequestHeaderDigest: hex.EncodeToString(header[:])}
	if !selectedActionMatchesObservation(action, obs) {
		t.Fatal("approved structured request was not bound to the observation")
	}
	obs.RequestQueryDigest = strings.Repeat("0", 64)
	if selectedActionMatchesObservation(action, obs) {
		t.Fatal("changed query was accepted as approved evidence")
	}
}

func TestSelectedLoginContractDigestChangesWithExtractor(t *testing.T) {
	login := diagnosisprep.TargetLogin{Method: "POST", Path: "/login", Format: "json", UsernameField: "username", PasswordField: "password",
		JSONField: "data.token", Header: "Authorization", Prefix: "Bearer "}
	digest := CandidateLoginDigest(login)
	if len(digest) != 64 || digest != CandidateLoginDigest(login) {
		t.Fatal("valid login contract has no stable digest")
	}
	login.JSONField = "other.token"
	if changed := CandidateLoginDigest(login); changed == digest || len(changed) != 64 {
		t.Fatal("changed token extractor retained approval")
	}
}
