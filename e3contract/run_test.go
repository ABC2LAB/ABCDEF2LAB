package e3contract_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3fake"
	"github.com/Autumn-27/artex/oracle"
)

var t0 = time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)

const frozenRef = "decl-frozen-1"

func declaration() e3contract.Declaration {
	ownerOnly := func(typ, path string) e3contract.TypeDeclaration {
		return e3contract.TypeDeclaration{Type: typ, Sharing: e3contract.SharingOwnerOnly, OwnerOnlyFields: []string{"body"},
			Create: e3contract.CreateSpec{Path: path, Method: "POST", Kind: e3contract.BodyJSON, BodyTemplate: `{"title":"t"}`,
				MarkerField: "body", IDPath: "id"},
			Read: e3contract.ReadSpec{PathTemplate: path + "/{id}", DataPath: "."}}
	}
	return e3contract.Declaration{Version: e3contract.DeclarationVersion, TargetRef: "target-1",
		TargetApprovalRevision: "approval-3", DeclaredBy: "operator-1", DeclaredAt: t0,
		Identity: e3contract.IdentitySpec{Path: "/api/me", SubjectIDPath: "id"},
		Types: []e3contract.TypeDeclaration{ownerOnly("note", "/api/notes"),
			{Type: "post", Sharing: e3contract.SharingShared}, ownerOnly("secret", "/api/secrets")}}
}

// seed는 표식 난수다. 같은 seed로 시험이 Run과 같은 표식을 다시 만들 수 있다.
func seed() []byte {
	var out []byte
	sum := sha256.Sum256([]byte("e3contract-test"))
	for len(out) < 1024 {
		out = append(out, sum[:]...)
		sum = sha256.Sum256(sum[:])
	}
	return out
}

func testMarker(t *testing.T) e3contract.Marker {
	t.Helper()
	m, err := e3contract.NewMarker(bytes.NewReader(seed()))
	if err != nil || !m.Valid() {
		t.Fatalf("시험 표식을 만들지 못함: %v", err)
	}
	return m
}

type fixture struct {
	fake  *e3fake.Transport
	decls e3fake.Declarations
	reset e3fake.Reset
	in    e3contract.Input
	logs  *bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	decl := declaration()
	src, err := e3contract.Freeze(decl, frozenRef)
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	fake := e3fake.New()
	fake.Behaviors[e3fake.Key{Purpose: e3contract.PurposeIdentity, Role: e3contract.RoleOwner}] = e3fake.Behavior{SubjectID: "user-a"}
	fake.Behaviors[e3fake.Key{Purpose: e3contract.PurposeIdentity, Role: e3contract.RoleCross}] = e3fake.Behavior{SubjectID: "user-b"}
	return &fixture{
		fake:  fake,
		decls: e3fake.Declarations{Stored: map[string]e3contract.Declaration{frozenRef: decl}},
		reset: e3fake.Reset{State: e3contract.ResetState{Residue: e3contract.ResidueAbsent}},
		in: e3contract.Input{
			Binding: e3contract.RunBinding{JobRef: "job-1", RunRef: "run-1", TaskRef: "task-1", E2PolicyRef: "e2-policy-1",
				GenerationRef: "gen-1", InstanceRef: "inst-1", Policy: src},
			TargetRef: "target-1", WriteConsent: true,
			Owner:           e3contract.Account{Label: "tester-a", Authenticated: true},
			Cross:           e3contract.Account{Label: "tester-b", Authenticated: true},
			PolicyValidFrom: t0.Add(-time.Hour), PolicyValidUntil: t0.Add(24 * time.Hour),
			AdmittedTypes: 8, RequestTimeout: 10 * time.Second,
			Candidates: []e3contract.Candidate{{Ref: "cand-note", Method: "GET", PathPattern: "/api/notes/{id}"},
				{Ref: "cand-post", Method: "POST", PathPattern: "/api/notes"}},
		},
		logs: &bytes.Buffer{},
	}
}

func (f *fixture) deps() e3contract.Deps {
	return e3contract.Deps{Transport: f.fake, Journal: f.fake, Declarations: f.decls, Reset: f.reset,
		Random: bytes.NewReader(seed()), Now: func() time.Time { return t0 }, Logger: slog.New(slog.NewJSONHandler(f.logs, nil))}
}

func (f *fixture) run(t *testing.T) e3contract.RunResult {
	t.Helper()
	return e3contract.Run(context.Background(), f.deps(), f.in)
}

func key(p e3contract.Purpose, r e3contract.Role, typ string) e3fake.Key {
	return e3fake.Key{Purpose: p, Role: r, Type: typ}
}

func countCalls(calls []e3fake.Call, match func(e3fake.Key) bool) int {
	n := 0
	for _, c := range calls {
		if match(c.Key) {
			n++
		}
	}
	return n
}

// noConfirmed는 계약 시험 공통 단언이다: 판정·후보 어디에도 CONFIRMED가 없다.
func noConfirmed(t *testing.T, res e3contract.RunResult) {
	t.Helper()
	for _, v := range res.Verdicts {
		if v.Value == oracle.Confirmed {
			t.Fatalf("CONFIRMED 판정이 나옴: %+v", v)
		}
	}
	for _, c := range res.Candidates {
		if c.Level == oracle.Confirmed {
			t.Fatalf("CONFIRMED 후보가 나옴: %+v", c)
		}
	}
}

func verdictOf(t *testing.T, res e3contract.RunResult, typ string) e3contract.Verdict {
	t.Helper()
	for _, v := range res.Verdicts {
		if v.ResourceType == typ {
			return v
		}
	}
	t.Fatalf("%s 판정 없음: %+v", typ, res)
	return e3contract.Verdict{}
}

func hasReason(v e3contract.Verdict, r e3contract.ProductReason) bool {
	return len(v.ProductReasons) == 1 && v.ProductReasons[0] == r
}

// 양성 대조: 가짜 E²가 B 교차 응답에서 표식을 보고하면 CONFIRMED가 나온다. 이게 안 되면 아래 CONFIRMED 0 시험이 공허하다.
func TestRunConfirmsWhenCrossSeesMarker(t *testing.T) {
	f := newFixture(t)
	res := f.run(t)
	if res.Status != e3contract.StatusCompleted || len(res.Verdicts) != 2 || res.Requests != 8 {
		t.Fatalf("status=%s verdicts=%d requests=%d", res.Status, len(res.Verdicts), res.Requests)
	}
	for _, typ := range []string{"note", "secret"} {
		v := verdictOf(t, res, typ)
		if v.Value != oracle.Confirmed || len(v.ProductReasons) != 0 || len(v.Evidence) != 5 {
			t.Fatalf("%s: %+v", typ, v)
		}
		if again := oracle.Evaluate(v.Case, v.ObservedAt); again.Value != v.Value || again.DataCheck != v.DataCheck {
			t.Fatalf("판정 값이 오라클 결과와 다름: %+v vs %+v", again, v)
		}
	}
	if len(res.Skipped) != 1 || res.Skipped[0].ResourceType != "post" {
		t.Fatalf("공유 종류를 건너뛰지 않음: %+v", res.Skipped)
	}
	if countCalls(f.fake.Calls(), func(k e3fake.Key) bool { return k.Type == "post" }) != 0 {
		t.Fatal("공유 종류에 요청을 보냄")
	}
	note := verdictOf(t, res, "note")
	if len(note.CandidateRefs) != 1 || note.CandidateRefs[0] != "cand-note" {
		t.Fatalf("후보 연결: %+v", note.CandidateRefs)
	}
	want := map[string]e3contract.CandidateConfirmation{
		"cand-note": {CandidateRef: "cand-note", Level: oracle.Confirmed, ResourceType: "note"},
		"cand-post": {CandidateRef: "cand-post", Level: oracle.Suspected, Reason: "link_none"},
	}
	for _, c := range res.Candidates {
		if c != want[c.CandidateRef] {
			t.Fatalf("후보 %s: %+v", c.CandidateRef, c)
		}
	}
}

func TestRunCrossBlockedOrAbsentIsNotConfirmed(t *testing.T) {
	for name, be := range map[string]e3fake.Behavior{
		"absent":     {Presence: e3contract.MarkerAbsent},
		"forbidden":  {Status: 403, Normal: oracle.StateNo, Presence: e3contract.MarkerAbsent},
		"normal_unk": {Normal: oracle.StateUnknown},
		"reflected":  {MarkerInRequest: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.fake.Behaviors[key(e3contract.PurposeRead, e3contract.RoleCross, "note")] = be
			res := f.run(t)
			if v := verdictOf(t, res, "note"); v.Value != oracle.Undecidable {
				t.Fatalf("note: %+v", v)
			}
			if v := verdictOf(t, res, "secret"); v.Value != oracle.Confirmed {
				t.Fatalf("다른 종류까지 바뀜: %+v", v)
			}
		})
	}
}

func TestRunMarkerCheckNotCompleteIsUndecidable(t *testing.T) {
	cases := []struct {
		name   string
		role   e3contract.Role
		be     e3fake.Behavior
		reason e3contract.ProductReason
		crossN int
	}{
		{"cross_partial", e3contract.RoleCross, e3fake.Behavior{MarkerStatus: e3contract.MarkerPartial}, e3contract.ReasonPartialResponse, 1},
		{"cross_unsupported", e3contract.RoleCross, e3fake.Behavior{MarkerStatus: e3contract.MarkerUnsupported}, e3contract.ReasonEncodingUnsupported, 1},
		{"cross_failed", e3contract.RoleCross, e3fake.Behavior{MarkerStatus: e3contract.MarkerFailed}, e3contract.ReasonDataCheckFailed, 1},
		{"owner_partial", e3contract.RoleOwner, e3fake.Behavior{MarkerStatus: e3contract.MarkerPartial}, e3contract.ReasonPartialResponse, 0},
		{"owner_unsupported", e3contract.RoleOwner, e3fake.Behavior{MarkerStatus: e3contract.MarkerUnsupported}, e3contract.ReasonEncodingUnsupported, 0},
		{"owner_absent", e3contract.RoleOwner, e3fake.Behavior{Presence: e3contract.MarkerAbsent}, e3contract.ReasonOwnerMarkerUnobserved, 0},
		{"owner_not_normal", e3contract.RoleOwner, e3fake.Behavior{Status: 500, Normal: oracle.StateNo}, e3contract.ReasonDataCheckFailed, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.fake.Behaviors[key(e3contract.PurposeRead, c.role, "note")] = c.be
			res := f.run(t)
			v := verdictOf(t, res, "note")
			if v.Value != oracle.Undecidable || !hasReason(v, c.reason) {
				t.Fatalf("note: %+v", v)
			}
			got := countCalls(f.fake.Calls(), func(k e3fake.Key) bool { return k == key(e3contract.PurposeRead, e3contract.RoleCross, "note") })
			if got != c.crossN {
				t.Fatalf("B 조회 %d번, 기대 %d", got, c.crossN)
			}
		})
	}
}

// 진위 실패 공통 단언: 판정 행 없음, 상태가 그대로 드러남, 후보는 SUSPECTED + 사유.
func integrityFailed(t *testing.T, res e3contract.RunResult, reason e3contract.IntegrityReason) {
	t.Helper()
	noConfirmed(t, res)
	if res.Status != e3contract.StatusIntegrityFailed || res.Reason != string(reason) || len(res.Verdicts) != 0 {
		t.Fatalf("status=%s reason=%s verdicts=%d", res.Status, res.Reason, len(res.Verdicts))
	}
	for _, c := range res.Candidates {
		if c.Level != oracle.Suspected || c.Reason != "confirmation_integrity_failed" {
			t.Fatalf("후보가 진위 실패를 드러내지 않음: %+v", c)
		}
	}
	raw, err := json.Marshal(res)
	if err != nil || !bytes.Contains(raw, []byte(`"status":"confirmation_integrity_failed"`)) {
		t.Fatalf("결과 JSON에 진위 실패 상태가 없음: %s %v", raw, err)
	}
}

func TestRunBindingMismatchIsIntegrityFailure(t *testing.T) {
	alter := map[string]func(*e3contract.RunBinding){
		"job":      func(b *e3contract.RunBinding) { b.JobRef = "job-2" },
		"run":      func(b *e3contract.RunBinding) { b.RunRef = "run-2" },
		"task":     func(b *e3contract.RunBinding) { b.TaskRef = "task-2" },
		"e2policy": func(b *e3contract.RunBinding) { b.E2PolicyRef = "e2-policy-2" },
		"gen":      func(b *e3contract.RunBinding) { b.GenerationRef = "gen-2" },
		"instance": func(b *e3contract.RunBinding) { b.InstanceRef = "inst-2" },
		"pol_ref":  func(b *e3contract.RunBinding) { b.Policy.Ref = "decl-frozen-2" },
		"pol_dig":  func(b *e3contract.RunBinding) { b.Policy.DigestSHA256 = strings.Repeat("0", 64) },
		"pol_rev":  func(b *e3contract.RunBinding) { b.Policy.TargetApprovalRevision = "approval-4" },
		"pol_kind": func(b *e3contract.RunBinding) { b.Policy.Kind = e3contract.PolicyKGSnapshot },
	}
	for name, fn := range alter {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			// 두 번째 종류의 B 교차에서 깨뜨린다. 앞 종류의 CONFIRMED도 버려져야 한다.
			f.fake.TamperBoth = func(c e3fake.Call, o *e3contract.Observation) {
				if c.Key == key(e3contract.PurposeRead, e3contract.RoleCross, "secret") {
					fn(&o.Binding)
				}
			}
			integrityFailed(t, f.run(t), e3contract.IntegrityBindingMismatch)
		})
	}
}

func TestRunObservationMismatchIsIntegrityFailure(t *testing.T) {
	alter := map[string]func(*e3contract.Observation){
		"account": func(o *e3contract.Observation) { o.AccountLabel = "tester-c" },
		"role":    func(o *e3contract.Observation) { o.Role = e3contract.RoleOwner },
		"type":    func(o *e3contract.Observation) { o.ResourceType = "secret" },
		"create":  func(o *e3contract.Observation) { o.Create.CallRef = "fake-call-99" },
		"purpose": func(o *e3contract.Observation) {
			o.Purpose = e3contract.PurposeIdentity
			o.ResourceType = ""
			o.Create = e3contract.CallRef{}
			o.Marker = e3contract.MarkerCheck{}
		},
		"field": func(o *e3contract.Observation) { o.Marker.Field = "other" },
	}
	for name, fn := range alter {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.fake.TamperBoth = func(c e3fake.Call, o *e3contract.Observation) {
				if c.Key == key(e3contract.PurposeRead, e3contract.RoleCross, "note") {
					fn(o)
				}
			}
			integrityFailed(t, f.run(t), e3contract.IntegrityObservationMismatch)
		})
	}
}

func TestRunInvalidObservationIsIntegrityFailure(t *testing.T) {
	alter := map[string]func(*e3contract.Observation){
		// 검사 상태가 complete가 아닌데 "표식 없음"을 적었다.
		"absent_without_complete": func(o *e3contract.Observation) {
			o.Marker = e3contract.MarkerCheck{Status: e3contract.MarkerPartial, Presence: e3contract.MarkerAbsent, Field: "body"}
		},
		"complete_partial_body":  func(o *e3contract.Observation) { o.BodyComplete = false },
		"present_without_method": func(o *e3contract.Observation) { o.Marker.Method = "" },
		"unknown_status":         func(o *e3contract.Observation) { o.Marker.Status = "maybe" },
		"not_sent_with_result":   func(o *e3contract.Observation) { o.Sent = false; o.NotSent = e3contract.NotSentPolicyDenied },
	}
	for name, fn := range alter {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.fake.TamperBoth = func(c e3fake.Call, o *e3contract.Observation) {
				if c.Key == key(e3contract.PurposeRead, e3contract.RoleCross, "note") {
					fn(o)
				}
			}
			integrityFailed(t, f.run(t), e3contract.IntegrityObservationInvalid)
		})
	}
}

func TestRunJournalCrossCheck(t *testing.T) {
	cross := key(e3contract.PurposeRead, e3contract.RoleCross, "note")
	t.Run("missing", func(t *testing.T) {
		f := newFixture(t)
		f.fake.DropJournal = func(c e3fake.Call) bool { return c.Key == cross }
		integrityFailed(t, f.run(t), e3contract.IntegrityJournalEventMissing)
	})
	alter := map[string]func(*e3contract.Observation){
		// 돌려준 값은 "표식 있음", 봉인된 저널은 "표식 없음"이다.
		"presence": func(o *e3contract.Observation) {},
		"time":     func(o *e3contract.Observation) { o.At = o.At.Add(time.Nanosecond) },
		"digest":   func(o *e3contract.Observation) { o.BodyDigestSHA256 = strings.Repeat("a", 64) },
	}
	for name, fn := range alter {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if name == "presence" {
				f.fake.Behaviors[cross] = e3fake.Behavior{Presence: e3contract.MarkerAbsent}
				fn = func(o *e3contract.Observation) {
					o.Marker.Presence, o.Marker.Method = e3contract.MarkerPresent, e3contract.MatchExact
				}
			}
			f.fake.Tamper = func(c e3fake.Call, o *e3contract.Observation) {
				if c.Key == cross {
					fn(o)
				}
			}
			integrityFailed(t, f.run(t), e3contract.IntegrityJournalEventMismatch)
		})
	}
	t.Run("reused_ref", func(t *testing.T) {
		f := newFixture(t)
		f.fake.TamperBoth = func(c e3fake.Call, o *e3contract.Observation) {
			if c.Key == cross {
				o.Ref = e3contract.CallRef{CallRef: "fake-call-1", AttemptID: "fake-attempt-1", Sequence: 1}
			}
		}
		integrityFailed(t, f.run(t), e3contract.IntegrityObservationReused)
	})
	t.Run("authority_revoked", func(t *testing.T) {
		f := newFixture(t)
		f.fake.Behaviors[cross] = e3fake.Behavior{Err: e3contract.ErrAuthorityRevoked}
		integrityFailed(t, f.run(t), e3contract.IntegrityAuthorityRevoked)
	})
}

func TestRunPolicySourceChecks(t *testing.T) {
	t.Run("digest_mismatch", func(t *testing.T) {
		f := newFixture(t)
		changed := declaration()
		changed.Types[0].Read.PathTemplate = "/api/notes/{id}/x"
		f.decls.Stored[frozenRef] = changed
		res := f.run(t)
		integrityFailed(t, res, e3contract.IntegrityPolicyDigestMismatch)
		if len(f.fake.Calls()) != 0 {
			t.Fatal("정책 불일치인데 요청을 보냄")
		}
	})
	t.Run("approval_revision_mismatch", func(t *testing.T) {
		f := newFixture(t)
		f.in.Binding.Policy.TargetApprovalRevision = "approval-4"
		integrityFailed(t, f.run(t), e3contract.IntegrityPolicyDigestMismatch)
	})
	t.Run("target_mismatch", func(t *testing.T) {
		f := newFixture(t)
		f.in.TargetRef = "target-2"
		integrityFailed(t, f.run(t), e3contract.IntegrityPolicyTargetMismatch)
	})
	t.Run("unavailable", func(t *testing.T) {
		f := newFixture(t)
		f.decls.Err = e3contract.ErrPolicyUnavailable
		res := f.run(t)
		if res.Status != e3contract.StatusStartRefused || res.Reason != string(e3contract.RefusePolicySourceUnavailable) ||
			len(f.fake.Calls()) != 0 {
			t.Fatalf("%+v calls=%d", res, len(f.fake.Calls()))
		}
	})
}

// 제품 경로는 product_declaration만 받는다. kg_snapshot이면 확정 단계를 켜지 않는다.
func TestRunProductRejectsKGSnapshot(t *testing.T) {
	f := newFixture(t)
	f.in.Binding.Policy.Kind = e3contract.PolicyKGSnapshot
	res := f.run(t)
	noConfirmed(t, res)
	if res.Status != e3contract.StatusDisabled || res.Reason != string(e3contract.DisabledPolicyKind) || len(f.fake.Calls()) != 0 {
		t.Fatalf("%+v calls=%d", res, len(f.fake.Calls()))
	}
	if err := f.in.Binding.Policy.ValidateProduct(); !errors.Is(err, e3contract.ErrProductPolicyKind) {
		t.Fatalf("ValidateProduct: %v", err)
	}
}

func TestRunDisabledSendsNothing(t *testing.T) {
	cases := map[string]struct {
		edit   func(*fixture, *e3contract.Deps)
		reason e3contract.DisabledReason
	}{
		"no_transport":    {func(_ *fixture, d *e3contract.Deps) { d.Transport = nil }, e3contract.DisabledNotWired},
		"no_journal":      {func(_ *fixture, d *e3contract.Deps) { d.Journal = nil }, e3contract.DisabledNotWired},
		"no_reset":        {func(_ *fixture, d *e3contract.Deps) { d.Reset = nil }, e3contract.DisabledNotWired},
		"no_declaration":  {func(f *fixture, _ *e3contract.Deps) { f.in.Binding.Policy = e3contract.PolicySource{} }, e3contract.DisabledNoDeclaration},
		"binding_invalid": {func(f *fixture, _ *e3contract.Deps) { f.in.Binding.RunRef = "run\n1" }, e3contract.DisabledBindingInvalid},
		// 연결이 없으면 선언이 없어도 not_wired다(최종 이미지에서 not_wired가 나오지 않는지 이 순서로 드러난다).
		"not_wired_first": {func(f *fixture, d *e3contract.Deps) {
			d.Transport = nil
			f.in.Binding.Policy = e3contract.PolicySource{}
		}, e3contract.DisabledNotWired},
		"no_write_consent": {func(f *fixture, _ *e3contract.Deps) { f.in.WriteConsent = false }, e3contract.DisabledWriteNotConsented},
		"cross_unauth":     {func(f *fixture, _ *e3contract.Deps) { f.in.Cross.Authenticated = false }, e3contract.DisabledAccountsUnverified},
		"owner_missing":    {func(f *fixture, _ *e3contract.Deps) { f.in.Owner = e3contract.Account{} }, e3contract.DisabledAccountsUnverified},
		"same_account":     {func(f *fixture, _ *e3contract.Deps) { f.in.Cross.Label = f.in.Owner.Label }, e3contract.DisabledAccountsUnverified},
		"policy_window":    {func(f *fixture, _ *e3contract.Deps) { f.in.PolicyValidUntil = f.in.PolicyValidFrom }, e3contract.DisabledPolicyWindow},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			deps := f.deps()
			c.edit(f, &deps)
			res := e3contract.Run(context.Background(), deps, f.in)
			noConfirmed(t, res)
			if res.Status != e3contract.StatusDisabled || res.Reason != string(c.reason) || len(f.fake.Calls()) != 0 {
				t.Fatalf("%+v calls=%d", res, len(f.fake.Calls()))
			}
		})
	}
}

func TestRunNotImplementedTransportDoesNotStart(t *testing.T) {
	f := newFixture(t)
	f.fake.RegisterErr = e3contract.ErrNotImplemented
	res := f.run(t)
	noConfirmed(t, res)
	if res.Status != e3contract.StatusStartRefused || res.Reason != string(e3contract.RefuseMarkerRegistrationFailed) ||
		len(f.fake.Calls()) != 0 || len(res.Verdicts) != 0 {
		t.Fatalf("%+v calls=%d", res, len(f.fake.Calls()))
	}
}

func TestRunResetGate(t *testing.T) {
	cases := map[string]struct {
		reset  e3fake.Reset
		refuse e3contract.StartRefusal
	}{
		"reset_missing": {e3fake.Reset{State: e3contract.ResetState{PreviousWrites: true, Residue: e3contract.ResidueAbsent}}, e3contract.RefuseResetConfirmationMissing},
		// 리셋 확인이 없으면 잔존을 아직 모르더라도 확인 없음이 사유다.
		"reset_missing_unknown": {e3fake.Reset{State: e3contract.ResetState{PreviousWrites: true, Residue: e3contract.ResidueUnknown}}, e3contract.RefuseResetConfirmationMissing},
		"residue_present":       {e3fake.Reset{State: e3contract.ResetState{PreviousWrites: true, ResetConfirmed: true, Residue: e3contract.ResiduePresent}}, e3contract.RefusePreviousMarkersPresent},
		"residue_unknown":       {e3fake.Reset{State: e3contract.ResetState{PreviousWrites: true, ResetConfirmed: true, Residue: e3contract.ResidueUnknown}}, e3contract.RefuseResetStateUnavailable},
		"residue_empty":         {e3fake.Reset{State: e3contract.ResetState{}}, e3contract.RefuseResetStateUnavailable},
		// 오류와 함께 돌아온 상태가 멀쩡해 보여도 오류가 이긴다.
		"gate_error": {e3fake.Reset{State: e3contract.ResetState{Residue: e3contract.ResidueAbsent}, Err: errors.New("저널 읽기 실패")}, e3contract.RefuseResetStateUnavailable},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.reset = c.reset
			res := f.run(t)
			noConfirmed(t, res)
			if res.Status != e3contract.StatusStartRefused || res.Reason != string(c.refuse) || len(f.fake.Calls()) != 0 {
				t.Fatalf("%+v calls=%d", res, len(f.fake.Calls()))
			}
		})
	}
	t.Run("reset_confirmed_runs", func(t *testing.T) {
		f := newFixture(t)
		f.reset = e3fake.Reset{State: e3contract.ResetState{PreviousWrites: true, ResetConfirmed: true, Residue: e3contract.ResidueAbsent}}
		if res := f.run(t); res.Status != e3contract.StatusCompleted {
			t.Fatalf("%+v", res)
		}
	})
}

func TestRunIdentityFailureStopsWrites(t *testing.T) {
	cases := map[string]struct {
		owner, cross e3fake.Behavior
		reason       e3contract.ProductReason
	}{
		"owner_no_subject": {e3fake.Behavior{}, e3fake.Behavior{SubjectID: "user-b"}, e3contract.ReasonIdentityUnverified},
		"cross_denied":     {e3fake.Behavior{SubjectID: "user-a"}, e3fake.Behavior{NotSent: e3contract.NotSentPolicyDenied}, e3contract.ReasonIdentityUnverified},
		"cross_not_normal": {e3fake.Behavior{SubjectID: "user-a"}, e3fake.Behavior{SubjectID: "user-b", Normal: oracle.StateNo}, e3contract.ReasonIdentityUnverified},
		"identical":        {e3fake.Behavior{SubjectID: "same"}, e3fake.Behavior{SubjectID: "same"}, e3contract.ReasonSubjectsIdentical},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.fake.Behaviors[key(e3contract.PurposeIdentity, e3contract.RoleOwner, "")] = c.owner
			f.fake.Behaviors[key(e3contract.PurposeIdentity, e3contract.RoleCross, "")] = c.cross
			res := f.run(t)
			noConfirmed(t, res)
			if len(res.Verdicts) != 2 {
				t.Fatalf("%+v", res)
			}
			for _, v := range res.Verdicts {
				if v.Value != oracle.Undecidable || !hasReason(v, c.reason) {
					t.Fatalf("%+v", v)
				}
			}
			if n := countCalls(f.fake.Calls(), func(k e3fake.Key) bool { return k.Purpose != e3contract.PurposeIdentity }); n != 0 {
				t.Fatalf("신원 확인 실패 뒤 생성·조회 %d건", n)
			}
		})
	}
}

func TestRunApprovalDenialStopsThatType(t *testing.T) {
	for reason, want := range map[e3contract.NotSentReason]e3contract.ProductReason{
		e3contract.NotSentApprovalDenied:  e3contract.ReasonCreateApprovalDenied,
		e3contract.NotSentApprovalTimeout: e3contract.ReasonCreateApprovalTimeout,
	} {
		t.Run(string(reason), func(t *testing.T) {
			f := newFixture(t)
			f.fake.Behaviors[key(e3contract.PurposeCreate, e3contract.RoleOwner, "note")] = e3fake.Behavior{NotSent: reason}
			res := f.run(t)
			if v := verdictOf(t, res, "note"); v.Value != oracle.Undecidable || !hasReason(v, want) {
				t.Fatalf("note: %+v", v)
			}
			calls := f.fake.Calls()
			if n := countCalls(calls, func(k e3fake.Key) bool { return k.Type == "note" }); n != 1 {
				t.Fatalf("승인 거부 뒤 그 종류 요청 %d건(생성 1건만 기대)", n)
			}
			// #596 §7: 거부·만료는 그 종류만 UNDECIDABLE이다. 다른 종류는 계속 시험한다.
			if v := verdictOf(t, res, "secret"); v.Value != oracle.Confirmed {
				t.Fatalf("secret: %+v", v)
			}
		})
	}
}

func TestRunBudgetExhaustionStopsAllRequests(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		f := newFixture(t)
		f.fake.Behaviors[key(e3contract.PurposeCreate, e3contract.RoleOwner, "note")] = e3fake.Behavior{NotSent: e3contract.NotSentBudgetExhausted}
		res := f.run(t)
		noConfirmed(t, res)
		for _, typ := range []string{"note", "secret"} {
			if v := verdictOf(t, res, typ); v.Value != oracle.Undecidable || !hasReason(v, e3contract.ReasonConfirmationBudgetExhaust) {
				t.Fatalf("%s: %+v", typ, v)
			}
		}
		if n := len(f.fake.Calls()); n != 3 {
			t.Fatalf("몫 소진 뒤에도 요청: 전체 %d건(신원 2 + 생성 1 기대)", n)
		}
	})
	t.Run("cross_read_keeps_finished", func(t *testing.T) {
		f := newFixture(t)
		f.fake.Behaviors[key(e3contract.PurposeRead, e3contract.RoleCross, "secret")] = e3fake.Behavior{NotSent: e3contract.NotSentBudgetExhausted}
		res := f.run(t)
		if v := verdictOf(t, res, "note"); v.Value != oracle.Confirmed {
			t.Fatalf("끝낸 판정이 사라짐: %+v", v)
		}
		if v := verdictOf(t, res, "secret"); v.Value != oracle.Undecidable || !hasReason(v, e3contract.ReasonConfirmationBudgetExhaust) {
			t.Fatalf("secret: %+v", v)
		}
	})
	t.Run("time_budget", func(t *testing.T) {
		f := newFixture(t)
		ctx, cancel := context.WithDeadline(context.Background(), t0)
		defer cancel()
		res := e3contract.Run(ctx, f.deps(), f.in)
		noConfirmed(t, res)
		if len(f.fake.Calls()) != 0 || len(res.Verdicts) != 2 || !hasReason(res.Verdicts[0], e3contract.ReasonConfirmationBudgetExhaust) {
			t.Fatalf("%+v calls=%d", res, len(f.fake.Calls()))
		}
	})
}

func TestRunCreateUnknownIsNotResent(t *testing.T) {
	for name, c := range map[string]struct {
		be     e3fake.Behavior
		reason e3contract.ProductReason
	}{
		"incomplete": {e3fake.Behavior{Incomplete: true}, e3contract.ReasonRequestResultUnknown},
		"error":      {e3fake.Behavior{Err: errors.New("연결 끊김")}, e3contract.ReasonRequestResultUnknown},
		"no_id":      {e3fake.Behavior{NoObjectID: true}, e3contract.ReasonCreateFailed},
		"not_normal": {e3fake.Behavior{Status: 400, Normal: oracle.StateNo}, e3contract.ReasonCreateFailed},
		"denied":     {e3fake.Behavior{NotSent: e3contract.NotSentPolicyDenied}, e3contract.ReasonRequestDenied},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.fake.Behaviors[key(e3contract.PurposeCreate, e3contract.RoleOwner, "note")] = c.be
			res := f.run(t)
			if v := verdictOf(t, res, "note"); v.Value != oracle.Undecidable || !hasReason(v, c.reason) {
				t.Fatalf("note: %+v", v)
			}
			if n := countCalls(f.fake.Calls(), func(k e3fake.Key) bool { return k.Type == "note" }); n != 1 {
				t.Fatalf("note 요청 %d건(생성 1건만 기대, 다시 보내지 않음)", n)
			}
		})
	}
}

func TestRunInterruptedKeepsFinishedVerdicts(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.fake.TamperBoth = func(c e3fake.Call, _ *e3contract.Observation) {
		if c.Key == key(e3contract.PurposeCreate, e3contract.RoleOwner, "secret") {
			cancel()
		}
	}
	res := e3contract.Run(ctx, f.deps(), f.in)
	if res.Status != e3contract.StatusInterrupted || len(res.Verdicts) != 1 || res.Verdicts[0].ResourceType != "note" {
		t.Fatalf("%+v", res)
	}
}

func TestRunCandidateLinking(t *testing.T) {
	f := newFixture(t)
	f.in.Candidates = []e3contract.Candidate{
		{Ref: "exact", Method: "GET", PathPattern: "/api/secrets/{id}"},
		{Ref: "other_name", Method: "GET", PathPattern: "/api/secrets/{n}"},
		{Ref: "literal", Method: "GET", PathPattern: "/api/secrets/7"},
		{Ref: "longer", Method: "GET", PathPattern: "/api/secrets/{id}/x"},
		{Ref: "relative", Method: "GET", PathPattern: "api/secrets/{id}"},
	}
	f.fake.Behaviors[key(e3contract.PurposeRead, e3contract.RoleCross, "secret")] = e3fake.Behavior{Presence: e3contract.MarkerAbsent}
	res := f.run(t)
	want := map[string]e3contract.CandidateConfirmation{
		"exact":      {CandidateRef: "exact", Level: oracle.Suspected, ResourceType: "secret"},
		"other_name": {CandidateRef: "other_name", Level: oracle.Suspected, ResourceType: "secret"},
		"literal":    {CandidateRef: "literal", Level: oracle.Suspected, Reason: "link_none"},
		"longer":     {CandidateRef: "longer", Level: oracle.Suspected, Reason: "link_none"},
		"relative":   {CandidateRef: "relative", Level: oracle.Suspected, Reason: "link_none"},
	}
	for _, c := range res.Candidates {
		if c != want[c.CandidateRef] {
			t.Fatalf("%s: %+v", c.CandidateRef, c)
		}
	}
	if v := verdictOf(t, res, "secret"); len(v.CandidateRefs) != 2 {
		t.Fatalf("UNDECIDABLE 판정도 연결은 남는다: %+v", v.CandidateRefs)
	}

	t.Run("ambiguous", func(t *testing.T) {
		f := newFixture(t)
		decl := declaration()
		decl.Types[2].Read.PathTemplate = "/api/notes/{id}"
		src, err := e3contract.Freeze(decl, frozenRef)
		if err != nil {
			t.Fatal(err)
		}
		f.decls.Stored[frozenRef] = decl
		f.in.Binding.Policy = src
		res := f.run(t)
		for _, c := range res.Candidates {
			if c.CandidateRef == "cand-note" && (c.Reason != "link_ambiguous" || c.Level != oracle.Suspected) {
				t.Fatalf("%+v", c)
			}
		}
		for _, v := range res.Verdicts {
			if len(v.CandidateRefs) != 0 {
				t.Fatalf("모호한 후보를 이음: %+v", v)
			}
		}
	})
}

// 확정 단계 한 회를 돈 뒤 결과·로그·가짜 E² 기록 어디에도 표식이 없다. 실제 E² 구현도 같은 확인을 저널·영수증·
// 승인 미리보기·로그·평가 내보내기에 대해 통과해야 한다(LeakingSinks).
func TestRunLeavesNoMarker(t *testing.T) {
	f := newFixture(t)
	res := f.run(t)
	if res.Status != e3contract.StatusCompleted {
		t.Fatalf("%+v", res)
	}
	m := testMarker(t)
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	sinks := f.fake.Sinks()
	sinks["result"] = raw
	sinks["orchestrator_log"] = f.logs.Bytes()
	if leaks := e3contract.LeakingSinks(m, sinks); len(leaks) != 0 {
		t.Fatalf("표식이 남은 기록: %v", leaks)
	}
	// 공허하지 않음: 가짜 E²는 생성 본문을 승인 미리보기에 쓰고(가린 뒤), 그 자리에 가린 글이 있다.
	if !bytes.Contains(sinks["approval_preview"], []byte(`"body":"[REDACTED]"`)) {
		t.Fatalf("승인 미리보기에 생성 본문이 없음: %s", sinks["approval_preview"])
	}
	// 등록하지 않으면 생성·조회를 보내지 않는다.
	f2 := newFixture(t)
	_, err = f2.fake.Create(context.Background(), e3contract.CreateRequest{Binding: f2.in.Binding, ResourceType: "note",
		AccountLabel: "tester-a", Marker: m})
	if !errors.Is(err, e3contract.ErrMarkerNotRegistered) {
		t.Fatalf("등록 전 생성: %v", err)
	}
}

func TestLeakingSinksFindsEveryForm(t *testing.T) {
	m := testMarker(t)
	forms := e3contract.MarkerForms(m)
	if len(forms) != 6 {
		t.Fatalf("모양 %d개", len(forms))
	}
	for i, form := range forms {
		sinks := map[string][]byte{"clean": []byte("nothing here"), "leak": append([]byte(`{"x":"`), append(form, '"', '}')...)}
		if leaks := e3contract.LeakingSinks(m, sinks); len(leaks) != 1 || leaks[0] != "leak" {
			t.Fatalf("모양 %d를 못 찾음: %v", i, leaks)
		}
	}
	// 대소문자가 섞인 모양(소문자 퍼센트 16진, 첫 글자만 대문자)도 찾는다.
	mixed := map[string][]byte{"pct_lower": bytes.ToLower(forms[3]), "title": append(bytes.ToUpper(forms[0][:1]), forms[0][1:]...)}
	if leaks := e3contract.LeakingSinks(m, mixed); len(leaks) != 2 {
		t.Fatalf("대소문자 섞인 모양을 못 찾음: %v", leaks)
	}
	if leaks := e3contract.LeakingSinks(e3contract.Marker{}, map[string][]byte{"x": nil}); len(leaks) != 1 || leaks[0] != "all" {
		t.Fatalf("모양이 틀린 표식은 확인할 수 없음을 드러내야 함: %v", leaks)
	}
}

// 결속 검증에 실패한 입력은 결과에 남지 않는다. 거부 사유는 남고, 직렬화된 결과에 검증 전 값이 없다(#698 이동찬 리뷰).
func TestRunResultOmitsUnvalidatedInput(t *testing.T) {
	f := newFixture(t)
	f.in.Binding.RunRef = "run-1\n<injected>"
	f.in.Candidates = append(f.in.Candidates, e3contract.Candidate{Ref: "bad ref\n<x>", Method: "GET", PathPattern: "/api/notes/{id}"})
	res := f.run(t)
	raw, err := json.Marshal(res)
	if err != nil || res.Status != e3contract.StatusDisabled || res.Reason != string(e3contract.DisabledBindingInvalid) || res.Binding != nil {
		t.Fatalf("%+v %v", res, err)
	}
	if bytes.Contains(raw, []byte("injected")) || bytes.Contains(raw, []byte("bad ref")) || bytes.Contains(raw, []byte(`"binding"`)) {
		t.Fatalf("검증 전 값이 결과에 남음: %s", raw)
	}
	// 검증을 통과한 결속은 싣고, 모양이 틀린 후보 참조만 뺀다.
	ok := newFixture(t)
	ok.in.Candidates = append(ok.in.Candidates, e3contract.Candidate{Ref: "bad ref\n<x>", Method: "GET", PathPattern: "/api/notes/{id}"})
	res = ok.run(t)
	raw, _ = json.Marshal(res)
	if res.Binding == nil || *res.Binding != ok.in.Binding || bytes.Contains(raw, []byte("bad ref")) || len(res.Candidates) != 2 {
		t.Fatalf("검증된 결속·후보: %+v", res)
	}
}
