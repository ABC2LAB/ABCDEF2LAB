// This file is the horizontal-IDOR verifier orchestration (최민준 담당, me.md:
// "다계정 + setup-success + destructive-last, 얇은 조회 경로부터"). It sequences one
// horizontal-IDOR check end to end — setup, owner baseline, cross access, compare,
// oracle — and returns the oracle verdict. It is the walking-skeleton glue between
// the merged pieces (idor.Compare + oracle.Evaluate).
//
// Separation of duties (CLAUDE.md 절대규칙 3, "판정·허용을 같은 손이 쥐지 않게"): this
// package proposes requests, it does NOT decide execution. Every request goes through
// the Transmitter seam, which E² (이동찬) owns and permits; the verifier never sends on
// its own. Policy, and the canary markers to plant, come through the PolicyResolver
// seam, which KG (이경준) owns and registers (kg-policy §3.1: KG registers+classifies,
// the verifier plants and the comparator consumes — neither reclassifies). This file
// ships only fake seams (in the test); the real ones are the respective owners' to
// implement. The two interfaces below ARE the boundary contract the verifier requires
// of E² and KG — review target for both.
//
// Read-only (CLAUDE.md 얇은 IDOR = 초기 조회 한정, 절대규칙 6): the only state-changing
// request is setup (planting A's own registered canary), marked StateChanging so E²
// gates it behind the approval + reset environment. Cross access is GET. destructive-
// last is not executed here — that is a later, separately gated increment.
package idor

import (
	"time"

	"github.com/Autumn-27/artex/oracle"
)

// AccountRef names an actor the verifier sends requests as. Label is a non-sensitive
// name that also serves as the subject ref the oracle sees (절대규칙 2: names only, no
// token/password values). Handle is E²'s opaque reference to the authenticated
// session — not the credential itself; empty Handle means not authenticated, so the
// verifier treats the subject as unverified (fail-closed).
type AccountRef struct {
	Label  string
	Handle string
}

// AccessRequest is one request the verifier asks E² to transmit. The verifier only
// proposes it; whether it goes out, and whether a state-changing one is permitted, is
// E²'s decision (절대규칙 3). ResourceRef is a logical id (e.g. "basket/42"), not a raw
// URL — resolving it to a target-scoped URL is E²'s job. Inputs are the non-sensitive
// values the verifier itself placed in the request, used downstream to exclude
// reflection; credentials are never here (절대규칙 2).
type AccessRequest struct {
	As            AccountRef
	Method        string
	ResourceRef   string
	StateChanging bool
	Inputs        []string
}

// Capture is one transmitted response as E² observed and classified it. ObservedAt is
// when the response was seen — the oracle judges policy validity at this time, not at
// judgment time (e3-oracle §3(a)). View carries only scoped, non-sensitive body text;
// E² is responsible for not letting raw sensitive data cross back (절대규칙 2/8).
type Capture struct {
	View       ResponseView
	ObservedAt time.Time
}

// Transmitter is the E² seam (TODO(이동찬): real impl is E²'s). The verifier hands it an
// AccessRequest; E² decides permission, performs the network transmission, scopes the
// body, and classifies Normal/ErrorTemplate by body shape (not by status code — the
// lab returns HTTP 200 on errors). A permitted, completed request returns ok=true with
// a Capture; a refused or undecidable transmission returns ok=false, and the verifier
// treats that step as unverifiable (절대규칙 5), never as success.
type Transmitter interface {
	Send(req AccessRequest) (Capture, bool)
}

// PolicyResolver is the KG seam (TODO(이경준): real impl is KG's). It supplies the
// things the verifier cannot know itself: the registered expected-access policy the
// oracle judges against, and the canary markers KG registered for a resource (field,
// non-sensitive value, and Trivial classification — kg-policy §3.1). The verifier
// reads KG; it does not author policy nor classify canaries.
type PolicyResolver interface {
	// ResolvePolicy returns the expected-access policy for evaluated accessing the
	// resource owned by owner. A failed or absent lookup must return ok=false so the
	// verifier leaves policy unresolved (→ UNDECIDABLE), never a silent allow (절대규칙 5).
	ResolvePolicy(resourceRef string, owner, evaluated AccountRef) (oracle.PolicyAssertion, bool)
	// Canaries returns the markers KG registered for the resource. An empty slice or
	// ok=false means no canary is available to prove data access, so setup-success
	// cannot be shown and the case degrades to UNDECIDABLE (fail-closed). Trivial is
	// KG's call (unknown → true, i.e. excluded — kg-policy §3.1); the verifier does
	// not reclassify.
	Canaries(resourceRef string, owner AccountRef) ([]Marker, bool)
}

// Candidate is one horizontal-IDOR check: evaluated subject B reading owner A's
// resource. The canary to prove access by data is supplied by KG, not named here.
type Candidate struct {
	Owner       AccountRef
	Evaluated   AccountRef
	ResourceRef string
}

// Result is the verifier's output for one candidate: the oracle verdict plus the facts
// it was judged on. Issuing, audit and revision of a stored record are the adapter's
// job (e3-oracle §8-⑤), not this package's.
type Result struct {
	Verdict oracle.OracleVerdict
	Facts   oracle.ComparisonFacts
}

// Verify runs the horizontal-IDOR sequence for one candidate over the E² and KG seams.
// It is fail-closed throughout: any seam that refuses or cannot answer leaves the
// dependent signal unverifiable, so the verdict degrades to UNDECIDABLE rather than
// claiming access (절대규칙 5). setup-success is proven by the owner read showing the
// canary, not by trusting the setup response.
func Verify(c Candidate, tx Transmitter, kg PolicyResolver) Result {
	policy, ok := kg.ResolvePolicy(c.ResourceRef, c.Owner, c.Evaluated)
	if !ok {
		// Unresolved policy: zero value has no registered source → expected access
		// unknown → oracle returns UNDECIDABLE. Never assume allow or deny.
		policy = oracle.PolicyAssertion{}
	}

	// KG-registered canaries (empty/failed → setup-success unprovable → UNDECIDABLE).
	canaries, _ := kg.Canaries(c.ResourceRef, c.Owner)

	// setup: plant the registered canaries in A's own resource. State-changing, so E²
	// gates execution behind the approval + reset environment; the result is
	// intentionally not inspected — setup success is proven below by the owner read.
	_, _ = tx.Send(AccessRequest{
		As: c.Owner, Method: "POST", ResourceRef: c.ResourceRef,
		StateChanging: true, Inputs: canaryValues(canaries),
	})

	ownerCap, ownerOK := tx.Send(AccessRequest{As: c.Owner, Method: "GET", ResourceRef: c.ResourceRef})

	crossReq := AccessRequest{
		As: c.Evaluated, Method: "GET", ResourceRef: c.ResourceRef,
		// The verifier authored this GET, so the resource reference is the only
		// B-supplied echo source it carries (no body, no verifier-set headers). That
		// makes the enumerated Inputs complete for this request shape — see capture below.
		Inputs: []string{c.ResourceRef},
	}
	crossCap, crossOK := tx.Send(crossReq)

	capture := CrossAccessCapture{Canaries: canaries}
	if ownerOK {
		capture.Owner = ownerCap.View
	}
	if crossOK {
		capture.Cross = crossCap.View
		capture.RequesterInputs = crossReq.Inputs
		// Complete only because the verifier fully authored this read and enumerated
		// every B-supplied echo source. A future candidate with un-enumerated sources
		// (custom headers, a body) must leave this false (경준 리뷰 ②, fail-closed).
		capture.RequesterInputsComplete = true
	}

	facts := Compare(capture)
	verdict := oracle.Evaluate(oracle.CaseInput{
		Policy:   policy,
		Subjects: subjectsOf(c.Owner, c.Evaluated),
		Facts:    facts,
	}, observationTime(ownerCap, ownerOK, crossCap, crossOK))

	return Result{Verdict: verdict, Facts: facts}
}

// canaryValues is the non-sensitive values to plant during setup, skipping empties.
func canaryValues(markers []Marker) []string {
	out := make([]string, 0, len(markers))
	for _, m := range markers {
		if m.Value != "" {
			out = append(out, m.Value)
		}
	}
	return out
}

// subjectsOf maps the two accounts to the oracle's subject context. A missing Handle
// leaves the subject unverified; the Label is the non-sensitive ref the oracle sees.
func subjectsOf(owner, evaluated AccountRef) oracle.SubjectContext {
	return oracle.SubjectContext{
		OwnerStatus:     subjectStatus(owner),
		EvaluatedStatus: subjectStatus(evaluated),
		OwnerRef:        owner.Label,
		EvaluatedRef:    evaluated.Label,
	}
}

func subjectStatus(a AccountRef) oracle.SubjectStatus {
	if a.Handle == "" {
		return oracle.SubjectUnknown
	}
	return oracle.SubjectVerified
}

// observationTime is when the evidence under judgment was observed: the cross read
// (the access under test) if captured, else the owner read, else the zero time —
// which falls outside any validity window, holding the policy invalid (fail-closed).
func observationTime(owner Capture, ownerOK bool, cross Capture, crossOK bool) time.Time {
	if crossOK {
		return cross.ObservedAt
	}
	if ownerOK {
		return owner.ObservedAt
	}
	return time.Time{}
}
