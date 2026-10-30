package transmit

import (
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
)

// VerifyRead is the supported composition with the existing IDOR verifier. It
// binds that verifier's independent KG canaries to the exact projection snapshot
// before any request. Directly composing Send with an unrelated marker catalog
// is unsafe because the existing comparator uses substring matching.
//
// A nonempty reason means preflight refused the case without transmitting; the
// returned verdict is UNDECIDABLE. An empty reason means the snapshots agreed,
// not that transmission succeeded: inspect Result.Verdict and the audit trail.
func (t *HTTPTransmitter) VerifyRead(candidate idor.Candidate, kg idor.PolicyResolver) (idor.Result, Reason) {
	refused := func(reason Reason) (idor.Result, Reason) {
		return idor.Result{Verdict: oracle.Evaluate(oracle.CaseInput{}, time.Time{})}, reason
	}
	if reason := t.gate(t.ctx); reason != "" {
		return refused(reason)
	}
	b, exists := t.resources[candidate.ResourceRef]
	if !exists {
		return refused(UnknownResource)
	}
	if candidate.Owner.Label == candidate.Evaluated.Label || candidate.Owner.Handle == candidate.Evaluated.Handle {
		return refused(InvalidCandidate)
	}
	for _, account := range []idor.AccountRef{candidate.Owner, candidate.Evaluated} {
		session, exists := t.sessions[account.Label]
		if !exists {
			return refused(UnknownAccount)
		}
		if reason := checkAccount(idor.AccessRequest{As: account}, session, time.Now()); reason != "" {
			return refused(reason)
		}
		if !b.accounts[account.Label] {
			return refused(AccountNotApproved)
		}
	}
	if kg == nil || b.resource.View == nil {
		return refused(CanaryMismatch)
	}
	policy, policyOK := kg.ResolvePolicy(candidate.ResourceRef, candidate.Owner, candidate.Evaluated)
	markers, markersOK := kg.Canaries(candidate.ResourceRef, candidate.Owner)
	markers = append([]idor.Marker(nil), markers...)
	if !markersOK || !sameMarkers(b.resource.View.Markers, markers) {
		return refused(CanaryMismatch)
	}
	// The verifier re-queries its PolicyResolver; pin both answers rather than
	// letting a mutable KG return a different snapshot after this comparison.
	frozen := frozenPolicy{candidate: candidate, policy: policy, policyOK: policyOK, markers: markers}
	if reason := t.gate(t.ctx); reason != "" {
		return refused(reason)
	}
	return idor.Verify(candidate, t, frozen), ""
}

func sameMarkers(expected, actual []idor.Marker) bool {
	if len(expected) == 0 || len(expected) != len(actual) {
		return false
	}
	set := make(map[idor.Marker]bool, len(expected))
	for _, marker := range expected {
		set[marker] = true
	}
	for _, marker := range actual {
		if !set[marker] {
			return false
		}
		delete(set, marker) // duplicate actual entries cannot match twice.
	}
	return len(set) == 0
}

type frozenPolicy struct {
	candidate idor.Candidate
	policy    oracle.PolicyAssertion
	policyOK  bool
	markers   []idor.Marker
}

func (f frozenPolicy) ResolvePolicy(resource string, owner, evaluated idor.AccountRef) (oracle.PolicyAssertion, bool) {
	if resource != f.candidate.ResourceRef || owner != f.candidate.Owner || evaluated != f.candidate.Evaluated {
		return oracle.PolicyAssertion{}, false
	}
	return f.policy, f.policyOK
}

func (f frozenPolicy) Canaries(resource string, owner idor.AccountRef) ([]idor.Marker, bool) {
	if resource != f.candidate.ResourceRef || owner != f.candidate.Owner {
		return nil, false
	}
	return append([]idor.Marker(nil), f.markers...), true
}
