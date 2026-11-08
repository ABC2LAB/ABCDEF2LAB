package transmit

import (
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
)

// ReadObservation is evidence from one actual read in a verification. Capture
// is ephemeral comparator input; it must not be exported or persisted as a body.
type ReadObservation struct {
	Capture  idor.Capture `json:"-"`
	Receipt  Receipt
	Captured bool
}

// ReadVerification keeps a verdict with the exact frozen policy and the two
// actual observations that produced it. It grants no finding-storage authority;
// the host must still seal the journal and verify provenance before saving.
type ReadVerification struct {
	Result         idor.Result
	FrozenPolicy   oracle.PolicyAssertion
	PolicyResolved bool
	CanaryFields   []string
	Owner          ReadObservation
	Evaluated      ReadObservation
}

// VerifyRead is the supported composition with the existing IDOR verifier. It
// binds that verifier's independent KG canaries to the exact projection snapshot
// before any request. Directly composing Send with an unrelated marker catalog
// is unsafe because the existing comparator uses substring matching.
//
// A nonempty reason means preflight refused the case without transmitting; the
// returned verdict is UNDECIDABLE. An empty reason means the snapshots agreed,
// not that transmission succeeded: inspect Result.Verdict and the audit trail.
func (t *HTTPTransmitter) VerifyRead(candidate idor.Candidate, kg idor.PolicyResolver) (idor.Result, Reason) {
	verification, reason := t.VerifyReadDetailed(candidate, kg)
	return verification.Result, reason
}

// VerifyReadDetailed uses the same preflight and one owner/cross read sequence
// as VerifyRead. Receipt and observation times are collected during that sequence,
// never reconstructed by issuing another request or reading a later KG snapshot.
func (t *HTTPTransmitter) VerifyReadDetailed(candidate idor.Candidate, kg idor.PolicyResolver) (ReadVerification, Reason) {
	refused := func(reason Reason) (ReadVerification, Reason) {
		return ReadVerification{Result: idor.Result{Verdict: oracle.Evaluate(oracle.CaseInput{}, time.Time{})}}, reason
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
	observed := &verificationTransmitter{tx: t}
	result := idor.VerifyRead(candidate, observed, frozen)
	verification := ReadVerification{
		Result: result, PolicyResolved: policyOK,
		Owner: observed.reads[0], Evaluated: observed.reads[1],
	}
	if policyOK {
		verification.FrozenPolicy = policy
	}
	for _, marker := range markers {
		verification.CanaryFields = append(verification.CanaryFields, marker.Field)
	}
	return verification, ""
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

// The supported verifier proposes precisely owner then evaluated GET. Keep all
// observations private to this invocation, including refused/partial reads.
type verificationTransmitter struct {
	tx    *HTTPTransmitter
	reads [2]ReadObservation
	next  int
}

func (v *verificationTransmitter) Send(request idor.AccessRequest) (idor.Capture, bool) {
	if v.next >= len(v.reads) {
		return idor.Capture{}, false
	}
	capture, receipt, ok := v.tx.SendDetailed(request)
	v.reads[v.next] = ReadObservation{Capture: capture, Receipt: receipt, Captured: ok}
	v.next++
	return capture, ok
}
