package server

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/responseevidence"
)

func TestCandidateClaimCategoryUsesWordBoundaries(t *testing.T) {
	for class, want := range map[string]string{
		"security_headers": claimSecurityHeader, "Clickjacking": claimSecurityHeader, "cors_misconfiguration": claimSecurityHeader,
		"insecure session cookie": claimCookieAttribute, "sql_injection": claimInputHandling, "SQLi": claimInputHandling,
		"nosql-injection": claimInputHandling, "reflected_xss": claimInputHandling, "path_traversal": claimInputHandling,
		"error_based_sql_injection": claimInputHandling, "verbose_error": claimErrorDisclosure, "stack trace disclosure": claimErrorDisclosure,
		"information_disclosure": claimDataExposure, "sensitive_data_exposure": claimDataExposure, "business_logic": claimBusinessRule,
		"broken_access_control": claimBusinessRule, "authorization_bypass": claimBusinessRule, "broken_authentication": claimAuthContext,
		"session_fixation": claimAuthContext, "directory_listing": claimUnknown, "authx": claimUnknown, "": claimUnknown,
		"cross_site_request_forgery": claimUnknown,
	} {
		if got := candidateClaimCategory(db.DiagnosticCandidate{VulnClass: class}); got != want {
			t.Errorf("%q: got %s want %s", class, got, want)
		}
	}
	if got := candidateClaimCategory(db.DiagnosticCandidate{VulnClass: "information_disclosure", VerificationClaim: "server_version_banner"}); got != claimSecurityHeader {
		t.Fatalf("fixed passive claim must use header facts: %s", got)
	}
	for category := range map[string]bool{claimSecurityHeader: true, claimCookieAttribute: true, claimErrorDisclosure: true, claimDataExposure: true,
		claimAuthContext: true, claimBusinessRule: true, claimInputHandling: true, claimUnknown: true} {
		if req := claimRequirements[category]; req.Category != category || len(req.AnyOf) == 0 {
			t.Fatalf("%s requirement missing", category)
		}
	}
}

func claimEvidence(status int, contentType, body string, header http.Header) responseevidence.Evidence {
	if header == nil {
		header = http.Header{}
	}
	header.Set("Content-Type", contentType)
	return responseevidence.Extract(responseevidence.Response{Received: true, Complete: true, StatusCode: status, Method: "GET",
		ContentType: contentType, Header: header, Body: []byte(body)}, responseevidence.DefaultLimits(), nil)
}

func TestObservationFactsSeparateGenericAndSpecificErrors(t *testing.T) {
	generic := observationFacts(claimEvidence(500, "application/json", `{"error":"Something went wrong"}`, nil))
	if !reflect.DeepEqual(generic, []string{factGenericError, factResponseHeaders}) {
		t.Fatalf("generic error facts: %v", generic)
	}
	internal := observationFacts(claimEvidence(500, "text/plain", "Internal Server Error", nil))
	if !reflect.DeepEqual(internal, []string{factGenericError, factResponseHeaders}) {
		t.Fatalf("generic server text is not a specific signal: %v", internal)
	}
	sqlError := observationFacts(claimEvidence(500, "application/json", `{"error":"SQLITE_ERROR: near \"'\": syntax error"}`, nil))
	if !reflect.DeepEqual(sqlError, []string{factResponseHeaders, factSpecificError}) {
		t.Fatalf("specific signal: %v", sqlError)
	}
	ok := observationFacts(claimEvidence(200, "application/json", `{"data":[{"id":1}]}`, http.Header{"Server": {"x"}}))
	if !reflect.DeepEqual(ok, []string{factDisclosureHeader, factResponseHeaders, factStructured}) {
		t.Fatalf("success facts: %v", ok)
	}
	errorShape := observationFacts(claimEvidence(200, "application/json", `{"error":"nope"}`, nil))
	if reflect.DeepEqual(errorShape, ok) || len(errorShape) != 1 {
		t.Fatalf("a 2xx error envelope is not structured content: %v", errorShape)
	}
	empty := observationFacts(claimEvidence(200, "application/json", ``, nil))
	if len(empty) != 1 || empty[0] != factResponseHeaders {
		t.Fatalf("empty body facts: %v", empty)
	}
	cookie := observationFacts(claimEvidence(200, "text/html", `<p>x</p>`, http.Header{"Set-Cookie": {"a=b; Path=/"}}))
	if !reflect.DeepEqual(cookie, []string{factIssuedCookie, factResponseHeaders, factStructured}) {
		t.Fatalf("cookie facts: %v", cookie)
	}
	if observationFacts(responseevidence.Evidence{Schema: "forged"}) != nil {
		t.Fatal("invalid evidence must yield no facts")
	}
}

func TestClaimFactsSatisfiedAndMissing(t *testing.T) {
	req := claimRequirements[claimDataExposure]
	if set, ok := claimFactsSatisfied(req, map[string]bool{factStructured: true}); ok || set != nil {
		t.Fatal("structured content alone must not satisfy data exposure")
	}
	if missing := claimMissingFacts(req, map[string]bool{factStructured: true}); !reflect.DeepEqual(missing, []string{factBusinessRule}) {
		t.Fatalf("missing: %v", missing)
	}
	if set, ok := claimFactsSatisfied(req, map[string]bool{factStructured: true, factBusinessRule: true}); !ok ||
		!reflect.DeepEqual(set, []string{factStructured, factBusinessRule}) {
		t.Fatalf("satisfied: %v %v", set, ok)
	}
	if missing := claimMissingFacts(claimRequirements[claimInputHandling], map[string]bool{}); !reflect.DeepEqual(missing, []string{factSpecificError}) {
		t.Fatalf("input handling missing: %v", missing)
	}
}

func TestInputContrastIgnoresGenericDifferences(t *testing.T) {
	ok := claimEvidence(200, "application/json", `{"data":[{"id":1,"name":"a"}]}`, nil)
	fail := claimEvidence(500, "application/json", `{"error":"Something went wrong"}`, nil)
	more := claimEvidence(200, "application/json", `{"data":[{"id":1,"name":"a"},{"id":2,"name":"b"}]}`, nil)
	generic := responseevidence.Compare(fail, ok, responseevidence.RoleReproduction, responseevidence.RoleNormalControl)
	if inputContrastObserved(generic, fail, ok, false) {
		t.Fatalf("status/error-shape-only difference is not an input effect: %+v", generic.Differences)
	}
	structural := responseevidence.Compare(more, ok, responseevidence.RoleReproduction, responseevidence.RoleNormalControl)
	if !inputContrastObserved(structural, more, ok, false) {
		t.Fatalf("structural difference must count: %+v", structural.Differences)
	}
	if inputContrastObserved(structural, more, ok, true) {
		t.Fatal("same input cannot show an input effect")
	}
}

func TestInputContrastRequiresStructuralDifference(t *testing.T) {
	for _, tc := range []struct {
		name, reproduction, control, expectedKind string
	}{
		{"string value", `{"message":"alpha"}`, `{"message":"bravo"}`, responseevidence.DiffValue},
		{"boolean value", `{"enabled":true}`, `{"enabled":false}`, responseevidence.DiffBool},
		{"string shape", `{"message":"alpha"}`, `{"message":"https://example.test/"}`, responseevidence.DiffShape},
		{"empty field", `{"message":""}`, `{"message":"alpha"}`, responseevidence.DiffEmptyValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reproduction := claimEvidence(200, "application/json", tc.reproduction, nil)
			control := claimEvidence(200, "application/json", tc.control, nil)
			comparison := responseevidence.Compare(reproduction, control, responseevidence.RoleReproduction, responseevidence.RoleNormalControl)
			found := false
			for _, difference := range comparison.Differences {
				found = found || difference.Kind == tc.expectedKind
				if difference.Kind != responseevidence.DiffValue && difference.Kind != responseevidence.DiffBool &&
					difference.Kind != responseevidence.DiffShape && difference.Kind != responseevidence.DiffEmptyValue {
					t.Fatalf("test needs scalar-only differences: %+v", comparison.Differences)
				}
			}
			if !found {
				t.Fatalf("expected %s in %+v", tc.expectedKind, comparison.Differences)
			}
			if inputContrastObserved(comparison, reproduction, control, false) {
				t.Fatal("scalar target data cannot establish a structural input effect")
			}
		})
	}
}
