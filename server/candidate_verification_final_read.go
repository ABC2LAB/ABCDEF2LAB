package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strconv"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
)

// A completed model event is read only after rechecking the immutable model
// output, original run, approved plan, sealed follow-up and cited sources.
// readCandidateFinalVerdict는 프로그램 비교 결과 없이 판정만 돌려준다(보고서 경로).
func readCandidateFinalVerdict(ctx context.Context, tx *sql.Tx, row candidateVerificationAttempt,
	binding candidateVerificationBinding, candidate db.DiagnosticCandidate) (*CandidateFinalVerdict, error) {
	verdict, _, err := readCandidateFinalVerdictReview(ctx, tx, row, binding, candidate)
	return verdict, err
}

func readCandidateFinalVerdictReview(ctx context.Context, tx *sql.Tx, row candidateVerificationAttempt,
	binding candidateVerificationBinding, candidate db.DiagnosticCandidate) (*CandidateFinalVerdict, *CandidateEvidenceReview, error) {
	if row.Method != "llm/v1" || row.Latest.State != "completed" {
		return nil, nil, nil
	}
	if row.LLMPlan == nil {
		return readStoredCandidateFinalVerdictReview(ctx, tx, row, binding, candidate)
	}
	if row.Latest.Verdict == nil || !binding.FollowupTask.Valid || !binding.RunRef.Valid {
		return nil, nil, ErrCandidateFinalVerdict
	}
	id, err := strconv.ParseInt(row.ID, 10, 64)
	if err != nil || id <= 0 {
		return nil, nil, ErrCandidateFinalVerdict
	}
	var raw []byte
	var digest string
	var profileID, inputTokens, outputTokens, costMicros int64
	var requests int
	if tx.QueryRowContext(ctx, `SELECT result,result_digest,profile_id,model_requests,input_tokens,output_tokens,cost_usd_micros
FROM diagnostic_candidate_verification_verdicts WHERE request_id=$1`, id).
		Scan(&raw, &digest, &profileID, &requests, &inputTokens, &outputTokens, &costMicros) != nil ||
		len(raw) > 16<<10 || requests != 1 || inputTokens < 0 || outputTokens < 0 || costMicros < 0 {
		return nil, nil, ErrCandidateFinalVerdict
	}
	analysis, err := readCandidateAnalysis(ctx, tx, id, candidate.ID)
	if err != nil || analysis == nil || analysis.ProfileID != profileID || !candidateLLMPlanMatchesAnalysis(*row.LLMPlan, *analysis) {
		return nil, nil, ErrCandidateFinalVerdict
	}
	answers, err := readCandidateBusinessAnswers(ctx, tx, id, *analysis)
	if err != nil {
		return nil, nil, ErrCandidateFinalVerdict
	}
	run, err := readDiagnosticRun(ctx, tx, candidate.TaskID)
	if err != nil || run == nil || run.State != report.DiagnosticRunCompleted || run.VerificationVersion != diagnosisVerificationSelected ||
		run.RunRef != candidate.RunRef {
		return nil, nil, ErrCandidateFinalVerdict
	}
	var originalRows []candidateverify.StoredObservationV2
	var original *candidateverify.StoredObservationV2
	if run.VerificationObservationsStored {
		originalRows, err = readCandidateObservations(ctx, tx, candidate.TaskID, candidate.RunRef)
		if err != nil || len(originalRows) != run.VerificationObservationsCount {
			return nil, nil, ErrCandidateFinalVerdict
		}
		originalDigest, err := candidateObservationsDigest(originalRows)
		if err != nil || originalDigest != run.VerificationObservationsDigest {
			return nil, nil, ErrCandidateFinalVerdict
		}
		seal, ok, sealErr := db.ReadSealCheck(ctx, tx, candidate.TaskID, candidate.RunRef)
		if sealErr != nil || !ok {
			return nil, nil, ErrCandidateFinalVerdict
		}
		originalRows = sealedCandidateObservations(originalRows, seal)
		original, err = selectedOriginalObservation(candidate, originalRows)
		if err != nil {
			return nil, nil, ErrCandidateFinalVerdict
		}
	} else if !run.VerificationSourceUnavailable {
		return nil, nil, ErrCandidateFinalVerdict
	}
	followup, err := readCandidateObservations(ctx, tx, binding.FollowupTask.Int64, binding.RunRef.String)
	if err != nil || len(followup) != len(row.LLMPlan.Actions) {
		return nil, nil, ErrCandidateFinalVerdict
	}
	var evidenceRefs []candidateVerificationSourceRef
	if tx.QueryRowContext(ctx, `SELECT sources FROM diagnostic_candidate_verification_events
WHERE request_id=$1 AND step=7 AND process_state='evidence_ready'`, id).Scan(&raw) != nil ||
		json.Unmarshal(raw, &evidenceRefs) != nil || len(evidenceRefs) != len(followup) {
		return nil, nil, ErrCandidateFinalVerdict
	}
	context := CandidateAssessmentContext{
		Original: CandidateSelectedContext{RequestID: id, TaskID: candidate.TaskID, Candidate: candidate, RunRef: candidate.RunRef,
			Observations: originalRows, OriginalObservation: original, SourceUnavailable: run.VerificationSourceUnavailable},
		Plan: *row.LLMPlan, Analysis: *analysis, Answers: answers, FollowupTaskID: binding.FollowupTask.Int64,
		FollowupRunRef: binding.RunRef.String, Followup: followup, EvidenceRefs: evidenceRefs,
	}
	if revalidateCandidateAssessmentEvidence(ctx, tx, context) != nil {
		return nil, nil, ErrCandidateFinalVerdict
	}
	if tx.QueryRowContext(ctx, `SELECT result FROM diagnostic_candidate_verification_verdicts WHERE request_id=$1`, id).Scan(&raw) != nil {
		return nil, nil, ErrCandidateFinalVerdict
	}
	var verdict CandidateFinalVerdict
	if strictJSON(raw, &verdict) != nil {
		return nil, nil, ErrCandidateFinalVerdict
	}
	// 저장된 판정에 적힌 게이트 판본으로 다시 검사한다(없으면 v1 이전 의미). 새 정책이 옛 판정을 바꾸지 않는다.
	context.EvidenceGate = verdict.EvidenceGate
	parsed, err := ParseCandidateFinalVerdict(raw, context)
	if err != nil || !reflect.DeepEqual(parsed, verdict) || verdict.Verdict != *row.Latest.Verdict ||
		row.Latest.Reason != "model_evidence_checked" {
		return nil, nil, ErrCandidateFinalVerdict
	}
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return nil, nil, ErrCandidateFinalVerdict
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, nil, ErrCandidateFinalVerdict
	}
	var refs []candidateVerificationSourceRef
	if json.Unmarshal(row.Latest.Sources, &refs) != nil || !reflect.DeepEqual(refs, verdict.Sources) {
		return nil, nil, ErrCandidateFinalVerdict
	}
	return &verdict, reviewCandidateEvidence(context), nil
}
