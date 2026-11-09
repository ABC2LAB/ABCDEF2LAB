package server

import (
	"bytes"
	"encoding/json"
	"math"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/e2export"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
)

// ValidateE2HTTPPolicyDocument는 기본 정책(prepared=false)과 감사 키 pin을
// 채운 정책(prepared=true)의 닫힌 JSON 형식·정적 집행 의미를 검사한다.
// 보호 파일의 존재·내용·권한과 현재 권위는 factory가 별도로 검사한다.
func ValidateE2HTTPPolicyDocument(raw []byte, prepared bool) (E2HTTPFilePolicy, error) {
	policy, _, err := validateE2HTTPPolicyDocument(raw, prepared)
	return policy, err
}

// E2HTTPPolicyContentFingerprint는 ValidateE2HTTPPolicyDocument를 통과한 정책의 내용 지문을 돌려준다.
// 평가 러너가 비교할 예상값을 실제 회차 전에 계산하는 용도이며, 승인이나 배치 검증을 대신하지 않는다.
func E2HTTPPolicyContentFingerprint(raw []byte, prepared bool) (string, error) {
	policy, fingerprint, err := validateE2HTTPPolicyDocument(raw, prepared)
	if err != nil {
		return "", err
	}
	// 자격 헤더 이름은 pin 파일을 읽어야 알 수 있고 지문에 들어간다. 파일 없이 계산하면 factory와 다른 값이 나온다.
	for _, rule := range policy.Rules {
		if rule.Credentials != nil {
			return "", ErrE2HTTPHost
		}
	}
	return fingerprint, nil
}

func validateE2HTTPPolicyDocument(raw []byte, prepared bool) (E2HTTPFilePolicy, string, error) {
	var policy E2HTTPFilePolicy
	if len(raw) == 0 || len(raw) > e2config.MaxPolicyBytes || !utf8.Valid(raw) || strictJSON(raw, &policy) != nil {
		return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
	}
	top := []string{"approval_mode", "format", "run_ref", "reset_ref", "policy_ref", "case_ref", "policy_revision", "budget_ref", "owner_ref", "account_ref", "session_ref", "valid_from", "valid_until", "max_requests", "header_mode", "evaluation", "state_dir", "helper", "helper_sha256", "ca_cert", "ca_key", "rules", "limits", "proxy", "runtime"}
	if prepared {
		top = append(top, "audit_key")
	}
	if !e2HTTPExactJSON(raw, top, []string{"upstream_ca"}, "evaluation", "upstream_ca") ||
		policy.Format != E2HTTPFilePolicyFormat || policy.ApprovalMode != e2proxy.ApprovalPreauthorizedScope ||
		policy.HeaderMode == "" || policy.MaxRequests == 0 || policy.MaxRequests > uint64(math.MaxInt) || len(policy.Rules) == 0 || len(policy.Rules) > 128 ||
		!policy.ValidUntil.After(policy.ValidFrom) || policy.ValidFrom.After(time.Now()) || !policy.ValidUntil.After(time.Now()) {
		return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
	}
	for _, value := range []string{policy.RunRef, policy.ResetRef, policy.PolicyRef, policy.CaseRef, policy.PolicyRevision, policy.BudgetRef, policy.OwnerRef, policy.AccountRef, policy.SessionRef} {
		if !e2HTTPHostRef.MatchString(value) {
			return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
		}
	}
	if !e2HTTPHash(policy.HelperSHA256) || policy.StateDir == "" || policy.Helper == "" {
		return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
	}
	for _, name := range []string{"ca_cert", "ca_key", "audit_key", "upstream_ca"} {
		if name == "audit_key" && !prepared {
			continue
		}
		if name == "upstream_ca" && policy.UpstreamCA == nil {
			continue
		}
		var pin E2HTTPFilePin
		switch name {
		case "ca_cert":
			pin = policy.CACert
		case "ca_key":
			pin = policy.CAKey
		case "audit_key":
			pin = policy.AuditKey
		case "upstream_ca":
			pin = *policy.UpstreamCA
		}
		if !e2HTTPHostRef.MatchString(pin.File) || !e2HTTPHash(pin.SHA256) {
			return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
		}
	}
	var document map[string]json.RawMessage
	_ = json.Unmarshal(raw, &document)
	for _, name := range []string{"ca_cert", "ca_key", "audit_key", "upstream_ca"} {
		if field, ok := document[name]; ok && !bytes.Equal(field, []byte("null")) && !e2HTTPExactJSON(field, []string{"file", "sha256"}, nil) {
			return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
		}
	}
	if policy.Evaluation != nil {
		if !e2HTTPExactJSON(document["evaluation"], []string{"run_ref", "llm_backend", "target_kind"}, nil) {
			return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
		}
	}
	for name, fields := range map[string][]string{
		"limits":  {"max_attempts", "max_calls", "max_concurrent", "max_body_bytes", "request_timeout_ms", "call_timeout_ms", "max_denials", "max_transient", "max_unknown"},
		"proxy":   {"header_timeout_ms", "request_timeout_ms", "max_header_bytes", "max_connections"},
		"runtime": {"max_worker_turns", "max_tool_calls", "worker_timeout_ms", "max_planner_rounds", "max_intent_runs", "drain_timeout_ms", "output_limit"},
	} {
		if !e2HTTPExactJSON(document[name], fields, nil) {
			return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
		}
	}
	var ruleJSON []json.RawMessage
	if json.Unmarshal(document["rules"], &ruleJSON) != nil || len(ruleJSON) != len(policy.Rules) {
		return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
	}
	rules := make([]e2proxy.Rule, 0, len(policy.Rules))
	for i, rule := range policy.Rules {
		if !e2HTTPExactJSON(ruleJSON[i], []string{"approval_display", "ref", "origin", "connect_ip", "path_prefix", "path_scope_ref", "methods"}, []string{"credentials"}, "credentials") {
			return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
		}
		var obj map[string]json.RawMessage
		_ = json.Unmarshal(ruleJSON[i], &obj)
		if !e2HTTPExactJSON(obj["approval_display"], []string{"public_path", "query_keys", "json_fields"}, nil, "query_keys", "json_fields") {
			return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
		}
		if value, ok := obj["credentials"]; ok && !bytes.Equal(value, []byte("null")) {
			if policy.HeaderMode != e2proxy.HeaderProtectedSessionProposal || !e2HTTPExactJSON(value, []string{"file", "sha256"}, nil) || rule.Credentials == nil ||
				!e2HTTPHostRef.MatchString(rule.Credentials.File) || !e2HTTPHash(rule.Credentials.SHA256) {
				return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
			}
		}
		ip, err := netip.ParseAddr(rule.ConnectIP)
		if err != nil {
			return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
		}
		rules = append(rules, e2proxy.Rule{Ref: rule.Ref, Origin: rule.Origin, ConnectIP: ip, PathPrefix: rule.PathPrefix, PathScopeRef: rule.PathScopeRef, Methods: rule.Methods})
	}
	requestTimeout, ok1 := e2HTTPMilliseconds(policy.Limits.RequestTimeoutMS, time.Minute)
	callTimeout, ok2 := e2HTTPMilliseconds(policy.Limits.CallTimeoutMS, time.Hour)
	headerTimeout, ok3 := e2HTTPMilliseconds(policy.Proxy.HeaderTimeoutMS, time.Minute)
	proxyTimeout, ok4 := e2HTTPMilliseconds(policy.Proxy.RequestTimeoutMS, time.Minute)
	workerTimeout, ok5 := e2HTTPMilliseconds(policy.Runtime.WorkerTimeoutMS, time.Hour)
	drainTimeout, ok6 := e2HTTPMilliseconds(policy.Runtime.DrainTimeoutMS, 2*time.Minute)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 {
		return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
	}
	cfg := e2proxy.Config{ApprovalMode: policy.ApprovalMode, Scope: transmit.Scope{RunRef: policy.RunRef}, HeaderMode: policy.HeaderMode,
		Evaluation: policy.Evaluation, Rules: rules, Limits: e2proxy.Limits{MaxAttempts: policy.Limits.MaxAttempts, MaxCalls: policy.Limits.MaxCalls,
			MaxConcurrent: policy.Limits.MaxConcurrent, MaxBodyBytes: policy.Limits.MaxBodyBytes, RequestTimeout: requestTimeout, CallTimeout: callTimeout,
			MaxDenials: policy.Limits.MaxDenials, MaxTransient: policy.Limits.MaxTransient, MaxUnknown: policy.Limits.MaxUnknown}}
	fingerprint, err := e2export.PolicyFingerprint(cfg, policy.MaxRequests, e2export.PolicyBehavior{
		MaxWorkerTurns: policy.Runtime.MaxWorkerTurns, MaxToolCalls: policy.Runtime.MaxToolCalls, WorkerTimeout: workerTimeout,
		MaxPlannerRounds: policy.Runtime.MaxPlannerRounds, MaxIntentRuns: policy.Runtime.MaxIntentRuns, DrainTimeout: drainTimeout,
		OutputLimit: policy.Runtime.OutputLimit, ProxyHeaderTimeout: headerTimeout, ProxyRequestTimeout: proxyTimeout,
		ProxyMaxHeaderBytes: policy.Proxy.MaxHeaderBytes, ProxyMaxConnections: policy.Proxy.MaxConnections,
	})
	if err != nil {
		return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
	}
	return policy, fingerprint, nil
}

func e2HTTPHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func e2HTTPExactJSON(raw []byte, required, optional []string, nullable ...string) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil || len(obj) < len(required) || len(obj) > len(required)+len(optional) {
		return false
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, key := range required {
		if _, ok := obj[key]; !ok {
			return false
		}
		allowed[key] = true
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key, value := range obj {
		if !allowed[key] {
			return false
		}
		if bytes.Equal(value, []byte("null")) {
			permit := false
			for _, name := range nullable {
				permit = permit || name == key
			}
			if !permit {
				return false
			}
		}
	}
	return true
}
