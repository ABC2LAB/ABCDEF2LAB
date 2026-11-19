package e3contract_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3fake"
	"github.com/Autumn-27/artex/oracle"
)

// 요청 본문 칸 ID(create.id_field, 사용자 결정 2026-11-19). ID 출처는 응답 경로(id_path)와 요청 본문 칸(id_field) 중
// 하나만이다.
func idFieldDeclaration() e3contract.Declaration {
	d := declaration()
	for i := range d.Types {
		if d.Types[i].Sharing == e3contract.SharingOwnerOnly {
			d.Types[i].Create.IDPath, d.Types[i].Create.IDField = "", "title_id"
		}
	}
	return d
}

func TestDeclarationIDSource(t *testing.T) {
	if err := idFieldDeclaration().Validate(); err != nil {
		t.Fatalf("id_field 선언: %v", err)
	}
	cases := map[string]struct {
		edit  func(*e3contract.CreateSpec)
		field string
	}{
		"both":            {func(c *e3contract.CreateSpec) { c.IDPath = "id" }, "create.id_field"},
		"neither":         {func(c *e3contract.CreateSpec) { c.IDField = "" }, "create.id_path"},
		"same_as_marker":  {func(c *e3contract.CreateSpec) { c.IDField = c.MarkerField }, "create.id_field"},
		"under_marker":    {func(c *e3contract.CreateSpec) { c.IDField = c.MarkerField + ".id" }, "create.id_field"},
		"above_marker":    {func(c *e3contract.CreateSpec) { c.MarkerField, c.IDField = "meta.body", "meta" }, "create.id_field"},
		"bad_shape":       {func(c *e3contract.CreateSpec) { c.IDField = "a b" }, "create.id_field"},
		"already_in_body": {func(c *e3contract.CreateSpec) { c.BodyTemplate = `{"title":"t","title_id":"x"}` }, "create.body_template"},
		"form_dotted": {func(c *e3contract.CreateSpec) {
			c.Kind, c.BodyTemplate, c.IDField = e3contract.BodyForm, "title=t", "a.b"
		}, "create.id_field"},
		"form_already_body": {func(c *e3contract.CreateSpec) { c.Kind, c.BodyTemplate = e3contract.BodyForm, "title=t&title_id=x" }, "create.body_template"},
	}
	for name, c := range cases {
		d := idFieldDeclaration()
		c.edit(&d.Types[0].Create)
		if d.Types[0].Create.MarkerField == "meta.body" {
			d.Types[0].OwnerOnlyFields = []string{"meta.body"}
		}
		var fe *e3contract.DeclarationFieldError
		if err := d.Validate(); !errors.As(err, &fe) || fe.Field != "types[0]."+c.field {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// 응답 경로 ID 선언의 정본 바이트에는 id_field 칸이 없다(이미 저장한 선언의 digest가 바뀌지 않음).
	raw, err := declaration().CanonicalBytes()
	if err != nil || bytes.Contains(raw, []byte(`"id_field"`)) || !bytes.Contains(raw, []byte(`"id_path":"id"`)) {
		t.Fatalf("id_path 정본: %s %v", raw, err)
	}
	// 공유 종류의 빈 create는 칸을 넣기 전과 같은 모양이다(id_path 빈 값 포함, id_field 없음). 기존 선언 digest가 그대로다.
	if !bytes.Contains(raw, []byte(`"create":{"path":"","method":"","body_kind":"","body_template":"","marker_field":"","id_path":""}`)) {
		t.Fatalf("공유 종류 create 정본이 바뀜: %s", raw)
	}
	raw, err = idFieldDeclaration().CanonicalBytes()
	if err != nil || !bytes.Contains(raw, []byte(`"id_field":"title_id"`)) || bytes.Contains(raw, []byte(`"id_path":"id"`)) {
		t.Fatalf("id_field 정본: %s %v", raw, err)
	}
}

// markerReader는 표식 하나를 만들 바이트를 앞에 두고 그 뒤에 rest를 잇는다.
func markerFromChars(t *testing.T, chars string) e3contract.Marker {
	t.Helper()
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, len(chars))
	for i, c := range chars {
		b[i] = byte(strings.IndexRune(alphabet, c))
	}
	m, err := e3contract.NewMarker(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// 객체 ID는 표식과 다른 새 난수다. 표식에서 파생하지 않고(같은 표식이어도 난수가 다르면 다른 ID), 표식과 겹치면 만들지 않는다.
func TestNewObjectIDIndependentOfMarker(t *testing.T) {
	m := testMarker(t)
	a, err := e3contract.NewObjectID(bytes.NewReader(bytes.Repeat([]byte{1}, 16)), m)
	if err != nil || !strings.HasPrefix(a, e3contract.ObjectIDPrefix) || len(a) != len(e3contract.ObjectIDPrefix)+32 {
		t.Fatalf("ID 모양: %q %v", a, err)
	}
	b, _ := e3contract.NewObjectID(bytes.NewReader(bytes.Repeat([]byte{2}, 16)), m)
	if a == b {
		t.Fatal("같은 표식·다른 난수인데 같은 ID(표식에서 파생)")
	}
	if e3contract.ContainsMarker([]byte(a), m) || bytes.Contains(m.Reveal(), []byte(strings.TrimPrefix(a, e3contract.ObjectIDPrefix))) {
		t.Fatal("ID와 표식이 겹침")
	}
	// 난수가 우연히 표식과 같은 글자를 만들면 그 ID는 쓰지 않는다.
	hexMarker := "0123456789abcdef0123456789abcdef"
	clash := markerFromChars(t, hexMarker)
	raw := []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	if _, err := e3contract.NewObjectID(bytes.NewReader(raw), clash); !errors.Is(err, e3contract.ErrObjectID) {
		t.Fatalf("표식과 같은 ID를 만듦: %v", err)
	}
	if _, err := e3contract.NewObjectID(bytes.NewReader(nil), m); !errors.Is(err, e3contract.ErrObjectID) {
		t.Fatal("난수 실패에도 ID를 만듦")
	}
}

// 본문 조립: 틀의 값(수 원문 포함)은 그대로 두고 표식·ID 칸을 더한다. 조립한 본문은 출력·직렬화하면 가려지거나 오류다.
func TestAssembleBody(t *testing.T) {
	m := testMarker(t)
	spec := e3contract.CreateSpec{Kind: e3contract.BodyJSON, BodyTemplate: `{"n":12345678901234567890,"t":"a<b"}`,
		MarkerField: "meta.secret", IDField: "title_id"}
	body, err := e3contract.AssembleBody(spec, m, "e3id-0000")
	if err != nil || !body.Valid() || body.Kind() != e3contract.BodyJSON {
		t.Fatal(err)
	}
	var got map[string]any
	dec := json.NewDecoder(bytes.NewReader(body.Reveal()))
	dec.UseNumber()
	if dec.Decode(&got) != nil || got["n"].(json.Number).String() != "12345678901234567890" || got["t"] != "a<b" ||
		got["title_id"] != "e3id-0000" || got["meta"].(map[string]any)["secret"] != string(m.Reveal()) {
		t.Fatalf("조립 본문: %v", got)
	}
	form, err := e3contract.AssembleBody(e3contract.CreateSpec{Kind: e3contract.BodyForm, BodyTemplate: "a=1", MarkerField: "s",
		IDField: "id"}, m, "e3id-1")
	if err != nil || string(form.Reveal()) != "a=1&id=e3id-1&s="+string(m.Reveal()) {
		t.Fatalf("form 본문: %v", err)
	}
	for name, c := range map[string]struct {
		spec e3contract.CreateSpec
		id   string
	}{
		"id_without_field": {e3contract.CreateSpec{Kind: e3contract.BodyJSON, BodyTemplate: "{}", MarkerField: "s"}, "e3id-1"},
		"field_without_id": {e3contract.CreateSpec{Kind: e3contract.BodyJSON, BodyTemplate: "{}", MarkerField: "s", IDField: "i"}, ""},
		"field_exists":     {e3contract.CreateSpec{Kind: e3contract.BodyJSON, BodyTemplate: `{"s":1}`, MarkerField: "s"}, ""},
		"through_scalar":   {e3contract.CreateSpec{Kind: e3contract.BodyJSON, BodyTemplate: `{"a":1}`, MarkerField: "a.s"}, ""},
		"not_object":       {e3contract.CreateSpec{Kind: e3contract.BodyJSON, BodyTemplate: `[1]`, MarkerField: "s"}, ""},
	} {
		if _, err := e3contract.AssembleBody(c.spec, m, c.id); !errors.Is(err, e3contract.ErrBodyAssembly) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// 본문에는 표식이 들어 있어 표식처럼 막는다.
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "body", body)
	for _, out := range []string{fmt.Sprint(body), fmt.Sprintf("%+v %#v %q %x", body, body, body, body), buf.String()} {
		if e3contract.ContainsMarker([]byte(out), m) {
			t.Fatal("본문 출력에 표식")
		}
	}
	if _, err := json.Marshal(body); err == nil {
		t.Fatal("본문 JSON 직렬화가 됨")
	}
}

func idFieldFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	d := idFieldDeclaration()
	src, err := e3contract.Freeze(d, frozenRef)
	if err != nil {
		t.Fatal(err)
	}
	f.decls.Stored[frozenRef], f.in.Binding.Policy = d, src
	return f
}

// id_field 경로: ID를 보내기 전에 쓰기 기록에 남기고, 생성·조회가 같은 ID를 싣는다. ID는 보낸 조회 경로와 결과 쓰기에만
// 원문이 있고(저널·영수증·로그에는 없음), 표식은 어디에도 없다.
func TestRunIDFieldConfirms(t *testing.T) {
	f := idFieldFixture(t)
	res := f.run(t)
	if res.Status != e3contract.StatusCompleted || len(res.Verdicts) != 2 {
		t.Fatalf("%+v", res)
	}
	ids := map[string]string{}
	for _, c := range f.fake.Calls() {
		if c.Key.Purpose == e3contract.PurposeIdentity {
			continue
		}
		if !strings.HasPrefix(c.ObjectID, e3contract.ObjectIDPrefix) {
			t.Fatalf("ID 없는 요청: %+v", c)
		}
		if prev, ok := ids[c.Key.Type]; ok && prev != c.ObjectID {
			t.Fatalf("%s 생성·조회 ID가 다름", c.Key.Type)
		}
		ids[c.Key.Type] = c.ObjectID
		if c.Key.Purpose == e3contract.PurposeRead && !strings.Contains(c.Path, c.ObjectID) {
			t.Fatalf("조회 경로에 ID 없음: %s", c.Path)
		}
	}
	if len(ids) != 2 || ids["note"] == ids["secret"] {
		t.Fatalf("종류마다 다른 ID여야 함: %v", ids)
	}
	for _, v := range res.Verdicts {
		if v.Value != oracle.Confirmed {
			t.Fatalf("id_field 정상 대상이 확정되지 않음: %+v", v)
		}
	}
	// 예약 때 이미 ID가 있고, 결과 쓰기에도 같은 ID가 남는다.
	for _, e := range f.writes.Events() {
		if e.Reserve && e.ObjectID != ids[e.Type] {
			t.Fatalf("예약 ID: %+v", e)
		}
	}
	for _, w := range res.Writes {
		if w.ObjectID != ids[w.ResourceType] || w.Outcome != e3contract.WriteCreated {
			t.Fatalf("결과 쓰기: %+v", w)
		}
	}
	m, err := e3contract.NewMarker(bytes.NewReader(seed()))
	if err != nil {
		t.Fatal(err)
	}
	sinks := f.fake.Sinks()
	for _, id := range ids {
		if e3contract.ContainsMarker([]byte(id), m) {
			t.Fatal("ID에 표식")
		}
		// v3 이벤트·영수증·공유 로그에는 ID 원문도 넣지 않는다(digest만, v3 계약 §2.2). 원문은 보낸 경로와 결과 쓰기에만 있다.
		// 승인 미리보기는 보낼 본문을 그대로(표식만 가려) 보여 주므로 생성 본문의 ID가 보인다(계약 §4).
		for name, data := range sinks {
			if name != "approval_preview" && bytes.Contains(data, []byte(id)) {
				t.Fatalf("%s에 ID 원문", name)
			}
		}
	}
	for name, data := range sinks {
		if e3contract.ContainsMarker(data, m) {
			t.Fatalf("%s에 표식", name)
		}
	}
	raw, _ := json.Marshal(res)
	if e3contract.ContainsMarker(raw, m) {
		t.Fatal("결과 파일에 표식")
	}
	// 회차마다 새 ID: 난수가 다르면 다른 ID다.
	g := idFieldFixture(t)
	deps := g.deps()
	deps.Random = bytes.NewReader(append(append([]byte(nil), seed()[:64]...), bytes.Repeat([]byte{7}, 512)...))
	e3contract.Run(t.Context(), deps, g.in)
	for _, c := range g.fake.Calls() {
		if c.Key.Purpose == e3contract.PurposeCreate && c.ObjectID == ids[c.Key.Type] {
			t.Fatal("다른 회차가 같은 ID")
		}
	}
}

// 생성이 ID 충돌 등으로 실패하면 그 종류는 UNDECIDABLE(product_create_failed)이고 다른 ID로 다시 보내지 않는다. 쓰기
// 기록은 그 ID와 함께 남는다(결과 불명이어도 ID로 조회해 볼 수 있음).
func TestRunIDFieldCreateFailureNoRetry(t *testing.T) {
	create := key(e3contract.PurposeCreate, e3contract.RoleOwner, "note")
	for name, c := range map[string]struct {
		be      e3fake.Behavior
		outcome e3contract.WriteOutcome
		reason  e3contract.ProductReason
	}{
		"rejected": {e3fake.Behavior{Status: 400, Normal: oracle.StateNo}, e3contract.WriteUnknown, e3contract.ReasonCreateFailed},
		"timeout":  {e3fake.Behavior{TimedOut: true}, e3contract.WriteUnknown, e3contract.ReasonRequestTimeout},
	} {
		t.Run(name, func(t *testing.T) {
			f := idFieldFixture(t)
			f.fake.Behaviors[create] = c.be
			res := f.run(t)
			// 실패한 종류만 본다(다른 종류는 정상 대상이라 확정될 수 있음).
			if v := verdictOf(t, res, "note"); v.Value != oracle.Undecidable || !hasReason(v, c.reason) {
				t.Fatalf("note: %+v", v)
			}
			var creates []e3fake.Call
			for _, call := range f.fake.Calls() {
				if call.Key == create {
					creates = append(creates, call)
				}
			}
			if len(creates) != 1 {
				t.Fatalf("생성 %d번(다시 보냄)", len(creates))
			}
			var note []e3contract.ConfirmationWrite
			for _, w := range res.Writes {
				if w.ResourceType == "note" {
					note = append(note, w)
				}
			}
			if len(note) != 1 || note[0].Outcome != c.outcome || note[0].ObjectID != creates[0].ObjectID {
				t.Fatalf("쓰기: %+v", res.Writes)
			}
		})
	}
	// 보내지 않은 것이 확실하면 ID와 함께 거둔다.
	f := idFieldFixture(t)
	f.fake.Behaviors[create] = e3fake.Behavior{NotSent: e3contract.NotSentPolicyDenied}
	f.run(t)
	var settled []e3fake.WriteEvent
	for _, e := range f.writes.Events() {
		if e.Type == "note" && !e.Reserve {
			settled = append(settled, e)
		}
	}
	if len(settled) != 1 || settled[0].Write.Outcome != e3contract.WriteNotSent || !strings.HasPrefix(settled[0].Write.ObjectID, e3contract.ObjectIDPrefix) {
		t.Fatalf("거둠: %+v", settled)
	}
}

// ID를 만들 난수가 없으면 생성 요청을 만들지 않고(예약도 없음) 그 종류는 UNDECIDABLE이다.
func TestRunIDFieldRandomFailure(t *testing.T) {
	f := idFieldFixture(t)
	deps := f.deps()
	deps.Random = bytes.NewReader(seed()[:64]) // 표식만 만들 수 있는 길이
	res := e3contract.Run(t.Context(), deps, f.in)
	noConfirmed(t, res)
	for _, typ := range []string{"note", "secret"} {
		if v := verdictOf(t, res, typ); v.Value != oracle.Undecidable || !hasReason(v, e3contract.ReasonCreateRequestUnavailable) {
			t.Fatalf("%s: %+v", typ, v)
		}
	}
	for _, c := range f.fake.Calls() {
		if c.Key.Purpose == e3contract.PurposeCreate {
			t.Fatal("ID 없이 생성 요청")
		}
	}
	if len(f.writes.Events()) != 0 {
		t.Fatal("요청을 만들지 못했는데 쓰기 예약")
	}
}

// Deps.Random이 없으면 crypto/rand가 기본이다(제품 생성자는 두지 않는다). 표식에 쓴 기본 난수원으로 ID도 만들어 확정까지
// 간다 — ID 생성에 nil을 넘겨 생성 요청 0건·product_create_request_unavailable로 끝나던 것(#766 이동찬 리뷰)의 회귀 시험.
func TestRunIDFieldDefaultRandom(t *testing.T) {
	f := idFieldFixture(t)
	deps := f.deps()
	deps.Random = nil
	res := e3contract.Run(t.Context(), deps, f.in)
	if res.Status != e3contract.StatusCompleted {
		t.Fatalf("%+v", res)
	}
	for _, typ := range []string{"note", "secret"} {
		if v := verdictOf(t, res, typ); v.Value != oracle.Confirmed {
			t.Fatalf("%s: %+v", typ, v)
		}
	}
	ids := map[string]string{}
	for _, c := range f.fake.Calls() {
		if c.Key.Purpose != e3contract.PurposeCreate {
			continue
		}
		if !strings.HasPrefix(c.ObjectID, e3contract.ObjectIDPrefix) || ids[c.Key.Type] != "" {
			t.Fatalf("생성 요청 ID: %+v", c)
		}
		ids[c.Key.Type] = c.ObjectID
	}
	if len(ids) != 2 || ids["note"] == ids["secret"] {
		t.Fatalf("종류마다 새 ID: %v", ids)
	}
}
