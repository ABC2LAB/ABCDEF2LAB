package responseevidence

import (
	"reflect"
	"testing"
)

func jsonEv(body string) Evidence { return Extract(complete("application/json", body), Limits{}, nil) }

func kinds(c Comparison) []string {
	var out []string
	for _, d := range c.Differences {
		out = append(out, d.Kind+":"+d.Path)
	}
	return out
}

func TestCompareEquivalent(t *testing.T) {
	c := Compare(jsonEv(`{"a":1,"b":["x"]}`), jsonEv(`{"b":["x"],"a":1}`), RoleOriginal, RoleReproduction)
	if c.Relation != RelationEquivalent || c.Similarity == nil || *c.Similarity != 100 || len(c.Differences) != 0 {
		t.Fatalf("equivalent: %+v", c)
	}
}

func TestCompareRootJSONScalars(t *testing.T) {
	for _, pair := range [][2]string{{"true", "false"}, {`"allow"`, `"deny"`}, {"1", "2"}, {"null", "true"}} {
		left, right := jsonEv(pair[0]), jsonEv(pair[1])
		if Validate(left) != nil || Validate(right) != nil {
			t.Fatalf("root scalar evidence invalid: %+v / %+v", left, right)
		}
		if c := Compare(left, right, RoleOriginal, RoleReproduction); c.Relation != RelationDifferent {
			t.Fatalf("root scalar difference disappeared for %s -> %s: %+v", pair[0], pair[1], c)
		}
	}
}

func TestCompareNormalizesVolatileValuesOnly(t *testing.T) {
	left := jsonEv(`{"csrf_token":"aaa","updatedAt":"2026-11-27T01:00:00Z","id":7,"role":"member"}`)
	right := jsonEv(`{"csrf_token":"bbb","updatedAt":"2026-11-27T02:00:00Z","id":8,"role":"member"}`)
	c := Compare(left, right, RoleOriginal, RoleReproduction)
	if c.Relation != RelationDifferent || !reflect.DeepEqual(kinds(c), []string{"field_value:id"}) {
		t.Fatalf("an identifier change must stay a difference: %+v", c)
	}
	if !reflect.DeepEqual(c.Normalized, []string{"updatedAt:volatile_name"}) || !reflect.DeepEqual(c.NotCompared, []string{"credential_value"}) {
		t.Fatalf("normalized = %v, not compared = %v", c.Normalized, c.NotCompared)
	}
	same := Compare(jsonEv(`{"token":"a","role":"member"}`), jsonEv(`{"token":"b","role":"member"}`), RoleOriginal, RoleReproduction)
	if same.Relation != RelationEquivalent || !reflect.DeepEqual(same.NotCompared, []string{"credential_value"}) {
		t.Fatalf("token-only change: %+v", same)
	}
	for _, pair := range [][2]string{{`{"session":1}`, `{"session":2}`}, {`{"auth":true}`, `{"auth":false}`}} {
		secret := Compare(jsonEv(pair[0]), jsonEv(pair[1]), RoleOriginal, RoleReproduction)
		if secret.Relation != RelationEquivalent || !reflect.DeepEqual(secret.NotCompared, []string{"credential_value"}) {
			t.Fatalf("credential scalar must remain uncomparable: %+v", secret)
		}
	}
	nonce := Compare(jsonEv(`{"nonce":"a"}`), jsonEv(`{"nonce":"b"}`), RoleOriginal, RoleReproduction)
	if nonce.Relation != RelationEquivalentNormalized {
		t.Fatalf("nonce change: %+v", nonce)
	}
	// 변동 이름이어도 존재 차이는 정규화하지 않는다.
	presence := Compare(jsonEv(`{"role":"member"}`), jsonEv(`{"role":"member","token":"x"}`), RoleOriginal, RoleReproduction)
	if !reflect.DeepEqual(kinds(presence), []string{"field_presence:token"}) {
		t.Fatalf("presence of a volatile name must remain: %+v", presence)
	}
}

func TestCompareSameStructureDifferentMeaning(t *testing.T) {
	c := Compare(jsonEv(`{"user":{"role":"member","admin":false}}`), jsonEv(`{"user":{"role":"owner","admin":true}}`), RoleNormalControl, RoleReproduction)
	if c.Relation != RelationDifferent || !reflect.DeepEqual(kinds(c), []string{"field_bool:user.admin", "field_value:user.role"}) {
		t.Fatalf("same paths, different values: %+v", c)
	}
	if *c.Similarity != 100 {
		t.Fatalf("structure similarity must stay 100: %d", *c.Similarity)
	}
}

func TestCompareLoginPageAgainstFunction(t *testing.T) {
	login := Extract(complete("text/html", `<html><form method=post action=/login><input name=user><input type=password name=pw></form></html>`), Limits{}, nil)
	fn := jsonEv(`{"orders":[{"id":1}]}`)
	c := Compare(fn, login, RoleOriginal, RoleReproduction)
	if c.Relation != RelationDifferent || !reflect.DeepEqual(kinds(c), []string{"media_class:"}) || !reflect.DeepEqual(c.NotCompared, []string{"body_structure"}) {
		t.Fatalf("login redirect: %+v", c)
	}
	page := Extract(complete("text/html", `<html><body><h1>Orders</h1><table><tr><td>1</td></tr></table></body></html>`), Limits{}, nil)
	c = Compare(page, login, RoleOriginal, RoleReproduction)
	found := false
	for _, d := range c.Differences {
		found = found || d.Kind == DiffLoginForm && d.Left == "absent" && d.Right == "present"
	}
	if !found || c.Relation != RelationDifferent {
		t.Fatalf("login form must be a difference: %+v", c)
	}
}

func TestCompareIncomparable(t *testing.T) {
	truncated := Extract(Response{Received: true, Truncated: true, StatusCode: 200, ContentType: "application/json"}, Limits{}, nil)
	c := Compare(jsonEv(`{"a":1}`), truncated, RoleOriginal, RoleReproduction)
	if c.Relation != RelationIncomparable || !reflect.DeepEqual(c.Incomparable, []string{"right_truncated"}) || len(c.Differences) != 0 {
		t.Fatalf("truncated: %+v", c)
	}
	missing := Evidence{Schema: Schema, State: StateNotRecorded, MediaClass: "none"}
	c = Compare(missing, jsonEv(`{}`), RoleOriginal, RoleReproduction)
	if c.Relation != RelationIncomparable || c.Incomparable[0] != "left_not_recorded" {
		t.Fatalf("not recorded: %+v", c)
	}
	if c := Compare(Evidence{}, jsonEv(`{}`), RoleOriginal, RoleReproduction); c.Relation != RelationIncomparable {
		t.Fatalf("zero evidence: %+v", c)
	}
}

func TestCompareFieldLimitDoesNotClaimAbsence(t *testing.T) {
	limited := jsonEv(`{"a":1}`)
	limited.JSON.FieldsOmitted, limited.State, limited.Limits = 3, StateLimited, []string{LimitFields}
	c := Compare(limited, jsonEv(`{"a":1,"b":2}`), RoleOriginal, RoleReproduction)
	if len(c.Differences) != 0 || c.Relation != RelationEquivalent {
		t.Fatalf("omitted fields must not become absence: %+v", c)
	}
	if !reflect.DeepEqual(c.NotCompared, []string{"json_fields_omitted", "left_fields_omitted", "limit:fields"}) {
		t.Fatalf("not compared = %v", c.NotCompared)
	}
}

func TestCompareErrorOnlyAndStatus(t *testing.T) {
	ok := jsonEv(`{"data":{"items":[]}}`)
	failed := Extract(Response{Received: true, Complete: true, StatusCode: 500, ContentType: "application/json",
		Body: []byte(`{"error":{"message":"SQLITE_ERROR: no such column"}}`)}, Limits{}, nil)
	c := Compare(ok, failed, RoleNormalControl, RoleReproduction)
	got := kinds(c)
	want := []string{"status_code:", "error_signal:sql_error", "json_error_shape:", "field_presence:data", "field_presence:data.items", "field_presence:error", "field_presence:error.message"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("differences = %v, want %v", got, want)
	}
}

func TestCompareHTMLTagOrder(t *testing.T) {
	a := Extract(complete("text/html", `<ul><li>a</li><p>x</p></ul>`), Limits{}, nil)
	b := Extract(complete("text/html", `<ul><p>x</p><li>a</li></ul>`), Limits{}, nil)
	c := Compare(a, b, RoleOriginal, RoleReproduction)
	if c.Relation != RelationDifferent || *c.Similarity != 99 || !reflect.DeepEqual(kinds(c), []string{"html_structure:"}) {
		t.Fatalf("tag order: %+v", c)
	}
	// 고정 목록에 없는 태그 이름도 구조 digest에 들어간다. 태그 수가 같다고 순서 차이로 단정할 수 없다.
	a = Extract(complete("text/html", `<div><article>x</article></div>`), Limits{}, nil)
	b = Extract(complete("text/html", `<div><aside>x</aside></div>`), Limits{}, nil)
	c = Compare(a, b, RoleOriginal, RoleReproduction)
	if c.Relation != RelationDifferent || !reflect.DeepEqual(kinds(c), []string{"html_structure:"}) {
		t.Fatalf("uncounted tag change must stay a difference: %+v", c)
	}
}

func TestCompareRejectsInvalidEvidence(t *testing.T) {
	left := jsonEv(`{"a":1}`)
	left.JSON.Fields[0].Path = "unsafe path"
	c := Compare(left, jsonEv(`{"a":1}`), RoleOriginal, RoleReproduction)
	if c.Relation != RelationIncomparable || !reflect.DeepEqual(c.Incomparable, []string{"left_evidence_invalid"}) {
		t.Fatalf("invalid field must not become a comparable observation: %+v", c)
	}
}

func TestCompareCookieAttributesAndRepeatedHeaders(t *testing.T) {
	a := complete("text/html", "<p>x</p>")
	b := a
	a.Header = map[string][]string{"Set-Cookie": {"id=x; SameSite=Strict"}, "X-Frame-Options": {"DENY"}}
	b.Header = map[string][]string{"Set-Cookie": {"id=y; SameSite=Lax"}, "X-Frame-Options": {"DENY", "SAMEORIGIN"}}
	c := Compare(Extract(a, Limits{}, nil), Extract(b, Limits{}, nil), RoleOriginal, RoleReproduction)
	if c.Relation != RelationDifferent || !reflect.DeepEqual(kinds(c), []string{"header_attribute:X-Frame-Options:repeated", "header_attribute:frame_options", "header_attribute:set_cookie_attributes"}) {
		t.Fatalf("cookie mode and repeated header must be visible: %+v", c)
	}
}

func TestCompareMissingTagCountsDoesNotNormalizeStructure(t *testing.T) {
	a := Extract(complete("text/html", `<div><p>x</p></div>`), Limits{}, nil)
	b := Extract(complete("text/html", `<div><span>x</span></div>`), Limits{}, nil)
	a.HTML.TagCounts = nil
	b.HTML.TagCounts = nil
	a.State, b.State = StateLimited, StateLimited
	a.Limits, b.Limits = []string{LimitEncoded}, []string{LimitEncoded}
	c := Compare(a, b, RoleOriginal, RoleReproduction)
	if c.Relation != RelationDifferent || c.Similarity != nil || !reflect.DeepEqual(kinds(c), []string{"html_structure:"}) || !reflect.DeepEqual(c.NotCompared, []string{"html_tag_counts", "limit:encoded_size"}) {
		t.Fatalf("missing tag counts cannot justify order normalization: %+v", c)
	}
}
