// Package candidateverify는 저장된 감사 완료 관찰만 대조한다.
// 새 요청을 보내거나 모델 글을 독립 증거로 취급하지 않는다.
package candidateverify

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
)

const (
	Confirmed    = "confirmed"
	Negative     = "negative"
	Insufficient = "insufficient"
	Unsupported  = "unsupported"
)

type Result struct {
	Status   string     `json:"status"`
	Reason   string     `json:"reason"`
	Attempts []string   `json:"attempt_refs,omitempty"`
	Sources  []SourceV2 `json:"sources,omitempty"`
}

type evidence struct {
	Related []json.RawMessage `json:"related_calls"`
	Trusted struct {
		Final bool `json:"final"`
		Call  struct {
			CallRef               string                    `json:"call_ref"`
			Final                 bool                      `json:"final"`
			AuditComplete         bool                      `json:"audit_complete"`
			ObservationsTruncated bool                      `json:"observations_truncated"`
			Observations          []e2proxy.HTTPObservation `json:"observations"`
		} `json:"call"`
	} `json:"trusted"`
}

// Assess는 승인된 경로 패턴에서 서로 다른 완료·감사 GET을 대조한다.
// 결과는 관찰된 설정·배너 사실이며 악용 가능성 판정은 아니다.
func Assess(c db.DiagnosticCandidate) Result {
	if (c.VerificationClaim != "html_nosniff_missing" || c.VulnClass != "security_headers") &&
		(c.VerificationClaim != "server_version_banner" || c.VulnClass != "information_disclosure") {
		return Result{Status: Unsupported, Reason: "type_not_supported"}
	}
	if c.Validate() != nil || c.Classification != "completed" || c.OutputTruncated {
		return Result{Status: Insufficient, Reason: "candidate_evidence_unavailable"}
	}
	var primary evidence
	if json.Unmarshal([]byte(c.Evidence), &primary) != nil {
		return Result{Status: Insufficient, Reason: "candidate_evidence_unavailable"}
	}
	base, baseState, ok := oneObservation(primary, c.CallRef, c.VerificationClaim)
	if !ok {
		return Result{Status: Insufficient, Reason: "primary_observation_unavailable"}
	}
	states := []string{baseState}
	refs := []string{base.AttemptRef}
	seen := map[string]bool{base.AttemptRef: true}
	for _, raw := range primary.Related {
		var other evidence
		if json.Unmarshal(raw, &other) != nil {
			return Result{Status: Insufficient, Reason: "related_observation_unavailable"}
		}
		obs, state, valid := oneObservation(other, other.Trusted.Call.CallRef, c.VerificationClaim)
		if !valid || obs.Endpoint != base.Endpoint || obs.RuleRef != base.RuleRef {
			continue
		}
		if seen[obs.AttemptRef] {
			return Result{Status: Insufficient, Reason: "observation_not_independent"}
		}
		seen[obs.AttemptRef] = true
		states = append(states, state)
		refs = append(refs, obs.AttemptRef)
	}
	if len(states) < 2 {
		return Result{Status: Insufficient, Reason: "independent_observation_missing"}
	}
	for _, state := range states[1:] {
		if state != states[0] {
			return Result{Status: Insufficient, Reason: "observations_inconsistent"}
		}
	}
	sort.Strings(refs)
	switch c.VerificationClaim {
	case "html_nosniff_missing":
		if states[0] == "nosniff_missing" {
			return Result{Status: Confirmed, Reason: "html_nosniff_missing_twice", Attempts: refs}
		}
		return Result{Status: Negative, Reason: "html_nosniff_present_twice", Attempts: refs}
	case "server_version_banner":
		if strings.HasPrefix(states[0], "banner/") {
			return Result{Status: Confirmed, Reason: "server_version_banner_twice", Attempts: refs}
		}
		return Result{Status: Negative, Reason: "server_versionless_banner_twice", Attempts: refs}
	}
	return Result{Status: Unsupported, Reason: "type_not_supported"}
}

func oneObservation(e evidence, callRef, claim string) (e2proxy.HTTPObservation, string, bool) {
	if !e.Trusted.Final || !e.Trusted.Call.Final || !e.Trusted.Call.AuditComplete || e.Trusted.Call.ObservationsTruncated || len(e.Trusted.Call.Observations) != 1 || e.Trusted.Call.CallRef != callRef {
		return e2proxy.HTTPObservation{}, "", false
	}
	var found e2proxy.HTTPObservation
	var foundState string
	for _, obs := range e.Trusted.Call.Observations {
		if obs.Sequence == 0 || obs.ObservedAt.IsZero() || obs.Method != "GET" || obs.StatusCode != 200 || obs.Endpoint == "" || obs.RuleRef == "" ||
			obs.Decision != "allowed" || !obs.ForwardAttempted || !obs.ResponseComplete || !obs.AuditComplete ||
			obs.ResponseTruncated || obs.AttemptRef == "" {
			continue
		}
		state := ""
		switch claim {
		case "html_nosniff_missing":
			state = obs.HTMLSecurity
			if state != "nosniff_missing" && state != "nosniff_present" {
				continue
			}
		case "server_version_banner":
			serverHeaders := 0
			for _, technology := range obs.Technologies {
				if technology.Header == "Server" && technology.Name != "" {
					serverHeaders++
					state = "versionless"
					if technology.Version != "" {
						state = "banner/" + technology.Name + "/" + technology.Version
					}
				}
			}
			if serverHeaders != 1 {
				continue
			}
		default:
			continue
		}
		if found.AttemptRef != "" {
			return e2proxy.HTTPObservation{}, "", false
		}
		found = obs
		foundState = state
	}
	return found, foundState, found.AttemptRef != ""
}
