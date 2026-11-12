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
	// v2 전용. 연속 일시 오류(429·5xx·연결 실패)가 이 수에 닿으면 정책을 막는다(P3 결정안 C06).
	// v1은 이 키가 없고 1로 본다(첫 일시 오류에 막는 이전 동작).
	MaxConsecutiveTransient int64 `json:"max_consecutive_transient_errors,omitempty"`
}

// openRouterManifestKeys는 판별 manifest 키 수다. v2는 연속 일시 오류 상한 하나가 더 있다.
var openRouterManifestKeys = map[int]int{1: 21, 2: 22}

// openRouterMaxConsecutiveTransient는 v2 상한의 형식 범위다. 실제 값은 설치자가 manifest에 고정한다.
const openRouterMaxConsecutiveTransient = 16

func (m openRouterManifest) transientLimit() int64 {
	if m.Version == 1 {
		return 1
	}
	return m.MaxConsecutiveTransient
}

// OpenRouterPolicy의 설정은 비공개이며 모든 클라이언트가 같은 예약 예산을 공유한다.
type OpenRouterPolicy struct {
	manifest openRouterManifest
	mu       sync.Mutex
	usage    OpenRouterUsage
	active   int64
	blocked  bool
	// 연속 일시 오류 수. 정상 응답 하나가 0으로 되돌린다.
	transient int64
	// ledger는 승인별 누적 예약 기록에 이번 예약을 더한다(P3 결정안 C07). 다른 프로세스·재시작의 예약까지 합쳐
	// 승인 총량을 넘으면 errOpenRouterApprovalExhausted를 돌려준다. 보호 파일 로더만 채운다.
	ledger    func(input, output, cost int64) error
	available func() error
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
	return parseOpenRouterManifestAt(data, time.Now())
}
func parseOpenRouterManifestAt(data []byte, now time.Time) (*OpenRouterPolicy, error) {
	if len(data) == 0 || len(data) > MaxProfileManifestBytes || !validManifestEncoding(data) {
		return nil, ErrOpenRouterPolicy
	}
	d := json.NewDecoder(bytes.NewReader(data))
	// 중복 키와 대소문자 별칭도 거부한다.
	var raw = map[string]json.RawMessage{}
	if !manifestObjectRange(d, openRouterManifestKeys[1], openRouterManifestKeys[2], func(k string) bool {
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
	if keys, known := openRouterManifestKeys[m.Version]; !known || len(raw) != keys ||
		m.Version == 1 && m.MaxConsecutiveTransient != 0 ||
		m.Version == 2 && (m.MaxConsecutiveTransient < 1 || m.MaxConsecutiveTransient > openRouterMaxConsecutiveTransient) {
		return nil, ErrOpenRouterPolicy
	}
	if m.ProfileID <= 0 || m.Format != "openai" || m.BaseURL != OpenRouterBaseURL || m.Model != OpenRouterModel ||
		!validProfileValue(m.ExpectedProviderName, 128) || !openRouterSlug.MatchString(m.ProviderSlug) || !openRouterSlug.MatchString(m.ApprovalID) || !m.StrictPrivacyAck || !m.AccountSpendCapAck ||
		!now.Before(m.ExpiresAt) || m.MaxRequests <= 0 || m.MaxInputTokens <= 0 || m.MaxOutputTokens <= 0 || m.MaxCostMicros <= 0 ||
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
	cost := openRouterReservedCost(m, output)
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
	// 승인 총량은 이 프로세스 밖의 예약까지 누적 기록으로 확인한다. 기록할 수 없거나 총량을 넘으면 막는다.
	if p.ledger == nil {
		p.blocked = true
		p.usage.FailureReason = "approval_unavailable"
		return "approval_unavailable", ErrOpenRouterPolicy
	}
	if err := p.ledger(m.MaxInputPerCall, output, cost); err != nil {
		p.blocked = true
		reason = "approval_unavailable"
		if errors.Is(err, errOpenRouterApprovalExhausted) {
			reason = "approval_consumed"
		}
		p.usage.FailureReason = reason
		return reason, ErrOpenRouterPolicy
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

// OpenRouterLimits는 신뢰된 URL 예산 어댑터에 제공하는 비밀 없는 집계다.
// 어댑터는 transport 진입 전에 동일한 보수적 가격 상한을 예약한다.
type OpenRouterLimits struct {
	Requests, InputTokens, OutputTokens, CostMicros int64
	InputPerCall, OutputPerCall                     int64
	Provider                                        string
	ExpiresAt                                       time.Time
}

func (p *OpenRouterPolicy) Limits() OpenRouterLimits {
	if p == nil {
		return OpenRouterLimits{}
	}
	m := p.manifest
	return OpenRouterLimits{m.MaxRequests, m.MaxInputTokens, m.MaxOutputTokens, m.MaxCostMicros, m.MaxInputPerCall, m.MaxOutputPerCall, m.ProviderSlug, m.ExpiresAt}
}

func (p *OpenRouterPolicy) Quote(output int64) (int64, error) {
	if p == nil || output < 1 || output > p.manifest.MaxOutputPerCall || !time.Now().Before(p.manifest.ExpiresAt) {
		return 0, ErrOpenRouterPolicy
	}
	m := p.manifest
	return openRouterReservedCost(m, output), nil
}

// DescribeOpenRouterManifest는 만료 후 저장 보고서 조회에 필요한 설치 메타데이터만 검증한다.
// 호출 가능한 정책이나 자격 증명은 반환하지 않는다.
func DescribeOpenRouterManifest(data []byte, output int64) (ProfileTarget, OpenRouterLimits, int64, error) {
	p, err := parseOpenRouterManifestAt(data, time.Time{})
	if err != nil || output < 1 || output > p.manifest.MaxOutputPerCall {
		return ProfileTarget{}, OpenRouterLimits{}, 0, ErrOpenRouterPolicy
	}
	m := p.manifest
	quote := openRouterReservedCost(m, output)
	return p.Target(), p.Limits(), quote, nil
}

// errOpenRouterApprovalExhausted는 누적 예약이 승인 총량에 닿아 한 번 더 예약할 수 없다는 뜻이다.
var errOpenRouterApprovalExhausted = errors.New("llmpolicy: OpenRouter approval exhausted")

// openRouterLedgerTotals는 승인별 누적 예약 합계다.
type openRouterLedgerTotals struct{ Requests, Input, Output, Cost int64 }

// fits는 합계에 예약 하나를 더해도 승인 총량 안인지 본다.
func (m openRouterManifest) fits(t openRouterLedgerTotals, input, output, cost int64) bool {
	return t.Requests < m.MaxRequests && input <= m.MaxInputTokens-t.Input && output <= m.MaxOutputTokens-t.Output && cost <= m.MaxCostMicros-t.Cost
}

// CheckAvailable은 조회만 하며 이미 다 쓴 승인(누적 예약이 한 번 더 들어갈 자리가 없음)의 새 URL 회차 사용을 거부한다.
func (p *OpenRouterPolicy) CheckAvailable() error {
	if p == nil || p.available == nil || !time.Now().Before(p.manifest.ExpiresAt) {
		return ErrOpenRouterPolicy
	}
	return p.available()
}

// 호출 예약·진단 관측·응답 비용 검증은 같은 보수적 올림식을 사용한다.
func openRouterReservedCost(m openRouterManifest, output int64) int64 {
	return (m.MaxInputPerCall*m.InputPrice+999999)/1000000 + (output*m.OutputPrice+999999)/1000000
}
