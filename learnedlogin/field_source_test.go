package learnedlogin

import (
	"net/http"
	"testing"
)

func TestDifferentPathCSRFMethodAndDigest(t *testing.T) {
	legacy := formMethod()
	v1 := legacy.Digest()
	m := legacy.Clone()
	m.Schema = MethodSchemaV2
	m.OtherFields = []string{"_csrf"}
	m.FieldsFromResponse = []string{"_csrf"}
	m.FieldSources = []FieldSource{{Destination: "_csrf", SourceMethod: "GET", SourcePath: "/api/csrf", SourceType: SourceJSONField, SourceField: "csrfToken"}}
	if reason := m.Validate(); reason != "" || m.PrepPath() != "/api/csrf" || m.Digest() == v1 {
		t.Fatalf("별도 출처 방법 %q %q", reason, m.PrepPath())
	}
	other := m.Clone()
	other.FieldSources[0].SourcePath = "/other"
	if other.Digest() == m.Digest() {
		t.Fatal("출처 변경 뒤 verified 해시 유지")
	}
	if legacy.Digest() != v1 || legacy.PrepPath() != legacy.Path {
		t.Fatal("기존 방법 해시·준비 경로 변경")
	}
	for name, change := range map[string]func(*Method){
		"external":    func(v *Method) { v.FieldSources[0].SourcePath = "https://other.test/csrf" },
		"post source": func(v *Method) { v.FieldSources[0].SourceMethod = "POST" },
		"extra prep": func(v *Method) {
			v.FieldSources = append(v.FieldSources, FieldSource{Destination: "other", SourceMethod: "GET", SourcePath: "/another", SourceType: SourceJSONField, SourceField: "x"})
		},
		"unknown destination": func(v *Method) { v.FieldSources[0].Destination = "unknown" },
		"v1 source":           func(v *Method) { v.Schema = "" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := m.Clone()
			change(&bad)
			if bad.Validate() == "" {
				t.Fatal("지원 범위 밖 출처를 허용함")
			}
		})
	}
}

func TestUnresolvedDifferentPathSourceBlocksLearnedLogin(t *testing.T) {
	older := succeededRow("diagnosis-1", "acct-a")
	latest := succeededRow("diagnosis-2", "acct-a")
	latest.SourceUnresolved = true
	for _, rows := range [][]Row{{latest}, {older, latest}} {
		got := Select(rows, run, []string{"acct-a"})
		if got["acct-a"].Reason != ReasonPrepUnavailable {
			t.Fatalf("출처를 확인할 수 없는 마지막 성공으로 E3 로그인을 진행함: %v", got["acct-a"].Reason)
		}
	}
}

func TestFindCSRFSourceFromSeparateResponse(t *testing.T) {
	for _, tc := range []struct{ name, contentType, body, token, wantType, wantField string }{
		{"json", "application/json", `{"csrfToken":"token-12345678"}`, "token-12345678", SourceJSONField, "csrfToken"},
		{"nested json", "application/json", `{"csrf":{"token":"token-12345678"}}`, "token-12345678", SourceJSONField, "csrf.token"},
		{"hidden", "text/html", `<form><input type="hidden" name="csrfKey" value="token-12345678"></form>`, "token-12345678", SourceHTMLHidden, "csrfKey"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, reason := ParseScreen(Response{Status: 200, Header: http.Header{"Content-Type": {tc.contentType}}, Body: []byte(tc.body)})
			if reason != "" {
				t.Fatal(reason)
			}
			typ, field, ok := s.FindValueSource(tc.token)
			if !ok || typ != tc.wantType || field != tc.wantField {
				t.Fatalf("출처 %q %q %v", typ, field, ok)
			}
		})
	}
	for _, body := range []string{`{"a":"token-12345678","b":"token-12345678"}`, `{"a":"short"}`} {
		s, _ := ParseScreen(Response{Status: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(body)})
		if _, _, ok := s.FindValueSource("token-12345678"); ok {
			t.Fatal("모호하거나 없는 출처를 수용함")
		}
	}
}
