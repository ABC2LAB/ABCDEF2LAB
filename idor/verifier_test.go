package idor

import (
	"testing"
	"time"

	"github.com/Autumn-27/artex/oracle"
)

// fakeTx is a scripted E² seam: it returns a programmed Capture per request key, so a
// test drives exactly what owner/cross/setup transmissions yield. A key with ok=false
// models a refused or failed transmission.
type fakeTx struct {
	byKey map[string]struct {
		cap Capture
		ok  bool
	}
}

func txKey(r AccessRequest) string { return r.As.Label + "|" + r.Method + "|" + r.ResourceRef }

func (f *fakeTx) Send(r AccessRequest) (Capture, bool) {
	s, present := f.byKey[txKey(r)]
	if !present {
		return Capture{}, false
	}
	return s.cap, s.ok
}

// fakeKG is a scripted KG seam.
type fakeKG struct {
	policy   oracle.PolicyAssertion
	policyOK bool
	trivial  map[string]bool
	known    map[string]bool
}

func (f fakeKG) ResolvePolicy(string, AccountRef, AccountRef) (oracle.PolicyAssertion, bool) {
	return f.policy, f.policyOK
}

func (f fakeKG) IsTrivial(_, field string) (bool, bool) {
	return f.trivial[field], f.known[field]
}

const (
	testResource = "basket/42"
	testCanary   = "canary-9b1c4d@lab.local"
)

// cleanScenario wires a confirmable horizontal IDOR: owner read and cross read both
// return a normal, classified body carrying A's nontrivial canary, under a resolved
// DENY policy with two verified distinct subjects. Verify should return CONFIRMED.
// Each test mutates one piece to drive a single signal.
func cleanScenario(now time.Time) (Candidate, *fakeTx, fakeKG) {
	body := `{"id":42,"email":"` + testCanary + `"}`
	view := ResponseView{ResourceRef: testResource, Normal: true, TemplateClassified: true, Body: body}
	capt := Capture{View: view, ObservedAt: now}

	cand := Candidate{
		Owner:       AccountRef{Label: "owner", Handle: "h-a"},
		Evaluated:   AccountRef{Label: "evaluated", Handle: "h-b"},
		ResourceRef: testResource,
		Canary:      CanarySpec{Field: "email", Value: testCanary},
	}
	tx := &fakeTx{byKey: map[string]struct {
		cap Capture
		ok  bool
	}{
		"owner|POST|" + testResource:    {Capture{ObservedAt: now}, true}, // setup executed
		"owner|GET|" + testResource:     {capt, true},                     // owner baseline
		"evaluated|GET|" + testResource: {capt, true},                     // cross access
	}}
	kg := fakeKG{
		policy: oracle.PolicyAssertion{
			ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone,
			HasRegisteredSource: true, ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour),
			HintMatchesCanonical: true,
		},
		policyOK: true,
		trivial:  map[string]bool{"email": false},
		known:    map[string]bool{"email": true},
	}
	return cand, tx, kg
}

func TestVerifyCleanLeakConfirmed(t *testing.T) {
	now := time.Now()
	cand, tx, kg := cleanScenario(now)
	got := Verify(cand, tx, kg)
	if got.Verdict.Value != oracle.Confirmed {
		t.Fatalf("clean leak: verdict = %q, want confirmed (reasons %v)", got.Verdict.Value, got.Verdict.ReasonCodes)
	}
}

func TestVerifyFailClosed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *Candidate, tx *fakeTx, kg *fakeKG, now time.Time)
	}{
		{
			"setup 미반영(owner 응답에 canary 없음) → setup-success 실패",
			func(c *Candidate, tx *fakeTx, kg *fakeKG, now time.Time) {
				noCanary := Capture{View: ResponseView{ResourceRef: testResource, Normal: true, TemplateClassified: true, Body: `{"id":42}`}, ObservedAt: now}
				tx.byKey["owner|GET|"+testResource] = struct {
					cap Capture
					ok  bool
				}{noCanary, true}
			},
		},
		{
			"정책 미해결 → expected access unknown",
			func(c *Candidate, tx *fakeTx, kg *fakeKG, now time.Time) { kg.policyOK = false },
		},
		{
			"교차 전송 거부 → cross 미관찰",
			func(c *Candidate, tx *fakeTx, kg *fakeKG, now time.Time) {
				delete(tx.byKey, "evaluated|GET|"+testResource)
			},
		},
		{
			"교차 응답 오류 템플릿 → CrossNormalData No",
			func(c *Candidate, tx *fakeTx, kg *fakeKG, now time.Time) {
				errView := Capture{View: ResponseView{ResourceRef: testResource, TemplateClassified: true, ErrorTemplate: true}, ObservedAt: now}
				tx.byKey["evaluated|GET|"+testResource] = struct {
					cap Capture
					ok  bool
				}{errView, true}
			},
		},
		{
			"평가 주체 미검증(handle 없음) → 신원 체크 실패",
			func(c *Candidate, tx *fakeTx, kg *fakeKG, now time.Time) { c.Evaluated.Handle = "" },
		},
		{
			"canary trivial 여부 불명 → 보수적 배제로 데이터매치 unknown",
			func(c *Candidate, tx *fakeTx, kg *fakeKG, now time.Time) {
				kg.known = map[string]bool{"email": false}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			cand, tx, kg := cleanScenario(now)
			tc.mutate(&cand, tx, &kg, now)
			got := Verify(cand, tx, kg)
			if got.Verdict.Value == oracle.Confirmed {
				t.Fatalf("%s: must not confirm, got confirmed (facts %+v)", tc.name, got.Facts)
			}
		})
	}
}

// TestVerifyReflectionWired checks the verifier feeds B's request inputs into the
// reflection check: a canary whose value is the resource reference B supplied must be
// treated as reflection, not a leak, so the case does not confirm.
func TestVerifyReflectionWired(t *testing.T) {
	now := time.Now()
	cand, tx, kg := cleanScenario(now)
	// Canary value == the ref B supplies; owner and cross bodies echo it. The field
	// must be a known nontrivial one, or the reflection check is skipped (excluded).
	cand.Canary = CanarySpec{Field: "ref", Value: testResource}
	kg.trivial = map[string]bool{"ref": false}
	kg.known = map[string]bool{"ref": true}
	body := `{"ref":"` + testResource + `"}`
	echo := Capture{View: ResponseView{ResourceRef: testResource, Normal: true, TemplateClassified: true, Body: body}, ObservedAt: now}
	tx.byKey["owner|GET|"+testResource] = struct {
		cap Capture
		ok  bool
	}{echo, true}
	tx.byKey["evaluated|GET|"+testResource] = struct {
		cap Capture
		ok  bool
	}{echo, true}

	got := Verify(cand, tx, kg)
	if got.Facts.ReflectionExcluded != oracle.StateNo {
		t.Fatalf("canary echoing B's input: ReflectionExcluded = %q, want no", got.Facts.ReflectionExcluded)
	}
	if got.Verdict.Value == oracle.Confirmed {
		t.Fatalf("reflected canary must not confirm, got confirmed")
	}
}
