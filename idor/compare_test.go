package idor

import (
	"testing"
	"time"

	"github.com/Autumn-27/artex/oracle"
)

// baseCapture is a clean horizontal-IDOR capture: a nontrivial email canary planted
// in A's basket, visible to A (setup-success) and leaking into B's cross response,
// not reflected from B's input, not a template. Every (c) signal should be YES.
func baseCapture() CrossAccessCapture {
	const canary = "canary-7f3a9c@lab.local"
	return CrossAccessCapture{
		Canaries: []Marker{
			{Field: "email", Value: canary, Trivial: false},
			{Field: "id", Value: "42", Trivial: true}, // identifier — excluded from match
		},
		Owner: ResponseView{
			ResourceRef: "basket/42", Normal: true, TemplateClassified: true,
			Body: `{"id":42,"email":"` + canary + `"}`,
		},
		Cross: ResponseView{
			ResourceRef: "basket/42", Normal: true, TemplateClassified: true,
			Body: `{"id":42,"email":"` + canary + `"}`,
		},
		RequesterInputs:         []string{"basket", "42"}, // id only; canary not among B's inputs
		RequesterInputsComplete: true,                     // all echo sources captured
	}
}

func TestCompareBaseAllYes(t *testing.T) {
	got := Compare(baseCapture())
	want := oracle.ComparisonFacts{
		SameResource:        oracle.StateYes,
		OwnerNormalData:     oracle.StateYes,
		CrossNormalData:     oracle.StateYes,
		NontrivialDataMatch: oracle.StateYes,
		ReflectionExcluded:  oracle.StateYes,
		TemplateExcluded:    oracle.StateYes,
	}
	if got != want {
		t.Fatalf("base capture facts = %+v, want %+v", got, want)
	}
}

func TestCompareSignals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *CrossAccessCapture)
		field  func(f oracle.ComparisonFacts) oracle.CheckState
		want   oracle.CheckState
	}{
		{
			"다른 자원 → SameResource No",
			func(c *CrossAccessCapture) { c.Cross.ResourceRef = "basket/99" },
			func(f oracle.ComparisonFacts) oracle.CheckState { return f.SameResource },
			oracle.StateNo,
		},
		{
			"자원 ref 없음 → SameResource Unknown",
			func(c *CrossAccessCapture) { c.Cross.ResourceRef = "" },
			func(f oracle.ComparisonFacts) oracle.CheckState { return f.SameResource },
			oracle.StateUnknown,
		},
		{
			"owner 비정상 응답 → OwnerNormalData Unknown (setup 미확인)",
			func(c *CrossAccessCapture) { c.Owner.Normal = false },
			func(f oracle.ComparisonFacts) oracle.CheckState { return f.OwnerNormalData },
			oracle.StateUnknown,
		},
		{
			"owner 정상인데 canary 없음 → OwnerNormalData No (setup 실패)",
			func(c *CrossAccessCapture) { c.Owner.Body = `{"id":42}` },
			func(f oracle.ComparisonFacts) oracle.CheckState { return f.OwnerNormalData },
			oracle.StateNo,
		},
		{
			"비자명 canary 없음(trivial만) → NontrivialDataMatch Unknown",
			func(c *CrossAccessCapture) { c.Canaries = []Marker{{Field: "id", Value: "42", Trivial: true}} },
			func(f oracle.ComparisonFacts) oracle.CheckState { return f.NontrivialDataMatch },
			oracle.StateUnknown,
		},
		{
			"교차응답에 canary 없음 → NontrivialDataMatch No",
			func(c *CrossAccessCapture) { c.Cross.Body = `{"id":42}` },
			func(f oracle.ComparisonFacts) oracle.CheckState { return f.NontrivialDataMatch },
			oracle.StateNo,
		},
		{
			"오류 템플릿 → CrossNormalData No",
			func(c *CrossAccessCapture) { c.Cross.ErrorTemplate = true },
			func(f oracle.ComparisonFacts) oracle.CheckState { return f.CrossNormalData },
			oracle.StateNo,
		},
		{
			"비정상·미분류 → CrossNormalData Unknown",
			func(c *CrossAccessCapture) { c.Cross.Normal = false; c.Cross.TemplateClassified = false },
			func(f oracle.ComparisonFacts) oracle.CheckState { return f.CrossNormalData },
			oracle.StateUnknown,
		},
		{
			"canary가 B 요청입력에 있음 → ReflectionExcluded No",
			func(c *CrossAccessCapture) { c.RequesterInputs = []string{"canary-7f3a9c@lab.local"} },
			func(f oracle.ComparisonFacts) oracle.CheckState { return f.ReflectionExcluded },
			oracle.StateNo,
		},
		{
			"echo 소스 불완전 포착 → ReflectionExcluded Unknown",
			func(c *CrossAccessCapture) { c.RequesterInputsComplete = false },
			func(f oracle.ComparisonFacts) oracle.CheckState { return f.ReflectionExcluded },
			oracle.StateUnknown,
		},
		{
			"공통 템플릿 → TemplateExcluded No",
			func(c *CrossAccessCapture) { c.Cross.CommonTemplate = true },
			func(f oracle.ComparisonFacts) oracle.CheckState { return f.TemplateExcluded },
			oracle.StateNo,
		},
		{
			"템플릿 미분류 → TemplateExcluded Unknown",
			func(c *CrossAccessCapture) { c.Cross.TemplateClassified = false },
			func(f oracle.ComparisonFacts) oracle.CheckState { return f.TemplateExcluded },
			oracle.StateUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := baseCapture()
			tc.mutate(&c)
			if got := tc.field(Compare(c)); got != tc.want {
				t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// denyCase builds a CaseInput with the given facts under a resolved-DENY policy and
// two distinct verified subjects — the setup where all-YES facts must give CONFIRMED.
func denyCase(facts oracle.ComparisonFacts, observed time.Time) oracle.CaseInput {
	return oracle.CaseInput{
		Policy: oracle.PolicyAssertion{
			ExpectedAccess:       oracle.AccessDeny,
			Sharing:              oracle.ResolvedNone,
			Delegation:           oracle.ResolvedNone,
			HasRegisteredSource:  true,
			ValidFrom:            observed.Add(-time.Hour),
			ValidUntil:           observed.Add(time.Hour),
			HintMatchesCanonical: true,
		},
		Subjects: oracle.SubjectContext{
			OwnerStatus: oracle.SubjectVerified, EvaluatedStatus: oracle.SubjectVerified,
			OwnerRef: "userA", EvaluatedRef: "userB",
		},
		Facts: facts,
	}
}

// TestCompareFeedsOracle is the walking-skeleton integration: compare → oracle.
// Clean leak confirms; a reflected canary must not confirm.
func TestCompareFeedsOracle(t *testing.T) {
	observed := time.Now()

	confirmed := oracle.Evaluate(denyCase(Compare(baseCapture()), observed), observed)
	if confirmed.Value != oracle.Confirmed {
		t.Fatalf("clean cross-leak: verdict = %q, want confirmed (reasons %v)", confirmed.Value, confirmed.ReasonCodes)
	}

	reflected := baseCapture()
	reflected.RequesterInputs = []string{"canary-7f3a9c@lab.local"}
	v := oracle.Evaluate(denyCase(Compare(reflected), observed), observed)
	if v.Value == oracle.Confirmed {
		t.Fatalf("reflected canary must not confirm, got %q", v.Value)
	}
}
