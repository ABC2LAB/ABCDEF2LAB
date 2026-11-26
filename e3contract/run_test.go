package e3contract_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
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
			{Type: "post", Sharing: e3contract.SharingShared}, ownerOnly("secret", "/api/secrets")},
		OwnerAccountLabel: "tester-a", CrossAccountLabel: "tester-b", AccountsSamePrivilege: true}
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
	fake   *e3fake.Transport
	decls  e3fake.Declarations
	reset  e3fake.Reset
	writes *e3fake.Writes
	in     e3contract.Input
	logs   *bytes.Buffer
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
		fake:   fake,
		writes: &e3fake.Writes{Transport: fake},
		decls:  e3fake.Declarations{Stored: map[string]e3contract.Declaration{frozenRef: decl}},
		reset:  e3fake.Reset{State: e3contract.ResetState{Residue: e3contract.ResidueAbsent}},
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
	return e3contract.Deps{Transport: f.fake, Journal: f.fake, Declarations: f.decls, Reset: f.reset, Writes: f.writes,
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
		// 저장 판정의 자원 참조가 될 생성 객체 digest는 종류마다 그 종류의 생성 관찰에서 온다(E1).
		if v.ObjectIDDigest == "" {
			t.Fatalf("%s: 생성 객체 digest 없음", typ)
		}
	}
	if verdictOf(t, res, "note").ObjectIDDigest == verdictOf(t, res, "secret").ObjectIDDigest {
		t.Fatal("종류가 다른데 생성 객체 digest가 같음")
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
	// 저널을 검증하지 못하면(신뢰 체크포인트 없음·불일치, MAC·체인 오류 등) 이벤트 없음과 다른 사유로 남는다(v3 계약 §5·§7).
	// 그 밖의 오류도 검증을 마치지 못한 것이라 무결성 실패다. 어느 쪽이든 판정 행 없이 진위 실패다.
	for name, err := range map[string]error{
		"integrity":         e3contract.ErrJournalIntegrity,
		"integrity_wrapped": fmt.Errorf("checkpoint mismatch: %w", e3contract.ErrJournalIntegrity),
		"other":             errors.New("journal read failed"),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.fake.SealedErr = err
			integrityFailed(t, f.run(t), e3contract.IntegrityJournalIntegrityFailed)
		})
	}
	t.Run("missing_wrapped", func(t *testing.T) {
		f := newFixture(t)
		f.fake.SealedErr = fmt.Errorf("not in verified snapshot: %w", e3contract.ErrJournalEventMissing)
		integrityFailed(t, f.run(t), e3contract.IntegrityJournalEventMissing)
	})
	t.Run("binding_mismatch", func(t *testing.T) {
		f := newFixture(t)
		f.fake.SealedErr = e3contract.ErrJournalBindingMismatch
		integrityFailed(t, f.run(t), e3contract.IntegrityBindingMismatch)
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
	// Transport가 E² 기록의 모양 위반·관찰 밖 칸 불일치를 오류로 알리면 결과 불명이 아니라 진위 실패다(v3 계약 §7).
	t.Run("transport_observation_invalid", func(t *testing.T) {
		f := newFixture(t)
		f.fake.Behaviors[cross] = e3fake.Behavior{Err: fmt.Errorf("event stage: %w", e3contract.ErrObservation)}
		integrityFailed(t, f.run(t), e3contract.IntegrityObservationInvalid)
	})
	t.Run("transport_observation_mismatch", func(t *testing.T) {
		f := newFixture(t)
		f.fake.Behaviors[cross] = e3fake.Behavior{Err: fmt.Errorf("account binding: %w", e3contract.ErrObservationMismatch)}
		integrityFailed(t, f.run(t), e3contract.IntegrityObservationMismatch)
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
		"no_writes":       {func(_ *fixture, d *e3contract.Deps) { d.Writes = nil }, e3contract.DisabledNotWired},
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
		// 잔존 점검이 없어도(not_checked) 리셋 확인이 없으면 시작하지 않는다.
		"reset_missing_not_checked": {e3fake.Reset{State: e3contract.ResetState{PreviousWrites: true, Residue: e3contract.ResidueNotChecked}}, e3contract.RefuseResetConfirmationMissing},
		"residue_present":           {e3fake.Reset{State: e3contract.ResetState{PreviousWrites: true, ResetConfirmed: true, Residue: e3contract.ResiduePresent}}, e3contract.RefusePreviousMarkersPresent},
		"residue_unknown":           {e3fake.Reset{State: e3contract.ResetState{PreviousWrites: true, ResetConfirmed: true, Residue: e3contract.ResidueUnknown}}, e3contract.RefuseResetStateUnavailable},
		"residue_empty":             {e3fake.Reset{State: e3contract.ResetState{}}, e3contract.RefuseResetStateUnavailable},
		// 오류와 함께 돌아온 상태가 멀쩡해 보여도 오류가 이긴다.
		"gate_error": {e3fake.Reset{State: e3contract.ResetState{Residue: e3contract.ResidueAbsent}, Err: errors.New("저널 읽기 실패")}, e3contract.RefuseResetStateUnavailable},
		// 실행 시작 뒤 대상이 바뀌었으면(또는 지금 대상을 확인하지 못하면) 다른 상태가 깨끗해도 요청 없이 멈춘다(v3 계약 §6).
		"target_changed": {e3fake.Reset{State: e3contract.ResetState{Residue: e3contract.ResidueAbsent, TargetChanged: true}}, e3contract.RefuseTargetChanged},
		// 대상이 바뀐 것이 리셋 확인 없음보다 먼저다(지난 쓰기의 리셋 여부가 바뀐 대상 기준이 아님).
		"target_changed_before_reset": {e3fake.Reset{State: e3contract.ResetState{PreviousWrites: true, Residue: e3contract.ResidueAbsent, TargetChanged: true}}, e3contract.RefuseTargetChanged},
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
	// 잔존 점검이 없으면 리셋 확인만으로 시작하고, 새 표식으로 판정한다(사용자 결정 2026-11-20, 계약 §12).
	t.Run("reset_confirmed_not_checked_runs", func(t *testing.T) {
		f := newFixture(t)
		f.reset = e3fake.Reset{State: e3contract.ResetState{PreviousWrites: true, ResetConfirmed: true, Residue: e3contract.ResidueNotChecked}}
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
			if v := verdictOf(t, res, "note"); v.Value != oracle.Undecidable || !hasReason(v, want) || v.ObjectIDDigest != "" {
				t.Fatalf("note: %+v", v)
			}
			calls := f.fake.Calls()
			if n := countCalls(calls, func(k e3fake.Key) bool { return k.Type == "note" }); n != 1 {
				t.Fatalf("승인 거부 뒤 그 종류 요청 %d건(생성 1건만 기대)", n)
			}
			// #596 §7: 거부·만료는 그 종류만 UNDECIDABLE이다. 다른 종류는 계속 시험한다.
			if v := verdictOf(t, res, "secret"); v.Value != oracle.Confirmed || v.ObjectIDDigest == "" {
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
		// 상태 줄은 받고 본문이 끊김: 진위 실패가 아니라 결과 불명이다(#774 리뷰)
		"incomplete_with_status": {e3fake.Behavior{Incomplete: true, Status: 200}, e3contract.ReasonRequestResultUnknown},
		"error":                  {e3fake.Behavior{Err: errors.New("연결 끊김")}, e3contract.ReasonRequestResultUnknown},
		"no_id":                  {e3fake.Behavior{NoObjectID: true}, e3contract.ReasonCreateFailed},
		"not_normal":             {e3fake.Behavior{Status: 400, Normal: oracle.StateNo}, e3contract.ReasonCreateFailed},
		"denied":                 {e3fake.Behavior{NotSent: e3contract.NotSentPolicyDenied}, e3contract.ReasonRequestDenied},
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

// 운영자 중지로 실행 문맥이 끝나 봉인 조회가 중단되면 진위 실패가 아니라 중단(interrupted)이다. 검증하지 못한 관찰은 판정에
// 쓰지 않는다. 문맥이 살아 있는데 저널이 취소 오류를 돌려주면 여전히 무결성 실패다(#753 이동찬 리뷰).
func TestRunSealedCancelledByStop(t *testing.T) {
	cross := key(e3contract.PurposeRead, e3contract.RoleCross, "note")
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps := f.deps()
	deps.Journal = cancelOnSealed{Transport: f.fake, cancel: cancel, key: cross, calls: f.fake.Calls}
	res := e3contract.Run(ctx, deps, f.in)
	noConfirmed(t, res)
	if res.Status != e3contract.StatusInterrupted || res.Reason != "" {
		t.Fatalf("중지로 끊긴 조회: %+v", res)
	}
	for _, v := range res.Verdicts {
		if v.ResourceType == "note" {
			t.Fatalf("검증 못 한 관찰로 판정: %+v", v)
		}
	}
	// 문맥은 살아 있는데 저널이 취소 오류를 돌려줌 → 무결성 실패.
	f = newFixture(t)
	f.fake.SealedErr = context.Canceled
	integrityFailed(t, f.run(t), e3contract.IntegrityJournalIntegrityFailed)
}

// cancelOnSealed는 교차 조회의 봉인 조회 때 실행 문맥을 끊고 그 오류를 돌려준다(운영자 중지가 조회 중에 옴).
type cancelOnSealed struct {
	*e3fake.Transport
	cancel context.CancelFunc
	key    e3fake.Key
	calls  func() []e3fake.Call
}

func (c cancelOnSealed) Sealed(ctx context.Context, b e3contract.RunBinding, ref e3contract.CallRef) (e3contract.Observation, error) {
	calls := c.calls()
	if len(calls) > 0 && calls[len(calls)-1].Key == c.key {
		c.cancel()
		return e3contract.Observation{}, ctx.Err()
	}
	return c.Transport.Sealed(ctx, b, ref)
}

// 확정 단계 시간 몫이 끝나(실행 문맥 마감) 봉인 조회가 중단되면 그 종류는 예산 사유 UNDECIDABLE이고 진위 실패가 아니다.
func TestRunSealedDeadlineIsBudget(t *testing.T) {
	cross := key(e3contract.PurposeRead, e3contract.RoleCross, "note")
	f := newFixture(t)
	ctx := &flipDeadline{Context: context.Background(), done: make(chan struct{})}
	deps := f.deps()
	deps.Journal = expireOnSealed{Transport: f.fake, ctx: ctx, key: cross, calls: f.fake.Calls}
	res := e3contract.Run(ctx, deps, f.in)
	noConfirmed(t, res)
	if res.Status == e3contract.StatusIntegrityFailed {
		t.Fatalf("시간 몫 끝을 진위 실패로: %+v", res)
	}
	if v := verdictOf(t, res, "note"); v.Value != oracle.Undecidable || !hasReason(v, e3contract.ReasonConfirmationBudgetExhaust) {
		t.Fatalf("note: %+v", v)
	}
}

// flipDeadline은 expire 뒤로 마감이 지난 문맥이다.
type flipDeadline struct {
	context.Context
	done    chan struct{}
	expired atomic.Bool
}

func (d *flipDeadline) Done() <-chan struct{} { return d.done }
func (d *flipDeadline) Err() error {
	if d.expired.Load() {
		return context.DeadlineExceeded
	}
	return nil
}
func (d *flipDeadline) expire() {
	if d.expired.CompareAndSwap(false, true) {
		close(d.done)
	}
}

type expireOnSealed struct {
	*e3fake.Transport
	ctx   *flipDeadline
	key   e3fake.Key
	calls func() []e3fake.Call
}

func (e expireOnSealed) Sealed(ctx context.Context, b e3contract.RunBinding, ref e3contract.CallRef) (e3contract.Observation, error) {
	calls := e.calls()
	if len(calls) > 0 && calls[len(calls)-1].Key == e.key {
		e.ctx.expire()
		return e3contract.Observation{}, ctx.Err()
	}
	return e.Transport.Sealed(ctx, b, ref)
}

// 앞 종류의 생성 객체 digest가 뒤 종류의 판정에 남지 않는다(생성 전에 멈춘 종류는 digest 없음).
func TestRunObjectDigestDoesNotCarryOver(t *testing.T) {
	for _, denied := range []string{"note", "secret"} {
		t.Run(denied, func(t *testing.T) {
			f := newFixture(t)
			f.fake.Behaviors[key(e3contract.PurposeCreate, e3contract.RoleOwner, denied)] = e3fake.Behavior{NotSent: e3contract.NotSentApprovalDenied}
			res := f.run(t)
			for _, typ := range []string{"note", "secret"} {
				if v := verdictOf(t, res, typ); (v.ObjectIDDigest == "") != (typ == denied) {
					t.Fatalf("%s: %+v", typ, v)
				}
			}
		})
	}
}

// 교차(B) 조회가 거부 응답(401·403·404)이면 표식 검사보다 상태를 먼저 본다(사용자 결정 2026-11-20). 표식 검사가 없음·실패면
// 정상 데이터가 아닌 응답이라 오라클 사유 data_no_match(UNDECIDABLE)이고 제품 사유는 없다. 거부인데 표식이 있으면 모순이라
// 제품 사유 product_data_check_contradiction으로 UNDECIDABLE이다. 어느 경우든 CONFIRMED가 아니다. 거부가 아닌 응답의 검사
// 실패는 그대로 product_data_check_failed다.
func TestRunCrossDeniedReadsStatusFirst(t *testing.T) {
	markers := map[string]e3fake.Behavior{
		"absent":  {Presence: e3contract.MarkerAbsent},
		"failed":  {MarkerStatus: e3contract.MarkerFailed},
		"present": {Presence: e3contract.MarkerPresent},
		// 거부 본문이 JSON이 아니어서 E²가 정상 여부를 unknown으로 둔 경우(e2proxy inspectRead)도 상태로 정한다.
		"failed_normal_unknown": {MarkerStatus: e3contract.MarkerFailed, Normal: oracle.StateUnknown},
	}
	for _, status := range []int{401, 403, 404} {
		for name, marker := range markers {
			t.Run(fmt.Sprintf("%d_%s", status, name), func(t *testing.T) {
				be := marker
				be.Status = status
				if be.Normal == "" {
					be.Normal = oracle.StateNo
				}
				f := newFixture(t)
				f.fake.Behaviors[key(e3contract.PurposeRead, e3contract.RoleCross, "note")] = be
				res := f.run(t)
				// 다른 종류(secret)는 양성 대조로 그대로 CONFIRMED다. 바뀐 종류만 본다.
				if v := verdictOf(t, res, "secret"); v.Value != oracle.Confirmed {
					t.Fatalf("다른 종류까지 바뀜: %+v", v)
				}
				v := verdictOf(t, res, "note")
				if v.Value != oracle.Undecidable {
					t.Fatalf("note: %+v", v)
				}
				if name == "present" {
					if !hasReason(v, e3contract.ReasonDataCheckContradiction) {
						t.Fatalf("모순 사유 없음: %+v", v)
					}
					return
				}
				if len(v.ProductReasons) != 0 || !slices.Contains(v.OracleReasons, oracle.ReasonDataNoMatch) || v.DataCheck != oracle.StateNo ||
					v.Case.Facts.CrossNormalData != oracle.StateNo {
					t.Fatalf("거부 응답이 data_no_match가 아님: %+v", v)
				}
			})
		}
	}
	// 거부가 아닌 응답(2xx·그 밖의 4xx·5xx)의 표식 검사 실패는 그대로 데이터 검사 실패다.
	for _, status := range []int{200, 400, 500} {
		t.Run(fmt.Sprintf("%d_failed", status), func(t *testing.T) {
			be := e3fake.Behavior{Status: status, MarkerStatus: e3contract.MarkerFailed}
			if status != 200 {
				be.Normal = oracle.StateNo
			}
			f := newFixture(t)
			f.fake.Behaviors[key(e3contract.PurposeRead, e3contract.RoleCross, "note")] = be
			v := verdictOf(t, f.run(t), "note")
			if v.Value != oracle.Undecidable || !hasReason(v, e3contract.ReasonDataCheckFailed) {
				t.Fatalf("note: %+v", v)
			}
		})
	}
	// A(소유) 조회는 이 순서를 쓰지 않는다: A가 404면 표식 검사 결과와 상관없이 그대로 데이터 검사 실패이고(모순 사유가
	// 아님) B 조회를 보내지 않는다.
	for name, owner := range map[string]e3fake.Behavior{
		"failed":  {Status: 404, Normal: oracle.StateNo, MarkerStatus: e3contract.MarkerFailed},
		"present": {Status: 404, Normal: oracle.StateNo, Presence: e3contract.MarkerPresent},
	} {
		t.Run("owner_404_"+name, func(t *testing.T) {
			f := newFixture(t)
			f.fake.Behaviors[key(e3contract.PurposeRead, e3contract.RoleOwner, "note")] = owner
			res := f.run(t)
			if v := verdictOf(t, res, "note"); v.Value != oracle.Undecidable || !hasReason(v, e3contract.ReasonDataCheckFailed) {
				t.Fatalf("A 404: %+v", v)
			}
			if n := countCalls(f.fake.Calls(), func(k e3fake.Key) bool { return k == key(e3contract.PurposeRead, e3contract.RoleCross, "note") }); n != 0 {
				t.Fatalf("A 404 뒤 B 조회 %d번", n)
			}
		})
	}
}

// 회차 시작 때 서버가 거절한 결과는 요청 0건이고 결속·판정·후보·쓰기가 없다. 정해진 시작 거부 사유만 Valid다.
func TestRefusedAtRunStart(t *testing.T) {
	res := e3contract.RefusedAtRunStart(e3contract.RefuseResetConfirmationMissing)
	if res.Version != e3contract.RunResultVersion || res.Status != e3contract.StatusStartRefused || res.Reason != "reset_confirmation_missing" ||
		res.Requests != 0 || res.Binding != nil || res.Verdicts == nil || len(res.Verdicts) != 0 || res.Candidates == nil || len(res.Candidates) != 0 ||
		res.Writes == nil || len(res.Writes) != 0 || res.Skipped == nil || len(res.Skipped) != 0 {
		t.Fatalf("%+v", res)
	}
	for _, r := range []e3contract.StartRefusal{e3contract.RefuseResetConfirmationMissing, e3contract.RefusePreviousMarkersPresent,
		e3contract.RefuseResetStateUnavailable, e3contract.RefusePolicySourceUnavailable, e3contract.RefuseMarkerGenerationFailed,
		e3contract.RefuseMarkerRegistrationFailed, e3contract.RefuseTargetChanged} {
		if !r.Valid() {
			t.Fatalf("%s 거부", r)
		}
	}
	for _, r := range []e3contract.StartRefusal{"", "target_approval_changed", "reset_confirmation_missing "} {
		if r.Valid() {
			t.Fatalf("%q 받음", r)
		}
	}
}

// 사용자 결정 2026-11-20 (가): 신원 확인 직전에 A·B를 한 번씩 다시 로그인시킨다. 보낸 다시 로그인은 확정 몫(Requests)으로
// 세고, 보내지 못했거나 실패해도 멈추지 않는다(바로 뒤 신원 확인이 근거다). 확정 단계가 돌지 않는 회차는 다시 로그인도 없다.
func TestRunReloginsBeforeIdentity(t *testing.T) {
	type event struct{ kind, label string }
	for name, c := range map[string]struct {
		perAccount int
		requests   int
	}{
		"sent":     {1, 8 + 2},
		"not-sent": {0, 8},
		// 학습 로그인 재사용(로그인 화면 GET + 로그인 POST)은 계정마다 요청 둘을 센다.
		"learned-with-prep": {2, 8 + 4},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			var order []event
			deps := f.deps()
			deps.Relogin = func(_ context.Context, label string) int {
				// 다시 로그인 때는 아직 신원 요청이 없어야 한다.
				if n := countCalls(f.fake.Calls(), func(k e3fake.Key) bool { return true }); n != 0 {
					t.Errorf("다시 로그인 전에 확정 요청 %d건", n)
				}
				order = append(order, event{"relogin", label})
				return c.perAccount
			}
			res := e3contract.Run(context.Background(), deps, f.in)
			if res.Status != e3contract.StatusCompleted || res.Requests != c.requests {
				t.Fatalf("status=%s requests=%d", res.Status, res.Requests)
			}
			if len(order) != 2 || order[0] != (event{"relogin", "tester-a"}) || order[1] != (event{"relogin", "tester-b"}) {
				t.Fatalf("다시 로그인 순서: %+v", order)
			}
			for _, typ := range []string{"note", "secret"} {
				if v := verdictOf(t, res, typ); v.Value != oracle.Confirmed {
					t.Fatalf("%s: %+v", typ, v)
				}
			}
		})
	}
	t.Run("disabled-run-no-relogin", func(t *testing.T) {
		f := newFixture(t)
		f.in.Owner.Authenticated = false
		calls := 0
		deps := f.deps()
		deps.Relogin = func(context.Context, string) int { calls++; return 1 }
		res := e3contract.Run(context.Background(), deps, f.in)
		if res.Status != e3contract.StatusDisabled || calls != 0 || res.Requests != 0 {
			t.Fatalf("꺼진 회차에서 다시 로그인: %s calls=%d requests=%d", res.Status, calls, res.Requests)
		}
	})
	t.Run("no-admitted-type-no-relogin", func(t *testing.T) {
		f := newFixture(t)
		f.in.AdmittedTypes = 0
		calls := 0
		deps := f.deps()
		deps.Relogin = func(context.Context, string) int { calls++; return 1 }
		res := e3contract.Run(context.Background(), deps, f.in)
		if calls != 0 || res.Requests != 0 {
			t.Fatalf("몫에 든 종류가 없는데 다시 로그인: calls=%d requests=%d", calls, res.Requests)
		}
	})
	t.Run("stopped-context-no-relogin", func(t *testing.T) {
		f := newFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		deps := f.deps()
		deps.Started = cancel // 시작 직후 중지된 것처럼
		deps.Relogin = func(context.Context, string) int { calls++; return 1 }
		res := e3contract.Run(ctx, deps, f.in)
		noConfirmed(t, res)
		if calls != 0 {
			t.Fatalf("중지된 문맥에서 다시 로그인 %d건", calls)
		}
	})
}

// recheckTransport는 B 신원 요청을 센다. 두 번째 B 신원 요청(교차 거부 뒤 재확인)부터 recheck 동작으로 답한다.
type recheckTransport struct {
	*e3fake.Transport
	recheck e3fake.Behavior
	cross   int
}

func (r *recheckTransport) Identity(ctx context.Context, req e3contract.IdentityRequest) (e3contract.Observation, error) {
	if req.Role == e3contract.RoleCross {
		r.cross++
		if r.cross == 2 {
			r.Transport.Behaviors[key(e3contract.PurposeIdentity, e3contract.RoleCross, "")] = r.recheck
		}
	}
	return r.Transport.Identity(ctx, req)
}

// 사용자 결정 2026-11-20 S21 (나): 교차 조회가 거부(401·403·404)면 B 신원을 한 번 더 확인한다. 살아 있으면(처음과 같은
// 주체) data_no_match, 신원 응답이 401·403이면 세션 만료(product_session_expired), 그 밖에 확인하지 못하면 확인 못 함
// (product_cross_denial_unverified)이다. 뒤의 둘은 B 신원 미확인으로 UNDECIDABLE이고, 어느 경우든 CONFIRMED가 아니다.
// 재확인은 거부일 때만 보내고 확정 몫(종류당 1건)으로 센다.
func TestRunCrossDenialRechecksIdentity(t *testing.T) {
	for name, c := range map[string]struct {
		recheck e3fake.Behavior
		reason  e3contract.ProductReason
		sent    bool
	}{
		"alive":              {e3fake.Behavior{SubjectID: "user-b"}, "", true},
		"expired_401":        {e3fake.Behavior{Status: 401, Normal: oracle.StateNo}, e3contract.ReasonSessionExpired, true},
		"expired_403":        {e3fake.Behavior{Status: 403, Normal: oracle.StateNo}, e3contract.ReasonSessionExpired, true},
		"other_subject":      {e3fake.Behavior{SubjectID: "user-c"}, e3contract.ReasonCrossDenialUnverified, true},
		"no_subject":         {e3fake.Behavior{}, e3contract.ReasonCrossDenialUnverified, true},
		"server_error":       {e3fake.Behavior{Status: 500, Normal: oracle.StateNo}, e3contract.ReasonCrossDenialUnverified, true},
		"not_found_identity": {e3fake.Behavior{Status: 404, Normal: oracle.StateNo}, e3contract.ReasonCrossDenialUnverified, true},
		"incomplete":         {e3fake.Behavior{Incomplete: true}, e3contract.ReasonCrossDenialUnverified, true},
		// 상태 줄(401)만 받고 본문이 끊긴 재확인은 결과 불명이라 만료로 단정하지 않는다.
		"incomplete_401":  {e3fake.Behavior{Incomplete: true, Status: 401}, e3contract.ReasonCrossDenialUnverified, true},
		"timed_out":       {e3fake.Behavior{TimedOut: true, Incomplete: true}, e3contract.ReasonCrossDenialUnverified, true},
		"not_sent_policy": {e3fake.Behavior{NotSent: e3contract.NotSentPolicyDenied}, e3contract.ReasonCrossDenialUnverified, false},
		"not_sent_budget": {e3fake.Behavior{NotSent: e3contract.NotSentBudgetExhausted}, e3contract.ReasonCrossDenialUnverified, false},
		"result_unknown":  {e3fake.Behavior{Err: errors.New("결과 불명")}, e3contract.ReasonCrossDenialUnverified, true},
	} {
		for _, status := range []int{401, 403, 404} {
			t.Run(fmt.Sprintf("%s_%d", name, status), func(t *testing.T) {
				f := newFixture(t)
				f.in.AdmittedTypes = 1 // note 하나만(재확인 뒤 다른 종류 동작과 섞이지 않게)
				f.fake.Behaviors[key(e3contract.PurposeRead, e3contract.RoleCross, "note")] = e3fake.Behavior{Status: status,
					Normal: oracle.StateNo, Presence: e3contract.MarkerAbsent}
				tr := &recheckTransport{Transport: f.fake, recheck: c.recheck}
				deps := f.deps()
				deps.Transport = tr
				res := e3contract.Run(context.Background(), deps, f.in)
				noConfirmed(t, res)
				if tr.cross != 2 {
					t.Fatalf("B 신원 요청 %d건(처음 + 재확인 1건이어야 함)", tr.cross)
				}
				v := verdictOf(t, res, "note")
				if v.Value != oracle.Undecidable {
					t.Fatalf("note: %+v", v)
				}
				if c.reason == "" {
					if len(v.ProductReasons) != 0 || !slices.Contains(v.OracleReasons, oracle.ReasonDataNoMatch) ||
						v.Case.Facts.CrossNormalData != oracle.StateNo || v.IdentityCheck != oracle.StateYes {
						t.Fatalf("살아 있는 세션의 거부가 data_no_match가 아님: %+v", v)
					}
				} else {
					if !hasReason(v, c.reason) || len(v.ProductReasons) != 1 || slices.Contains(v.OracleReasons, oracle.ReasonDataNoMatch) ||
						v.IdentityCheck == oracle.StateYes || v.Case.Facts.CrossNormalData != oracle.StateUnknown {
						t.Fatalf("재확인 실패가 사유 %s로 남지 않음: %+v", c.reason, v)
					}
				}
				// 신원 2 + 생성 + A 조회 + B 조회 + 재확인(보냈으면 셈).
				want := 5
				if c.sent || c.recheck.NotSent != "" {
					want = 6
				}
				if res.Requests != want {
					t.Fatalf("요청 %d건, 원함 %d", res.Requests, want)
				}
			})
		}
	}
}

// 거부가 아닌 교차 응답(표식 없음·있음)에는 재확인을 보내지 않는다.
func TestRunCrossNotDeniedDoesNotRecheck(t *testing.T) {
	for name, be := range map[string]e3fake.Behavior{
		"visible": {},
		"absent":  {Presence: e3contract.MarkerAbsent},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.in.AdmittedTypes = 1
			f.fake.Behaviors[key(e3contract.PurposeRead, e3contract.RoleCross, "note")] = be
			tr := &recheckTransport{Transport: f.fake, recheck: e3fake.Behavior{SubjectID: "user-b"}}
			deps := f.deps()
			deps.Transport = tr
			res := e3contract.Run(context.Background(), deps, f.in)
			if tr.cross != 1 || res.Requests != 5 {
				t.Fatalf("거부 아닌 응답에 재확인: B 신원 %d건 요청 %d", tr.cross, res.Requests)
			}
		})
	}
}

// 재확인 관찰이 저널과 맞지 않으면 진위 실패다(판정 행 없음).
func TestRunCrossRecheckIntegrityFailure(t *testing.T) {
	f := newFixture(t)
	f.in.AdmittedTypes = 1
	f.fake.Behaviors[key(e3contract.PurposeRead, e3contract.RoleCross, "note")] = e3fake.Behavior{Status: 404, Normal: oracle.StateNo,
		Presence: e3contract.MarkerAbsent}
	tr := &recheckTransport{Transport: f.fake, recheck: e3fake.Behavior{SubjectID: "user-b"}}
	calls := 0
	f.fake.Tamper = func(_ e3fake.Call, o *e3contract.Observation) {
		if o.Purpose == e3contract.PurposeIdentity && o.Role == e3contract.RoleCross {
			calls++
			if calls == 2 {
				o.StatusCode = 418
			}
		}
	}
	deps := f.deps()
	deps.Transport = tr
	res := e3contract.Run(context.Background(), deps, f.in)
	integrityFailed(t, res, e3contract.IntegrityJournalEventMismatch)
}

// 사용자 결정 2026-11-20 분기점 2: 모델 단계가 E② 실행을 한 번도 잡지 않고 스스로 끝나면 요청 없이 남긴다. 꺼진 회차는 실제
// 사유, 켜진 회차는 e2_execution_unavailable이다(2026-11-22 F12: 후보 유무로 가르지 않는다). 인증 준비는 E② 실행을 잡을 때
// 하므로 실행이 없으면 인증 결과가 없다. 그것을 test_accounts_unavailable로 가리지 않는다. 라벨 형식 오류는 그대로 끈다.
func TestRunWithoutExecution(t *testing.T) {
	for name, c := range map[string]struct {
		prepare func(*fixture)
		status  e3contract.ConfirmationStatus
		reason  string
	}{
		"disabled-declaration": {func(f *fixture) { f.in.Binding.Policy = e3contract.PolicySource{} }, e3contract.StatusDisabled,
			string(e3contract.DisabledNoDeclaration)},
		"unauthenticated-without-auth-prep": {func(f *fixture) { f.in.Owner.Authenticated, f.in.Cross.Authenticated = false, false },
			e3contract.StatusStartRefused, string(e3contract.RefuseE2ExecutionUnavailable)},
		"disabled-same-labels": {func(f *fixture) { f.in.Cross.Label = f.in.Owner.Label }, e3contract.StatusDisabled,
			string(e3contract.DisabledAccountsUnverified)},
		"no-candidates": {func(f *fixture) { f.in.Candidates = nil }, e3contract.StatusStartRefused, string(e3contract.RefuseE2ExecutionUnavailable)},
		"candidates":    {func(*fixture) {}, e3contract.StatusStartRefused, string(e3contract.RefuseE2ExecutionUnavailable)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			c.prepare(f)
			started := 0
			deps := f.deps()
			deps.Started = func() { started++ }
			deps.Relogin = func(context.Context, string) int {
				t.Error("실행 없음 기록이 다시 로그인함")
				return 1
			}
			res := e3contract.RunWithoutExecution(context.Background(), deps, f.in)
			if res.Status != c.status || res.Reason != c.reason || res.Requests != 0 || len(res.Verdicts) != 0 || started != 0 ||
				len(f.fake.Calls()) != 0 || len(f.writes.Events()) != 0 {
				t.Fatalf("%s/%s 요청 %d 판정 %d 시작 %d 대상 %d", res.Status, res.Reason, res.Requests, len(res.Verdicts), started, len(f.fake.Calls()))
			}
			noConfirmed(t, res)
		})
	}
	if !e3contract.RefuseE2ExecutionUnavailable.Valid() {
		t.Fatal("e2_execution_unavailable이 정해진 시작 거부 사유가 아님")
	}
}

// 사용자 승인 모드에서 생성 요청의 승인이 확정 단계 시간 몫 끝과 같은 순간에 만료되면(승인 기한 = 시간 몫 끝), 그 생성은
// 보내지 않은 것으로 끝나야 한다: 판정 사유 product_create_approval_timeout, 쓰기 기록은 거둠(not_sent), 결과 쓰기 없음(리셋
// 확인 대상 아님). 끝난 문맥으로 저널을 대조하다 결과 불명·쓰기 unknown으로 남으면 안 된다(리허설 F15, 2026-11-21).
func TestRunCreateApprovalExpiryAtShareEnd(t *testing.T) {
	f := newFixture(t)
	note := key(e3contract.PurposeCreate, e3contract.RoleOwner, "note")
	f.fake.Behaviors[note] = e3fake.Behavior{NotSent: e3contract.NotSentApprovalTimeout}
	ctx := &flipDeadline{Context: context.Background(), done: make(chan struct{})}
	exp := expireOnCreate{Transport: f.fake, ctx: ctx, key: note}
	deps := f.deps()
	deps.Transport, deps.Journal = exp, exp
	res := e3contract.Run(ctx, deps, f.in)
	noConfirmed(t, res)
	if res.Status == e3contract.StatusIntegrityFailed {
		t.Fatalf("승인 만료를 진위 실패로: %+v", res)
	}
	if v := verdictOf(t, res, "note"); v.Value != oracle.Undecidable || !hasReason(v, e3contract.ReasonCreateApprovalTimeout) ||
		hasReason(v, e3contract.ReasonRequestResultUnknown) || hasReason(v, e3contract.ReasonConfirmationBudgetExhaust) {
		t.Fatalf("note: %+v", v)
	}
	withdrawn := 0
	for _, e := range f.writes.Events() {
		if e.Type == "note" && !e.Reserve {
			if e.Write.Outcome != e3contract.WriteNotSent {
				t.Fatalf("보내지 않은 생성의 쓰기 기록을 %s로 갱신", e.Write.Outcome)
			}
			withdrawn++
		}
	}
	if withdrawn != 1 {
		t.Fatalf("미리 남긴 쓰기 기록을 거두지 않음(거둠 %d건)", withdrawn)
	}
	for _, w := range res.Writes {
		if w.ResourceType == "note" {
			t.Fatalf("보내지 않은 생성이 결과 쓰기에 남음(리셋 확인 대상이 됨): %+v", w)
		}
	}
	// 시간 몫이 끝났으므로 다음 종류는 보내지 않고 확정 몫 끝으로 남는다.
	if v := verdictOf(t, res, "secret"); v.Value != oracle.Undecidable || !hasReason(v, e3contract.ReasonConfirmationBudgetExhaust) {
		t.Fatalf("secret: %+v", v)
	}
}

// expireOnCreate는 key 생성 요청의 거부를 돌려주는 순간 확정 단계 문맥의 마감을 지나게 한다(승인 기한 = 시간 몫 끝).
// 저널 대조는 실제 E²(e2live)처럼 끝난 문맥이면 그 오류를 돌려준다.
type expireOnCreate struct {
	*e3fake.Transport
	ctx *flipDeadline
	key e3fake.Key
}

func (e expireOnCreate) Create(ctx context.Context, req e3contract.CreateRequest) (e3contract.Observation, error) {
	obs, err := e.Transport.Create(ctx, req)
	if req.ResourceType == e.key.Type {
		e.ctx.expire()
	}
	return obs, err
}

func (e expireOnCreate) Sealed(ctx context.Context, b e3contract.RunBinding, ref e3contract.CallRef) (e3contract.Observation, error) {
	if err := ctx.Err(); err != nil {
		return e3contract.Observation{}, err
	}
	return e.Transport.Sealed(ctx, b, ref)
}
