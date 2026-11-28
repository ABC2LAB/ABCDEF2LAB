package server

import (
	"bytes"
	"encoding/json"
	"math"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/diagnosisflow"
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

// ValidateE2HTTPRunPolicyDocument는 제품 실행이 쓴 정책(감사 키 pin 포함)을 실행과 같은 닫힌 형식으로 읽고 내용
// 지문을 돌려준다. 평가 단독 E② 서비스용 ValidateE2HTTPPolicyDocument와 달리 실행 factory가 받는 것을 그대로 받는다:
// 진단 워크플로 칸(workflow)과 승인 방식 셋(사전 승인 범위·요청마다 확인·작업대). 시간·파일 배치는 보지 않아 끝난
// 실행도 읽는다. 작업대 실데이터 조회(계약 workbench-live-data §3·§4)가 쓴다.
func ValidateE2HTTPRunPolicyDocument(raw []byte) (E2HTTPFilePolicy, string, error) {
	return validateE2HTTPPolicyDocumentMode(raw, true, true)
}

func validateE2HTTPPolicyDocument(raw []byte, prepared bool) (E2HTTPFilePolicy, string, error) {
	return validateE2HTTPPolicyDocumentMode(raw, prepared, false)
}

func validateE2HTTPPolicyDocumentMode(raw []byte, prepared, run bool) (E2HTTPFilePolicy, string, error) {
	var policy E2HTTPFilePolicy
	if len(raw) == 0 || len(raw) > e2config.MaxPolicyBytes || !validE2HTTPRuleSelectorFields(raw) || !validE2HTTPPasswordResponseField(raw) || !utf8.Valid(raw) || strictJSON(raw, &policy) != nil {
		return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
	}
	top := []string{"approval_mode", "format", "run_ref", "reset_ref", "policy_ref", "case_ref", "policy_revision", "budget_ref", "owner_ref", "account_ref", "session_ref", "valid_from", "valid_until", "max_requests", "header_mode", "evaluation", "state_dir", "helper", "helper_sha256", "ca_cert", "ca_key", "rules", "limits", "proxy", "runtime"}
	if prepared {
		top = append(top, "audit_key")
	}
	optional := []string{"upstream_ca", "auth", "login_attempts"}
	approvalMode := policy.ApprovalMode == e2proxy.ApprovalPreauthorizedScope
	if run {
		// e3_write_consent·e3_resettable은 제품 발급(productE3PolicyRules)이 E③ 확정 단계를 켤 때 싣는 칸이다. factory는
		// 같은 칸을 받으므로 실행 정책 문서도 받는다(선언 있는 회차의 확정 몫 읽기가 이 검증을 쓴다).
		optional = append(optional, "workflow", "e3_write_consent", "e3_resettable", "selected_account_ref", "exploration_login_evidence", "exploration_source_digest")
		approvalMode = approvalMode || policy.ApprovalMode == e2proxy.ApprovalConfirmEveryRequest || policy.ApprovalMode == e2proxy.ApprovalWorkbenchV1
	}
	if !e2HTTPExactJSON(raw, top, optional, "evaluation", "upstream_ca") || !validE2HTTPFileAuth(policy.Auth, policy.HeaderMode, policy.Rules) ||
		policy.Format != E2HTTPFilePolicyFormat || !approvalMode ||
		policy.HeaderMode == "" || policy.MaxRequests == 0 || policy.MaxRequests > uint64(math.MaxInt) || len(policy.Rules) == 0 || len(policy.Rules) > 128 ||
		!policy.ValidUntil.After(policy.ValidFrom) {
		return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
	}
	if policy.ExplorationLoginEvidence != "" && (!run || policy.ExplorationLoginEvidence != "required/v2" || policy.SelectedAccountRef != "" || !e2HTTPHash(policy.ExplorationSourceDigest)) ||
		policy.ExplorationLoginEvidence == "" && policy.ExplorationSourceDigest != "" {
		return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
	}
	// 판정 설정은 있으면 맞는 값이어야 하고, 모델 로그인 인증 칸은 판정 설정 없이 받지 않는다(상한 없이 로그인이 나가지 않게).
	if !validE2HTTPLoginAttempts(policy) {
		return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
	}
	if policy.SelectedAccountRef != "" && (!run || policy.Auth == nil || len(policy.Auth.Accounts) != 1 ||
		policy.HeaderMode != e2proxy.HeaderTestAccountPassthrough || policy.ApprovalMode != e2proxy.ApprovalPreauthorizedScope ||
		CandidateAccountRevision(policy.Auth.Accounts[0].Label, policy.Auth.Accounts[0].Credential.SHA256) != policy.SelectedAccountRef) {
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
	if policy.Workflow != nil && (!run || !e2HTTPExactJSON(document["workflow"], []string{"format", "mode", "input_sha256"}, []string{"excluded_paths"}) ||
		policy.Workflow.Format != diagnosisflow.Version || policy.Workflow.Mode == "" || !e2HTTPHash(policy.Workflow.InputSHA256)) {
		return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
	}
	// 제외 경로 칸은 쓰면 비지 않아야 한다. null·빈 배열이 "제외 없음"으로 조용히 바뀌지 않게 한다(E② 정책 검사가 패턴 문법을 본다).
	if policy.Workflow != nil {
		var workflow map[string]json.RawMessage
		_ = json.Unmarshal(document["workflow"], &workflow)
		if _, present := workflow["excluded_paths"]; present && len(policy.Workflow.ExcludedPaths) == 0 {
			return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
		}
	}
	if policy.Evaluation != nil {
		if !e2HTTPExactJSON(document["evaluation"], []string{"run_ref", "llm_backend", "target_kind"}, nil) {
			return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
		}
	}
	// 응답 전달·검사 상한(C16 결정안 3)은 선택 칸이다. 없으면 요청 본문 상한을 같이 쓴다. E③ 확정 단계 몫은 실행 정책에서만
	// 받는다(둘 다 있거나 둘 다 없음, 몫은 실행 요청 상한의 1/4 이하).
	limitOptional := []string{"max_response_body_bytes", "max_response_scan_bytes"}
	if run {
		limitOptional = append(limitOptional, "e3_confirmation_requests", "e3_confirmation_timeout_ms")
	}
	share, shareOK := policy.e3ConfirmationShare()
	if !shareOK || share != nil && !run {
		return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
	}
	// 쓰기 동의·리셋 가능 표시는 E③ 확정 몫 칸이 있는 실행 정책에만 실린다.
	if (policy.E3WriteConsent || policy.E3Resettable) && share == nil {
		return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
	}
	for name, fields := range map[string]struct{ required, optional []string }{
		"limits": {[]string{"max_attempts", "max_calls", "max_concurrent", "max_body_bytes", "request_timeout_ms", "call_timeout_ms", "max_denials", "max_transient", "max_unknown"},
			limitOptional},
		"proxy":   {[]string{"header_timeout_ms", "request_timeout_ms", "max_header_bytes", "max_connections"}, nil},
		"runtime": {[]string{"max_worker_turns", "max_tool_calls", "worker_timeout_ms", "max_planner_rounds", "max_intent_runs", "drain_timeout_ms", "output_limit"}, nil},
	} {
		if !e2HTTPExactJSON(document[name], fields.required, fields.optional) {
			return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
		}
	}
	var ruleJSON []json.RawMessage
	if json.Unmarshal(document["rules"], &ruleJSON) != nil || len(ruleJSON) != len(policy.Rules) {
		return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
	}
	rules := make([]e2proxy.Rule, 0, len(policy.Rules))
	for i, rule := range policy.Rules {
		if !e2HTTPExactJSON(ruleJSON[i], []string{"approval_display", "ref", "origin", "connect_ip", "path_prefix", "path_scope_ref", "methods"}, []string{"credentials", "tls_spki_sha256", "purpose", "path_match", "exact_query", "request_headers", "strict_headers"}, "credentials") {
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
		if !validSelectedActionInputs(nil, rule.RequestHeaders) || !validSelectedPolicyQuery(rule.ExactQuery) ||
			(rule.ExactQuery != "" || len(rule.RequestHeaders) > 0 || rule.StrictHeaders) && (rule.PathMatch != "exact" || rule.Purpose != "" || len(rule.Methods) != 1 ||
				rule.Methods[0] != "GET" && rule.Methods[0] != "HEAD") {
			return E2HTTPFilePolicy{}, "", ErrE2HTTPHost
		}
		rules = append(rules, e2proxy.Rule{Ref: rule.Ref, Purpose: rule.Purpose, Origin: rule.Origin, ConnectIP: ip, TLSSPKISHA256: rule.TLSSPKISHA256, PathPrefix: rule.PathPrefix, PathMatch: rule.PathMatch, ExactQuery: rule.ExactQuery, StrictHeaders: rule.StrictHeaders, Headers: selectedActionHeaders(rule.RequestHeaders), PathScopeRef: rule.PathScopeRef, Methods: rule.Methods})
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
	cfg := e2proxy.Config{ApprovalMode: policy.ApprovalMode, Scope: transmit.Scope{RunRef: policy.RunRef}, HeaderMode: policy.HeaderMode, PasswordResponse: e2HTTPPasswordResponse(policy.Auth),
		Evaluation: policy.Evaluation, Rules: rules, ExcludedPaths: policy.excludedPaths(), Limits: e2proxy.Limits{MaxAttempts: policy.Limits.MaxAttempts, MaxCalls: policy.Limits.MaxCalls,
			MaxConcurrent: policy.Limits.MaxConcurrent, MaxBodyBytes: policy.Limits.MaxBodyBytes, MaxResponseBodyBytes: policy.Limits.MaxResponseBodyBytes,
			MaxResponseScanBytes: policy.Limits.MaxResponseScanBytes, RequestTimeout: requestTimeout, CallTimeout: callTimeout,
			MaxDenials: policy.Limits.MaxDenials, MaxTransient: policy.Limits.MaxTransient, MaxUnknown: policy.Limits.MaxUnknown}}
	fingerprint, err := e2export.PolicyFingerprint(cfg, policy.MaxRequests, e2export.PolicyBehavior{
		MaxWorkerTurns: policy.Runtime.MaxWorkerTurns, MaxToolCalls: policy.Runtime.MaxToolCalls, WorkerTimeout: workerTimeout,
		MaxPlannerRounds: policy.Runtime.MaxPlannerRounds, MaxIntentRuns: policy.Runtime.MaxIntentRuns, DrainTimeout: drainTimeout,
		OutputLimit: policy.Runtime.OutputLimit, ProxyHeaderTimeout: headerTimeout, ProxyRequestTimeout: proxyTimeout,
		ProxyMaxHeaderBytes: policy.Proxy.MaxHeaderBytes, ProxyMaxConnections: policy.Proxy.MaxConnections, E3Confirmation: share,
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

// validE2HTTPRuleSelectorFields는 명시한 선택 칸이 접두 경로나 일반 Worker용 기본값으로 해석되는 것을 막는다.
func validE2HTTPRuleSelectorFields(raw []byte) bool {
	var document map[string]json.RawMessage
	if json.Unmarshal(raw, &document) != nil {
		return false
	}
	for name := range document {
		if strings.EqualFold(name, "rules") && name != "rules" {
			return false
		}
	}
	var rules []map[string]json.RawMessage
	if json.Unmarshal(document["rules"], &rules) != nil || rules == nil {
		return false
	}
	for _, rule := range rules {
		for name, value := range rule {
			switch {
			case strings.EqualFold(name, "path_match"):
				if name != "path_match" || !bytes.Equal(value, []byte(`"exact"`)) {
					return false
				}
			case strings.EqualFold(name, "purpose"):
				var purpose string
				if name != "purpose" || json.Unmarshal(value, &purpose) != nil || purpose == "" {
					return false
				}
			}
		}
	}
	return true
}

// 선택 모드의 null·별칭이 이전 전체 보류 기본값으로 해석되지 않게 factory와 정적 검증이 함께 쓴다.
func validE2HTTPPasswordResponseField(raw []byte) bool {
	var document map[string]json.RawMessage
	if json.Unmarshal(raw, &document) != nil {
		return false
	}
	for name := range document {
		if strings.EqualFold(name, "auth") && name != "auth" {
			return false
		}
	}
	var auth map[string]json.RawMessage
	if value, ok := document["auth"]; ok && !bytes.Equal(value, []byte("null")) {
		if json.Unmarshal(value, &auth) != nil {
			return false
		}
	}
	for name, value := range auth {
		if strings.EqualFold(name, "password_response") && (name != "password_response" || !bytes.Equal(value, []byte(`"mask_spans_v1"`)) && !bytes.Equal(value, []byte(`"model_visible_v1"`))) {
			return false
		}
	}
	return true
}
