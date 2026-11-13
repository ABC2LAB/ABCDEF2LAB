// Package exploreboundary는 탐색 경계의 요청별 판정·상한·기록을 담는다(docs/contracts/exploration-boundary.md §2~§6).
// 리슨·TLS 가로채기·제어 소켓 같은 네트워크 조립은 이 패키지 밖(cmd)이 맡고, 송신 직전에 이 판정을 받는다.
// E② receipt·감사 체인·E③ 근거를 만들지 않는다. 승인 범위를 넓히거나 대상 값을 실행기 입력에서 받지 않는다.
package exploreboundary

import (
	"context"
	"errors"
	"net/netip"
	"path"
	"strings"
	"time"

	"github.com/Autumn-27/artex/targetaddr"
)

// Ingress는 요청이 들어온 입구다. 헤더가 아니라 네트워크 배치(어느 리슨에서 받았는지)로 정한다(§3).
type Ingress string

const (
	BrowserIngress Ingress = "browser"
	DirectIngress  Ingress = "direct"
)

// DestClass는 목적지 분류다(§4 dest_class).
type DestClass string

const (
	DestApproved DestClass = "approved"
	DestExternal DestClass = "external"
	DestInternal DestClass = "internal"
	DestMetadata DestClass = "metadata"
	DestControl  DestClass = "control"
)

// 차단 사유(§4 reason). 값은 기록·화면 어휘이므로 바꾸면 계약 문서도 같이 고친다.
const (
	ReasonOutOfScopeOrigin     = "out_of_scope_origin"
	ReasonOutOfScopePath       = "out_of_scope_path"
	ReasonMethodNotApproved    = "method_not_approved"
	ReasonRiskyPath            = "risky_path"
	ReasonInternalAddress      = "internal_address"
	ReasonMetadataAddress      = "metadata_address"
	ReasonControlEndpoint      = "control_endpoint"
	ReasonExternalBlockedLocal = "external_blocked_local"
	ReasonExternalDirect       = "external_direct"
	ReasonExternalNavigation   = "external_navigation"
	ReasonExternalMethod       = "external_method"
	ReasonCapExhausted         = "cap_exhausted"
	ReasonSessionClosed        = "session_closed"
	ReasonTargetEnded          = "target_ended"
	ReasonWSReadOnly           = "ws_read_only"
	ReasonProtocolUnsupported  = "protocol_unsupported"
)

var (
	ErrConfig      = errors.New("explore_boundary_config_invalid")
	ErrTargetEnded = errors.New("explore_boundary_target_ended")
	ErrUnavailable = errors.New("explore_boundary_unavailable")
)

// Target은 경계가 보호된 대상 등록 저장소에서 직접 읽은 승인 대상이다(§2). 실행기·모델 입력으로 만들지 않는다.
type Target struct {
	Ref      string
	Revision uint64
	// Scheme은 승인 origin의 scheme이다("https" 또는 "http", 비우면 https). http 대상은 TLS가 없어 SPKI 고정도 없다(C22).
	Scheme string
	// Host·Port는 승인 origin의 이름과 포트다.
	Host         string
	Port         uint16
	PathPrefixes []string
	// Methods는 대상 승인 메서드다. read_only 회차는 이 중 GET·HEAD만 쓴다.
	Methods []string
	// ConnectIP는 등록 때 고른 승인 주소다. 경계는 대상 이름을 다시 풀지 않고 이 주소로만 연결한다(DNS rebinding 방지).
	ConnectIP  netip.Addr
	SPKISHA256 string
	ExpiresAt  time.Time
	// RiskyWords·ExcludedPrefixes는 대상 정책의 위험 단어·제외 경로다. 쓰기 승인 회차에서도 막는다.
	RiskyWords       []string
	ExcludedPrefixes []string
}

// scheme은 승인 origin의 scheme이다. 이전 기록처럼 비어 있으면 https다.
func (t Target) scheme() string {
	if t.Scheme == "" {
		return "https"
	}
	return t.Scheme
}

// TargetSource는 대상 등록 저장소의 읽기 쪽이다. 같은 ref·revision이 지금도 승인 상태일 때만 돌려준다.
// 철회·만료·판 변경은 ErrTargetEnded, 저장소 장애는 다른 오류다.
type TargetSource interface {
	Current(ctx context.Context, ref string, revision uint64) (Target, error)
}

// Resolver는 경계가 직접 쓰는 이름 풀이다. 실행기의 resolver를 믿지 않는다(§3-1).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Policy는 설치가 정하는 경계 고정 설정이다. 회차마다 바뀌는 값은 Binding·Target에서 온다.
type Policy struct {
	// ControlEndpoints는 E② 제어·LLM 게이트웨이·관리 API·평가 계측기 관측 포트처럼 막아야 할 끝점이다.
	ControlEndpoints []netip.AddrPort
	Resolver         Resolver
	// PathPattern은 기록용 경로 가림이다. 평가 회차는 계측기와 같은 키, 제품 회차는 표시용 패턴을 넣는다(§4).
	PathPattern func(escapedPath string) string
	// MaxDeniedRecords는 차단 기록 행의 상한이다. 넘친 행은 수만 센다. 0이면 설정 오류다.
	MaxDeniedRecords int
	Now              func() time.Time
}

func (p Policy) valid() bool {
	return p.Resolver != nil && p.PathPattern != nil && p.MaxDeniedRecords > 0
}

// Request는 경계가 판정할 요청의 메타데이터다. 헤더 값·쿠키·본문·query 값은 넣지 않는다.
type Request struct {
	Ingress Ingress
	Method  string
	// Scheme·Host·Port는 요청 대상 authority다. 경계는 CONNECT·절대 URI에서 읽는다.
	Scheme string
	Host   string
	Port   uint16
	// EscapedPath는 url.URL.EscapedPath() 값이다. 정규화로 바뀌는 경로는 승인 경로로 보지 않는다.
	EscapedPath string
	FetchMode   string
	FetchDest   string
	HasAuth     bool
	WebSocket   bool
}

// Decision은 판정 결과다. Forward일 때만 Dial이 정해지고, 승인 대상이면 SPKI 검증값이 붙는다.
type Decision struct {
	Forward bool
	Reason  string
	Dest    DestClass
	Dial    netip.AddrPort
	// VerifySPKI가 비면 시스템 루트로 검증한다(외부 호스트, §9-2). 승인 대상은 항상 고정값이 있다.
	VerifySPKI string
}

func canonicalPath(escaped string) (string, bool) {
	if escaped == "" {
		escaped = "/"
	}
	if !strings.HasPrefix(escaped, "/") || strings.ContainsAny(escaped, "%\\\x00\r\n") || strings.Contains(escaped, "//") {
		return "", false
	}
	clean := path.Clean(escaped)
	if strings.HasSuffix(escaped, "/") && clean != "/" {
		clean += "/"
	}
	return clean, clean == escaped
}

func within(p, prefix string) bool {
	prefix = strings.TrimSuffix(prefix, "/")
	return prefix == "" || p == prefix || strings.HasPrefix(p, prefix+"/")
}

// excludedWithin은 제외 경로 패턴 매칭이다. 제품의 대상 등록·진단 흐름과 같은 규칙이다(diagnosisflow excludedWithin):
// 조각 하나 전체인 "*"는 그 자리의 아무 조각 하나와 맞고, 패턴은 그 아래 경로까지 덮는다. 등록 검사는 "*"를 받으므로
// 여기서 글자 그대로 비교하면 제외가 조용히 빠진다(#482 리뷰 R1).
func excludedWithin(p, pattern string) bool {
	if pattern == "/" {
		return true
	}
	parts := strings.Split(strings.Trim(pattern, "/"), "/")
	actual := strings.Split(strings.Trim(p, "/"), "/")
	if len(actual) < len(parts) {
		return false
	}
	for i, part := range parts {
		if part != "*" && part != actual[i] {
			return false
		}
	}
	return true
}

func risky(p string, words []string) bool {
	for _, segment := range strings.Split(strings.ToLower(p), "/") {
		for _, word := range words {
			if word != "" && strings.Contains(segment, strings.ToLower(word)) {
				return true
			}
		}
	}
	return false
}

func hasMethod(methods []string, m string) bool {
	for _, v := range methods {
		if v == m {
			return true
		}
	}
	return false
}

// classify는 승인 대상이 아닌 주소를 분류한다. 승인 대상의 주소만 예외이고 그 밖의 사설·loopback은 내부망이다.
func (p Policy) classify(ip netip.AddrPort) DestClass {
	for _, control := range p.ControlEndpoints {
		if control.Addr().Unmap() == ip.Addr().Unmap() && (control.Port() == 0 || control.Port() == ip.Port()) {
			return DestControl
		}
	}
	if targetaddr.Metadata(ip.Addr()) {
		return DestMetadata
	}
	if !targetaddr.Allowed(ip.Addr().Unmap(), false) {
		return DestInternal
	}
	return DestExternal
}

func reasonFor(class DestClass) string {
	switch class {
	case DestControl:
		return ReasonControlEndpoint
	case DestMetadata:
		return ReasonMetadataAddress
	default:
		return ReasonInternalAddress
	}
}
