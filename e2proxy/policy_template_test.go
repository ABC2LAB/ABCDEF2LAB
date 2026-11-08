package e2proxy

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"testing"
	"time"
)

func policyTemplateFixture() Config {
	return Config{Rules: []Rule{{Ref: "fixture-rule", Origin: "http://127.0.0.1:18080", ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/fixture", PathScopeRef: "fixture-path", Methods: []string{"GET"}}}, HeaderMode: HeaderPreserve, Limits: Limits{MaxAttempts: 2, MaxCalls: 2, MaxConcurrent: 1, MaxBodyBytes: 1024, RequestTimeout: time.Second, CallTimeout: time.Second, MaxDenials: 1, MaxTransient: 1, MaxUnknown: 1}}
}

func TestPolicyTemplateStaticValidationDoesNotGrantExecution(t *testing.T) {
	cfg := policyTemplateFixture()
	if err := ValidatePolicyTemplate(cfg); err != nil {
		t.Fatal(err)
	}
	if execution, err := NewExecution(context.Background(), cfg); execution != nil || !errors.Is(err, ErrConfig) {
		t.Fatal("static template became an execution without actual task, intent, recorder, or budget")
	}
}

func TestPolicyTemplateRejectsMalformedRulesLimitsAndHeaders(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Config)
	}{
		{"zero_limit", func(c *Config) { c.Limits.MaxAttempts = 0 }},
		{"unsupported_scheme", func(c *Config) { c.Rules[0].Origin = "ftp://127.0.0.1" }},
		{"unscoped_path", func(c *Config) { c.Rules[0].PathPrefix = "/fixture%2Fadmin" }},
		{"unspecified_ip", func(c *Config) { c.Rules[0].ConnectIP = netip.IPv4Unspecified() }},
		{"duplicate_method", func(c *Config) { c.Rules[0].Methods = []string{"GET", "GET"} }},
		{"unsupported_method", func(c *Config) { c.Rules[0].Methods = []string{"CONNECT"} }},
		{"caller_credentials_in_default_mode", func(c *Config) {
			c.Rules[0].CredentialHeaders = http.Header{"Authorization": []string{"fixture-private-value"}}
		}},
		{"unknown_header_mode", func(c *Config) { c.HeaderMode = HeaderMode("unknown") }},
		{"evaluation_missing_run_binding", func(c *Config) {
			c.HeaderMode = HeaderEvaluationCallerPassthrough
			c.Evaluation = &EvaluationCallerContext{RunRef: "other", LLMBackend: EvaluationLLMBackendLocal, TargetKind: EvaluationPublicPractice}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := policyTemplateFixture()
			tc.change(&cfg)
			if ValidatePolicyTemplate(cfg) == nil {
				t.Fatal("invalid static template accepted")
			}
		})
	}
}
