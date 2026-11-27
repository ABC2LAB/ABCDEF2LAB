package learnedlogin

import (
	"encoding/json"
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

func TestUnresolvedMethodKeepsOtherAccountHistory(t *testing.T) {
	a := succeededRow("diagnosis-1", "acct-a")
	b := succeededRow("diagnosis-2", "acct-b")
	b.SourceUnresolved = true
	encode := func(rows ...Row) []byte {
		var raw []byte
		for _, row := range rows {
			line, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			raw = append(append(raw, line...), '\n')
		}
		return raw
	}
	rows, skipped, err := ParseRows(encode(a, b))
	if err != nil || skipped != 0 {
		t.Fatalf("지원하지 않는 방법 때문에 이력 전체를 거부: %v, skipped=%d", err, skipped)
	}
	selected := Select(rows, run, []string{"acct-a", "acct-b"})
	if selected["acct-a"].Reason != "" || selected["acct-b"].Reason != ReasonPrepUnavailable {
		t.Fatalf("계정별 방법 분리 실패: A=%q B=%q", selected["acct-a"].Reason, selected["acct-b"].Reason)
	}
	b.SourceUnresolved = false
	b.OtherFields = append(b.OtherFields, "_csrf_second")
	b.FieldsFromResponse = append(b.FieldsFromResponse, "_csrf_second")
	b.FieldSources = []RowFieldSource{{Destination: "_csrf", SourceMethod: "GET", SourcePath: "/api/csrf", SourceType: SourceJSONField, SourceField: "csrfToken", SourceRef: "call_b/attempt_2", SourceStatus: 200}}
	for name, old := range map[string]Row{
		"partial": b,
		"multiple issuers": func() Row {
			row := b
			row.FieldSources = append([]RowFieldSource(nil), b.FieldSources...)
			row.FieldSources = append(row.FieldSources, RowFieldSource{Destination: "_csrf_second", SourceMethod: "GET", SourcePath: "/api/other", SourceType: SourceJSONField, SourceField: "second", SourceRef: "call_c/attempt_3", SourceStatus: 200})
			return row
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			previous, skipped, err := ParseRows(encode(a, old))
			if err != nil || skipped != 0 {
				t.Fatalf("이전 판정기가 만든 재현 불가 행으로 이력 전체 무효: %v", err)
			}
			got := Select(previous, run, []string{"acct-a", "acct-b"})
			if got["acct-a"].Reason != "" || got["acct-b"].Reason != ReasonPrepUnavailable {
				t.Fatalf("이전 행 방법 선택: A=%q B=%q", got["acct-a"].Reason, got["acct-b"].Reason)
			}
			failed := succeededRow("diagnosis-3", "acct-b")
			failed.Outcome, failed.Followup = OutcomeFailed, FollowupNone
			unknown := succeededRow("diagnosis-4", "acct-b")
			unknown.Transport, unknown.Outcome = TransportUnknown, OutcomeUnknown
			history, skipped, err := ParseRows(encode(a, old, failed, unknown))
			if err != nil || skipped != 0 || len(history) != 4 {
				t.Fatalf("실패·결과 불명 이력 유실: rows=%d skipped=%d err=%v", len(history), skipped, err)
			}
			counts, guard := ReplayFailureCounts(history, run, 3)
			if counts[failed.Identifier] != 1 || guard[failed.Identifier] != 1 || history[3].Outcome != OutcomeUnknown {
				t.Fatalf("재현 불가 방법 뒤 실패·불명 수 복원 실패: count=%d guard=%d", counts[failed.Identifier], guard[failed.Identifier])
			}
		})
	}
	b.SourceUnresolved = true
	if rows, _, err := ParseRows(encode(a, b)); err != nil || Select(rows, run, []string{"acct-b"})["acct-b"].Reason != ReasonPrepUnavailable {
		t.Fatalf("이전 부분·미확인 출처 행 호환 실패: %v", err)
	}
	b.FieldSources[0].SourceMethod = "POST"
	if _, _, err := ParseRows(encode(a, b)); err == nil {
		t.Fatal("모순된 출처 기록을 손상으로 거부하지 않음")
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
