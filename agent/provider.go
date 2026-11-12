// Package agent wires real LLM-driven planner and work agents (on top of the
// agent-core SDK) to the dual SQLite graph. See docs/ARTEX-架构设计.md
// §4.3 (planner) and §4.4 (work agent).
//
// Provider configuration is read from the environment so the system runs with
// any Anthropic- or OpenAI-format endpoint. If no key is configured, FromEnv
// returns ok=false and the exploration engine stays idle (an LLM is required).
package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
	acperm "github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/transcript"
)

// Config describes the LLM backend resolved from the environment.
type Config struct {
	Format  llm.Format
	BaseURL string
	APIKey  string
	Model   string
	// Proxy routes all LLM requests through the given proxy URL (http/https/socks5,
	// optionally with user:pass@ credentials). Empty means direct — it does NOT
	// fall back to the standard *_PROXY environment variables.
	Proxy string
	// RatePerSecond / RatePerMinute cap the shared request rate across ALL agents
	// using the provider (0 = that window unlimited).
	RatePerSecond float64
	RatePerMinute float64
	// ContextWindowK is the model's context window in K tokens (user-configured),
	// used to size compaction thresholds. 0 = default; see CompactionWindow.
	ContextWindowK int
	// ThinkingType 独立控制思考「开关」字段(thinking.type):
	//   "" = 不发送(默认,兼容不支持该字段的模型); "disabled" = 显式关闭;
	//   "enabled" = 开启. 与 ReasoningEffort 完全解耦——有些接口没有 thinking 字段、
	//   只靠强度参数就能激活思考,故两者可各自单独设置.
	ThinkingType string
	// ReasoningEffort 独立控制思考「强度」字段:
	//   "" = 不发送(默认); "low"/"medium"/"high"/"xhigh"/"max" = 对应强度.
	//   OpenAI 映射为顶层 reasoning_effort;Anthropic 映射为 output_config.effort.
	ReasoningEffort string
	// Stream 控制该 profile 是否使用流式(SSE)接口。true(默认)= 流式;false = 真·
	// 非流式(发 stream:false,一次性拿完整 JSON,走 Provider.Complete)。非流式可绕开
	// 某些网关糟糕的 SSE 实现(空帧、思考字段丢帧),代价是失去运行中的实时进度/实时
	// token 计数。映射为 agentcore.Options.NonStreaming = !Stream。
	Stream bool
	// MaxTokens 是单次回复的输出上限(token)。0 = 不发送该字段,由服务端默认值决定
	// (历史行为)。与 ContextWindowK 不同:后者是模型总容量,只在本地用来算压缩阈值,
	// 不出现在请求里;本值随每次请求发出。映射为 agentcore.Options.MaxTokens。
	MaxTokens int
	// MaxTokensField 选择 MaxTokens 用哪个请求字段名,仅对 format=openai 生效:
	//   "" = max_tokens(默认); "max_completion_tokens" = 新字段。
	// OpenAI 推理模型(o 系列/GPT-5)只认后者,收到 max_tokens 会直接报
	// unsupported_parameter;而多数兼容网关只认前者,故不做自动推断,交由用户按端点选。
	MaxTokensField string
	// SessionHeaderKey,非空时,让每次 LLM 请求带上一个自定义 HTTP 头,头名为该值、
	// 头值为【当前会话的 session id】(chat 会话=conv-<id>,worker=exp<x>-worker-i<intent>
	// 等,见 WorkerSessionID)。用于某些按 session-id 头做提示缓存/粘性路由的网关。
	// 空 = 不发送。值由 transcript.WithSessionID 挂在请求 context 上,由 RoundTripper
	// 读取填入,因此同一共享 provider 也能按会话发出不同的头值。
	SessionHeaderKey string
	// Retry 是该配置解析后的重试参数(profile 覆盖 → 全局策略 → 内置默认,由
	// server 侧解析)。三层的含义见 RetryConfig;零值 = 完全沿用内置默认。
	Retry RetryConfig
}

// RetryConfig 是随一个 LLM 配置走的重试参数。每层的「次数」统一语义:
// 0 = 用内置默认次数;负数 = 关闭该层重试;>0 = 用该值。每层的「间隔」:
// 0 = 用该层原本的指数退避;>0 = 改用这个固定间隔。
type RetryConfig struct {
	// ConnectAttempts/ConnectInterval:SDK 建连重试(连接重置/超时/429/5xx,流开始前),
	// 直接映射为 llm.Config.MaxRetries / RetryInterval。默认 3 次、0.5s 起指数(封顶 8s)。
	ConnectAttempts int
	ConnectInterval time.Duration
	// EmptyAttempts/EmptyInterval:SDK 空响应重试(完成但无 content block,仅 openai
	// 格式),映射为 llm.Config.EmptyResponseRetries / EmptyResponseInterval。
	// 默认 2 次、同一条指数梯度。
	EmptyAttempts int
	EmptyInterval time.Duration
	// StreamAttempts/StreamInterval:同 provider 安全窗口重试——本项目在 SDK 之上补的
	// 一层,只在「还没向调用方交付任何输出」时重放断流/过载/流内 429。SDK 看不到它,
	// 由 server/task_llm.go 消费。默认 2 次、0.5s 起指数(封顶 4s)。
	StreamAttempts int
	StreamInterval time.Duration
}

// compaction window resolution bounds (in K tokens). Below the floor the
// threshold math (window − summary reserve − buffer) would go non-positive and
// compaction would fire every turn; above the cap it would never fire.
const (
	defaultWindowK = 200  // unset → assume a 200K window (Claude default)
	minWindowK     = 32   // floor so effectiveWindow stays comfortably positive
	maxWindowK     = 1000 // cap at 1M tokens (user request)
)

// CompactionWindow returns the model context window in TOKENS for compaction
// thresholds, resolved from the user-configured size (ContextWindowK). 0/unset →
// a 200K default; otherwise clamped to [32K, 1M] so compaction stays effective.
func (c Config) CompactionWindow() int {
	k := c.ContextWindowK
	if k <= 0 {
		k = defaultWindowK
	}
	if k < minWindowK {
		k = minWindowK
	}
	if k > maxWindowK {
		k = maxWindowK
	}
	return k * 1000
}

// compactionConfig builds the agent-core compaction config for a context window
// in tokens. agentcore.NewSession wires the summarizer (same provider) when this
// is set on Options.Compaction.
func compactionConfig(windowTokens int) *compaction.Config {
	if windowTokens <= 0 {
		windowTokens = defaultWindowK * 1000
	}
	return &compaction.Config{ContextWindow: windowTokens}
}

// FromEnv resolves the LLM provider config:
//
//	ARTEX_LLM_PROVIDER = anthropic|openai (default: inferred from keys)
//	ARTEX_LLM_MODEL    = model id        (default: per provider)
//	ARTEX_LLM_BASE_URL = endpoint        (optional)
//	ARTEX_LLM_PROXY    = proxy URL        (optional; http/https/socks5)
//	ANTHROPIC_API_KEY / OPENAI_API_KEY         = credentials
func FromEnv() (Config, bool) {
	prov := os.Getenv("ARTEX_LLM_PROVIDER")
	anthKey := os.Getenv("ANTHROPIC_API_KEY")
	oaiKey := os.Getenv("OPENAI_API_KEY")

	if prov == "" {
		switch {
		case anthKey != "":
			prov = "anthropic"
		case oaiKey != "":
			prov = "openai"
		default:
			return Config{}, false
		}
	}

	c := Config{
		BaseURL: os.Getenv("ARTEX_LLM_BASE_URL"),
		Model:   os.Getenv("ARTEX_LLM_MODEL"),
		Proxy:   strings.TrimSpace(os.Getenv("ARTEX_LLM_PROXY")),
		// 默认流式;ARTEX_LLM_STREAM=false/0/off 显式关闭走非流式。
		Stream: !isFalsy(os.Getenv("ARTEX_LLM_STREAM")),
	}
	switch prov {
	case "openai":
		c.Format = llm.FormatOpenAI
		c.APIKey = oaiKey
		if c.Model == "" {
			c.Model = "gpt-4o"
		}
	case "openai-responses":
		c.Format = llm.FormatOpenAIResponses
		c.APIKey = oaiKey
		if c.Model == "" {
			c.Model = "gpt-5"
		}
	default:
		c.Format = llm.FormatAnthropic
		c.APIKey = anthKey
		if c.Model == "" {
			c.Model = "claude-opus-4-8"
		}
	}
	if c.APIKey == "" {
		return Config{}, false
	}
	return c, true
}

// ConfigFrom builds a Config from UI-provided strings (provider defaults to
// anthropic; model defaults per provider). Inputs are trimmed and the base URL
// is normalized to the API base the provider expects (the provider appends the
// endpoint path itself), so a full endpoint URL is tolerated.
func ConfigFrom(provider, model, baseURL, apiKey, proxy string) Config {
	c := Config{
		Model:   strings.TrimSpace(model),
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:  strings.TrimSpace(apiKey),
		Proxy:   strings.TrimSpace(proxy),
		Stream:  true, // 默认流式;调用方按 profile 覆盖
	}
	switch strings.TrimSpace(provider) {
	case "openai":
		c.Format = llm.FormatOpenAI
		// provider appends "/chat/completions"; tolerate a full endpoint URL.
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/chat/completions"), "/")
		if c.Model == "" {
			c.Model = "gpt-4o"
		}
	case "openai-responses":
		c.Format = llm.FormatOpenAIResponses
		// provider appends "/responses"; tolerate a full endpoint URL.
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/responses"), "/")
		if c.Model == "" {
			c.Model = "gpt-5"
		}
	default:
		c.Format = llm.FormatAnthropic
		// provider appends "/v1/messages".
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/v1/messages"), "/")
		if c.Model == "" {
			c.Model = "claude-opus-4-8"
		}
	}
	return c
}

// isFalsy reports whether an env-var string explicitly requests "off". Empty or
// unrecognized → false (so an unset var keeps the streaming default).
func isFalsy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

// Provider returns the short provider name ("anthropic"/"openai").
func (c Config) Provider() string {
	switch c.Format {
	case llm.FormatOpenAI:
		return "openai"
	case llm.FormatOpenAIResponses:
		return "openai-responses"
	}
	return "anthropic"
}

// NewProvider builds an llm.Provider from the config. When a rate is set, the
// limiter lives on the single provider instance — so planner + all workers +
// main agent (which share this provider) are bounded by one shared rate limit.
func (c Config) NewProvider() (llm.Provider, error) {
	client, err := quotaAwareHTTPClient(c.Proxy, c.SessionHeaderKey)
	if err != nil {
		return nil, err
	}
	return c.newProviderWithClient(client)
}

var (
	// These errors intentionally expose only the local-provider contract, not
	// configuration values or policy/transport details.
	ErrLocalProviderProxy   = errors.New("llm: local provider does not allow a proxy")
	ErrLocalProviderBaseURL = errors.New("llm: local provider requires a base URL")
	ErrLocalProvider        = errors.New("llm: local provider request failed")
	ErrLocalProviderQuota   = errors.New("llm: quota exhausted")
)

// NewLocalProvider builds a provider whose HTTP client enforces the immutable
// host-owned local LLM policy. Server/profile wiring belongs to the caller;
// this factory only connects an already-resolved Config to the guarded client.
func (c Config) NewLocalProvider(policy *llmpolicy.Policy, options llmpolicy.ClientOptions) (llm.Provider, error) {
	if strings.TrimSpace(c.Proxy) != "" {
		return nil, ErrLocalProviderProxy
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, ErrLocalProviderBaseURL
	}
	client, err := llmpolicy.NewClient(policy, options)
	if err != nil {
		return nil, err
	}
	// Keep quota conversion, request capture and per-session headers on the
	// outside of the policy transport. The policy client remains responsible for
	// redirect and DNS/peer validation while every actual request passes through
	// the existing observability and quota boundary.
	client.Transport = quotaAwareTransport{
		base:             client.Transport,
		sessionHeaderKey: strings.TrimSpace(c.SessionHeaderKey),
	}
	provider, err := c.newProviderWithClient(client)
	if err != nil {
		return nil, ErrLocalProvider
	}
	return localProvider{inner: provider}, nil
}

// localProvider is the error boundary for the local-only connection path.
// The underlying SDK may include URLs, response bodies, or transport details;
// none of those are safe to cross this boundary. Legacy NewProvider deliberately
// does not use this wrapper and retains its historical error contract.
type localProvider struct{ inner llm.Provider }

// ErrLocalProviderPolicy는 로컬 모델 연결 정책(origin·DNS 결속·peer 대조·리다이렉트 금지)이 요청을 막았다는 고정
// 분류다. 문구는 ErrLocalProvider와 같아 원문(URL·응답 본문)을 넘기지 않고, errors.Is로 ErrLocalProvider와
// llmpolicy.ErrRequestBlocked 둘 다로 식별된다. 모델 예산은 이 분류로 정책 거부를 일시 실패와 구별해 계속 막는다
// (P3 결정안 C05). 감싼 원인은 없다(Unwrap 없음).
var ErrLocalProviderPolicy error = localProviderPolicyError{}

type localProviderPolicyError struct{}

func (localProviderPolicyError) Error() string { return ErrLocalProvider.Error() }

func (localProviderPolicyError) Is(target error) bool {
	return target == ErrLocalProvider || target == llmpolicy.ErrRequestBlocked
}

func (p localProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		for event, err := range p.inner.Stream(ctx, req) {
			if err != nil {
				yield(llm.StreamEvent{}, localProviderError(ctx, err))
				return
			}
			if !yield(event, nil) {
				return
			}
		}
	}
}

func (p localProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	message, stop, usage, err := p.inner.Complete(ctx, req)
	if err != nil {
		return llm.Message{}, "", llm.Usage{}, localProviderError(ctx, err)
	}
	return message, stop, usage, nil
}

func localProviderError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err == nil {
		return ErrLocalProvider
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, llmpolicy.ErrRequestBlocked) {
		return ErrLocalProviderPolicy
	}
	if errors.Is(err, llmpolicy.ErrTransport) || errors.Is(err, llmpolicy.ErrBodyClose) {
		return ErrLocalProvider
	}
	if IsQuotaExhaustedMessage(err.Error()) {
		return ErrLocalProviderQuota
	}
	return ErrLocalProvider
}

func (c Config) newProviderWithClient(client *http.Client) (llm.Provider, error) {
	if client == nil {
		return nil, errors.New("llm: nil HTTP client")
	}
	lc := llm.Config{
		Format:     c.Format,
		BaseURL:    c.BaseURL,
		APIKey:     c.APIKey,
		Model:      c.Model,
		HTTPClient: client,
	}
	// 思考开关与强度两个字段各自透传(空 = 该字段不发送)。二者解耦:
	// 可只发 thinking.type、只发 effort、都发、或都不发。
	lc.ThinkingType = c.ThinkingType
	lc.ReasoningEffort = c.ReasoningEffort
	// 输出上限的字段名选择(空 = 用 max_tokens)。上限的「值」不在这里:它每轮随
	// agentcore.Options.MaxTokens 走,provider 只决定把它塞进哪个键。
	lc.MaxTokensField = c.MaxTokensField
	// 重试参数与 SDK 同语义(次数 0=默认/负=关闭,间隔 0=指数退避/>0=固定),原样透传。
	lc.MaxRetries = c.Retry.ConnectAttempts
	lc.RetryInterval = c.Retry.ConnectInterval
	lc.EmptyResponseRetries = c.Retry.EmptyAttempts
	lc.EmptyResponseInterval = c.Retry.EmptyInterval
	if c.RatePerSecond > 0 || c.RatePerMinute > 0 {
		lc.RateLimit = &llm.RateLimit{PerSecond: c.RatePerSecond, PerMinute: c.RatePerMinute}
	}
	return llm.NewProvider(lc)
}

// IsQuotaExhaustedMessage deliberately recognizes only explicit balance,
// billing, credit, or quota-exhaustion signals. Generic 429/rate-limit text,
// authentication failures, network errors, and server failures are excluded.
var nonFailoverHTTPStatus = regexp.MustCompile(`(?:status(?:\s+code)?|http(?:\s+status)?)\s*[=:]?\s*(?:401|403|5\d\d)\b`)
var transientQuotaLimit = regexp.MustCompile(`(?i)(?:\b(?:rpm|tpm|rpd|qps)\b|quota[_\s-]*metric|rate[_\s-]*limit|too many requests|(?:requests?|tokens?)\s+(?:per|/)\s*(?:second|minute)|(?:per|/)\s*(?:second|minute)\s+(?:requests?|tokens?)|generate[_\s-]*requests[_\s-]*per[_\s-]*(?:minute|second)|tokens?[_\s-]*per[_\s-]*(?:minute|second))`)

func IsQuotaExhaustedMessage(message string) bool {
	message = strings.ToLower(message)
	// Authentication/authorization and provider-side 5xx failures never rotate,
	// even when a gateway happens to echo a quota-looking phrase in the body.
	if nonFailoverHTTPStatus.MatchString(message) {
		return false
	}
	// Provider APIs frequently describe an ordinary rate limit as "quota
	// exceeded", especially Google-style responses containing a quota metric.
	// These limits recover with time and must stay on the current provider.
	if transientQuotaLimit.MatchString(message) {
		return false
	}
	markers := []string{
		"insufficient_quota", "quota_exceeded", "quota exceeded", "quota exhausted",
		"exceeded your current quota", "billing_hard_limit_reached",
		"billing hard limit", "billing_not_active", "credit balance", "insufficient credit",
		"insufficient balance", "balance is too low", "payment required", "status 402",
		"余额不足", "额度不足", "额度已用尽", "欠费",
	}
	for _, marker := range markers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	// gRPC RESOURCE_EXHAUSTED is overloaded for both account quota and ordinary
	// request-rate limiting. Preserve it as an explicit exhaustion signal only
	// when the same error does not identify a transient rate limit.
	return strings.Contains(message, "resource_exhausted") &&
		!strings.Contains(message, "rate limit") &&
		!strings.Contains(message, "too many requests")
}

// quotaAwareTransport preserves Norma's normal retry behavior except for a 429
// whose body explicitly says the account quota/balance is exhausted. Norma's
// retry loop treats every 429 as transient; normalizing only that response to
// 402 lets a task router fail over immediately while retaining the original
// response body for provider-specific classification and audit logs.
type quotaAwareTransport struct {
	base http.RoundTripper
	// sessionHeaderKey, when non-empty, is the HTTP header name each request
	// carries; its value is the session id read from the request context. Empty
	// disables it. See Config.SessionHeaderKey.
	sessionHeaderKey string
}

func (t quotaAwareTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Custom session-id header: name is user-configured, value is THIS run's
	// session id (norma stashes it on the context via transcript.WithSessionID).
	// Stable across a session's turns and distinct across sessions — exactly what
	// a session-keyed prompt cache wants. Skipped when no session id is present.
	if t.sessionHeaderKey != "" {
		if sid := transcript.SessionIDFrom(req.Context()); sid != "" {
			req.Header.Set(t.sessionHeaderKey, sid)
		}
	}
	// Recorder and connection diagnostics retain only request/response presence
	// and numeric wire metadata. Explicit raw diagnostic captures still see the
	// bodies here, before norma decodes provider events.
	capt := llmrec.CaptureFrom(req.Context())
	capt.MarkRequest()
	capt.SetRequestSnapshot(func() string { return requestBodySnapshot(req) })

	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	// Tee rather than read: a 200 is an SSE stream that must keep streaming. The
	// 429 branch below reads through this wrapper, so its body lands in the
	// capture before being replaced.
	resp.Body = capt.TeeResponse(resp.StatusCode, resp.Body)

	if resp.StatusCode != http.StatusTooManyRequests {
		return resp, nil
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	if readErr != nil {
		return resp, nil
	}
	if IsQuotaExhaustedMessage(string(body)) {
		resp.StatusCode = http.StatusPaymentRequired
		resp.Status = "402 Payment Required"
	}
	return resp, nil
}

// requestBodySnapshot copies an outgoing request body without consuming it.
// norma builds every model request from a *bytes.Reader, so net/http populates
// GetBody and the copy has no effect on what gets sent.
func requestBodySnapshot(req *http.Request) string {
	if req.GetBody == nil {
		return ""
	}
	rc, err := req.GetBody()
	if err != nil {
		return ""
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return ""
	}
	return string(b)
}

func quotaAwareHTTPClient(proxy, sessionHeaderKey string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		transport.Proxy = nil // 留空=直连,不回退 HTTP_PROXY/HTTPS_PROXY 环境变量
	} else {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("llm: invalid proxy %q: %w", proxy, err)
		}
		switch proxyURL.Scheme {
		case "http", "https", "socks5":
		case "":
			return nil, fmt.Errorf("llm: proxy %q missing scheme (use http://, https:// or socks5://)", proxy)
		default:
			return nil, fmt.Errorf("llm: unsupported proxy scheme %q (use http, https or socks5)", proxyURL.Scheme)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{Transport: quotaAwareTransport{base: transport, sessionHeaderKey: strings.TrimSpace(sessionHeaderKey)}}, nil
}

// Only numeric diagnostics cross the connection-test logging boundary. Neither
// configuration strings nor provider text/errors are safe log fields.
func logTestConnection(capt *llmrec.Capture, latency time.Duration, failed bool) {
	attempts := capt.Metadata()
	if len(attempts) == 0 {
		log.Printf("[llm-test] latency_ms=%d attempts=0 failed=%t", latency.Milliseconds(), failed)
		return
	}
	for i, a := range attempts {
		log.Printf("[llm-test] latency_ms=%d attempt=%d/%d http_status=%d response_bytes=%d failed=%t",
			latency.Milliseconds(), i+1, len(attempts), a.Status, a.BodyBytes, failed)
	}
}

// TestConnection makes a minimal real completion to verify the provider/model/
// endpoint/key actually work. Returns the round-trip latency and the model's
// reply text for internal callers; public API handlers must redact the reply
// and error. Diagnostic logs contain only status, size and latency metadata.
func TestConnection(ctx context.Context, c Config) (latency time.Duration, reply string, err error) {
	ctx, capt := llmrec.NewMetadataCapture(ctx)
	// Read final return values, including initialization errors and empty replies.
	defer func() { logTestConnection(capt, latency, err != nil) }()
	prov, err := c.NewProvider()
	if err != nil {
		return 0, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Capture is transient. Only its numeric metadata is used by this log sink.
	// 连接测试是一条单发路径,不经过 agentcore 的会话循环,因此没人往 context 上挂
	// session id。对配了 SessionHeaderKey 的端点(如 opencode zen 强制要求
	// x-opencode-session 头,缺了直接 400 MissingSessionID),这会导致"对话正常、
	// 点击测试却 400"的落差。这里补挂一个一次性随机 session id,让测试与真实对话走同
	// 一套发头逻辑;未配 SessionHeaderKey 的端点不读它,无副作用。
	ctx = transcript.WithSessionID(ctx, "conntest-"+transcript.NewSessionID())
	start := time.Now()
	// MaxTokens 要给足：推理模型(如 deepseek-v4-pro)在给出答案前会先产出一大段
	// 思考(实测对一句 "ping" 也能烧 ~2900 token)。若只给 32,模型会一直卡在"思考阶段"
	// 就撞到输出上限(finish=length)、被截断,连接测试虽仍算通(err=nil)但显示成
	// "已中断/length/resume" 一团糟。给足预算让它把 OK 干净吐完(finish=stop)。
	// EscalateMaxTokens 保持 false:不因截断而抬额重试,避免 resume 循环空烧。
	reply, err = agentcore.Run(ctx, agentcore.Options{
		Provider:       prov,
		SystemPrompt:   []string{"你是连接测试。直接输出两个字符 OK 即可，不要思考、不要解释、不要别的。"},
		PermissionMode: acperm.ModeBypass,
		MaxTurns:       1,
		MaxTokens:      8192,
		NonStreaming:   !c.Stream, // 用该 profile 的真实收发模式做连接测试
	}, "ping")
	latency = time.Since(start)
	if err != nil {
		return latency, "", err
	}
	// err==nil 还不够：请求通了但模型一个字都不吐的情况真实存在(思考把预算烧光、
	// 正文被安全策略吞掉、兼容层把 content 丢了)。这种配置在会话里就是"不回话",
	// 测试却报成功——正是本项要消除的落差。没有可见正文一律判失败。
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return latency, "", fmt.Errorf("模型无回复内容（请求已通，但未返回任何文本）")
	}
	return latency, reply, nil
}
