package oracle

import (
	"reflect"
	"testing"
	"time"
)

// FuzzOracleTimeNormalization checks that the observed instant, rather than
// its presentation location, is the policy-window input. It also checks that
// repeated evaluation is deterministic and that returned reason storage is
// isolated between calls.
func FuzzOracleTimeNormalization(f *testing.F) {
	f.Add(int64(0), int32(0), uint8(0))
	f.Add(int64(time.Hour), int32(23*3600), uint8(1))
	f.Add(int64(-time.Hour), int32(-23*3600), uint8(255))
	for _, delta := range []time.Duration{-time.Hour - 1, -time.Hour + 1, time.Hour - 1, time.Hour + 1} {
		f.Add(int64(delta), int32(5*3600+1800), uint8(7))
	}
	f.Fuzz(func(t *testing.T, deltaNanos int64, offsetSeconds int32, mutation uint8) {
		delta := time.Duration(deltaNanos % int64(2*time.Hour))
		offset := int(offsetSeconds % (24 * 3600))
		loc := time.FixedZone("fuzz-offset", offset)
		instant := observedAt.Add(delta)
		localInstant := instant.In(loc)
		input := baseCase()
		before := input

		utc := Evaluate(input, instant)
		shifted := Evaluate(input, localInstant)
		if !reflect.DeepEqual(utc, shifted) {
			t.Fatalf("same instant changed verdict: UTC=%+v local=%+v", utc, shifted)
		}
		inside := !instant.Before(input.Policy.ValidFrom) && instant.Before(input.Policy.ValidUntil)
		wantValue := Undecidable
		wantReasons := []JudgeReason{ReasonExpectedAccessPolicyInvalid, ReasonSubjectsDistinctVerified, ReasonDataNontrivialMatch}
		if inside {
			wantValue = Confirmed
			wantReasons[0] = ReasonPolicyDenyResolved
		}
		if shifted.Value != wantValue || !reflect.DeepEqual(shifted.ReasonCodes, wantReasons) {
			t.Fatalf("policy-window result = %+v, want value=%s reasons=%v", shifted, wantValue, wantReasons)
		}
		if !reflect.DeepEqual(input, before) {
			t.Fatal("Evaluate mutated CaseInput")
		}

		again := Evaluate(input, localInstant)
		if !reflect.DeepEqual(shifted, again) {
			t.Fatalf("repeat evaluation changed verdict: first=%+v second=%+v", shifted, again)
		}
		expectedReasons := append([]JudgeReason(nil), again.ReasonCodes...)
		if len(shifted.ReasonCodes) > 0 {
			shifted.ReasonCodes[0] = JudgeReason(string(shifted.ReasonCodes[0]) + string(rune(mutation)))
		}
		if !reflect.DeepEqual(utc.ReasonCodes, expectedReasons) || !reflect.DeepEqual(again.ReasonCodes, expectedReasons) {
			t.Fatal("returned reasons alias another verdict")
		}
		if len(again.ReasonCodes) > 0 {
			again.ReasonCodes[0] = JudgeReason(string(again.ReasonCodes[0]) + "-mutated")
		}
		fresh := Evaluate(input, localInstant)
		want := again
		want.ReasonCodes = expectedReasons
		if !reflect.DeepEqual(fresh, want) {
			t.Fatal("mutating returned reasons affected a later evaluation")
		}
	})
}

// FuzzOracleDenyEvidenceConservative keeps a confirmed deny case as a strong
// control and weakens one independent proof axis from fuzz input. A confirmed
// result is valid only while the policy, both verified distinct identities,
// and every required comparison fact remain explicit.
func FuzzOracleDenyEvidenceConservative(f *testing.F) {
	f.Add(uint64(0))
	f.Add(uint64(0x0102030405060708))
	f.Add(^uint64(0))
	// Exercise each missing proof independently; combining them can let an
	// unrelated invalid policy mask a regression in identity or comparison.
	for i := range 6 {
		for state := uint64(1); state <= 3; state++ {
			f.Add(state << uint(3+2*i))
		}
	}
	for bit := range 25 {
		f.Add(uint64(1) << uint(bit))
	}
	f.Fuzz(func(t *testing.T, control uint64) {
		strong := Evaluate(baseCase(), observedAt)
		if strong.Value != Confirmed || strong.PolicyCheck != StateYes || strong.IdentityCheck != StateYes || strong.DataCheck != StateYes {
			t.Fatalf("confirmed positive control regressed: %+v", strong)
		}

		candidate := baseCase()
		// Each field can remove one required proof while retaining a valid deny
		// policy. No generated value can add a new authorization or network fact.
		if control&(1<<0) != 0 {
			candidate.Subjects.OwnerStatus = SubjectUnknown
		}
		if control&(1<<1) != 0 {
			candidate.Subjects.EvaluatedStatus = SubjectExpired
		}
		if control&(1<<2) != 0 {
			candidate.Subjects.EvaluatedRef = candidate.Subjects.OwnerRef
		}
		facts := []*CheckState{
			&candidate.Facts.SameResource,
			&candidate.Facts.OwnerNormalData,
			&candidate.Facts.CrossNormalData,
			&candidate.Facts.NontrivialDataMatch,
			&candidate.Facts.ReflectionExcluded,
			&candidate.Facts.TemplateExcluded,
		}
		for i, fact := range facts {
			switch (control >> uint(3+2*i)) & 3 {
			case 1:
				*fact = StateUnknown
			case 2:
				*fact = StateNo
			case 3:
				*fact = CheckState("unrecognized-fact")
			}
		}
		switch (control >> 15) & 3 {
		case 1:
			candidate.Subjects.OwnerStatus = SubjectUnknown
		case 2:
			candidate.Subjects.OwnerStatus = SubjectExpired
		case 3:
			candidate.Subjects.OwnerStatus = SubjectStatus("unrecognized-status")
		}
		switch (control >> 17) & 3 {
		case 1:
			candidate.Subjects.EvaluatedStatus = SubjectUnknown
		case 2:
			candidate.Subjects.EvaluatedStatus = SubjectMismatch
		case 3:
			candidate.Subjects.EvaluatedStatus = SubjectStatus("unrecognized-status")
		}
		if control&(1<<19) != 0 {
			candidate.Subjects.OwnerRef = ""
		}
		if control&(1<<20) != 0 {
			candidate.Subjects.EvaluatedRef = ""
		}
		if control&(1<<21) != 0 {
			candidate.Subjects.EvaluatedRef = candidate.Subjects.OwnerRef
		}
		if control&(1<<22) != 0 {
			candidate.Policy.ExpectedAccess = ExpectedAccess("unrecognized-policy")
		}
		if control&(1<<23) != 0 {
			candidate.Policy.Sharing = ResolutionState("unrecognized-sharing")
		}
		if control&(1<<24) != 0 {
			candidate.Policy.Delegation = ResolutionState("unrecognized-delegation")
		}

		verdict := Evaluate(candidate, observedAt)
		allProofsPresent := candidate.Policy.ExpectedAccess == AccessDeny &&
			candidate.Policy.Sharing == ResolvedNone && candidate.Policy.Delegation == ResolvedNone &&
			candidate.Subjects.OwnerStatus == SubjectVerified &&
			candidate.Subjects.EvaluatedStatus == SubjectVerified &&
			candidate.Subjects.OwnerRef != "" && candidate.Subjects.EvaluatedRef != "" &&
			candidate.Subjects.OwnerRef != candidate.Subjects.EvaluatedRef &&
			candidate.Facts == strongFacts()
		want := Undecidable
		if allProofsPresent {
			want = Confirmed
		}
		if verdict.Value != want {
			t.Fatalf("deny evidence: control=%x verdict=%+v want=%s", control, verdict, want)
		}
	})
}

// FuzzOracleAllowedSemantics preserves the special allow path: a verified B is
// enough for Allowed even when A and comparison facts are unavailable. The
// corresponding B verification failure must never become Allowed.
func FuzzOracleAllowedSemantics(f *testing.F) {
	for mode := uint8(0); mode < 3; mode++ {
		for status := uint8(0); status < 4; status++ {
			f.Add(mode, status)
		}
	}
	f.Fuzz(func(t *testing.T, mode, status uint8) {
		candidate := baseCase()
		var allowReason JudgeReason
		switch mode % 3 {
		case 0:
			candidate.Policy.ExpectedAccess = AccessAllow
			candidate.Policy.Sharing = ResolutionUnknown
			candidate.Policy.Delegation = ResolutionUnknown
			allowReason = ReasonPolicyAllows
		case 1:
			candidate.Policy.ExpectedAccess = AccessDeny
			candidate.Policy.Sharing = ResolvedAllow
			candidate.Policy.Delegation = ResolutionUnknown
			allowReason = ReasonSharingOrDelegationAllows
		case 2:
			candidate.Policy.ExpectedAccess = AccessDeny
			candidate.Policy.Sharing = ResolvedNone
			candidate.Policy.Delegation = ResolvedAllow
			allowReason = ReasonSharingOrDelegationAllows
		}
		candidate.Subjects.OwnerStatus = SubjectUnknown
		candidate.Subjects.OwnerRef = ""
		candidate.Facts = noComparison()
		if status&1 != 0 {
			candidate.Subjects.EvaluatedStatus = SubjectExpired
		}
		if status&2 != 0 {
			candidate.Subjects.EvaluatedRef = ""
		}
		verdict := Evaluate(candidate, observedAt)
		bVerified := candidate.Subjects.EvaluatedStatus == SubjectVerified && candidate.Subjects.EvaluatedRef != ""
		if bVerified && verdict.Value != Allowed {
			t.Fatalf("verified B allow path was blocked (mode=%d): %+v", mode, verdict)
		}
		if !bVerified && verdict.Value != Undecidable {
			t.Fatalf("unverified B did not remain undecidable (mode=%d): %+v", mode, verdict)
		}
		if bVerified && (len(verdict.ReasonCodes) != 1 || verdict.ReasonCodes[0] != allowReason) {
			t.Fatalf("allow verdict did not carry only its policy reason: %+v", verdict)
		}
	})
}
