package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strconv"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
)

func readStoredCandidateFinalVerdictReview(ctx context.Context, tx *sql.Tx, row candidateVerificationAttempt,
	binding candidateVerificationBinding, candidate db.DiagnosticCandidate) (*CandidateFinalVerdict, *CandidateEvidenceReview, error) {
	if row.Method != "llm/v1" || row.Latest.State != "completed" || row.Latest.Reason != "stored_model_evidence_checked" ||
		row.LLMPlan != nil || row.PlanDigest != "" || row.Latest.ExecutionRef != "" ||
		binding.FollowupTask.Valid || binding.RunRef.Valid || binding.Decision.Valid || row.Latest.Verdict == nil {
		return nil, nil, ErrCandidateFinalVerdict
	}
	id, err := strconv.ParseInt(row.ID, 10, 64)
	if err != nil || id <= 0 {
		return nil, nil, ErrCandidateFinalVerdict
	}
	run, err := readDiagnosticRun(ctx, tx, candidate.TaskID)
	if err != nil || run == nil || run.State != report.DiagnosticRunCompleted ||
		run.VerificationVersion != diagnosisVerificationSelected || run.RunRef != candidate.RunRef || !run.VerificationObservationsStored {
		return nil, nil, ErrCandidateFinalVerdict
	}
	rows, err := readCandidateObservations(ctx, tx, candidate.TaskID, candidate.RunRef)
	if err != nil || len(rows) != run.VerificationObservationsCount {
		return nil, nil, ErrCandidateFinalVerdict
	}
	digest, err := candidateObservationsDigest(rows)
	if err != nil || digest != run.VerificationObservationsDigest {
		return nil, nil, ErrCandidateFinalVerdict
	}
	seal, ok, err := db.ReadSealCheck(ctx, tx, candidate.TaskID, candidate.RunRef)
	if err != nil || !ok {
		return nil, nil, ErrCandidateFinalVerdict
	}
	rows = sealedCandidateObservations(rows, seal)
	primary, err := selectedOriginalObservation(candidate, rows)
	if err != nil || primary == nil {
		return nil, nil, ErrCandidateFinalVerdict
	}
	original := CandidateSelectedContext{RequestID: id, TaskID: candidate.TaskID, Candidate: candidate,
		RunRef: candidate.RunRef, Observations: rows, OriginalObservation: primary}
	comparisons, err := selectedStoredComparisons(original, seal)
	if err != nil {
		return nil, nil, ErrCandidateFinalVerdict
	}
	analysis, err := readCandidateAnalysis(ctx, tx, id, candidate.ID)
	if err != nil || analysis == nil {
		return nil, nil, ErrCandidateFinalVerdict
	}
	answers, err := readCandidateBusinessAnswers(ctx, tx, id, *analysis)
	if err != nil {
		return nil, nil, ErrCandidateFinalVerdict
	}
	c := CandidateAssessmentContext{Original: original, StoredOnly: true, Analysis: *analysis, Answers: answers,
		FollowupTaskID: candidate.TaskID, FollowupRunRef: candidate.RunRef, Followup: comparisons,
		EvidenceRefs: candidateVerificationSourceRefs(storedSources(comparisons))}
	if revalidateStoredCandidateEvidence(ctx, tx, c) != nil {
		return nil, nil, ErrCandidateFinalVerdict
	}
	var raw []byte
	var savedDigest string
	var profileID, inputTokens, outputTokens, costMicros int64
	var modelRequests int
	if tx.QueryRowContext(ctx, `SELECT result,result_digest,profile_id,model_requests,input_tokens,output_tokens,cost_usd_micros
FROM diagnostic_candidate_verification_verdicts WHERE request_id=$1`, id).
		Scan(&raw, &savedDigest, &profileID, &modelRequests, &inputTokens, &outputTokens, &costMicros) != nil ||
		len(raw) > 16<<10 || profileID != analysis.ProfileID || modelRequests != 1 ||
		inputTokens < 0 || outputTokens < 0 || costMicros < 0 {
		return nil, nil, ErrCandidateFinalVerdict
	}
	var verdict CandidateFinalVerdict
	if strictJSON(raw, &verdict) != nil {
		return nil, nil, ErrCandidateFinalVerdict
	}
	// 저장된 판정에 적힌 게이트 판본으로 다시 검사한다(없으면 v1 이전 의미). 새 정책이 옛 판정을 바꾸지 않는다.
	c.EvidenceGate = verdict.EvidenceGate
	parsed, err := ParseCandidateFinalVerdict(raw, c)
	if err != nil || !reflect.DeepEqual(parsed, verdict) || verdict.Verdict != *row.Latest.Verdict {
		return nil, nil, ErrCandidateFinalVerdict
	}
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return nil, nil, ErrCandidateFinalVerdict
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != savedDigest {
		return nil, nil, ErrCandidateFinalVerdict
	}
	var refs []candidateVerificationSourceRef
	if json.Unmarshal(row.Latest.Sources, &refs) != nil || !reflect.DeepEqual(refs, verdict.Sources) {
		return nil, nil, ErrCandidateFinalVerdict
	}
	return &verdict, reviewCandidateEvidence(c), nil
}
