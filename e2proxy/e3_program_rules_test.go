package e2proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func e3Rule(purpose, method, path string) Rule {
	return Rule{Ref: "e3-rule", Purpose: purpose, Origin: "http://127.0.0.1:1", ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: path, PathScopeRef: "e3-path", Methods: []string{method}}
}

func TestE3ProgramRuleShapes(t *testing.T) {
	for _, tc := range []struct{ purpose, method, path string }{
		{PurposeE3Identity, "GET", "/identity"},
		{PurposeE3Create, "POST", "/items"},
		{PurposeE3Create, "PUT", "/items"},
		{PurposeE3Read, "GET", "/items/{id}"},
	} {
		if _, err := compileRules([]Rule{e3Rule(tc.purpose, tc.method, tc.path)}); err != nil {
			t.Errorf("valid %s %s %s: %v", tc.purpose, tc.method, tc.path, err)
		}
	}
	for _, tc := range []struct{ purpose, method, path string }{
		{PurposeE3Identity, "POST", "/identity"},
		{PurposeE3Create, "GET", "/items"},
		{PurposeE3Create, "DELETE", "/items"},
		{PurposeE3Read, "POST", "/items/{id}"},
		{PurposeE3Read, "GET", "/items/{id}/children/{id}"},
		{PurposeE3Read, "GET", "/items/id"},
		{PurposeE3Read, "GET", "/items/{id}suffix"},
		{PurposeE3Identity, "GET", "/identity?x=1"},
		{PurposeE3Create, "POST", "/items#fragment"},
		{PurposeE3Identity, "GET", "/identity/{id}"},
		{PurposeE3Identity, "GET", "/identity\x01"},
		{"e3_unknown", "GET", "/identity"},
	} {
		if _, err := compileRules([]Rule{e3Rule(tc.purpose, tc.method, tc.path)}); err == nil {
			t.Errorf("invalid %s %s %s accepted", tc.purpose, tc.method, tc.path)
		}
	}
	r := e3Rule(PurposeE3Create, "POST", "/items")
	r.Methods = []string{"POST", "PUT"}
	if _, err := compileRules([]Rule{r}); err == nil {
		t.Fatal("multiple write methods accepted")
	}
	r = e3Rule(PurposeE3Create, "POST", "/items")
	r.CredentialHeaders = http.Header{"Authorization": {"redacted"}}
	if _, err := compileRules([]Rule{r}); err == nil {
		t.Fatal("credential in E3 rule accepted")
	}
}

func TestE3PurposeMatchingAndWorkerIsolation(t *testing.T) {
	rules, err := compileRules([]Rule{
		e3Rule(PurposeE3Identity, "GET", "/identity"),
	})
	if err != nil {
		t.Fatal(err)
	}
	e := &Execution{rules: rules}
	if _, ok := e.matchLocked(coreRequest(t, "GET", "http://127.0.0.1:1/identity")); ok {
		t.Fatal("Worker matched program rule")
	}
	for _, tc := range []struct {
		method, url, purpose string
		want                 bool
	}{
		{"GET", "http://127.0.0.1:1/identity", PurposeE3Identity, true},
		{"POST", "http://127.0.0.1:1/identity", PurposeE3Identity, false},
		{"GET", "http://127.0.0.1:1/identity2", PurposeE3Identity, false},
		{"GET", "http://127.0.0.1:1/identity/sub", PurposeE3Identity, false},
		{"GET", "http://127.0.0.1:1/identity?x=1", PurposeE3Identity, false},
		{"GET", "http://127.0.0.1:1/identity?", PurposeE3Identity, false},
		{"GET", "http://127.0.0.1:1/%2e%2e/identity", PurposeE3Identity, false},
		{"GET", "http://127.0.0.1:1/identity", PurposeE3Read, false},
	} {
		_, got := e.matchPurposeLocked(coreRequest(t, tc.method, tc.url), tc.purpose)
		if got != tc.want {
			t.Errorf("match %s %s %s = %v", tc.method, tc.url, tc.purpose, got)
		}
	}
	rules, err = compileRules([]Rule{e3Rule(PurposeE3Read, "GET", "/items/{id}/detail")})
	if err != nil {
		t.Fatal(err)
	}
	e.rules = rules
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/items/42/detail", true},
		{"/items/42/extra/detail", false},
		{"/items//detail", false},
		{"/items/../detail", false},
		{"/items/42;admin/detail", false},
		{"/items/%2f/detail", false},
		{"/items/42/detail?next=x", false},
	} {
		_, got := e.matchPurposeLocked(coreRequest(t, "GET", "http://127.0.0.1:1"+tc.path), PurposeE3Read)
		if got != tc.want {
			t.Errorf("read %s = %v", tc.path, got)
		}
	}
	rules, err = compileRules([]Rule{e3Rule(PurposeE3Create, "PUT", "/items")})
	if err != nil {
		t.Fatal(err)
	}
	e.rules = rules
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"PUT", "/items", true},
		{"POST", "/items", false},
		{"DELETE", "/items", false},
		{"PUT", "/items2", false},
		{"PUT", "/items/child", false},
		{"PUT", "/items?x=1", false},
	} {
		_, got := e.matchPurposeLocked(coreRequest(t, tc.method, "http://127.0.0.1:1"+tc.path), PurposeE3Create)
		if got != tc.want {
			t.Errorf("create %s %s = %v", tc.method, tc.path, got)
		}
	}
}

func TestE3NoV3CannotStartOrSend(t *testing.T) {
	var reached atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached.Add(1) }))
	t.Cleanup(srv.Close)
	cfg := coreConfig(t, srv.URL, &coreRecorder{}, 2)
	for _, kind := range []ProgramCallKind{ProgramE3Identity, ProgramE3Create, ProgramE3Read} {
		r := e3Rule(string(kind), "GET", "/identity")
		if kind == ProgramE3Create {
			r.Methods, r.PathPrefix = []string{"POST"}, "/items"
		}
		if kind == ProgramE3Read {
			r.PathPrefix = "/items/{id}"
		}
		r.Ref, r.Origin = string(kind), srv.URL
		cfg.Rules = append(cfg.Rules, r)
	}
	e, worker := coreOpen(t, cfg)
	before := e.SnapshotForIntent(1)
	budgetBefore, err := cfg.Budget.Ledger.Remaining(cfg.Budget.Key, cfg.Budget.Binding, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []ProgramCallKind{ProgramE3Identity, ProgramE3Create, ProgramE3Read} {
		if _, err := e.BeginProgramCall(context.Background(), kind, "e3-program"); !errors.Is(err, ErrE3JournalUnavailable) {
			t.Errorf("%s started without v3: %v", kind, err)
		}
	}
	after := e.SnapshotForIntent(1)
	if after.Counts != before.Counts || after.ProgramCounts != before.ProgramCounts || len(e.Events(0, 100)) != 0 {
		t.Fatal("refused E3 call changed accounting or audit")
	}
	if _, r, err := worker.Do(context.Background(), coreRequest(t, "POST", srv.URL+"/items")); !errors.Is(err, ErrDenied) || r.Reason != PolicyDenied || r.ForwardAttempted {
		t.Fatalf("Worker used create rule: %v %+v", err, r)
	}
	if _, _, r, err := worker.do(context.Background(), coreRequest(t, "POST", srv.URL+"/items"), PurposeE3Create, nil); !errors.Is(err, ErrConfig) || r != (Receipt{}) {
		t.Fatalf("Worker forged E3 purpose: %v %+v", err, r)
	}
	if reached.Load() != 0 {
		t.Fatal("E3 refusal reached target")
	}
	budgetAfter, err := cfg.Budget.Ledger.Remaining(cfg.Budget.Key, cfg.Budget.Binding, time.Now())
	if err != nil || budgetAfter != budgetBefore {
		t.Fatal("refused E3 call changed budget", err)
	}
}

func TestE3V2JournalRejectedAndCreateApprovalRequired(t *testing.T) {
	event := journalTestEvent()
	event.IntentID = 0
	for _, kind := range []ProgramCallKind{ProgramE3Identity, ProgramE3Create, ProgramE3Read} {
		event.Program = kind
		if validJournalEvent(event, Version) || validJournalEvent(event, VersionV1) {
			t.Errorf("%s accepted in old journal", kind)
		}
	}
	gate := &e3ApprovalGate{}
	e := &Execution{cfg: Config{ApprovalMode: ApprovalWorkbenchV1, RequestApproval: gate}}
	if !e.needsRequestApproval(PurposeE3Create, "POST", []byte("body"), RequestView{}) {
		t.Fatal("E3 create skipped user approval")
	}
	if e.needsRequestApproval(PurposeE3Read, "GET", nil, RequestView{}) || e.needsRequestApproval(PurposeE3Identity, "GET", nil, RequestView{}) {
		t.Fatal("E3 read or identity unexpectedly needs write approval")
	}
	if strings.Contains(ErrE3JournalUnavailable.Error(), "session") {
		t.Fatal("secret in error")
	}
}

type e3ApprovalGate struct{}

func (*e3ApprovalGate) AwaitRequestApproval(context.Context, RequestApprovalProposal) (bool, error) {
	return false, nil
}
