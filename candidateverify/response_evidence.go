package candidateverify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/responseevidence"
)

// SelectedResponseEvidence는 저장 관찰 하나에 결속한 응답 구조 근거다.
//
// 근거는 E② 프록시가 완결 본문에서 만든 Runner 관찰이다. Host 감사 저널은 상태·시도·결정만 증명하고 본문 구조는 독립적으로
// 증명하지 않는다. 그래서 이 근거의 신뢰 수준은 같은 관찰의 미디어 범주·기술 배너와 같다. 결속(attempt_ref·sequence·
// observation_digest)과 자체 digest는 저장 뒤 바꿔치기와 다른 관찰로의 이동을 막지만, 본문 구조를 Host가 봤다는 뜻은 아니다.
type SelectedResponseEvidence struct {
	AttemptRef        string                    `json:"attempt_ref"`
	Sequence          uint64                    `json:"sequence"`
	ObservationDigest string                    `json:"observation_digest"`
	Evidence          responseevidence.Evidence `json:"evidence"`
	Digest            string                    `json:"digest"`
}

func responseEvidenceDigest(e SelectedResponseEvidence) string {
	e.Digest = ""
	raw, err := json.Marshal(e)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// BindResponseEvidence는 근거를 저장 관찰의 출처에 결속하고 자체 digest를 붙인다.
func BindResponseEvidence(source SourceV2, ev responseevidence.Evidence) *SelectedResponseEvidence {
	out := &SelectedResponseEvidence{AttemptRef: source.Observation.AttemptRef, Sequence: source.Observation.Sequence,
		ObservationDigest: source.Digest, Evidence: ev}
	out.Digest = responseEvidenceDigest(*out)
	return out
}

// notRecordedEvidence는 새 형식 행에서 근거를 만들지 못했음을 명시한다(예산·크기 초과·중복 근거). 빈 구조로 읽지 않는다.
func notRecordedEvidence(statusCode int, mediaClass string) responseevidence.Evidence {
	if mediaClass == "" {
		mediaClass = "unknown"
	}
	return responseevidence.Evidence{Schema: responseevidence.Schema, State: responseevidence.StateNotRecorded, MediaClass: mediaClass, StatusCode: statusCode}
}

// callResponseEvidence는 관찰과 같은 시도의 근거를 정확히 하나 찾는다. 없거나 둘 이상이면 근거를 쓰지 않는다.
func callResponseEvidence(call e2runner.TrustedResult, obs e2proxy.HTTPObservation) (responseevidence.Evidence, bool) {
	var found *responseevidence.Evidence
	for i := range call.Call.ResponseEvidence {
		item := call.Call.ResponseEvidence[i]
		if item.AttemptRef != obs.AttemptRef && item.Sequence != obs.Sequence {
			continue
		}
		if found != nil || item.AttemptRef != obs.AttemptRef || item.Sequence != obs.Sequence {
			// 같은 시도를 가리키는 근거가 둘이거나 한쪽 참조만 맞는다. 어느 쪽도 고르지 않는다.
			return responseevidence.Evidence{}, false
		}
		found = &item.Evidence
	}
	if found == nil || responseevidence.Validate(*found) != nil || found.State == responseevidence.StateNotRecorded ||
		found.StatusCode != obs.StatusCode || !compatibleResponseMedia(obs.ResponseMediaClass, found.MediaClass) {
		return responseevidence.Evidence{}, false
	}
	return *found, true
}

// 기존 관찰 범주는 +json과 XHTML을 other로 묶는다. 관찰 digest 의미를 바꾸지 않고 새 추출기의 세부 범주만 허용한다.
func compatibleResponseMedia(observed, extracted string) bool {
	return observed == "" || observed == extracted || observed == "other" && (extracted == "json" || extracted == "html")
}

// ValidResponseEvidence는 저장 근거의 형식과 관찰 결속을 확인한다. nil(옛 행)은 근거 없음으로 유효하다.
func ValidResponseEvidence(row StoredObservationV2) bool {
	e := row.ResponseEvidence
	if e == nil {
		return true
	}
	obs := row.Source.Observation
	if e.AttemptRef != obs.AttemptRef || e.Sequence != obs.Sequence || e.ObservationDigest != row.Source.Digest ||
		e.Digest == "" || e.Digest != responseEvidenceDigest(*e) || responseevidence.Validate(e.Evidence) != nil {
		return false
	}
	switch e.Evidence.State {
	case responseevidence.StateNotReceived:
		return false
	case responseevidence.StateNotRecorded:
		return e.Evidence.JSON == nil && e.Evidence.HTML == nil && e.Evidence.Text == nil &&
			e.Evidence.StatusCode == obs.StatusCode && compatibleResponseMedia(obs.ResponseMediaClass, e.Evidence.MediaClass)
	}
	return e.Evidence.StatusCode == obs.StatusCode && compatibleResponseMedia(obs.ResponseMediaClass, e.Evidence.MediaClass)
}

// RowResponseEvidence는 비교에 넣을 근거를 돌려준다. 옛 행(봉투 없음)은 not_recorded다. 이 값은 소급 생성이 아니라 '기록 없음'이다.
func RowResponseEvidence(row StoredObservationV2) responseevidence.Evidence {
	if row.ResponseEvidence == nil {
		return notRecordedEvidence(row.Source.Observation.StatusCode, row.Source.Observation.ResponseMediaClass)
	}
	return row.ResponseEvidence.Evidence
}

// fitSelectedRow는 행을 4096바이트 안에 넣는다. 응답 근거를 먼저 줄이고, 그래도 넘으면 근거를 not_recorded로 바꾼다.
// Host 증빙은 근거 때문에 버리지 않는다(증빙 자체가 상한을 넘는 기존 경우만 기존 규칙대로 증빙 없는 관찰로 남긴다).
func fitSelectedRow(row StoredObservationV2, ev *responseevidence.Evidence) (StoredObservationV2, bool) {
	evidence := notRecordedEvidence(row.Source.Observation.StatusCode, row.Source.Observation.ResponseMediaClass)
	if ev != nil {
		evidence = *ev
	}
	attempt := func(r StoredObservationV2, e responseevidence.Evidence) (StoredObservationV2, bool) {
		r.ResponseEvidence = BindResponseEvidence(r.Source, e)
		raw, err := json.Marshal(r)
		return r, err == nil && len(raw) <= storedObservationRowBytes
	}
	for _, proof := range []bool{true, false} {
		r := row
		if !proof {
			if row.SelectedProof == nil {
				break
			}
			// 기존 규칙: 증빙이 행 상한을 넘으면 증빙 없는 관찰을 남긴다(저장만으로는 확정 근거가 못 된다).
			r.SelectedProof = nil
		}
		if out, ok := attempt(r, evidence); ok {
			return out, true
		}
		base, _ := attempt(r, notRecordedEvidence(evidence.StatusCode, evidence.MediaClass))
		raw, err := json.Marshal(base)
		if err != nil {
			continue
		}
		overhead := len(raw) - encodedLen(notRecordedEvidence(evidence.StatusCode, evidence.MediaClass))
		if fitted, ok := responseevidence.Fit(evidence, storedObservationRowBytes-overhead); ok {
			if out, ok := attempt(r, fitted); ok {
				return out, true
			}
		}
		recorded := notRecordedEvidence(evidence.StatusCode, evidence.MediaClass)
		if evidence.State == responseevidence.StateNotRecorded {
			recorded = evidence
		}
		if out, ok := attempt(r, recorded); ok {
			return out, true
		}
	}
	return StoredObservationV2{}, false
}

func encodedLen(ev responseevidence.Evidence) int {
	raw, _ := json.Marshal(ev)
	return len(raw)
}

const storedObservationRowBytes = 4096
