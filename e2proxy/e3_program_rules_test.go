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
		{PurposeE3Identity, "GET", "/" + strings.Repeat("a", 511)},
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
		{PurposeE3Identity, "GET", "/identity check"},
		{PurposeE3Identity, "GET", "/" + strings.Repeat("a", 512)},
		{PurposeE3Identity, "GET", "/a{b}"},
		{PurposeE3Identity, "GET", "/"},
		{PurposeE3Identity, "GET", "/identity/"},
		{PurposeE3Create, "POST", "/"},
		{PurposeE3Create, "POST", "/items/"},
		{PurposeE3Read, "GET", "/items/{id}/"},
		{PurposeE3Read, "GET", "/{id}"},
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
	identity := e3Rule(PurposeE3Identity, "GET", "/identity")
	identity.Origin = "https://127.0.0.1:1"
	worker := identity
	worker.Ref, worker.Purpose, worker.PathMatch = "worker", "", "exact"
	rules, err := compileRules([]Rule{identity, worker})
	if err != nil {
		t.Fatal(err)
	}
	e := &Execution{rules: rules[:1]}
	if _, ok := e.matchLocked(coreRequest(t, "GET", identity.Origin+"/identity")); ok {
		t.Fatal("Worker matched program rule")
	}
	e.rules = rules
	for _, tc := range []struct {
		method, url, purpose string
		want                 bool
	}{
		{"GET", identity.Origin + "/identity", PurposeE3Identity, true},
		{"GET", "http://127.0.0.1:1/identity", PurposeE3Identity, false},
		{"POST", identity.Origin + "/identity", PurposeE3Identity, false},
		{"GET", identity.Origin + "/identity2", PurposeE3Identity, false},
		{"GET", identity.Origin + "/identity/sub", PurposeE3Identity, false},
		{"GET", identity.Origin + "/identity?x=1", PurposeE3Identity, false},
		{"GET", identity.Origin + "/identity?", PurposeE3Identity, false},
		{"GET", identity.Origin + "/%2e%2e/identity", PurposeE3Identity, false},
		{"GET", identity.Origin + "/identity", PurposeE3Read, false},
	} {
		_, got := e.matchPurposeLocked(coreRequest(t, tc.method, tc.url), tc.purpose)
		if got != tc.want {
			t.Errorf("match %s %s %s = %v", tc.method, tc.url, tc.purpose, got)
		}
	}
	worker.Origin, worker.PathPrefix, worker.PathMatch = "http://127.0.0.1:1", "/", ""
	rules, err = compileRules([]Rule{e3Rule(PurposeE3Read, "GET", "/items/{id}/detail"), worker})
	if err != nil {
		t.Fatal(err)
	}
	e.rules = rules
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/items/42/detail", true},
		{"/admin/42/delete", false},
		{"/items/42/other", false},
		{"/items/42", false},
		{"/items/42/extra/detail", false},
		{"/items/42/detail/extra", false},
		{"/items//detail", false},
		{"/items/../detail", false},
		{"/items/42;admin/detail", false},
		{"/items/%3f/detail", false},
		{"/items/%7b42%7d/detail", false},
		{"/items/%2f/detail", false},
		{"/items/42/detail?next=x", false},
	} {
		_, got := e.matchPurposeLocked(coreRequest(t, "GET", "http://127.0.0.1:1"+tc.path), PurposeE3Read)
		if got != tc.want {
			t.Errorf("read %s = %v", tc.path, got)
		}
	}
	if matchE3ReadPath("/items/{id}", "/items/") {
		t.Error("read accepted an empty ID")
	}
	worker.Methods = []string{"PUT"}
	rules, err = compileRules([]Rule{e3Rule(PurposeE3Create, "PUT", "/items"), worker})
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

func TestE3ProgramPolicyRequiresWorkerApproval(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"bound", nil},
		{"scheme", func(c *Config) {
			c.Rules[1].Origin = "http://fixture.invalid:443"
			c.Rules[0].TLSSPKISHA256, c.Rules[1].TLSSPKISHA256 = "", ""
		}},
		{"host", func(c *Config) { c.Rules[1].Origin = "https://other.invalid" }},
		{"port", func(c *Config) { c.Rules[1].Origin = "https://fixture.invalid:8443" }},
		{"ip", func(c *Config) { c.Rules[1].ConnectIP = netip.MustParseAddr("127.0.0.2") }},
		{"tls_pin", func(c *Config) { c.Rules[1].TLSSPKISHA256 = strings.Repeat("b", 64) }},
		{"missing_tls_pin", func(c *Config) { c.Rules[1].TLSSPKISHA256 = "" }},
		{"method", func(c *Config) { c.Rules[0].Methods = []string{"HEAD"} }},
		{"prefix_boundary", func(c *Config) { c.Rules[1].PathPrefix = "/allowed-other/items/{id}" }},
		{"exact_worker_path", func(c *Config) { c.Rules[0].PathPrefix, c.Rules[0].PathMatch = "/allowed/items", "exact" }},
		{"only_one_id_approved", func(c *Config) { c.Rules[0].PathPrefix = "/allowed/items/42" }},
		{"whole_read_template_excluded", func(c *Config) { c.ExcludedPaths = []string{"/allowed/items/*/detail"} }},
		{"no_worker_rule", func(c *Config) { c.Rules = c.Rules[1:] }},
		{"fixed_path_excluded", func(c *Config) {
			c.Rules[1].Purpose, c.Rules[1].PathPrefix = PurposeE3Identity, "/allowed/identity"
			c.ExcludedPaths = []string{"/allowed/identity"}
		}},
		{"no_worker_for_fixed_path", func(c *Config) {
			c.Rules[1].Purpose, c.Rules[1].PathPrefix = PurposeE3Identity, "/allowed/identity"
			c.Rules = c.Rules[1:]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := coreConfig(t, "https://fixture.invalid", &coreRecorder{}, 2)
			cfg.Rules[0].TLSSPKISHA256 = strings.Repeat("a", 64)
			program := e3Rule(PurposeE3Read, "GET", "/allowed/items/{id}/detail")
			program.Origin = "https://FIXTURE.invalid:443"
			program.TLSSPKISHA256 = cfg.Rules[0].TLSSPKISHA256
			program.ConnectIP = netip.MustParseAddr("::ffff:127.0.0.1")
			cfg.Rules = append(cfg.Rules, program)
			if tc.change != nil {
				tc.change(&cfg)
			}
			templateErr := ValidatePolicyTemplate(cfg)
			execution, err := NewExecution(t.Context(), cfg)
			if tc.change == nil {
				if templateErr != nil || err != nil {
					t.Fatalf("bound program rule rejected: %v / %v", templateErr, err)
				}
				return
			}
			if !errors.Is(templateErr, ErrConfig) || !errors.Is(err, ErrConfig) || execution != nil {
				t.Fatalf("unapproved program rule accepted: %v / %v", templateErr, err)
			}
		})
	}
}

func TestE3PurposeChecksConcreteExclusionsAndCurrentWorkerRules(t *testing.T) {
	origin := "https://fixture.invalid"
	cfg := coreConfig(t, origin, &coreRecorder{}, 2)
	program := e3Rule(PurposeE3Read, "GET", "/allowed/items/{id}")
	program.Origin = origin
	cfg.Rules = append(cfg.Rules, program)
	cfg.ExcludedPaths = []string{"/allowed/items/private", "/allowed/items/{id}"}
	e, worker := coreOpen(t, cfg)
	match := func(path string) bool {
		_, ok := e.matchPurposeLocked(coreRequest(t, "GET", origin+path), PurposeE3Read)
		return ok
	}
	if !match("/allowed/items/42") {
		t.Fatal("ordinary ID rejected")
	}
	if match("/allowed/items/%70rivate") {
		t.Fatal("decoded path bypassed the exclusion")
	}
	if err := e.Restrict(t.Context(), Restriction{
		PolicyRevision: "revision2", ExpiresAt: time.Now().Add(time.Minute), RuleRefs: []string{program.Ref}, Methods: []string{"GET"},
	}); err != nil {
		t.Fatal(err)
	}
	if match("/allowed/items/42") {
		t.Fatal("purpose rule outlived Worker approval")
	}
	if worker.CheckCONNECTAuthority("fixture.invalid:443") {
		t.Fatal("purpose rule kept Worker CONNECT approval")
	}
}

func TestE3NoV3CannotStartOrSend(t *testing.T) {
	var reached atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached.Add(1) }))
	t.Cleanup(srv.Close)
	cfg := coreConfig(t, srv.URL, &coreRecorder{}, 2)
	cfg.Rules[0].Methods = []string{"GET", "POST"}
	for _, kind := range []ProgramCallKind{ProgramE3Identity, ProgramE3Create, ProgramE3Read} {
		r := e3Rule(string(kind), "GET", "/allowed/identity")
		if kind == ProgramE3Create {
			r.Methods, r.PathPrefix = []string{"POST"}, "/allowed/items"
		}
		if kind == ProgramE3Read {
			r.PathPrefix = "/allowed/items/{id}"
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
		t.Fatalf("Worker sent outside its scope: %v %+v", err, r)
	}
	if _, _, r, err := worker.do(context.Background(), coreRequest(t, "POST", srv.URL+"/allowed/items"), PurposeE3Create, nil); !errors.Is(err, ErrConfig) || r != (Receipt{}) {
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
		for _, version := range []string{VersionV1, Version} {
			event.Version = version
			if validJournalEvent(event, version) {
				t.Errorf("%s accepted in %s journal", kind, version)
			}
		}
	}
	gate := &e3ApprovalGate{}
	e := &Execution{cfg: Config{ApprovalMode: ApprovalWorkbenchV1, RequestApproval: gate}}
	if !e.needsRequestApproval(PurposeE3Create, "POST", []byte("body"), RequestView{}) {
		t.Fatal("E3 create skipped user approval")
	}
	e.cfg.ApprovalMode = ApprovalConfirmEveryRequest
	if !e.needsRequestApproval(PurposeE3Create, "POST", []byte("body"), RequestView{}) {
		t.Fatal("E3 create skipped per-request approval")
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
