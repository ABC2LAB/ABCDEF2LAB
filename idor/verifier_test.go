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
	canaries []Marker
	canOK    bool
}

func (f fakeKG) ResolvePolicy(string, AccountRef, AccountRef) (oracle.PolicyAssertion, bool) {
	return f.policy, f.policyOK
}

func (f fakeKG) Canaries(string, AccountRef) ([]Marker, bool) {
	return f.canaries, f.canOK
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
		Owner:       AccountRef{Label: "owner", Handle: "h-a", Status: oracle.SubjectVerified},
		Evaluated:   AccountRef{Label: "evaluated", Handle: "h-b", Status: oracle.SubjectVerified},
		ResourceRef: testResource,
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
		canaries: []Marker{{Field: "email", Value: testCanary, Trivial: false}},
		canOK:    true,
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
			"평가 주체 세션 만료(handle은 있음) → 신원 체크 실패",
			func(c *Candidate, tx *fakeTx, kg *fakeKG, now time.Time) { c.Evaluated.Status = oracle.SubjectExpired },
		},
		{
			"주체 status 미설정(E² 미기재) → 신원 체크 실패(fail-closed)",
			func(c *Candidate, tx *fakeTx, kg *fakeKG, now time.Time) { c.Evaluated.Status = "" },
		},
		{
			"canary 미등록(KG) → 데이터 매치 입증 불가",
			func(c *Candidate, tx *fakeTx, kg *fakeKG, now time.Time) {
				kg.canaries = nil
				kg.canOK = false
			},
		},
		{
			"canary 전부 trivial(식별자) → 비자명 매치 unknown",
			func(c *Candidate, tx *fakeTx, kg *fakeKG, now time.Time) {
				kg.canaries = []Marker{{Field: "id", Value: "42", Trivial: true}}
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
	// KG-registered canary whose value is the ref B supplies; owner and cross bodies
	// echo it. The marker is nontrivial, so the reflection check actually runs.
	kg.canaries = []Marker{{Field: "ref", Value: testResource, Trivial: false}}
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

// recordingTx wraps a Transmitter and keeps every proposed request, so a test can
// assert what the verifier asked E² to send — not only what came back.
type recordingTx struct {
	inner Transmitter
	sent  []AccessRequest
}

func (r *recordingTx) Send(req AccessRequest) (Capture, bool) {
	r.sent = append(r.sent, req)
	return r.inner.Send(req)
}

func TestVerifyReadProposesOnlyReads(t *testing.T) {
	now := time.Now()
	cand, tx, kg := cleanScenario(now)

	rec := &recordingTx{inner: tx}
	VerifyRead(cand, rec, kg)
	if len(rec.sent) != 2 {
		t.Fatalf("VerifyRead proposed %d requests, want exactly owner GET + cross GET", len(rec.sent))
	}
	for _, r := range rec.sent {
		if r.Method != "GET" || r.StateChanging {
			t.Fatalf("VerifyRead proposed %s (StateChanging=%v), want read-only GET", r.Method, r.StateChanging)
		}
	}

	// Contrast: the same recorder does catch Verify's setup write.
	rec = &recordingTx{inner: tx}
	Verify(cand, rec, kg)
	if !rec.sent[0].StateChanging {
		t.Fatalf("Verify's first request should be the state-changing setup")
	}
}

func TestVerifyReadConfirmsWithFixtureCanary(t *testing.T) {
	// The canary was planted by an approved fixture, and the transmitter refuses writes
	// (no POST key), as transmit.HTTPTransmitter does. Reads alone confirm the leak.
	now := time.Now()
	cand, tx, kg := cleanScenario(now)
	delete(tx.byKey, "owner|POST|"+testResource)

	got := VerifyRead(cand, tx, kg)
	if got.Verdict.Value != oracle.Confirmed {
		t.Fatalf("fixture canary: verdict = %q, want confirmed (reasons %v)", got.Verdict.Value, got.Verdict.ReasonCodes)
	}
}

func TestVerifyReadSharesVerifyReadSequence(t *testing.T) {
	// Verify ignores its setup result, so with the same reads both entry points must
	// judge identically — they differ only in the setup proposal.
	now := time.Now()
	cand, tx, kg := cleanScenario(now)
	read, full := VerifyRead(cand, tx, kg), Verify(cand, tx, kg)
	if read.Facts != full.Facts || read.Verdict.Value != full.Verdict.Value {
		t.Fatalf("VerifyRead %+v / %q != Verify %+v / %q", read.Facts, read.Verdict.Value, full.Facts, full.Verdict.Value)
	}
}

func TestVerifyReadFailClosed(t *testing.T) {
	now := time.Now()

	t.Run("stub transmitter refuses everything", func(t *testing.T) {
		cand, _, kg := cleanScenario(now)
		if got := VerifyRead(cand, StubTransmitter{}, kg); got.Verdict.Value != oracle.Undecidable {
			t.Fatalf("stub: verdict = %q, want undecidable", got.Verdict.Value)
		}
	})

	t.Run("fixture canary missing from owner read", func(t *testing.T) {
		cand, tx, kg := cleanScenario(now)
		bare := Capture{View: ResponseView{ResourceRef: testResource, Normal: true, TemplateClassified: true, Body: `{"id":42}`}, ObservedAt: now}
		tx.byKey["owner|GET|"+testResource] = struct {
			cap Capture
			ok  bool
		}{bare, true}
		if got := VerifyRead(cand, tx, kg); got.Verdict.Value == oracle.Confirmed {
			t.Fatalf("missing fixture canary must not confirm, got confirmed")
		}
	})
}
