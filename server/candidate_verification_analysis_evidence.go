package server

import (
	"slices"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/responseevidence"
)

// 최초 분석 입력의 관찰 선택과 근거 요약이다. 프로그램은 관찰의 관련성·독립성·근거 유무만 정리하고, 의미 해석과 부족한 근거에
// 맞는 행동 제안은 분석 모델이 한다. 여기서 정한 값은 승인·저장 관찰 평가 자격을 바꾸지 않는다(그 판단은 기존 프로그램 경로).

// candidateAnalysisMaxObservations는 분석 입력의 관찰 상한이다(원본 포함, #950과 같음).
const candidateAnalysisMaxObservations = 6

// candidateAnalysisPromptBytes는 분석 입력 상한이다(#950과 같음). 넘으면 순위가 낮은 관찰의 구조 근거부터 뺀다.
const candidateAnalysisPromptBytes = 48 << 10

// 관찰과 원본의 관계다. 모델이 독립 근거의 수를 부풀리지 않게 프로그램이 정한다.
const (
	analysisRelationOriginal = "original"
	// 같은 기능·조건의 독립 관찰: 원본과 같은 메서드·경로·규칙·요청 조건(인증·계정·Query·Header)이고 원본과 다른 호출·시도이며
	// 후보 저장 이후 관찰이다. 기존 저장 관찰 평가가 쓰는 조건과 같다.
	analysisRelationIndependent = "independent_same_condition"
	// 같은 메서드·경로지만 독립 재현 조건을 채우지 못한 관찰: 입력·계정이 다르거나(정상 대조처럼 읽을 수 있음), 후보 저장 이전
	// 관찰이거나, 정책·범위·헤더 모드가 다르다. 재현이 아니다.
	analysisRelationOtherCondition = "same_function_not_independent"
	// 같은 경로의 다른 메서드.
	analysisRelationSamePath = "same_path_other_method"
	analysisRelationOther    = "other_function"
)

var analysisRelationRank = map[string]int{analysisRelationOriginal: 0, analysisRelationIndependent: 1,
	analysisRelationOtherCondition: 2, analysisRelationSamePath: 3, analysisRelationOther: 4}

// candidateAnalysisRelation은 저장 관찰 하나와 원본의 관계다.
func candidateAnalysisRelation(c CandidateSelectedContext, original *candidateverify.StoredObservationV2, row candidateverify.StoredObservationV2) string {
	if original == nil {
		return analysisRelationOther
	}
	if row.Source.CallRef == original.Source.CallRef {
		return analysisRelationOriginal
	}
	base, obs := original.Source.Observation, row.Source.Observation
	if obs.Endpoint != base.Endpoint {
		return analysisRelationOther
	}
	if obs.Method != base.Method {
		return analysisRelationSamePath
	}
	if storedComparisonEligible(c, *original, row) {
		return analysisRelationIndependent
	}
	return analysisRelationOtherCondition
}

// storedComparisonEligible은 selectedStoredComparisons의 행 조건(봉인 확인 제외)이다. 분석 입력은 이미 봉인된 관찰만 받는다.
func storedComparisonEligible(c CandidateSelectedContext, original, row candidateverify.StoredObservationV2) bool {
	primary, source := original.Source, row.Source
	base, obs := primary.Observation, source.Observation
	return c.Candidate.SaveFence != nil && c.Candidate.SaveFence.AttemptHighWater != 0 && base.ExactFunction && base.Endpoint != "" &&
		base.Method != "" && base.RuleRef != "" && base.RequestAuthContext != "" &&
		source.CallRef != primary.CallRef && obs.AttemptRef != base.AttemptRef && obs.Method == base.Method && obs.Endpoint == base.Endpoint &&
		obs.RuleRef == base.RuleRef && candidateRequestContextComparable(base, obs) && obs.ExactFunction &&
		source.ScopeDigest == primary.ScopeDigest && source.PolicyRevision == primary.PolicyRevision && row.HeaderMode == original.HeaderMode &&
		obs.Sequence > c.Candidate.SaveFence.AttemptHighWater
}

// candidateAnalysisObservations는 분석 입력에 넣을 관찰을 결정적으로 고른다: 원본, 같은 조건의 독립 관찰, 같은 기능의 다른
// 조건, 같은 경로의 다른 메서드, 그 밖 순서. 같은 순위에서는 구조 근거가 있는 관찰, 그다음 시퀀스·호출 참조 순이다. 같은 호출이나
// 같은 시도의 다른 행은 독립 근거가 아니므로 하나만 넣는다. 모델이 인용할 수 있는 참조는 이 목록과 같다.
func candidateAnalysisObservations(c CandidateSelectedContext, original *candidateverify.StoredObservationV2) []candidateverify.StoredObservationV2 {
	type ranked struct {
		row      candidateverify.StoredObservationV2
		rank     int
		evidence bool
	}
	var pool []ranked
	for _, row := range c.Observations {
		if row.Source.CallRef == c.Candidate.CallRef {
			continue
		}
		pool = append(pool, ranked{row: row, rank: analysisRelationRank[candidateAnalysisRelation(c, original, row)],
			evidence: candidateverify.RowResponseEvidence(row).State.Structured()})
	}
	slices.SortStableFunc(pool, func(a, b ranked) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		if a.evidence != b.evidence {
			if a.evidence {
				return -1
			}
			return 1
		}
		if a.row.Source.Observation.Sequence != b.row.Source.Observation.Sequence {
			if a.row.Source.Observation.Sequence < b.row.Source.Observation.Sequence {
				return -1
			}
			return 1
		}
		switch {
		case a.row.Source.CallRef < b.row.Source.CallRef:
			return -1
		case a.row.Source.CallRef > b.row.Source.CallRef:
			return 1
		}
		return 0
	})
	selected := make([]candidateverify.StoredObservationV2, 0, candidateAnalysisMaxObservations)
	calls, attempts := map[string]bool{}, map[string]bool{}
	if original != nil {
		selected = append(selected, *original)
		calls[original.Source.CallRef], attempts[original.Source.Observation.AttemptRef] = true, true
	}
	for _, item := range pool {
		if len(selected) >= candidateAnalysisMaxObservations {
			break
		}
		call, attempt := item.row.Source.CallRef, item.row.Source.Observation.AttemptRef
		if calls[call] || attempt != "" && attempts[attempt] {
			continue
		}
		calls[call], attempts[attempt] = true, true
		selected = append(selected, item.row)
	}
	return selected
}

// candidateAnalysisObservation은 분석 모델이 보는 관찰이다. 관계는 프로그램이 정한 값이다.
type candidateAnalysisObservation struct {
	candidateModelObservation
	Relation string `json:"relation_to_original"`
	// EvidenceOmitted는 입력 상한 때문에 이 관찰의 구조 근거를 뺐다는 뜻이다(관찰이 없다는 뜻이 아님).
	EvidenceOmitted bool `json:"response_evidence_omitted,omitempty"`
}

// candidateAnalysisEvidenceSummary는 주장에 필요한 사실과 지금 관찰에 있는 사실이다. 판정이 아니다.
type candidateAnalysisEvidenceSummary struct {
	candidateClaimRequirement
	// OriginalEvidenceState는 원본 관찰 구조 근거의 상태다(not_recorded·limited·truncated 등은 그대로 전한다).
	OriginalEvidenceState string   `json:"original_evidence_state"`
	OriginalFacts         []string `json:"original_observed_facts"`
	// StoredReproductions는 같은 조건의 독립 저장 관찰 수다. 1~3이면 프로그램이 새 대상 송신 없이 저장 관찰 평가를 먼저 시도한다.
	StoredReproductions     int      `json:"stored_independent_observations"`
	StoredAssessmentPending bool     `json:"stored_assessment_expected"`
	StoredFacts             []string `json:"stored_observed_facts"`
	// Missing은 원본과 저장 독립 관찰에 공통으로 없는 사실이다(업무 진술은 분석 단계에서 늘 없음).
	Missing []string `json:"missing_facts"`
}

// candidateAnalysisEvidence는 분석 입력의 근거 요약을 만든다. 관찰 사실은 원본과 모든 저장 독립 관찰에 공통인 것만 센다.
func candidateAnalysisEvidence(c CandidateSelectedContext, original *candidateverify.StoredObservationV2) candidateAnalysisEvidenceSummary {
	req := candidateClaimRequirementOf(c.Candidate)
	out := candidateAnalysisEvidenceSummary{candidateClaimRequirement: req, OriginalEvidenceState: string(responseevidence.StateNotRecorded),
		OriginalFacts: []string{}, StoredFacts: []string{}}
	if original == nil {
		out.Missing = claimMissingFacts(req, nil)
		return out
	}
	originalEv := candidateverify.RowResponseEvidence(*original)
	out.OriginalEvidenceState = string(originalEv.State)
	have := map[string]bool{}
	for _, fact := range observationFacts(originalEv) {
		if claimObservationFacts[fact] || fact == factGenericError {
			out.OriginalFacts = append(out.OriginalFacts, fact)
		}
		have[fact] = claimObservationFacts[fact]
	}
	var stored map[string]bool
	for _, row := range c.Observations {
		if row.Source.CallRef == original.Source.CallRef || !storedComparisonEligible(c, *original, row) {
			continue
		}
		out.StoredReproductions++
		facts := map[string]bool{}
		for _, fact := range observationFacts(candidateverify.RowResponseEvidence(row)) {
			facts[fact] = true
		}
		if stored == nil {
			stored = facts
			continue
		}
		for fact := range stored {
			stored[fact] = stored[fact] && facts[fact]
		}
	}
	// 저장 관찰 평가는 같은 조건 독립 관찰이 1~3개일 때만 한다(넘으면 골라 쓰지 않는다: selectedStoredComparisons).
	out.StoredAssessmentPending = out.StoredReproductions >= 1 && out.StoredReproductions <= 3
	for fact, ok := range stored {
		if ok && claimObservationFacts[fact] {
			out.StoredFacts = append(out.StoredFacts, fact)
		}
	}
	slices.Sort(out.StoredFacts)
	if stored != nil {
		for fact := range have {
			have[fact] = have[fact] && stored[fact]
		}
	}
	out.Missing = claimMissingFacts(req, have)
	return out
}
