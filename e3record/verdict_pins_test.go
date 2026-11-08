package e3record

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Autumn-27/artex/oracle"
)

// 증거를 바꾼 뒤 판정과 digest를 다시 맞춰서 Prepare의 해당 분기까지 도달하게 한다.
func reissueVerdict(t *testing.T, in *Input, source *Source) {
	t.Helper()
	result := &in.Evidence.Result.Result
	result.Verdict = oracle.Evaluate(oracle.CaseInput{
		Policy: in.Evidence.Policy.Assertion,
		Subjects: oracle.SubjectContext{
			OwnerStatus: in.Expected.Owner.Status, EvaluatedStatus: in.Expected.Evaluated.Status,
			OwnerRef: in.Expected.Owner.Label, EvaluatedRef: in.Expected.Evaluated.Label,
		},
		Facts: result.Facts,
	}, in.Evidence.Result.ObservedAt)
	digest, err := EvidenceDigest(in.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	source.EvidenceDigest = digest
}

func TestPreparePayloadMapsEachVerdict(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     func(*Input, *Source)
		value      oracle.VerdictValue
		vulnerable any
		access     string
	}{
		{"confirmed", func(*Input, *Source) {}, oracle.Confirmed, "confirmed", "위반_후보"},
		{"suspected", func(in *Input, s *Source) {
			in.Evidence.Policy.Assertion.ExpectedAccess = oracle.AccessUnknown
			s.Policy.ExpectedAccess = oracle.AccessUnknown
		}, oracle.Suspected, "suspected", "위반_후보"},
		{"allowed", func(in *Input, s *Source) {
			in.Evidence.Policy.Assertion.ExpectedAccess = oracle.AccessAllow
			s.Policy.ExpectedAccess = oracle.AccessAllow
		}, oracle.Allowed, nil, "위반_미관찰"},
		{"undecidable", func(in *Input, _ *Source) {
			in.Evidence.Result.Result.Facts.NontrivialDataMatch = oracle.StateUnknown
		}, oracle.Undecidable, "undecidable", "판정_불가"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, source := fixture()
			tc.mutate(&in, &source)
			reissueVerdict(t, &in, &source)
			prepared, err := Prepare(t.Context(), in, sourceReader(source))
			if err != nil {
				t.Fatal(err)
			}
			snapshot, valid := prepared.Snapshot()
			if !valid || snapshot.Value != tc.value {
				t.Fatalf("value = %q valid = %v, want %q", snapshot.Value, valid, tc.value)
			}
			var payload map[string]any
			if err := json.Unmarshal(snapshot.Canonical, &payload); err != nil {
				t.Fatal(err)
			}
			vulnerable, present := payload["vuln_verdict"]
			if !present || vulnerable != tc.vulnerable {
				t.Fatalf("vuln_verdict = %v present = %v, want %v", vulnerable, present, tc.vulnerable)
			}
			if payload["access_policy_result"] != tc.access || payload["oracle_value"] != string(tc.value) {
				t.Fatalf("access = %v oracle = %v", payload["access_policy_result"], payload["oracle_value"])
			}
		})
	}
}

func TestPrepareRejectsEachPolicyFieldMismatch(t *testing.T) {
	in, source := fixture()
	for _, tc := range []struct {
		name   string
		mutate func(*oracle.PolicyAssertion)
	}{
		{"sharing", func(p *oracle.PolicyAssertion) { p.Sharing = oracle.ResolvedAllow }},
		{"delegation", func(p *oracle.PolicyAssertion) { p.Delegation = oracle.ResolvedAllow }},
		{"registered source", func(p *oracle.PolicyAssertion) { p.HasRegisteredSource = false }},
		{"canonical hint", func(p *oracle.PolicyAssertion) { p.HintMatchesCanonical = false }},
		{"valid from", func(p *oracle.PolicyAssertion) { p.ValidFrom = p.ValidFrom.Add(-time.Second) }},
		{"valid until", func(p *oracle.PolicyAssertion) { p.ValidUntil = p.ValidUntil.Add(time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := cloneSource(source)
			tc.mutate(&s.Policy)
			if _, err := Prepare(t.Context(), in, sourceReader(s)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v want ErrInvalid", err)
			}
		})
	}
}

func TestPrepareRejectsNonPositiveTrafficEvidenceIDs(t *testing.T) {
	in, source := fixture()
	valid := cloneSource(source)
	valid.TrafficEvidenceIDs = []int64{7}
	if _, err := Prepare(t.Context(), in, sourceReader(valid)); err != nil {
		t.Fatalf("control rejected: %v", err)
	}
	for _, ids := range [][]int64{{0}, {-1}, {7, 0}} {
		s := cloneSource(source)
		s.TrafficEvidenceIDs = ids
		if _, err := Prepare(t.Context(), in, sourceReader(s)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ids %v: got %v want ErrInvalid", ids, err)
		}
	}
}
