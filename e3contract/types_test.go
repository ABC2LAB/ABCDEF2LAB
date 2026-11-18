package e3contract_test

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/oracle"
)

func TestMarkerNeverSerializes(t *testing.T) {
	m := testMarker(t)
	raw := m.Reveal()
	if len(raw) != e3contract.MarkerLength || !m.Valid() {
		t.Fatalf("표식 모양: %d", len(raw))
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X"} {
		out := fmt.Sprintf(verb, m)
		if strings.Contains(out, string(raw)) || !strings.Contains(out, "[REDACTED]") {
			t.Fatalf("%s 출력이 표식을 드러냄", verb)
		}
		wrapped := fmt.Sprintf(verb, struct{ M e3contract.Marker }{m})
		if bytes.Contains(bytes.ToLower([]byte(wrapped)), raw) {
			t.Fatalf("%s 구조체 출력이 표식을 드러냄", verb)
		}
	}
	if _, err := json.Marshal(struct{ M e3contract.Marker }{m}); !errors.Is(err, e3contract.ErrMarkerSerialization) {
		t.Fatalf("JSON: %v", err)
	}
	if _, err := m.MarshalText(); !errors.Is(err, e3contract.ErrMarkerSerialization) {
		t.Fatalf("Text: %v", err)
	}
	if err := gob.NewEncoder(&bytes.Buffer{}).Encode(m); err == nil {
		t.Fatal("gob이 표식을 썼음")
	}
	var back e3contract.Marker
	if err := json.Unmarshal([]byte(`"abc"`), &back); !errors.Is(err, e3contract.ErrMarkerSerialization) {
		t.Fatalf("JSON 읽기: %v", err)
	}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("x", "marker", m)
	if bytes.Contains(logs.Bytes(), raw) || !bytes.Contains(logs.Bytes(), []byte("[REDACTED]")) {
		t.Fatalf("slog: %s", logs.Bytes())
	}
	// Reveal은 사본이다.
	raw[0] ^= 1
	if !m.Valid() || bytes.Equal(m.Reveal(), raw) {
		t.Fatal("Reveal이 내부 값을 내줌")
	}
}

func TestNewMarkerShape(t *testing.T) {
	if _, err := e3contract.NewMarker(nil); !errors.Is(err, e3contract.ErrMarkerInvalid) {
		t.Fatalf("nil: %v", err)
	}
	if _, err := e3contract.NewMarker(bytes.NewReader(make([]byte, 10))); !errors.Is(err, e3contract.ErrMarkerInvalid) {
		t.Fatalf("짧은 난수: %v", err)
	}
	// 252 이상은 버린다: 전부 255면 끝까지 못 만든다.
	if _, err := e3contract.NewMarker(bytes.NewReader(bytes.Repeat([]byte{255}, 4096))); err == nil {
		t.Fatal("쏠린 난수를 받음")
	}
	m, err := e3contract.NewMarker(bytes.NewReader(append(bytes.Repeat([]byte{252}, 40), bytes.Repeat([]byte{37}, 64)...)))
	if err != nil {
		t.Fatal(err)
	}
	if string(m.Reveal()) != strings.Repeat("b", e3contract.MarkerLength) {
		t.Fatalf("거부 표집이 틀림: %q", m.Reveal())
	}
	if (e3contract.Marker{}).Valid() {
		t.Fatal("빈 표식이 유효")
	}
}

func TestDeclarationValidate(t *testing.T) {
	if err := declaration().Validate(); err != nil {
		t.Fatalf("기본 선언: %v", err)
	}
	cases := map[string]func(*e3contract.Declaration){
		"version":          func(d *e3contract.Declaration) { d.Version = "v0" },
		"no_types":         func(d *e3contract.Declaration) { d.Types = nil },
		"order":            func(d *e3contract.Declaration) { d.Types[0], d.Types[2] = d.Types[2], d.Types[0] },
		"dup":              func(d *e3contract.Declaration) { d.Types[2].Type = "note" },
		"declared_by":      func(d *e3contract.Declaration) { d.DeclaredBy = "" },
		"declared_at":      func(d *e3contract.Declaration) { d.DeclaredAt = time.Time{} },
		"identity_path":    func(d *e3contract.Declaration) { d.Identity.Path = "/api/me?x=1" },
		"marker_not_owner": func(d *e3contract.Declaration) { d.Types[0].Create.MarkerField = "title" },
		"no_owner_fields":  func(d *e3contract.Declaration) { d.Types[0].OwnerOnlyFields = nil },
		"create_get":       func(d *e3contract.Declaration) { d.Types[0].Create.Method = "GET" },
		"create_delete":    func(d *e3contract.Declaration) { d.Types[0].Create.Method = "DELETE" },
		"create_dotdot":    func(d *e3contract.Declaration) { d.Types[0].Create.Path = "/api/../notes" },
		"create_template":  func(d *e3contract.Declaration) { d.Types[0].Create.Path = "/api/{id}" },
		"body_not_object":  func(d *e3contract.Declaration) { d.Types[0].Create.BodyTemplate = `[1]` },
		"body_has_marker":  func(d *e3contract.Declaration) { d.Types[0].Create.BodyTemplate = `{"body":"x"}` },
		"read_no_id":       func(d *e3contract.Declaration) { d.Types[0].Read.PathTemplate = "/api/notes" },
		"read_two_ids":     func(d *e3contract.Declaration) { d.Types[0].Read.PathTemplate = "/api/{id}/{id}" },
		"read_other_brace": func(d *e3contract.Declaration) { d.Types[0].Read.PathTemplate = "/api/{x}/{id}" },
		"status_half":      func(d *e3contract.Declaration) { d.Types[0].Read.StatusPath = "status" },
		"shared_requests":  func(d *e3contract.Declaration) { d.Types[1].Create.Path = "/api/posts" },
		"sharing":          func(d *e3contract.Declaration) { d.Types[0].Sharing = "public" },
		"form_dotted": func(d *e3contract.Declaration) {
			d.Types[0].Create.Kind = e3contract.BodyForm
			d.Types[0].Create.BodyTemplate = "a=1"
			d.Types[0].Create.MarkerField = "a.b"
			d.Types[0].OwnerOnlyFields = []string{"a.b"}
		},
		"form_has_marker": func(d *e3contract.Declaration) {
			d.Types[0].Create.Kind = e3contract.BodyForm
			d.Types[0].Create.BodyTemplate = "body=1"
		},
		"body_kind": func(d *e3contract.Declaration) { d.Types[0].Create.Kind = "xml" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			d := declaration()
			edit(&d)
			if err := d.Validate(); !errors.Is(err, e3contract.ErrDeclaration) {
				t.Fatalf("받아들임: %v", err)
			}
			if _, err := e3contract.Freeze(d, frozenRef); err == nil {
				t.Fatal("틀린 선언을 동결함")
			}
		})
	}
	ok := declaration()
	ok.Types[0].Create.Kind, ok.Types[0].Create.BodyTemplate = e3contract.BodyForm, "title=t"
	ok.Types[0].Read.StatusPath, ok.Types[0].Read.NormalValue = "owner", "tester-a"
	if err := ok.Validate(); err != nil {
		t.Fatalf("form·상태 칸 선언: %v", err)
	}
}

func TestDeclarationDigest(t *testing.T) {
	base, err := declaration().Digest()
	if err != nil || len(base) != 64 {
		t.Fatalf("%q %v", base, err)
	}
	same := declaration()
	same.DeclaredAt = same.DeclaredAt.In(time.FixedZone("KST", 9*3600))
	same.Types[1].OwnerOnlyFields = []string{}
	if d, _ := same.Digest(); d != base {
		t.Fatal("같은 내용(시간대·빈 목록)이 다른 digest")
	}
	changed := declaration()
	changed.Types[0].OwnerOnlyFields = []string{"body", "secret"}
	if d, _ := changed.Digest(); d == base {
		t.Fatal("다른 내용이 같은 digest")
	}
	src, err := e3contract.Freeze(declaration(), frozenRef)
	if err != nil || src.Kind != e3contract.PolicyProductDeclaration || src.DigestSHA256 != base ||
		src.TargetApprovalRevision != "approval-3" {
		t.Fatalf("%+v %v", src, err)
	}
	if err := e3contract.VerifyFrozen(src, declaration()); err != nil {
		t.Fatalf("VerifyFrozen: %v", err)
	}
	if err := e3contract.VerifyFrozen(src, changed); !errors.Is(err, e3contract.ErrPolicyDigestMismatch) {
		t.Fatalf("다른 동결본: %v", err)
	}
	kg := src
	kg.Kind = e3contract.PolicyKGSnapshot
	if err := e3contract.VerifyFrozen(kg, declaration()); !errors.Is(err, e3contract.ErrProductPolicyKind) {
		t.Fatalf("kg_snapshot 동결본: %v", err)
	}
	if err := kg.Validate(); err != nil {
		t.Fatalf("kg_snapshot 모양 자체는 유효해야 함(평가 경로): %v", err)
	}
	for name, p := range map[string]e3contract.PolicySource{
		"kind":   {Kind: "other", Ref: "r", DigestSHA256: base, TargetApprovalRevision: "a"},
		"digest": {Kind: e3contract.PolicyProductDeclaration, Ref: "r", DigestSHA256: strings.ToUpper(base), TargetApprovalRevision: "a"},
		"ref":    {Kind: e3contract.PolicyProductDeclaration, Ref: "", DigestSHA256: base, TargetApprovalRevision: "a"},
		"rev":    {Kind: e3contract.PolicyProductDeclaration, Ref: "r", DigestSHA256: base},
	} {
		if err := p.Validate(); !errors.Is(err, e3contract.ErrPolicySource) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func validRead(t *testing.T) e3contract.Observation {
	t.Helper()
	f := newFixture(t)
	return e3contract.Observation{Binding: f.in.Binding, Purpose: e3contract.PurposeRead, Role: e3contract.RoleCross,
		AccountLabel: "tester-b", ResourceType: "note", Ref: e3contract.CallRef{CallRef: "c-2", AttemptID: "a-2", Sequence: 2},
		Sent: true, ResponseComplete: true, StatusCode: 200, Normal: oracle.StateYes, RequestMarkerFree: true,
		Marker:           e3contract.MarkerCheck{Status: e3contract.MarkerComplete, Presence: e3contract.MarkerPresent, Method: e3contract.MatchDecodedJSON, Field: "body"},
		BodyDigestSHA256: strings.Repeat("b", 64), BodyComplete: true,
		Create: e3contract.CallRef{CallRef: "c-1", AttemptID: "a-1", Sequence: 1}, At: t0}
}

func TestObservationValidate(t *testing.T) {
	if err := validRead(t).Validate(); err != nil {
		t.Fatalf("기본 관찰: %v", err)
	}
	cases := map[string]func(*e3contract.Observation){
		"binding":         func(o *e3contract.Observation) { o.Binding.RunRef = "" },
		"role":            func(o *e3contract.Observation) { o.Role = "admin" },
		"label":           func(o *e3contract.Observation) { o.AccountLabel = "" },
		"ref_seq":         func(o *e3contract.Observation) { o.Ref.Sequence = 0 },
		"at":              func(o *e3contract.Observation) { o.At = time.Time{} },
		"purpose":         func(o *e3contract.Observation) { o.Purpose = "auth_prep" },
		"status_code":     func(o *e3contract.Observation) { o.StatusCode = 0 },
		"normal":          func(o *e3contract.Observation) { o.Normal = "" },
		"body_digest":     func(o *e3contract.Observation) { o.BodyDigestSHA256 = "" },
		"not_sent_reason": func(o *e3contract.Observation) { *o = notSent(*o, "") },
		"not_sent_result": func(o *e3contract.Observation) { *o = notSent(*o, e3contract.NotSentPolicyDenied); o.StatusCode = 200 },
		"not_sent_free": func(o *e3contract.Observation) {
			*o = notSent(*o, e3contract.NotSentPolicyDenied)
			o.RequestMarkerFree = true
		},
		"sent_and_reason": func(o *e3contract.Observation) { o.NotSent = e3contract.NotSentApprovalDenied },
		"unknown_result":  func(o *e3contract.Observation) { o.ResponseComplete = false },
		"absent_partial": func(o *e3contract.Observation) {
			o.Marker = e3contract.MarkerCheck{Status: e3contract.MarkerFailed, Presence: e3contract.MarkerAbsent, Field: "body"}
		},
		"complete_no_pres": func(o *e3contract.Observation) { o.Marker.Presence = "" },
		"partial_body":     func(o *e3contract.Observation) { o.BodyComplete = false },
		"method_absent":    func(o *e3contract.Observation) { o.Marker.Presence = e3contract.MarkerAbsent },
		"method_unknown":   func(o *e3contract.Observation) { o.Marker.Method = "guess" },
		"field":            func(o *e3contract.Observation) { o.Marker.Field = "" },
		"read_create_ref":  func(o *e3contract.Observation) { o.Create = e3contract.CallRef{} },
		"read_object_id":   func(o *e3contract.Observation) { o.ObjectIDDigest = strings.Repeat("c", 64) },
		"read_subject":     func(o *e3contract.Observation) { o.SubjectDigest = strings.Repeat("c", 64) },
		"create_cross":     func(o *e3contract.Observation) { *o = asCreate(*o); o.Role = e3contract.RoleCross },
		"create_marker":    func(o *e3contract.Observation) { *o = asCreate(*o); o.Marker.Status = e3contract.MarkerComplete },
		"create_id_shape":  func(o *e3contract.Observation) { *o = asCreate(*o); o.ObjectIDDigest = "abc" },
		"identity_type":    func(o *e3contract.Observation) { *o = asIdentity(*o); o.ResourceType = "note" },
		"identity_digest":  func(o *e3contract.Observation) { *o = asIdentity(*o); o.SubjectDigest = "abc" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			o := validRead(t)
			edit(&o)
			if err := o.Validate(); !errors.Is(err, e3contract.ErrObservation) {
				t.Fatalf("받아들임: %v", err)
			}
		})
	}
	for name, o := range map[string]e3contract.Observation{
		"create":   asCreate(validRead(t)),
		"identity": asIdentity(validRead(t)),
		"not_sent": notSent(validRead(t), e3contract.NotSentApprovalTimeout),
		"partial":  withMarker(validRead(t), e3contract.MarkerCheck{Status: e3contract.MarkerPartial, Field: "body"}, false),
		"absent":   withMarker(validRead(t), e3contract.MarkerCheck{Status: e3contract.MarkerComplete, Presence: e3contract.MarkerAbsent, Field: "body"}, true),
	} {
		if err := o.Validate(); err != nil {
			t.Fatalf("%s 유효 관찰을 거부: %v", name, err)
		}
	}
}

func notSent(o e3contract.Observation, reason e3contract.NotSentReason) e3contract.Observation {
	return e3contract.Observation{Binding: o.Binding, Purpose: o.Purpose, Role: o.Role, AccountLabel: o.AccountLabel,
		ResourceType: o.ResourceType, Ref: o.Ref, Create: o.Create, NotSent: reason, At: o.At}
}

func asCreate(o e3contract.Observation) e3contract.Observation {
	o.Purpose, o.Role, o.AccountLabel = e3contract.PurposeCreate, e3contract.RoleOwner, "tester-a"
	o.Marker, o.Create, o.RequestMarkerFree = e3contract.MarkerCheck{}, e3contract.CallRef{}, false
	o.ObjectIDDigest = strings.Repeat("d", 64)
	return o
}

func asIdentity(o e3contract.Observation) e3contract.Observation {
	o.Purpose, o.ResourceType = e3contract.PurposeIdentity, ""
	o.Marker, o.Create = e3contract.MarkerCheck{}, e3contract.CallRef{}
	o.SubjectDigest = strings.Repeat("e", 64)
	return o
}

func withMarker(o e3contract.Observation, m e3contract.MarkerCheck, fullBody bool) e3contract.Observation {
	o.Marker, o.BodyComplete = m, fullBody
	return o
}

func TestObservationSameComparesInstant(t *testing.T) {
	a := validRead(t)
	b := a
	b.At = a.At.In(time.FixedZone("KST", 9*3600))
	if !a.Same(b) {
		t.Fatal("같은 순간을 다르게 봄")
	}
	b.At = a.At.Add(time.Microsecond)
	if a.Same(b) {
		t.Fatal("다른 순간을 같게 봄")
	}
	c := a
	c.Marker.Method = e3contract.MatchExact
	if a.Same(c) {
		t.Fatal("다른 칸을 같게 봄")
	}
}

func TestCheckRunStartRejectsNilGate(t *testing.T) {
	f := newFixture(t)
	if got := e3contract.CheckRunStart(t.Context(), nil, f.in.Binding, "target-1"); got != e3contract.RefuseResetStateUnavailable {
		t.Fatalf("%q", got)
	}
}

func TestDeclarationFieldErrorNamesFieldOnly(t *testing.T) {
	cases := map[string]struct {
		edit  func(*e3contract.Declaration)
		field string
	}{
		"create_path":   {func(d *e3contract.Declaration) { d.Types[2].Create.Path = "/api/x?secret-value" }, "types[2].create.path"},
		"read_template": {func(d *e3contract.Declaration) { d.Types[0].Read.PathTemplate = "/api/notes" }, "types[0].read.path_template"},
		"normal_ctrl":   {func(d *e3contract.Declaration) { d.Types[0].Read.StatusPath, d.Types[0].Read.NormalValue = "s", "a\nb" }, "types[0].read.normal_value"},
		"normal_long": {func(d *e3contract.Declaration) {
			d.Types[0].Read.StatusPath, d.Types[0].Read.NormalValue = "s", strings.Repeat("v", 129)
		}, "types[0].read.normal_value"},
		"owner_field": {func(d *e3contract.Declaration) { d.Types[0].OwnerOnlyFields = []string{"body", "bad field"} }, "types[0].owner_only_fields[1]"},
		"too_many": {func(d *e3contract.Declaration) {
			d.Types = append(d.Types, manyTypes(e3contract.MaxDeclarationTypes)...)
		}, "types"},
		"template_long": {func(d *e3contract.Declaration) {
			d.Types[0].Create.BodyTemplate = `{"t":"` + strings.Repeat("x", e3contract.MaxBodyTemplateBytes) + `"}`
		}, "types[0].create.body_template"},
		"identity": {func(d *e3contract.Declaration) { d.Identity.SubjectIDPath = "" }, "identity.subject_id_path"},
		"size": {func(d *e3contract.Declaration) {
			d.Types = manyTypes(e3contract.MaxDeclarationTypes)
			for i := range d.Types {
				d.Types[i].Create.BodyTemplate = `{"t":"` + strings.Repeat("<", e3contract.MaxBodyTemplateBytes-16) + `"}`
			}
		}, "size"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d := declaration()
			c.edit(&d)
			var fe *e3contract.DeclarationFieldError
			err := d.Validate()
			if !errors.As(err, &fe) || fe.Field != c.field || !errors.Is(err, e3contract.ErrDeclaration) {
				t.Fatalf("칸 %v, 기대 %s", err, c.field)
			}
			if strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "<<<") {
				t.Fatal("오류에 입력 값이 들어감")
			}
		})
	}
}

func manyTypes(n int) []e3contract.TypeDeclaration {
	out := make([]e3contract.TypeDeclaration, n)
	for i := range out {
		t := declaration().Types[0]
		t.Type = fmt.Sprintf("t%02d", i)
		out[i] = t
	}
	return out
}

func TestNormalizeSortsWithoutMutating(t *testing.T) {
	d := declaration()
	d.Types[0], d.Types[2] = d.Types[2], d.Types[0]
	d.Types[0].OwnerOnlyFields = []string{"zeta", "body"}
	if err := d.Validate(); err == nil {
		t.Fatal("정렬 전 선언을 받음")
	}
	n := e3contract.Normalize(d)
	if err := n.Validate(); err != nil {
		t.Fatalf("정렬 뒤: %v", err)
	}
	if d.Types[0].Type != "secret" || d.Types[0].OwnerOnlyFields[0] != "zeta" {
		t.Fatal("원본이 바뀜")
	}
	if n.Types[0].Type != "note" || n.Types[2].Type != "secret" || n.Types[2].OwnerOnlyFields[0] != "body" {
		t.Fatalf("정렬 결과: %+v", n.Types)
	}
	dup := declaration()
	dup.Types[2].Type = "note"
	if err := e3contract.Normalize(dup).Validate(); err == nil {
		t.Fatal("정렬이 중복을 숨김")
	}
}

func TestCredentialField(t *testing.T) {
	if f := e3contract.CredentialField(declaration()); f != "" {
		t.Fatalf("기본 선언에서 자격 모양: %s", f)
	}
	const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJlLXZhbHVl"
	cases := map[string]struct {
		edit  func(*e3contract.Declaration)
		field string
	}{
		"json_password": {func(d *e3contract.Declaration) { d.Types[0].Create.BodyTemplate = `{"password":"hunter2-value"}` }, "types[0].create.body_template"},
		"json_token":    {func(d *e3contract.Declaration) { d.Types[2].Create.BodyTemplate = `{"title":"t","api_key":"k-value"}` }, "types[2].create.body_template"},
		"form_session":  {func(d *e3contract.Declaration) { d.Types[0].Create.BodyTemplate = "title=t&session_id=abc-value" }, "types[0].create.body_template"},
		"bearer":        {func(d *e3contract.Declaration) { d.Types[0].Create.BodyTemplate = `{"note":"Bearer abc.def-value"}` }, "types[0].create.body_template"},
		"jwt_in_value":  {func(d *e3contract.Declaration) { d.Types[0].Read.StatusPath, d.Types[0].Read.NormalValue = "s", jwt }, "types[0].read.normal_value"},
		"cookie_header": {func(d *e3contract.Declaration) { d.Types[0].Create.BodyTemplate = `{"h":"Cookie: sid=abc-value"}` }, "types[0].create.body_template"},
		"authorization": {func(d *e3contract.Declaration) {
			d.Types[0].Read.StatusPath, d.Types[0].Read.NormalValue = "s", "Authorization: Basic abc"
		}, "types[0].read.normal_value"},
		"normal_contains": {func(d *e3contract.Declaration) {
			d.Types[2].Read.StatusPath, d.Types[2].Read.NormalValue = "s", "token=abc-value"
		}, "types[2].read.normal_value"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d := declaration()
			c.edit(&d)
			if f := e3contract.CredentialField(d); f != c.field {
				t.Fatalf("칸 %q, 기대 %q", f, c.field)
			}
		})
	}
}
