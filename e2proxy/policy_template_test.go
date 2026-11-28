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

// 모델이 직접 로그인하는 결속은 프로그램 인증 준비 규칙이 없다. 대상 응답의 시험 계정 비밀번호는 계속 모델에 줄 수 있어야
// 한다. 기존 mask_spans_v1은 인증 준비 규칙이 없는 정책에서 계속 거부한다.
func TestPolicyTemplateModelVisibleWithoutAuthPrep(t *testing.T) {
	cfg := policyTemplateFixture()
	cfg.HeaderMode = HeaderTestAccountPassthrough
	cfg.PasswordResponse = PasswordResponseModelVisible
	if err := ValidatePolicyTemplate(cfg); err != nil {
		t.Fatalf("모델 로그인 정책 거부: %v", err)
	}
	cfg.PasswordResponse = PasswordResponseMaskSpans
	if err := ValidatePolicyTemplate(cfg); err == nil {
		t.Fatal("인증 준비 규칙 없는 mask_spans 정책 수용")
	}
	cfg.PasswordResponse = PasswordResponseModelVisible
	cfg.HeaderMode = HeaderPreserve
	if err := ValidatePolicyTemplate(cfg); err == nil {
		t.Fatal("시험 계정 헤더 모드 밖에서 model_visible 정책 수용")
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
