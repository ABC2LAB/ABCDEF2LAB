package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/internal/pathscope"
	"github.com/Autumn-27/artex/report"
)

var errCandidateActivePlan = errors.New("candidate active verification plan unavailable")

// A plan binds one original candidate to one approved exact read-only function.
// It does not grant execution authority. The protected host must check the
// current target revision, exclusions, TLS pin, installation budget and this
// digest again before issuing a fresh E² lease.
type candidateActivePlan struct {
	Version          string    `json:"version"`
	TaskID           int64     `json:"task_id"`
	CandidateID      int64     `json:"candidate_id"`
	EvidenceDigest   string    `json:"evidence_digest"`
	OriginalRunRef   string    `json:"original_run_ref"`
	TargetID         string    `json:"target_id"`
	TargetRevision   uint64    `json:"target_revision"`
	Origin           string    `json:"origin"`
	Method           string    `json:"method"`
	ExactPath        string    `json:"exact_path"`
	ExcludedPaths    []string  `json:"excluded_paths,omitempty"`
	Claim            string    `json:"claim"`
	MaxRequests      int       `json:"max_requests"`
	DurationSeconds  int       `json:"duration_seconds"`
	ModelCostMicros  int       `json:"model_cost_micros"`
	ApprovedAt       time.Time `json:"target_approved_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	NeedsNewEvidence bool      `json:"needs_new_evidence"`
}

// CandidateActivePlan is exposed to the protected runtime's target checker.
// It is a proposal, not a bearer authority.
type CandidateActivePlan = candidateActivePlan

func candidatePlanDigest(p candidateActivePlan) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", errCandidateActivePlan
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Recheck a previously approved proposal against today's target and the
// original run's frozen scope. No new execution authority follows from it.
func candidatePlanStillApproved(now time.Time, p candidateActivePlan, original diagnosisprep.Input, scope diagnosisprep.Scope, target diagnosisprep.ProductTarget) bool {
	expectedExcluded := []string(nil)
	if original.ExcludedPaths != "" {
		expectedExcluded = strings.Split(original.ExcludedPaths, "\n")
	}
	if p.Version != "candidate-active/v1" || p.Method != "GET" || p.MaxRequests != 2 || p.DurationSeconds != 60 || p.ModelCostMicros != 0 ||
		p.TargetID != target.ID || p.TargetRevision != target.Revision || p.Origin != target.Origin || p.ApprovedAt != target.ApprovedAt ||
		target.State != "approved" || !now.Before(p.ExpiresAt) || !now.Before(target.ExpiresAt) || !now.Before(scope.ExpiresAt) ||
		scope.TargetRef != target.ID || scope.Origin != target.Origin || !slices.Contains(scope.Methods, "GET") || !slices.Contains(target.Methods, "GET") ||
		p.ExactPath == "" || !pathscope.Allows(p.ExactPath, false, "="+p.ExactPath) ||
		!slices.Equal(p.ExcludedPaths, expectedExcluded) ||
		pathscope.ExcludedAny(p.ExactPath, p.ExcludedPaths) {
		return false
	}
	parsed, err := url.Parse(original.TargetURL)
	if err != nil || parsed.Scheme+"://"+parsed.Host != p.Origin {
		return false
	}
	for _, prefixes := range [][]string{scope.PathPrefixes, target.PathPrefixes} {
		allowed := false
		for _, prefix := range prefixes {
			if pathscope.Allows(p.ExactPath, false, prefix) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	return true
}

func CandidatePlanStillApproved(now time.Time, p CandidateActivePlan, original diagnosisprep.Input, scope diagnosisprep.Scope, target diagnosisprep.ProductTarget) bool {
	return candidatePlanStillApproved(now, p, original, scope, target)
}

func candidatePlanMatchesEvidence(p candidateActivePlan, candidate db.DiagnosticCandidate) bool {
	if !candidateverify.SupportedV2(candidate) || candidate.Validate() != nil || p.TaskID != candidate.TaskID || p.CandidateID != candidate.ID ||
		p.EvidenceDigest != candidate.EvidenceDigest || p.OriginalRunRef != candidate.RunRef || p.Claim != candidate.VerificationClaim ||
		p.Method != "GET" || !candidateFixedGETURL.MatchString(p.Origin+p.ExactPath) {
		return false
	}
	var evidence struct {
		Trusted struct {
			Call      e2proxy.CallResult    `json:"call"`
			Execution e2proxy.ExecutionView `json:"execution"`
		} `json:"trusted"`
	}
	if json.Unmarshal([]byte(candidate.Evidence), &evidence) != nil || evidence.Trusted.Call.CallRef != candidate.CallRef ||
		len(evidence.Trusted.Call.Observations) != 1 || evidence.Trusted.Execution.HeaderMode != e2proxy.HeaderPreserve {
		return false
	}
	obs := evidence.Trusted.Call.Observations[0]
	return candidateActiveUnauthenticatedObservation(obs) && obs.ExactFunction && obs.Method == "GET" && obs.Endpoint == p.ExactPath && !strings.Contains(obs.Endpoint, "{")
}

// active/v1 has no account-bound login or credential revision. An authenticated
// original response cannot safely be compared with its anonymous follow-up.
// Older records without an explicit auth classification remain readable, but
// cannot authorize a new active execution.
func candidateActiveUnauthenticatedObservation(obs e2proxy.HTTPObservation) bool {
	return obs.RequestAuthContext == "none" && obs.RequestAccountRef == ""
}

func makeCandidateActivePlan(now time.Time, candidate db.DiagnosticCandidate, automatic report.CandidateAssessment, original diagnosisprep.Input, scope diagnosisprep.Scope, target diagnosisprep.ProductTarget) (candidateActivePlan, string, error) {
	if !candidateverify.SupportedV2(candidate) || candidate.Validate() != nil || !validStoredAutomaticCandidateAssessment(candidate, automatic) ||
		automatic.Status != candidateverify.Insufficient || target.State != "approved" || target.Revision == 0 || target.ID == "" ||
		target.ApprovedAt.IsZero() || !now.Before(target.ExpiresAt) || !now.Before(scope.ExpiresAt) || !slices.Contains(target.Methods, "GET") ||
		scope.TargetRef != target.ID || scope.Origin != target.Origin || !slices.Contains(scope.Methods, "GET") {
		return candidateActivePlan{}, "", errCandidateActivePlan
	}
	parsed, err := url.Parse(original.TargetURL)
	if err != nil || parsed.Scheme+"://"+parsed.Host != target.Origin {
		return candidateActivePlan{}, "", errCandidateActivePlan
	}
	var evidence struct {
		Trusted struct {
			Call      e2proxy.CallResult    `json:"call"`
			Execution e2proxy.ExecutionView `json:"execution"`
		} `json:"trusted"`
	}
	if json.Unmarshal([]byte(candidate.Evidence), &evidence) != nil || evidence.Trusted.Call.CallRef != candidate.CallRef ||
		len(evidence.Trusted.Call.Observations) != 1 || evidence.Trusted.Execution.HeaderMode != e2proxy.HeaderPreserve {
		return candidateActivePlan{}, "", errCandidateActivePlan
	}
	obs := evidence.Trusted.Call.Observations[0]
	path := obs.Endpoint
	if !candidateActiveUnauthenticatedObservation(obs) || !obs.ExactFunction || obs.Method != "GET" || !strings.HasPrefix(path, "/") || strings.Contains(path, "{") ||
		!pathscope.Allows(path, false, "="+path) || !candidateFixedGETURL.MatchString(target.Origin+path) ||
		original.ExcludedPaths != "" && pathscope.ExcludedAny(path, strings.Split(original.ExcludedPaths, "\n")) {
		return candidateActivePlan{}, "", errCandidateActivePlan
	}
	allowed := false
	for _, prefix := range scope.PathPrefixes {
		if pathscope.Allows(path, false, prefix) {
			allowed = true
			break
		}
	}
	if !allowed {
		return candidateActivePlan{}, "", errCandidateActivePlan
	}
	allowed = false
	for _, prefix := range target.PathPrefixes {
		if pathscope.Allows(path, false, prefix) {
			allowed = true
			break
		}
	}
	if !allowed {
		return candidateActivePlan{}, "", errCandidateActivePlan
	}
	until := now.Add(10 * time.Minute)
	if target.ExpiresAt.Before(until) {
		until = target.ExpiresAt
	}
	if until.Sub(now) < time.Minute {
		return candidateActivePlan{}, "", errCandidateActivePlan
	}
	p := candidateActivePlan{Version: "candidate-active/v1", TaskID: candidate.TaskID, CandidateID: candidate.ID,
		EvidenceDigest: candidate.EvidenceDigest, OriginalRunRef: candidate.RunRef, TargetID: target.ID,
		TargetRevision: target.Revision, Origin: target.Origin, Method: "GET", ExactPath: path,
		Claim: candidate.VerificationClaim, MaxRequests: 2, DurationSeconds: 60, ModelCostMicros: 0,
		ApprovedAt: target.ApprovedAt, ExpiresAt: until, NeedsNewEvidence: true}
	if original.ExcludedPaths != "" {
		p.ExcludedPaths = strings.Split(original.ExcludedPaths, "\n")
	}
	digest, err := candidatePlanDigest(p)
	return p, digest, err
}
