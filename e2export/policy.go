package e2export

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/e2proxy"
)

const PolicyContentFormat = "abc2lab.e2-policy-content/v1"

// PolicyBehavior는 실제 서버/러너의 고정 상한이다. 회차 참조·IP·키·경로는 받지 않는다.
// 이 encoder는 검토용 v1 제안이며 내용 지문이 승인이나 배치 검증을 대신하지 않는다.
type PolicyBehavior struct {
	MaxWorkerTurns      int
	MaxToolCalls        int
	WorkerTimeout       time.Duration
	MaxPlannerRounds    uint64
	MaxIntentRuns       uint64
	DrainTimeout        time.Duration
	OutputLimit         int
	ProxyHeaderTimeout  time.Duration
	ProxyRequestTimeout time.Duration
	ProxyMaxHeaderBytes int
	ProxyMaxConnections int
}

func (PolicyBehavior) String() string   { return "e2export.PolicyBehavior[protected]" }
func (PolicyBehavior) GoString() string { return "e2export.PolicyBehavior[protected]" }

// PolicyFingerprint는 실행 시작 전과 실제 획득 시 같은 검증/encoder를 사용한다.
// canonical 입력은 내부에서만 보존하고 공유 산출물에는 지문만 내보낸다.
func PolicyFingerprint(cfg e2proxy.Config, maxRequests uint64, behavior PolicyBehavior) (string, error) {
	data, err := canonicalPolicy(cfg, maxRequests, behavior)
	if err != nil {
		return "", err
	}
	defer clear(data)
	return digest(data), nil
}

func canonicalPolicy(cfg e2proxy.Config, maxRequests uint64, b PolicyBehavior) ([]byte, error) {
	if e2proxy.ValidatePolicyTemplate(cfg) != nil || maxRequests == 0 || b.MaxWorkerTurns <= 0 || b.MaxWorkerTurns > 1000 || b.MaxToolCalls <= 0 || b.MaxToolCalls > 1000 || b.WorkerTimeout <= 0 || b.WorkerTimeout > time.Hour || b.MaxPlannerRounds == 0 || b.MaxPlannerRounds > 100 || b.MaxIntentRuns == 0 || b.MaxIntentRuns > 10 || b.DrainTimeout <= 0 || b.DrainTimeout > 2*time.Minute || b.OutputLimit <= 0 || b.OutputLimit > 4<<20 || b.ProxyHeaderTimeout <= 0 || b.ProxyHeaderTimeout > time.Minute || b.ProxyRequestTimeout <= 0 || b.ProxyRequestTimeout > time.Minute || b.ProxyMaxHeaderBytes <= 0 || b.ProxyMaxHeaderBytes > 1<<20 || b.ProxyMaxConnections <= 0 || b.ProxyMaxConnections > 128 || b.ProxyHeaderTimeout > b.ProxyRequestTimeout {
		return nil, ErrConfig
	}
	mode := cfg.HeaderMode
	if mode == "" {
		mode = e2proxy.HeaderPreserve
	}
	var evaluation any
	if cfg.Evaluation != nil {
		evaluation = map[string]any{"llm_backend": cfg.Evaluation.LLMBackend, "target_kind": cfg.Evaluation.TargetKind}
	}
	rules := make([]any, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		u, err := url.Parse(r.Origin)
		if err != nil {
			return nil, ErrConfig
		}
		host := strings.ToLower(u.Hostname())
		port := u.Port()
		if port == "" {
			port = "80"
			if u.Scheme == "https" {
				port = "443"
			}
		}
		n, err := strconv.Atoi(port)
		if err != nil {
			return nil, ErrConfig
		}
		headers := make(map[string]any, len(r.Headers))
		for name, values := range r.Headers {
			for _, value := range values {
				if !utf8.ValidString(value) {
					return nil, ErrConfig
				}
			}
			headers[http.CanonicalHeaderKey(name)] = append([]string{}, values...)
		}
		credentials := make([]string, 0, len(r.CredentialHeaders))
		for name := range r.CredentialHeaders {
			credentials = append(credentials, http.CanonicalHeaderKey(name))
		}
		sort.Strings(credentials)
		if !utf8.ValidString(r.PathPrefix) {
			return nil, ErrConfig
		}
		rules = append(rules, map[string]any{"origin": map[string]any{"scheme": u.Scheme, "host": host, "port": n}, "path_prefix": r.PathPrefix, "path_match": "clean_segment_prefix_v1", "methods": append([]string{}, r.Methods...), "headers": headers, "credential_header_names": credentials, "credential_handling": string(mode)})
	}
	l := cfg.Limits
	content := map[string]any{
		"format": PolicyContentFormat, "mode": "general_http", "approval_mode": string(cfg.ApprovalMode), "enforcement_version": e2proxy.Version, "profile_transform": "explicit_prefix_v1", "header_mode": string(mode), "evaluation": evaluation, "rules": rules,
		"transport": map[string]any{"address_binding": "exact_origin_pinned_ip_v1", "tls": "verified_target_certificate_v1", "redirect": "no_automatic_follow_each_request_rechecked_v1", "retry": "no_automatic_retry_v1", "http": "http1_request_response_v1", "upgrade": "disabled", "connect": "proxy_tls_only_v1"},
		"limits":    map[string]any{"max_requests": maxRequests, "max_attempts": l.MaxAttempts, "max_calls": l.MaxCalls, "max_concurrent": l.MaxConcurrent, "max_body_bytes": l.MaxBodyBytes, "request_timeout_ns": int64(l.RequestTimeout), "call_timeout_ns": int64(l.CallTimeout), "max_denials": l.MaxDenials, "max_transient": l.MaxTransient, "max_unknown": l.MaxUnknown},
		"runtime":   map[string]any{"max_worker_turns": b.MaxWorkerTurns, "max_tool_calls": b.MaxToolCalls, "worker_timeout_ns": int64(b.WorkerTimeout), "max_planner_rounds": b.MaxPlannerRounds, "max_intent_runs": b.MaxIntentRuns, "drain_timeout_ns": int64(b.DrainTimeout), "output_limit": b.OutputLimit, "proxy_header_timeout_ns": int64(b.ProxyHeaderTimeout), "proxy_request_timeout_ns": int64(b.ProxyRequestTimeout), "proxy_max_header_bytes": b.ProxyMaxHeaderBytes, "proxy_max_connections": b.ProxyMaxConnections},
		"tools":     map[string]any{"network_tools": []string{"bash"}, "planner_tools": []string{"graph_overview", "list_findings", "list_facts", "node_detail", "expand_digest", "get_worker_output", "get_worker_trace", "search_all_worker_traces", "list_goals", "add_intent", "list_companies", "list_assets", "list_untested_assets"}, "worker_graph_tools": []string{"graph_overview", "list_facts", "node_detail", "get_worker_output", "list_goals", "record_fact"}, "external_augmentation": "disabled_v1", "other_tools": "disabled_v1", "planner_bash": "disabled_before_real_intent_v1", "egress": "native_network_namespace_no_direct_route_v1", "scope": "http_https_only_v1"},
	}
	// map의 키는 encoding/json의 사전순, 정수는 10진수, UTF-8 문자열은 Go JSON escape로 고정한다.
	// HTML escape를 끄며 개행 없이 고정한다. 배열은 입력 순서를 그대로 보존한다.
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(content) != nil {
		return nil, ErrConfig
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}
