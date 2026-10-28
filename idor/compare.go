// Package idor holds the horizontal-IDOR verifier. This file is the compare
// rule (e3-oracle 스펙 §8-④: "이동찬 E² capture + 최민준 compare 규칙"): it turns a
// captured owner/cross access pair into oracle.ComparisonFacts — the (c) data
// signals the pure oracle consumes. It does NOT send requests or judge; it only
// compares what E² already captured.
//
// Why canary markers, not raw data: per e3-oracle 스펙 §3(c) and CLAUDE.md 절대규칙 8
// (완전 로컬·외부 유출 금지) + DLP, we prove cross access by matching A's registered
// non-sensitive canary in B's cross response — never by reading/storing real
// sensitive data. In the lab the canary is non-sensitive by construction. For any
// real target, E² capture must scope/sanitize what it hands in (no raw sensitive
// bodies, no HMAC keys) — that scoping is the open boundary below.
//
// TODO(이동찬): CrossAccessCapture는 E² capture 출력에서 매핑된다 — 이 타입은 잠정 경계다
// (스펙 §8-④ 리뷰에서 정렬). capture가 Body에 민감 원문을 넣지 않도록 스코프하는 책임은 E² 쪽.
package idor

import (
	"strings"

	"github.com/Autumn-27/artex/oracle"
)

// Marker is one registered canary: a field and its non-sensitive value planted in
// A's resource. Trivial marks a pure identifier (id/owner_id) — excluded from the
// nontrivial-match check, since an id reflection or owner_id alone is not evidence
// of data access (스펙 §3(c)).
type Marker struct {
	Field   string
	Value   string
	Trivial bool
}

// ResponseView is one captured response, distilled for comparison. Body is text to
// scan for canary values (non-sensitive in the lab; E² scopes it otherwise).
// Normal = a 2xx data payload (not empty/redirect). TemplateClassified says whether
// capture ran template/error classification at all — if not, template exclusion is
// Unknown rather than silently assumed clean (fail-closed, 절대규칙 5).
type ResponseView struct {
	ResourceRef        string
	Normal             bool
	TemplateClassified bool
	ErrorTemplate      bool
	CommonTemplate     bool
	Body               string
}

// CrossAccessCapture is one owner/cross pair for one resource under one reset
// generation. Owner = A accessing A's own resource (setup-success baseline); Cross =
// B accessing A's resource (the access under test). RequesterInputs are B's
// request-supplied values, used to exclude reflection; RequesterInputsKnown=false
// means capture did not record them, so reflection cannot be excluded.
type CrossAccessCapture struct {
	Canaries             []Marker
	Owner                ResponseView
	Cross                ResponseView
	RequesterInputs      []string
	RequesterInputsKnown bool
}

// Compare produces the six (c) data signals. Each is fail-closed: anything we
// cannot confirm is StateUnknown, never a silent pass. The oracle's checkData
// requires all six YES, so each is reported independently here.
func Compare(c CrossAccessCapture) oracle.ComparisonFacts {
	return oracle.ComparisonFacts{
		SameResource:        sameResource(c.Owner, c.Cross),
		OwnerNormalData:     ownerNormalData(c.Owner, c.Canaries),
		CrossNormalData:     crossNormalData(c.Cross),
		NontrivialDataMatch: nontrivialDataMatch(c.Cross, c.Canaries),
		ReflectionExcluded:  reflectionExcluded(c.Canaries, c.RequesterInputs, c.RequesterInputsKnown),
		TemplateExcluded:    templateExcluded(c.Cross),
	}
}

// sameResource — owner and cross must address the same resource, or the comparison
// is not a cross-access of one object. Missing a ref is Unknown.
func sameResource(owner, cross ResponseView) oracle.CheckState {
	if owner.ResourceRef == "" || cross.ResourceRef == "" {
		return oracle.StateUnknown
	}
	if owner.ResourceRef == cross.ResourceRef {
		return oracle.StateYes
	}
	return oracle.StateNo
}

// ownerNormalData — setup-success: the nontrivial canary is actually visible in A's
// own normal response. If owner's response is not normal, or no nontrivial canary
// is registered, we cannot confirm setup → Unknown. Absent from a normal owner
// response → No (setup not confirmed; the comparison would be untrustworthy).
func ownerNormalData(owner ResponseView, canaries []Marker) oracle.CheckState {
	nontrivial := nontrivialCanaries(canaries)
	if len(nontrivial) == 0 {
		return oracle.StateUnknown
	}
	if !owner.Normal {
		return oracle.StateUnknown
	}
	if anyValuePresent(owner.Body, nontrivial) {
		return oracle.StateYes
	}
	return oracle.StateNo
}

// crossNormalData — did B's cross response return normal data at all. An error
// template is a definite No; a normal payload is Yes; otherwise Unknown.
func crossNormalData(cross ResponseView) oracle.CheckState {
	if cross.TemplateClassified && cross.ErrorTemplate {
		return oracle.StateNo
	}
	if cross.Normal {
		return oracle.StateYes
	}
	return oracle.StateUnknown
}

// nontrivialDataMatch — does a nontrivial canary value appear in the cross body.
// Raw presence only; reflection and template exclusion are separate signals that
// the oracle also requires. No nontrivial canary to test → Unknown.
func nontrivialDataMatch(cross ResponseView, canaries []Marker) oracle.CheckState {
	nontrivial := nontrivialCanaries(canaries)
	if len(nontrivial) == 0 {
		return oracle.StateUnknown
	}
	if anyValuePresent(cross.Body, nontrivial) {
		return oracle.StateYes
	}
	return oracle.StateNo
}

// reflectionExcluded — the canary must not be mere reflection of B's own request
// input. Inputs not recorded → Unknown (cannot exclude). A nontrivial canary value
// echoed in B's inputs → No (reflection possible). Otherwise Yes.
func reflectionExcluded(canaries []Marker, inputs []string, inputsKnown bool) oracle.CheckState {
	if !inputsKnown {
		return oracle.StateUnknown
	}
	for _, m := range nontrivialCanaries(canaries) {
		for _, in := range inputs {
			if strings.Contains(in, m.Value) {
				return oracle.StateNo
			}
		}
	}
	return oracle.StateYes
}

// templateExcluded — the cross response must not be a common/error template. Not
// classified → Unknown (fail-closed). Flagged as template → No. Otherwise Yes.
func templateExcluded(cross ResponseView) oracle.CheckState {
	if !cross.TemplateClassified {
		return oracle.StateUnknown
	}
	if cross.ErrorTemplate || cross.CommonTemplate {
		return oracle.StateNo
	}
	return oracle.StateYes
}

// nontrivialCanaries returns canaries that carry nontrivial data (not identifiers)
// and a non-empty value — the only ones that can evidence data access (스펙 §3(c)).
func nontrivialCanaries(canaries []Marker) []Marker {
	out := make([]Marker, 0, len(canaries))
	for _, m := range canaries {
		if !m.Trivial && m.Value != "" {
			out = append(out, m)
		}
	}
	return out
}

// anyValuePresent reports whether any marker value is a substring of body. Empty
// values are skipped (an empty canary never counts as a match).
func anyValuePresent(body string, markers []Marker) bool {
	for _, m := range markers {
		if m.Value != "" && strings.Contains(body, m.Value) {
			return true
		}
	}
	return false
}
