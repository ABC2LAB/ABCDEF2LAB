package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/internal/pathscope"
	"github.com/Autumn-27/artex/learnedlogin"
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
	Purpose   string            `json:"purpose"`
	Method    string            `json:"method"`
	ExactPath string            `json:"exact_path"`
	Kind      string            `json:"kind,omitempty"`
	Query     map[string]string `json:"query,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// CandidateLLMAuthPreparation은 승인 계획의 인증 준비 구조다. 값(아이디·비밀번호·세션·CSRF)은 없다.
//   - candidate-llm-plan/v1(로그인 설정 9칸): method·exact_path·login_digest(9칸 전체 digest)만. 칸 모양·digest는 그대로다.
//   - candidate-llm-plan/v2(학습 로그인, docs/contracts/selected-candidate-learned-login.md): kind가 learned_login/v1(같은 경로
//     준비) 또는 learned_login/v2(별도 경로 CSRF), login_digest는 학습 방법의 method_sha256, prep_*는 준비 GET 하나(필요할 때),
//     requests는 인증 준비 요청 수(1 또는 2), trust는 계획을 만들 때 확인한 방법의 출처 수준이다.
type CandidateLLMAuthPreparation struct {
	Method      string `json:"method"`
	ExactPath   string `json:"exact_path"`
	LoginDigest string `json:"login_digest"`
	Kind        string `json:"kind,omitempty"`
	PrepMethod  string `json:"prep_method,omitempty"`
	PrepPath    string `json:"prep_path,omitempty"`
	Requests    int    `json:"requests,omitempty"`
	Trust       string `json:"trust,omitempty"`
}

const (
	candidateLLMPlanV1 = "candidate-llm-plan/v1"
	candidateLLMPlanV2 = "candidate-llm-plan/v2"
	// 학습 로그인 준비 유형. v1은 로그인 경로의 준비 GET(또는 준비 없음), v2는 출처가 결속된 별도 경로 준비 GET이다.
	CandidateAuthLearnedV1 = "learned_login/v1"
	CandidateAuthLearnedV2 = "learned_login/v2"
	// 선택 후보 검증이 쓰는 학습 방법의 출처 수준(제안, 팀 확인 필요). verified: 같은 방법·지금 대상 판·자격 판의 E③
	// 다시 로그인 성공(login_method_verified). exploration_observed: 원본 회차의 보호 기록·탐색 경계 근거로 다시 대조함.
	CandidateLoginTrustVerified            = "verified"
	CandidateLoginTrustExplorationObserved = "exploration_observed"
	// e2_audited: 진단 단계 원본 시도가 원본 회차 E② 감사 저널(실행 감사 키로 끝까지 검증한 사본)의 같은 호출·시도 결과와
	// 맞는다(learnedlogin.VerifyTransport와 같은 칸).
	CandidateLoginTrustE2Audited = "e2_audited"
)

// CandidateLearnedLogin은 제품 계층이 고른, 선택 후보 검증에 쓸 학습 방법이다(값 없음).
type CandidateLearnedLogin struct {
	Method learnedlogin.Method
	Trust  string
}

// CandidatePlanAuth는 인증된 원본 후보의 인증 준비 원천이다. 둘 중 하나만 있다.
type CandidatePlanAuth struct {
	Configured *diagnosisprep.TargetLogin
	Learned    *CandidateLearnedLogin
}

func validCandidateLoginTrust(trust string) bool {
	return trust == CandidateLoginTrustVerified || trust == CandidateLoginTrustExplorationObserved || trust == CandidateLoginTrustE2Audited
}

// CandidateLLMAuthRequests는 계획의 인증 준비 요청 수다(없음 0, 9칸 POST 1, 학습 로그인 1 또는 2).
func CandidateLLMAuthRequests(a *CandidateLLMAuthPreparation) int {
	switch {
	case a == nil:
		return 0
	case a.Kind == "":
		return 1
	}
	return a.Requests
}

// CandidateLearnedAuthPreparation은 학습 방법에서 v2 계획의 인증 준비 구조를 만든다. 방법을 재현할 수 없거나 출처 수준이
// 정책 밖이면 nil이다.
func CandidateLearnedAuthPreparation(l CandidateLearnedLogin) *CandidateLLMAuthPreparation {
	m := l.Method.Clone()
	if m.Validate() != "" || m.Method != "POST" || !validCandidateLoginTrust(l.Trust) {
		return nil
	}
	a := &CandidateLLMAuthPreparation{Method: m.Method, ExactPath: m.Path, LoginDigest: m.Digest(), Kind: CandidateAuthLearnedV1, Requests: 1, Trust: l.Trust}
	if len(m.FieldSources) > 0 {
		a.Kind = CandidateAuthLearnedV2
	}
	if m.NeedsPrep() {
		a.PrepMethod, a.PrepPath, a.Requests = "GET", m.PrepPath(), 2
	}
	return a
}

// candidateRiskyLoginPath는 학습 로그인 경로의 위험 동작 단어 검사다(원래 경로와 한 번 푼 경로, E③ 재사용과 같음).
func candidateRiskyLoginPath(method, raw string) bool {
	decoded, err := url.PathUnescape(raw)
	return err != nil || diagnosisflow.RiskyRequest(method, raw) || diagnosisflow.RiskyRequest(method, decoded)
}

// The login contract has no credential values. Binding its complete shape
// prevents a changed token extractor or cookie name from silently changing
// the approved verification request.
func candidateLoginDigest(login diagnosisprep.TargetLogin) string {
	if !diagnosisprep.ValidTargetLogin(login) {
		return ""
	}
	raw, err := json.Marshal(login)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func CandidateLoginDigest(login diagnosisprep.TargetLogin) string {
	return candidateLoginDigest(login)
}

// CandidateLLMPlan is a reviewable proposal for one selected candidate. The
// protected executor must recheck every field; this object is never a lease.
type CandidateLLMPlan struct {
	Version         string                       `json:"version"`
	TaskID          int64                        `json:"task_id"`
	CandidateID     int64                        `json:"candidate_id"`
	EvidenceDigest  string                       `json:"evidence_digest"`
	OriginalRunRef  string                       `json:"original_run_ref"`
	AnalysisDigest  string                       `json:"analysis_digest"`
	AccountRef      string                       `json:"account_ref,omitempty"`
	AuthPreparation *CandidateLLMAuthPreparation `json:"auth_preparation,omitempty"`
	TargetID        string                       `json:"target_id"`
	TargetRevision  uint64                       `json:"target_revision"`
	Origin          string                       `json:"origin"`
	ExcludedPaths   []string                     `json:"excluded_paths,omitempty"`
	Actions         []CandidateLLMPlanAction     `json:"actions"`
	MaxRequests     int                          `json:"max_requests"`
	DurationSeconds int                          `json:"duration_seconds"`
	ApprovedAt      time.Time                    `json:"target_approved_at"`
	ExpiresAt       time.Time                    `json:"expires_at"`
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
		if action.Purpose != proposal.Purpose || action.Method != proposal.Method || action.ExactPath != proposal.PathHint || action.Kind != proposal.Kind ||
			!maps.Equal(action.Query, proposal.Query) || !maps.Equal(action.Headers, proposal.Headers) {
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
	if p.Version != candidateLLMPlanV1 && p.Version != candidateLLMPlanV2 || p.TaskID <= 0 || p.CandidateID <= 0 || !candidateVerificationDigest.MatchString(p.EvidenceDigest) ||
		!candidateVerificationDigest.MatchString(p.AnalysisDigest) || (p.AccountRef != "" && !candidateAccountRevision.MatchString(p.AccountRef)) || p.OriginalRunRef == "" ||
		p.TargetID != target.ID || p.TargetRevision != target.Revision || p.Origin != target.Origin || p.ApprovedAt != target.ApprovedAt ||
		target.State != "approved" || !now.Before(p.ExpiresAt) || !now.Before(target.ExpiresAt) || !now.Before(scope.ExpiresAt) ||
		scope.TargetRef != target.ID || scope.Origin != target.Origin || !slices.Equal(p.ExcludedPaths, expectedExcluded) ||
		len(p.Actions) < 1 || len(p.Actions) > 3 || p.MaxRequests != len(p.Actions)+CandidateLLMAuthRequests(p.AuthPreparation) || p.DurationSeconds != 90 ||
		(p.AccountRef == "") != (p.AuthPreparation == nil) {
		return false
	}
	// v1은 로그인 설정(9칸) 인증 준비만, v2는 학습 로그인 인증 준비만 담는다. 저장된 v1 계획의 의미·digest는 그대로다.
	learned := p.AuthPreparation != nil && p.AuthPreparation.Kind != ""
	if p.Version == candidateLLMPlanV2 != learned || learned && !candidateLearnedPreparationApproved(*p.AuthPreparation, p.ExcludedPaths, target) {
		return false
	}
	if p.AuthPreparation != nil && !learned && (p.AuthPreparation.PrepMethod != "" || p.AuthPreparation.PrepPath != "" || p.AuthPreparation.Requests != 0 ||
		p.AuthPreparation.Trust != "") {
		return false
	}
	if p.AuthPreparation != nil && (p.AuthPreparation.Method != "POST" || !validSelectedExactPath(p.AuthPreparation.ExactPath) ||
		!candidateVerificationDigest.MatchString(p.AuthPreparation.LoginDigest) ||
		pathscope.ExcludedAny(p.AuthPreparation.ExactPath, p.ExcludedPaths) ||
		!pathWithinAny(p.AuthPreparation.ExactPath, target.PathPrefixes) ||
		!slices.Contains(target.Methods, "POST")) {
		return false
	}
	parsed, err := url.Parse(original.TargetURL)
	if err != nil || parsed.Scheme+"://"+parsed.Host != p.Origin {
		return false
	}
	reproductions := 0
	for _, action := range p.Actions {
		if action.Purpose == "" || len(action.Purpose) > 1024 || action.Method != "GET" && action.Method != "HEAD" ||
			!validSelectedExactPath(action.ExactPath) || action.Kind != "" && action.Kind != "normal_control" ||
			!validSelectedActionInputs(action.Query, action.Headers) ||
			pathscope.ExcludedAny(action.ExactPath, p.ExcludedPaths) ||
			!slices.Contains(scope.Methods, action.Method) || !slices.Contains(target.Methods, action.Method) {
			return false
		}
		if action.Kind == "" {
			reproductions++
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
	return reproductions > 0
}

// candidateLearnedPreparationApproved는 학습 로그인 준비 요청이 지금 대상 승인 안인지다: 정확한 로그인 POST와 (있으면)
// 준비 GET 하나가 같은 origin의 정확한 경로이고, 제외 경로·위험 동작 단어 밖이며, 대상 승인 경로 접두 안이고, 대상이 그
// 메서드를 허용한다.
func candidateLearnedPreparationApproved(a CandidateLLMAuthPreparation, excluded []string, target diagnosisprep.ProductTarget) bool {
	if a.Kind != CandidateAuthLearnedV1 && a.Kind != CandidateAuthLearnedV2 || !validCandidateLoginTrust(a.Trust) ||
		candidateRiskyLoginPath("POST", a.ExactPath) {
		return false
	}
	switch a.Requests {
	case 1:
		if a.PrepMethod != "" || a.PrepPath != "" || a.Kind == CandidateAuthLearnedV2 {
			return false
		}
	case 2:
		if a.PrepMethod != "GET" || !validSelectedExactPath(a.PrepPath) || pathscope.ExcludedAny(a.PrepPath, excluded) ||
			!pathWithinAny(a.PrepPath, target.PathPrefixes) || !slices.Contains(target.Methods, "GET") || candidateRiskyLoginPath("GET", a.PrepPath) ||
			a.Kind == CandidateAuthLearnedV1 && a.PrepPath != a.ExactPath || a.Kind == CandidateAuthLearnedV2 && a.PrepPath == a.ExactPath {
			return false
		}
	default:
		return false
	}
	return true
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func pathWithinAny(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if pathscope.Allows(path, false, prefix) {
			return true
		}
	}
	return false
}

func CandidateLLMPlanStillApproved(now time.Time, p CandidateLLMPlan, original diagnosisprep.Input, scope diagnosisprep.Scope, target diagnosisprep.ProductTarget) bool {
	return candidateLLMPlanStillApproved(now, p, original, scope, target)
}

func makeCandidateLLMPlan(now time.Time, candidate db.DiagnosticCandidate, originalObservation *candidateverify.StoredObservationV2, analysis candidateAnalysisView,
	original diagnosisprep.Input, scope diagnosisprep.Scope, target diagnosisprep.ProductTarget, auth *CandidatePlanAuth) (CandidateLLMPlan, string, error) {
	if candidate.Validate() != nil || ownershipIDORCandidate(candidate) || analysis.Proposal.CandidateID != candidate.ID ||
		analysis.Proposal.Version != "selected/analysis-v1" || target.State != "approved" || target.Revision == 0 || target.ApprovedAt.IsZero() ||
		!now.Before(target.ExpiresAt) || !now.Before(scope.ExpiresAt) || len(analysis.Proposal.Actions) == 0 ||
		originalObservation == nil || !candidateverify.ValidSelectedStoredObservation(candidate.TaskID, candidate.RunRef, *originalObservation) ||
		originalObservation.Source.CallRef != candidate.CallRef {
		return CandidateLLMPlan{}, "", ErrCandidateLLMPlan
	}
	accountRef := originalObservation.Source.Observation.RequestAccountRef
	if originalObservation.Source.Observation.RequestAuthContext == "none" && accountRef != "" ||
		originalObservation.Source.Observation.RequestAuthContext != "none" && !candidateAccountRevision.MatchString(accountRef) ||
		(accountRef == "") != (auth == nil) || auth != nil && (auth.Configured == nil) == (auth.Learned == nil) {
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
	p := CandidateLLMPlan{Version: candidateLLMPlanV1, TaskID: candidate.TaskID, CandidateID: candidate.ID,
		EvidenceDigest: candidate.EvidenceDigest, OriginalRunRef: candidate.RunRef, AnalysisDigest: analysis.Digest,
		AccountRef: accountRef,
		TargetID:   target.ID, TargetRevision: target.Revision, Origin: target.Origin, MaxRequests: len(analysis.Proposal.Actions),
		DurationSeconds: 90, ApprovedAt: target.ApprovedAt, ExpiresAt: until}
	switch {
	case auth != nil && auth.Configured != nil:
		login := auth.Configured
		p.AuthPreparation = &CandidateLLMAuthPreparation{Method: login.Method, ExactPath: login.Path, LoginDigest: candidateLoginDigest(*login)}
		p.MaxRequests++
	case auth != nil:
		p.AuthPreparation = CandidateLearnedAuthPreparation(*auth.Learned)
		if p.AuthPreparation == nil {
			return CandidateLLMPlan{}, "", ErrCandidateLLMPlan
		}
		p.Version = candidateLLMPlanV2
		p.MaxRequests += p.AuthPreparation.Requests
	}
	if original.ExcludedPaths != "" {
		p.ExcludedPaths = strings.Split(original.ExcludedPaths, "\n")
	}
	for _, proposal := range analysis.Proposal.Actions {
		p.Actions = append(p.Actions, CandidateLLMPlanAction{Purpose: proposal.Purpose, Method: proposal.Method, ExactPath: proposal.PathHint,
			Kind: proposal.Kind, Query: proposal.Query, Headers: proposal.Headers})
	}
	comparableReproduction := false
	for _, action := range p.Actions {
		if action.Kind == "" && selectedActionMatchesObservation(action, originalObservation.Source.Observation) {
			comparableReproduction = true
			break
		}
	}
	if !comparableReproduction {
		return CandidateLLMPlan{}, "", ErrCandidateLLMPlan
	}
	if !candidateLLMPlanStillApproved(now, p, original, scope, target) {
		return CandidateLLMPlan{}, "", ErrCandidateLLMPlan
	}
	digest, err := candidateLLMPlanDigest(p)
	return p, digest, err
}
