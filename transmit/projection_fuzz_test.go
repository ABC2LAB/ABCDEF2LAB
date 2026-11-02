package transmit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
)

const (
	maxProjectionFuzzInput = 64 << 10
	maxProjectionFuzzMIME  = 256
	projectionTokenBytes   = 16
	projectionFuzzFirst    = "fixture-fuzz-canary-alpha-37f9a4"
	projectionFuzzSecond   = "fixture-fuzz-canary-beta-49d2b8"
	projectionFuzzPrivate  = "UNREGISTERED-SYNTHETIC-VALUE"
)

func projectionFuzzFixture() (*JSONViewRule, *http.Request, idor.AccessRequest) {
	rule := &JSONViewRule{
		StatusPath: []string{"status"}, NormalValue: "ok", ErrorValues: []string{"error"},
		DataPath: []string{"data"},
		Markers:  []idor.Marker{{Field: "note", Value: projectionFuzzFirst}, {Field: "tag", Value: projectionFuzzSecond}},
	}
	request := &http.Request{
		Method: http.MethodGet, URL: &url.URL{Scheme: "http", Host: "fixture.invalid", Path: "/document"},
		Header: http.Header{"X-Session": {projectionFuzzPrivate}},
	}
	proposal := idor.AccessRequest{
		As:     idor.AccountRef{Label: "owner", Handle: "fixture-owner-session", Status: oracle.SubjectVerified},
		Method: http.MethodGet, ResourceRef: "document:one", Inputs: []string{"fixture-input"},
	}
	return rule, request, proposal
}

func projectionFuzzJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal("synthetic fuzz fixture could not be encoded")
	}
	return body
}

// Snapshot the policy and all request fields read by project, without using
// project's parser or reflection helpers to decide whether evidence is valid.
func projectionFuzzSnapshot(t *testing.T, rule *JSONViewRule, request *http.Request, proposal idor.AccessRequest) []byte {
	t.Helper()
	return projectionFuzzJSON(t, []any{rule, request.Method, request.URL, request.Header, proposal})
}

func assertProjectionFuzzView(t *testing.T, view idor.ResponseView, resource string, markers []idor.Marker) {
	t.Helper()
	if view.ResourceRef != resource || view.Normal && (!view.TemplateClassified || view.ErrorTemplate) || view.ErrorTemplate && (!view.TemplateClassified || view.Normal || view.Body != "") {
		t.Fatal("projection lost resource binding or returned inconsistent flags")
	}
	if view.Body == "" {
		return
	}
	if !view.Normal || !view.TemplateClassified || view.ErrorTemplate {
		t.Fatal("unclassified response produced marker evidence")
	}
	allowed := make(map[string]bool, len(markers))
	for _, marker := range markers {
		if !marker.Trivial {
			allowed[marker.Value] = true
		}
	}
	seen := make(map[string]bool, len(markers))
	for _, value := range strings.Split(view.Body, "\n") {
		if !allowed[value] || seen[value] {
			t.Fatal("projection emitted an unregistered or duplicate marker")
		}
		seen[value] = true
	}
}

func FuzzProjectionRegisteredEvidenceOnly(f *testing.F) {
	valid := `{"status":"ok","data":{"note":"` + projectionFuzzFirst + `","tag":"` + projectionFuzzSecond + `","private":"` + projectionFuzzPrivate + `"}}`
	for _, seed := range []struct{ body, contentType string }{
		{valid, "application/json"},
		{valid, "application/problem+json"},
		{valid, "application/json; charset=utf-8"},
		{valid, "text/plain"},
		{"", "application/json"},
		{"null", "application/json"},
		{"[]", "application/json"},
		{`{"status":"error","data":{"note":"` + projectionFuzzFirst + `"}}`, "application/json"},
		{`{"status":"ok","data":{"private":"` + projectionFuzzFirst + `"}}`, "application/json"},
		{`{"status":"ok","status":"ok","data":{"note":"` + projectionFuzzFirst + `"}}`, "application/json"},
		{valid + " {}", "application/json"},
		{valid + string([]byte{0xff}), "application/json"},
		{`{"status":"ok","data":{"note":"` + projectionFuzzFirst + `"},"extra":` + strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64) + "}", "application/json"},
		{`{"status":"ok","data":{"note":"` + projectionFuzzFirst + `"},"extra":1e999999}`, "application/json"},
		{projectionFuzzFirst, "application/json"},
		{projectionFuzzSecond, "application/json"},
	} {
		f.Add([]byte(seed.body), seed.contentType)
	}
	f.Fuzz(func(t *testing.T, body []byte, contentType string) {
		if len(body) > maxProjectionFuzzInput || len(contentType) > maxProjectionFuzzMIME {
			t.Skip()
		}
		rule, request, proposal := projectionFuzzFixture()
		before := projectionFuzzSnapshot(t, rule, request, proposal)
		original := bytes.Clone(body)
		view := project(body, contentType, proposal.ResourceRef, rule, request, proposal)
		assertProjectionFuzzView(t, view, proposal.ResourceRef, rule.Markers)
		if again := project(body, contentType, proposal.ResourceRef, rule, request, proposal); again != view {
			t.Fatal("identical inputs produced different projections")
		}
		if absent := project(body, contentType, proposal.ResourceRef, nil, request, proposal); absent != (idor.ResponseView{ResourceRef: proposal.ResourceRef}) {
			t.Fatal("missing policy produced classified evidence")
		}

		// Controls remain valid JSON even when the raw fuzz bytes are malformed.
		// Arbitrary values in an unrelated field must not change known evidence.
		control := projectionFuzzJSON(t, map[string]any{
			"status": "ok", "data": map[string]string{"note": projectionFuzzFirst, "noise": string(body)},
		})
		positive := project(control, "application/json", proposal.ResourceRef, rule, request, proposal)
		if positive.Body != projectionFuzzFirst || !positive.Normal || !positive.TemplateClassified || positive.ErrorTemplate {
			t.Fatal("unregistered noise changed the positive control")
		}
		if nonJSON := project(control, "text/plain", proposal.ResourceRef, rule, request, proposal); nonJSON != (idor.ResponseView{ResourceRef: proposal.ResourceRef}) {
			t.Fatal("non-JSON response became classified evidence")
		}
		// Finding a marker in an unrelated field or in a different registered
		// field cannot substitute for an exact match in its own registered field.
		mismatch := projectionFuzzJSON(t, map[string]any{
			"status": "ok", "data": map[string]string{"note": string(body), "unregistered": projectionFuzzSecond, "private": projectionFuzzPrivate},
		})
		want := ""
		if string(body) == projectionFuzzFirst {
			want = projectionFuzzFirst
		}
		if got := project(mismatch, "application/json", proposal.ResourceRef, rule, request, proposal); got.Body != want {
			t.Fatal("projection borrowed a marker from an unregistered or mismatched field")
		}
		if !bytes.Equal(body, original) || !bytes.Equal(before, projectionFuzzSnapshot(t, rule, request, proposal)) {
			t.Fatal("projection mutated response, policy or request inputs")
		}
	})
}

func FuzzProjectionRejectsAmbiguousJSON(f *testing.F) {
	for _, seed := range [][]byte{nil, []byte("synthetic noise"), []byte("\"\\\n\x00"), {0xff, 0xfe}, []byte(projectionFuzzFirst)} {
		f.Add(seed, uint8(0))
	}
	f.Fuzz(func(t *testing.T, noise []byte, nesting uint8) {
		if len(noise) > maxProjectionFuzzInput {
			t.Skip()
		}
		rule, request, proposal := projectionFuzzFixture()
		before := projectionFuzzSnapshot(t, rule, request, proposal)
		encodedNoise := string(projectionFuzzJSON(t, string(noise)))
		encodedKey := string(projectionFuzzJSON(t, "key-"+string(noise)))
		data := `"data":{"note":"` + projectionFuzzFirst + `"}`
		base := `{"status":"ok",` + data + `,"noise":` + encodedNoise + `}`
		if control := project([]byte(base), "application/json", proposal.ResourceRef, rule, request, proposal); control.Body != projectionFuzzFirst || !control.Normal || !control.TemplateClassified || control.ErrorTemplate {
			t.Fatal("ambiguous JSON positive control did not emit its registered marker")
		}
		depth := maxJSONDepth + 1 + int(nesting)
		cases := []string{
			`{"status":"ok","status":"ok",` + data + `,"noise":` + encodedNoise + `}`,
			`{"status":"ok","\u0073tatus":"ok",` + data + `,"noise":` + encodedNoise + `}`,
			`{"status":"ok","data":{"note":"` + projectionFuzzFirst + `","n\u006fte":"` + projectionFuzzFirst + `"},"noise":` + encodedNoise + `}`,
			`{"status":"ok",` + data + `,"other":{` + encodedKey + `:0,` + encodedKey + `:1}}`,
			base + "\n[]",
			`{"status":"ok",` + data + `,"noise":"` + string([]byte{0xff}) + `"}`,
			`{"status":"ok",` + data + `,"noise":` + strings.Repeat("[", depth) + encodedNoise + strings.Repeat("]", depth) + `}`,
			base[:len(base)-1],
		}
		for i, body := range cases {
			if got := project([]byte(body), "application/json", proposal.ResourceRef, rule, request, proposal); got != (idor.ResponseView{ResourceRef: proposal.ResourceRef}) {
				t.Fatalf("ambiguous JSON family %d produced evidence or classification", i)
			}
		}
		if !bytes.Equal(before, projectionFuzzSnapshot(t, rule, request, proposal)) {
			t.Fatal("ambiguous input mutated policy or request inputs")
		}
	})
}

func FuzzProjectionSuppressesRequestReflection(f *testing.F) {
	for _, seed := range [][]byte{nil, []byte("canary seed"), {0, 1, 255}, []byte("path?query=header")} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > maxProjectionFuzzInput {
			t.Skip()
		}
		// The derived token stays valid as a ref, header name and DNS label.
		// Hashing is fixture construction, not a test for any cryptographic claim.
		sum := sha256.Sum256(input)
		marker := http.CanonicalHeaderKey("X-E2-Canary-" + hex.EncodeToString(sum[:projectionTokenBytes]))
		body := projectionFuzzJSON(t, map[string]any{"status": "ok", "data": map[string]string{"note": marker, "private": projectionFuzzPrivate}})
		for _, source := range []string{"path", "encoded-path", "query", "encoded-query", "host", "header-name", "header-value", "input", "label", "handle", "resource"} {
			rule, request, proposal := projectionFuzzFixture()
			rule.Markers = []idor.Marker{{Field: "note", Value: marker}}
			if control := project(body, "application/json", proposal.ResourceRef, rule, request, proposal); control.Body != marker || !control.Normal || !control.TemplateClassified || control.ErrorTemplate {
				t.Fatal("reflection positive control did not emit its registered marker")
			}
			switch source {
			case "path":
				request.URL.Path += "/" + marker
			case "encoded-path":
				parsed, err := url.Parse("http://fixture.invalid/" + strings.ReplaceAll(marker, "-", "%2D"))
				if err != nil {
					t.Fatal("synthetic encoded path is invalid")
				}
				request.URL = parsed
			case "query":
				request.URL.RawQuery = "echo=" + marker
			case "encoded-query":
				request.URL.RawQuery = "echo=" + strings.ReplaceAll(marker, "-", "%2D")
			case "host":
				request.URL.Host = marker + ".invalid"
			case "header-name":
				request.Header.Set(marker, "fixture-value")
			case "header-value":
				request.Header.Set("X-Echo", "prefix-"+marker+"-suffix")
			case "input":
				proposal.Inputs = append(proposal.Inputs, "prefix-"+marker+"-suffix")
			case "label":
				proposal.As.Label = marker
			case "handle":
				proposal.As.Handle = marker
			case "resource":
				proposal.ResourceRef = marker
			}
			before := projectionFuzzSnapshot(t, rule, request, proposal)
			view := project(body, "application/json", proposal.ResourceRef, rule, request, proposal)
			assertProjectionFuzzView(t, view, proposal.ResourceRef, rule.Markers)
			if view.Body != "" {
				t.Fatalf("request reflection from %s became marker evidence", source)
			}
			if !bytes.Equal(before, projectionFuzzSnapshot(t, rule, request, proposal)) {
				t.Fatal("reflection check mutated policy or request inputs")
			}
		}
	})
}
