package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2host"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/transmit"
)

func TestSelectedObservationRequiresExactHostSealCheck(t *testing.T) {
	scope := transmit.Scope{TaskRef: "42", RunRef: "run-selected", CaseRef: "case-selected", ResetRef: "reset-selected", PolicyRef: "policy-selected"}
	at := time.Now().UTC()
	event := e2proxy.Event{Index: 2, Stage: "result", Receipt: e2proxy.Receipt{
		Version: e2proxy.JournalVersionV3, Scope: scope, IntentID: 7, PolicyRevision: "revision-selected",
		CallRef: "call-selected", AttemptID: "attempt-selected", Sequence: 1, Method: "GET", RuleRef: "rule-selected",
		Decision: "allowed", Reason: e2proxy.Complete, ForwardAttempted: true, RequestWritten: true,
		ResponseReceived: true, ResponseComplete: true, AuditComplete: true, StatusCode: 200, At: at.Add(25 * time.Millisecond),
	}}
	cp := e2proxy.JournalCheckpoint{Version: e2proxy.JournalVersionV3, JournalID: "journal-selected", Sequence: 2,
		MAC: strings.Repeat("a", 64), Scope: scope}
	rawScope, _ := json.Marshal(scope)
	observation := e2proxy.HTTPObservation{Sequence: 1, AttemptRef: event.AttemptID, Method: event.Method, Endpoint: "/documents",
		ExactFunction: true, RuleRef: event.RuleRef, Decision: event.Decision, Reason: event.Reason,
		StatusCode: event.StatusCode, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true, ObservedAt: at}
	rawObs, _ := json.Marshal(observation)
	row := candidateverify.StoredObservationV2{Revision: 2, HeaderMode: e2proxy.HeaderPreserve,
		Source: candidateverify.SourceV2{CallRef: event.CallRef, IntentID: event.IntentID, TaskRef: scope.TaskRef, RunRef: scope.RunRef,
			ScopeDigest: db.DiagnosticEvidenceDigest(string(rawScope)), PolicyRevision: event.PolicyRevision,
			Observation: observation, Digest: db.DiagnosticEvidenceDigest(string(rawObs))},
		SelectedProof: &e2runner.SelectedAuditProof{Event: event, Checkpoint: cp}}
	seal := db.SealCheckRecord{TaskID: 42, RunRef: scope.RunRef, Head: db.ProductCheckpoint{JournalID: cp.JournalID, Sequence: cp.Sequence, MAC: cp.MAC},
		Checks: []db.SealCheck{{Sequence: cp.Sequence, MAC: cp.MAC, Included: true}}}
	if !candidateverify.ValidSelectedStoredObservation(42, scope.RunRef, row) || !selectedObservationSealed(row, seal) {
		t.Fatal("matching ordinary result was rejected")
	}
	trusted := e2runner.TrustedResult{Execution: e2proxy.ExecutionView{Scope: scope, IntentID: event.IntentID, PolicyRevision: event.PolicyRevision},
		Call: e2proxy.CallResult{CallRef: event.CallRef, IntentID: event.IntentID, Observations: []e2proxy.HTTPObservation{observation}}}
	if !selectedVerifiedResult(trusted, e2host.Verification{Event: event, Checkpoint: cp}) {
		t.Fatal("Host v3 result did not match Runner observation")
	}
	legacyEvent := event
	legacyEvent.Version = e2proxy.Version
	if selectedVerifiedResult(trusted, e2host.Verification{Event: legacyEvent, Checkpoint: cp}) {
		t.Fatal("legacy unauthenticated event accepted as Host v3 evidence")
	}
	encoded, err := json.Marshal(row)
	if err != nil || len(encoded) > 4096 {
		t.Fatal("sealed projection exceeds durable row limit", err, len(encoded))
	}
	for name, mutate := range map[string]func(*candidateverify.StoredObservationV2, *db.SealCheckRecord){
		"missing proof": func(r *candidateverify.StoredObservationV2, _ *db.SealCheckRecord) { r.SelectedProof = nil },
		"other call": func(r *candidateverify.StoredObservationV2, _ *db.SealCheckRecord) {
			r.SelectedProof.Event.CallRef = "other"
		},
		"other attempt": func(r *candidateverify.StoredObservationV2, _ *db.SealCheckRecord) {
			r.SelectedProof.Event.AttemptID = "other"
		},
		"other scope": func(r *candidateverify.StoredObservationV2, _ *db.SealCheckRecord) {
			r.SelectedProof.Checkpoint.Scope.PolicyRef = "other"
		},
		"changed observation": func(r *candidateverify.StoredObservationV2, _ *db.SealCheckRecord) {
			r.Source.Observation.StatusCode = 500
		},
		"wrong checkpoint": func(r *candidateverify.StoredObservationV2, _ *db.SealCheckRecord) {
			r.SelectedProof.Checkpoint.MAC = strings.Repeat("b", 64)
		},
		"excluded":      func(_ *candidateverify.StoredObservationV2, s *db.SealCheckRecord) { s.Checks[0].Included = false },
		"wrong journal": func(_ *candidateverify.StoredObservationV2, s *db.SealCheckRecord) { s.Head.JournalID = "other" },
		"journal before receipt": func(r *candidateverify.StoredObservationV2, _ *db.SealCheckRecord) {
			r.SelectedProof.Event.At = at.Add(-time.Millisecond)
		},
		"journal too late": func(r *candidateverify.StoredObservationV2, _ *db.SealCheckRecord) {
			r.SelectedProof.Event.At = at.Add(16 * time.Second)
		},
	} {
		t.Run(name, func(t *testing.T) {
			copyRow, copySeal := row, seal
			proof := *row.SelectedProof
			copyRow.SelectedProof = &proof
			copySeal.Checks = append([]db.SealCheck(nil), seal.Checks...)
			mutate(&copyRow, &copySeal)
			if selectedObservationSealed(copyRow, copySeal) {
				t.Fatal("altered attestation accepted")
			}
		})
	}
}

func TestSelectedStoredComparisonsRequirePostSaveIndependentSealedCalls(t *testing.T) {
	scope := transmit.Scope{TaskRef: "42", RunRef: "stored-run", CaseRef: "case-stored", ResetRef: "reset-stored", PolicyRef: "policy-stored"}
	at := time.Now().UTC()
	rawScope, _ := json.Marshal(scope)
	makeRow := func(sequence uint64, callRef, attemptRef string) candidateverify.StoredObservationV2 {
		obs := e2proxy.HTTPObservation{Sequence: sequence, AttemptRef: attemptRef, Method: "GET", Endpoint: "/documents", RequestAuthContext: "none",
			ExactFunction: true, RuleRef: "same-rule", Decision: "allowed", Reason: e2proxy.Complete,
			StatusCode: 200, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true, ObservedAt: at}
		rawObs, _ := json.Marshal(obs)
		event := e2proxy.Event{Index: sequence * 2, Stage: "result", Receipt: e2proxy.Receipt{
			Version: e2proxy.JournalVersionV3, Scope: scope, IntentID: 7, PolicyRevision: "revision-stored",
			CallRef: callRef, AttemptID: attemptRef, Sequence: sequence, Method: "GET", RuleRef: "same-rule",
			Decision: "allowed", Reason: e2proxy.Complete, ForwardAttempted: true, RequestWritten: true,
			ResponseReceived: true, ResponseComplete: true, AuditComplete: true, StatusCode: 200, At: at.Add(25 * time.Millisecond),
		}}
		cp := e2proxy.JournalCheckpoint{Version: e2proxy.JournalVersionV3, JournalID: "journal-stored", Sequence: sequence * 2,
			MAC: strings.Repeat("a", 64), Scope: scope}
		return candidateverify.StoredObservationV2{Revision: 2, HeaderMode: e2proxy.HeaderPreserve,
			Source: candidateverify.SourceV2{CallRef: callRef, IntentID: 7, TaskRef: "42", RunRef: scope.RunRef,
				ScopeDigest: db.DiagnosticEvidenceDigest(string(rawScope)), PolicyRevision: "revision-stored", Observation: obs,
				Digest: db.DiagnosticEvidenceDigest(string(rawObs))}, SelectedProof: &e2runner.SelectedAuditProof{Event: event, Checkpoint: cp}}
	}
	first, second := makeRow(1, "call-first", "attempt-first"), makeRow(2, "call-second", "attempt-second")
	seal := db.SealCheckRecord{TaskID: 42, RunRef: scope.RunRef,
		Head: db.ProductCheckpoint{JournalID: "journal-stored", Sequence: 4, MAC: strings.Repeat("a", 64)},
		Checks: []db.SealCheck{{Sequence: 2, MAC: strings.Repeat("a", 64), Included: true},
			{Sequence: 4, MAC: strings.Repeat("a", 64), Included: true}}}
	c := CandidateSelectedContext{TaskID: 42, RunRef: scope.RunRef,
		Candidate:           db.DiagnosticCandidate{CallRef: first.Source.CallRef, SaveFence: &db.DiagnosticCandidateSaveFence{AttemptHighWater: 1}},
		OriginalObservation: &first, Observations: []candidateverify.StoredObservationV2{first, second}}
	if got, err := selectedStoredComparisons(c, seal); err != nil || len(got) != 1 || got[0].Source.CallRef != second.Source.CallRef {
		t.Fatal("independent sealed post-save observation rejected", err)
	}
	for name, mutate := range map[string]func(*CandidateSelectedContext, *db.SealCheckRecord){
		"before save": func(c *CandidateSelectedContext, _ *db.SealCheckRecord) { c.Candidate.SaveFence.AttemptHighWater = 2 },
		"same call": func(c *CandidateSelectedContext, _ *db.SealCheckRecord) {
			c.Observations[1].Source.CallRef = c.OriginalObservation.Source.CallRef
		},
		"different function": func(c *CandidateSelectedContext, _ *db.SealCheckRecord) {
			c.Observations[1].Source.Observation.Endpoint = "/other"
		},
		"different authentication": func(c *CandidateSelectedContext, _ *db.SealCheckRecord) {
			c.Observations[1].Source.Observation.RequestAuthContext = "authorization"
		},
		"missing proof": func(c *CandidateSelectedContext, _ *db.SealCheckRecord) { c.Observations[1].SelectedProof = nil },
		"missing seal":  func(_ *CandidateSelectedContext, s *db.SealCheckRecord) { s.Checks[1].Included = false },
	} {
		t.Run(name, func(t *testing.T) {
			copyContext, copySeal := c, seal
			copyContext.Candidate.SaveFence = &db.DiagnosticCandidateSaveFence{AttemptHighWater: 1}
			copyContext.Observations = append([]candidateverify.StoredObservationV2(nil), c.Observations...)
			copySeal.Checks = append([]db.SealCheck(nil), seal.Checks...)
			mutate(&copyContext, &copySeal)
			if _, err := selectedStoredComparisons(copyContext, copySeal); err == nil {
				t.Fatal("untrusted stored comparison accepted")
			}
		})
	}
}
