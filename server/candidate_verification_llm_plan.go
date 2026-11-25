package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/internal/pathscope"
)

var ErrCandidateLLMPlan = errors.New("candidate_llm_plan_unavailable")
var selectedExactPath = regexp.MustCompile(`^/[A-Za-z0-9._~/-]{0,255}$`)

func ownershipIDORCandidate(candidate db.DiagnosticCandidate) bool {
	for _, label := range []string{candidate.VulnClass, candidate.VerificationClaim, candidate.Name} {
		value := strings.ToLower(label)
		if strings.Contains(value, "idor") || strings.Contains(value, "bola") || strings.Contains(value, "object-level authorization") ||
			strings.Contains(value, "object level authorization") {
			return true
		}
	}
	return false
}

// E③'s product issuance is a second, structured fence. A candidate already
// claimed by an E③ issuance cannot acquire an LLM verdict even if its model
// supplied name and class avoid the usual IDOR words.
func candidateClaimedByE3(ctx context.Context, tx *sql.Tx, taskID, candidateID int64) (bool, error) {
	if ctx == nil || tx == nil || taskID <= 0 || candidateID <= 0 {
		return false, ErrCandidateLLMPlan
	}
	ref, _ := json.Marshal([]string{"candidate-" + strconv.FormatInt(candidateID, 10)})
	var linked bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM e3_verdict_issuances
WHERE task_id=$1 AND source_kind='product' AND candidate_refs @> $2::jsonb)`, taskID, ref).Scan(&linked)
	if err != nil {
		return false, ErrCandidateLLMPlan
	}
	return linked, nil
}

type CandidateLLMPlanAction struct {
	Purpose   string `json:"purpose"`
	Method    string `json:"method"`
	ExactPath string `json:"exact_path"`
}

// CandidateLLMPlan is a reviewable proposal for one selected candidate. The
// protected executor must recheck every field; this object is never a lease.
type CandidateLLMPlan struct {
	Version         string                   `json:"version"`
	TaskID          int64                    `json:"task_id"`
	CandidateID     int64                    `json:"candidate_id"`
	EvidenceDigest  string                   `json:"evidence_digest"`
	OriginalRunRef  string                   `json:"original_run_ref"`
	AnalysisDigest  string                   `json:"analysis_digest"`
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

func candidateLLMPlanDigest(p CandidateLLMPlan) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 4096 {
		return "", ErrCandidateLLMPlan
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func candidateLLMPlanMatchesAnalysis(p CandidateLLMPlan, analysis candidateAnalysisView) bool {
	if p.AnalysisDigest != analysis.Digest || len(p.Actions) != len(analysis.Proposal.Actions) {
		return false
	}
	for i, action := range p.Actions {
		proposal := analysis.Proposal.Actions[i]
		if action.Purpose != proposal.Purpose || action.Method != proposal.Method || action.ExactPath != proposal.PathHint {
			return false
		}
	}
	return true
}

func validSelectedExactPath(path string) bool {
	if !selectedExactPath.MatchString(path) || strings.Contains(path, "//") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return pathscope.Allows(path, false, "="+path)
}

func candidateLLMPlanStillApproved(now time.Time, p CandidateLLMPlan, original diagnosisprep.Input, scope diagnosisprep.Scope, target diagnosisprep.ProductTarget) bool {
	expectedExcluded := []string(nil)
	if original.ExcludedPaths != "" {
		expectedExcluded = strings.Split(original.ExcludedPaths, "\n")
	}
	if p.Version != "candidate-llm-plan/v1" || p.TaskID <= 0 || p.CandidateID <= 0 || !candidateVerificationDigest.MatchString(p.EvidenceDigest) ||
		!candidateVerificationDigest.MatchString(p.AnalysisDigest) || p.OriginalRunRef == "" ||
		p.TargetID != target.ID || p.TargetRevision != target.Revision || p.Origin != target.Origin || p.ApprovedAt != target.ApprovedAt ||
		target.State != "approved" || !now.Before(p.ExpiresAt) || !now.Before(target.ExpiresAt) || !now.Before(scope.ExpiresAt) ||
		scope.TargetRef != target.ID || scope.Origin != target.Origin || !slices.Equal(p.ExcludedPaths, expectedExcluded) ||
		len(p.Actions) < 1 || len(p.Actions) > 3 || p.MaxRequests != len(p.Actions) || p.DurationSeconds != 90 {
		return false
	}
	parsed, err := url.Parse(original.TargetURL)
	if err != nil || parsed.Scheme+"://"+parsed.Host != p.Origin {
		return false
	}
	for _, action := range p.Actions {
		if action.Purpose == "" || len(action.Purpose) > 1024 || action.Method != "GET" && action.Method != "HEAD" ||
			!validSelectedExactPath(action.ExactPath) || pathscope.ExcludedAny(action.ExactPath, p.ExcludedPaths) ||
			!slices.Contains(scope.Methods, action.Method) || !slices.Contains(target.Methods, action.Method) {
			return false
		}
		for _, prefixes := range [][]string{scope.PathPrefixes, target.PathPrefixes} {
			allowed := false
			for _, prefix := range prefixes {
				if pathscope.Allows(action.ExactPath, false, prefix) {
					allowed = true
					break
				}
			}
			if !allowed {
				return false
			}
		}
	}
	return true
}

func CandidateLLMPlanStillApproved(now time.Time, p CandidateLLMPlan, original diagnosisprep.Input, scope diagnosisprep.Scope, target diagnosisprep.ProductTarget) bool {
	return candidateLLMPlanStillApproved(now, p, original, scope, target)
}

func makeCandidateLLMPlan(now time.Time, candidate db.DiagnosticCandidate, analysis candidateAnalysisView,
	original diagnosisprep.Input, scope diagnosisprep.Scope, target diagnosisprep.ProductTarget) (CandidateLLMPlan, string, error) {
	if candidate.Validate() != nil || ownershipIDORCandidate(candidate) || analysis.Proposal.CandidateID != candidate.ID ||
		analysis.Proposal.Version != "selected/analysis-v1" || target.State != "approved" || target.Revision == 0 || target.ApprovedAt.IsZero() ||
		!now.Before(target.ExpiresAt) || !now.Before(scope.ExpiresAt) || len(analysis.Proposal.Actions) == 0 {
		return CandidateLLMPlan{}, "", ErrCandidateLLMPlan
	}
	until := now.Add(10 * time.Minute)
	if target.ExpiresAt.Before(until) {
		until = target.ExpiresAt
	}
	if scope.ExpiresAt.Before(until) {
		until = scope.ExpiresAt
	}
	if until.Sub(now) < time.Minute {
		return CandidateLLMPlan{}, "", ErrCandidateLLMPlan
	}
	p := CandidateLLMPlan{Version: "candidate-llm-plan/v1", TaskID: candidate.TaskID, CandidateID: candidate.ID,
		EvidenceDigest: candidate.EvidenceDigest, OriginalRunRef: candidate.RunRef, AnalysisDigest: analysis.Digest,
		TargetID: target.ID, TargetRevision: target.Revision, Origin: target.Origin, MaxRequests: len(analysis.Proposal.Actions),
		DurationSeconds: 90, ApprovedAt: target.ApprovedAt, ExpiresAt: until}
	if original.ExcludedPaths != "" {
		p.ExcludedPaths = strings.Split(original.ExcludedPaths, "\n")
	}
	for _, proposal := range analysis.Proposal.Actions {
		p.Actions = append(p.Actions, CandidateLLMPlanAction{Purpose: proposal.Purpose, Method: proposal.Method, ExactPath: proposal.PathHint})
	}
	if !candidateLLMPlanStillApproved(now, p, original, scope, target) {
		return CandidateLLMPlan{}, "", ErrCandidateLLMPlan
	}
	digest, err := candidateLLMPlanDigest(p)
	return p, digest, err
}
