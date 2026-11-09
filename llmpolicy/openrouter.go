package llmpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sync"
	"time"
)

const OpenRouterBaseURL = "https://openrouter.ai/api/v1"
const OpenRouterModel = "qwen/qwen3.5-35b-a3b"
const openRouterContext = 262144

var ErrOpenRouterPolicy = errors.New("llmpolicy: OpenRouter 정책 또는 예산이 유효하지 않습니다")

type openRouterManifest struct {
	Version              int       `json:"version"`
	ProfileID            int64     `json:"profile_id"`
	Format               string    `json:"format"`
	BaseURL              string    `json:"base_url"`
	Model                string    `json:"model"`
	ProviderSlug         string    `json:"provider_slug"`
	ExpectedProviderName string    `json:"expected_provider_name"`
	ApprovalID           string    `json:"approval_id"`
	ExpiresAt            time.Time `json:"expires_at"`
	StrictPrivacyAck     bool      `json:"strict_privacy_ack"`
	AccountSpendCapAck   bool      `json:"account_spend_cap_ack"`
	MaxRequests          int64     `json:"max_requests"`
	MaxInputTokens       int64     `json:"max_input_tokens"`
	MaxOutputTokens      int64     `json:"max_output_tokens"`
	MaxCostMicros        int64     `json:"max_cost_usd_micros"`
	MaxInputPerCall      int64     `json:"max_input_tokens_per_call"`
	MaxOutputPerCall     int64     `json:"max_output_tokens_per_call"`
	TimeoutSeconds       int       `json:"timeout_seconds"`
	MaxConcurrent        int64     `json:"max_concurrent"`
	InputPrice           int64     `json:"max_input_price_usd_micros_per_million"`
	OutputPrice          int64     `json:"max_output_price_usd_micros_per_million"`
}

// OpenRouterPolicy의 설정은 비공개이며 모든 클라이언트가 같은 예약 예산을 공유한다.
type OpenRouterPolicy struct {
	manifest openRouterManifest
	mu       sync.Mutex
	usage    OpenRouterUsage
	active   int64
	blocked  bool
	started  bool
	consume  func() error
}
type OpenRouterUsage struct {
	Requests             int64  `json:"requests"`
	Errors               int64  `json:"errors"`
	ReservedInputTokens  int64  `json:"reserved_input_tokens"`
	ReservedOutputTokens int64  `json:"reserved_output_tokens"`
	ReservedCostMicros   int64  `json:"reserved_cost_usd_micros"`
	InputTokens          int64  `json:"input_tokens"`
	OutputTokens         int64  `json:"output_tokens"`
	CostMicros           int64  `json:"cost_usd_micros"`
	LatencyMillis        int64  `json:"latency_ms"`
	ApprovalRef          string `json:"approval_ref"`
	Model                string `json:"model"`
	Provider             string `json:"provider_slug"`
	ReturnedProvider     string `json:"returned_provider"`
	Blocked              bool   `json:"blocked"`
	FailureReason        string `json:"failure_reason,omitempty"`
}

func (p *OpenRouterPolicy) Target() ProfileTarget {
	if p == nil {
		return ProfileTarget{}
	}
	m := p.manifest
	return ProfileTarget{ID: m.ProfileID, Format: m.Format, Model: m.Model, BaseURL: m.BaseURL}
}
func (p *OpenRouterPolicy) Resolve(t ProfileTarget) (*OpenRouterPolicy, error) {
	if p == nil || t != p.Target() || !time.Now().Before(p.manifest.ExpiresAt) {
		return nil, ErrOpenRouterPolicy
	}
	return p, nil
}
func (p *OpenRouterPolicy) Snapshot() OpenRouterUsage {
	if p == nil {
		return OpenRouterUsage{Blocked: true}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	u := p.usage
	u.Blocked = p.blocked
	return u
}
func (*OpenRouterPolicy) String() string     { return "llmpolicy.OpenRouterPolicy{[REDACTED]}" }
func (p *OpenRouterPolicy) GoString() string { return p.String() }

var openRouterSlug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,127}$`)

// ParseOpenRouterManifest는 구문 검증 전용이다. 실제 전송에는 보호 파일 로더가 필요하다.
func ParseOpenRouterManifest(data []byte) (*OpenRouterPolicy, error) {
	if len(data) == 0 || len(data) > MaxProfileManifestBytes || !validManifestEncoding(data) {
		return nil, ErrOpenRouterPolicy
	}
	d := json.NewDecoder(bytes.NewReader(data))
	// 중복 키와 대소문자 별칭도 거부한다.
	var raw = map[string]json.RawMessage{}
	if !manifestObject(d, 21, func(k string) bool {
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return false
		}
		raw[k] = v
		return true
	}) {
		return nil, ErrOpenRouterPolicy
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrOpenRouterPolicy
	}
	normalized, _ := json.Marshal(raw)
	dec := json.NewDecoder(bytes.NewReader(normalized))
	dec.DisallowUnknownFields()
	var m openRouterManifest
	if dec.Decode(&m) != nil {
		return nil, ErrOpenRouterPolicy
	}
	canonical, _ := json.Marshal(m)
	var names map[string]json.RawMessage
	_ = json.Unmarshal(canonical, &names)
	for k := range raw {
		if _, ok := names[k]; !ok {
			return nil, ErrOpenRouterPolicy
		}
	}
	if m.Version != 1 || m.ProfileID <= 0 || m.Format != "openai" || m.BaseURL != OpenRouterBaseURL || m.Model != OpenRouterModel ||
		!validProfileValue(m.ExpectedProviderName, 128) || !openRouterSlug.MatchString(m.ProviderSlug) || !openRouterSlug.MatchString(m.ApprovalID) || !m.StrictPrivacyAck || !m.AccountSpendCapAck ||
		!time.Now().Before(m.ExpiresAt) || m.MaxRequests <= 0 || m.MaxInputTokens <= 0 || m.MaxOutputTokens <= 0 || m.MaxCostMicros <= 0 ||
		m.MaxInputPerCall != openRouterContext || m.MaxOutputPerCall <= 0 || m.MaxOutputPerCall > openRouterContext ||
		m.TimeoutSeconds < 1 || m.TimeoutSeconds > 300 || m.MaxConcurrent < 1 || m.MaxConcurrent > 16 ||
		m.InputPrice <= 0 || m.OutputPrice <= 0 || m.InputPrice > 1000000000 || m.OutputPrice > 1000000000 ||
		m.MaxRequests > math.MaxInt32 || m.MaxInputTokens > 1<<53 || m.MaxOutputTokens > 1<<53 || m.MaxCostMicros > 1<<53 {
		return nil, ErrOpenRouterPolicy
	}
	return &OpenRouterPolicy{manifest: m, usage: OpenRouterUsage{Provider: m.ProviderSlug, Model: OpenRouterModel, ApprovalRef: fmt.Sprintf("%x", sha256.Sum256([]byte(m.ApprovalID)))}}, nil
}
func (p *OpenRouterPolicy) reserve(output int64) error {
	_, err := p.reserveDiagnostic(output)
	return err
}

// 같은 잠금에서 얻은 거절 사유만 관측해 동시 호출의 집계를 섞지 않는다.
func (p *OpenRouterPolicy) reserveDiagnostic(output int64) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := p.manifest
	cost := ((m.MaxInputPerCall*m.InputPrice + 999999) / 1000000) + ((output*m.OutputPrice + 999999) / 1000000)
	reason := ""
	switch {
	case p.blocked:
		return "policy_denied", ErrOpenRouterPolicy
	case !time.Now().Before(m.ExpiresAt):
		reason = "expired"
	case output <= 0 || output > m.MaxOutputPerCall:
		reason = "output_limit"
	case p.active >= m.MaxConcurrent:
		reason = "concurrency_limit"
	case p.usage.Requests >= m.MaxRequests:
		reason = "request_limit"
	case m.MaxInputPerCall > m.MaxInputTokens-p.usage.ReservedInputTokens || output > m.MaxOutputTokens-p.usage.ReservedOutputTokens:
		reason = "token_limit"
	case cost > m.MaxCostMicros-p.usage.ReservedCostMicros:
		reason = "cost_limit"
	}
	if reason != "" {
		p.usage.FailureReason = reason
		return reason, ErrOpenRouterPolicy
	}
	if !p.started {
		if p.consume == nil || p.consume() != nil {
			p.blocked = true
			p.usage.FailureReason = "approval_consumed"
			return "approval_consumed", ErrOpenRouterPolicy
		}
		p.started = true
	}
	p.active++
	p.usage.Requests++
	p.usage.ReservedInputTokens += m.MaxInputPerCall
	p.usage.ReservedOutputTokens += output
	p.usage.ReservedCostMicros += cost
	return "", nil
}

// ValidateCallLimits는 프로필 기본값이 승인된 요청 범위 안인지 검사한다.
func (p *OpenRouterPolicy) ValidateCallLimits(output, contextK int) error {
	if p == nil || output <= 0 || int64(output) > p.manifest.MaxOutputPerCall || contextK < 1 || contextK > 262 {
		return ErrOpenRouterPolicy
	}
	return nil
}
