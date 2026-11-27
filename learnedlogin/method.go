// Package learnedlogin은 모델이 성공한 로그인 기록에서 계정별 로그인 방법을 배우고, 확정 단계(E③)에서 프로그램이 그
// 방법을 다시 재현할 때 쓰는 순수 판정이다(docs/contracts/model-login-attempts.md §3·§6·§6-1·§6-2).
//
// 이 패키지는 네트워크로 아무것도 보내지 않는다. 요청 본문을 만들고, 대상 응답에서 필요한 값을 꺼내고, 결과를 판정할
// 뿐이다. 실제 송신은 E②(server의 확정 단계 다시 로그인)가 한다. 학습한 방법에는 아이디·비밀번호·세션·CSRF 값과 응답
// 본문을 넣지 않는다(§6-1 데이터 최소화).
package learnedlogin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// 로그인 시도 기록의 값(§3). 기록을 쓰는 탐색 경계·E② 판정기(#969 loginattempt)와 같은 문자열이다.
const (
	RowSchema = "jindo.login-attempt/v1"

	StageExploration  = "exploration"
	StageDiagnosis    = "diagnosis"
	StageConfirmation = "confirmation"
	ActorModel        = "model"
	ActorProgram      = "program"

	TransportSent    = "sent"
	TransportNotSent = "not_sent"
	TransportUnknown = "result_unknown"

	OutcomeSucceeded   = "succeeded"
	OutcomeUnconfirmed = "unconfirmed"
	OutcomeFailed      = "failed"
	OutcomeNotSent     = "not_sent"
	OutcomeUnknown     = "result_unknown"

	FollowupContrast = "authenticated_contrast"
	FollowupNone     = "none"
	ContrastProbe    = "probe"

	FormatForm      = "form"
	FormatJSON      = "json"
	FormatMultipart = "multipart"

	SourceSetCookie = "set_cookie"
	SourceJSONField = "json_field"
	SourceHeader    = "header"
	SourceContinued = "continued"
	SourceNone      = "none"

	// SentAsCookie는 세션을 Cookie 헤더로 실어 보낸 방식이다(§3 session_sent_as). 나머지는 "헤더이름:접두"다.
	SentAsCookie = "cookie"
)

// Reason은 학습한 로그인 방법을 쓰지 못했거나 재현에 실패한 고정 사유다(§6-2). 값·라벨은 들어가지 않는다.
type Reason string

const (
	// ReasonNone: 이 계정에 쓸 학습 방법이 없다(이 회차 성공 기록도 저장된 방법도 없음).
	ReasonNone Reason = "learned_login_none"
	// ReasonRecordInvalid: 로그인 시도 기록을 읽지 못했거나 출처·결속 검증에 실패했다(형식·회차·시도 칸 불일치).
	ReasonRecordInvalid Reason = "learned_login_record_invalid"
	// ReasonTargetChanged: 저장된 방법의 대상 승인 판이 지금 실행의 판과 다르다(재승인).
	ReasonTargetChanged Reason = "learned_login_target_changed"
	// ReasonCredentialChanged: 저장된 방법의 자격 판이 지금 실행에 결속한 자격과 다르다(자격 변경).
	ReasonCredentialChanged Reason = "learned_login_credential_changed"
	// ReasonFormatUnsupported: 메서드·본문 형식·세션 자리·전달 방식이 지원 범위 밖이다.
	ReasonFormatUnsupported Reason = "learned_login_format_unsupported"
	// ReasonInputUnavailable: 원래 요청의 칸 값을 다시 만들 수 없다(기록되지 않은 칸 값·질의 값·잘린 칸 목록·모호한 칸).
	ReasonInputUnavailable Reason = "learned_login_input_unavailable"
	// ReasonPrepUnavailable: 응답에서 받아야 하는 칸이 있는데 로그인 화면(준비 요청)을 확인하지 못했다.
	ReasonPrepUnavailable Reason = "learned_login_prep_unavailable"
	// ReasonPrepFieldMissing: 로그인 화면에서 받아야 하는 칸(CSRF 등)의 값을 하나로 꺼내지 못했다(없음·여럿·형식 밖).
	ReasonPrepFieldMissing Reason = "learned_login_prep_field_missing"
	// ReasonScopeDenied: 로그인·준비 요청이 실행의 승인 범위(쓰기 승인·제외 경로·위험 단어) 밖이다.
	ReasonScopeDenied Reason = "learned_login_scope_denied"
	// ReasonAttemptsExhausted: 같은 아이디의 연속 실패가 회차 상한에 닿았다(§5 고정 사유와 같은 문자열).
	ReasonAttemptsExhausted Reason = "login_attempts_exhausted"
	// ReasonResultUnknown: 로그인 요청을 보냈는지·응답을 모른다. 다시 보내지 않는다.
	ReasonResultUnknown Reason = "learned_login_result_unknown"
	// ReasonRejected: 로그인 응답에 실패 신호가 있다(400 이상·로그인 화면을 다시 줌).
	ReasonRejected Reason = "learned_login_rejected"
	// ReasonSessionNotFound: 로그인 응답에서 배운 세션 자리의 세션을 찾지 못했다.
	ReasonSessionNotFound Reason = "learned_login_session_not_found"
	// ReasonAuthUnconfirmed: 세션은 받았지만 인증 대조(세션 없는 막힘 + 이 세션의 열림)를 세우지 못했다.
	ReasonAuthUnconfirmed Reason = "learned_login_auth_unconfirmed"
	// ReasonBudgetExhausted: 확정 단계 요청 몫·시간이 끝나 보내지 못했다.
	ReasonBudgetExhausted Reason = "learned_login_budget_exhausted"
	// ReasonNotSent: 그 밖의 E② 거부(실행 닫힘·권한 철회 등)로 보내지 못했다.
	ReasonNotSent Reason = "learned_login_not_sent"
	// ReasonCredentialUnavailable: 실행에 결속한 자격 파일을 읽지 못했거나 보호할 수 없는 비밀번호다.
	ReasonCredentialUnavailable Reason = "learned_login_credential_unavailable"
	// ReasonTransportUnverified: 진단 단계 성공 기록의 transport_ref가 이 실행의 감사 완료된 E② 송신 결과와 맞지 않거나
	// 대조할 수 없다(없는 참조·다른 회차·다른 요청·보내지 않음·감사 미완료).
	ReasonTransportUnverified Reason = "learned_login_transport_unverified"
)

// 기록·방법 칸 상한이다. 탐색 경계·E② 판정기(§3)가 남기는 상한과 맞춘다. 판정기는 32개를 넘는 칸 이름을 버리므로, 정확히
// 32개면 잘렸을 수 있어 재현하지 않는다.
const (
	MaxFieldNames     = 32
	maxFieldNameBytes = 64
	maxSessionNames   = 16
	maxPathBytes      = 2048
	maxSentAsPrefix   = 16
	maxRefBytes       = 128
)

// Method는 계정 하나의 학습된 로그인 방법이다(§6 재사용 칸 = §6-1-2 방법 객체 login_method). 값은 하나도 없고 칸 이름·자리만
// 있다. JSON 칸 이름·순서는 계약 §6-1-2 표와 같다(메서드 칸은 http_method).
type Method struct {
	Method             string   `json:"http_method"`
	Path               string   `json:"path"`
	Format             string   `json:"format"`
	IdentifierField    string   `json:"identifier_field"`
	PasswordField      string   `json:"password_field"`
	OtherFields        []string `json:"other_fields"`
	FieldsFromResponse []string `json:"fields_from_response"`
	SessionSource      string   `json:"session_source"`
	SessionNames       []string `json:"session_names"`
	SessionSentAs      string   `json:"session_sent_as"`
}

// Source는 방법을 배운 모델 로그인 시도의 출처다(§6-1). 회차·시도 참조와 보낸 시각뿐이다.
type Source struct {
	RunRef     string    `json:"run_ref"`
	AttemptRef string    `json:"attempt_ref"`
	At         time.Time `json:"at"`
}

// Learned는 출처가 붙은 방법이다.
type Learned struct {
	Method Method `json:"method"`
	Source Source `json:"source"`
}

// Clone은 목록 칸을 복사한다(호출자 사이에 배열을 공유하지 않게).
func (m Method) Clone() Method {
	m.OtherFields = append([]string{}, m.OtherFields...)
	m.FieldsFromResponse = append([]string{}, m.FieldsFromResponse...)
	m.SessionNames = append([]string{}, m.SessionNames...)
	return m
}

// Digest는 방법 객체의 method_sha256이다(계약 §6-1-2: 키 순서를 고정한 JSON의 SHA-256, 목록은 비어도 []). 출처(회차·
// 시도·시각)가 달라도 같은 방법이면 같다. learned 저장(⑤-B)과 verified(E③ 재사용)가 같은 값을 쓰도록 이 함수 하나로
// 계산한다.
func (m Method) Digest() string {
	c := m.Clone()
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// NeedsPrep은 로그인 전에 로그인 화면을 먼저 받아야 하는지다. 응답에서 받는 칸, 값이 기록되지 않은 다른 칸(폼 기본값),
// 로그인 요청에 실려 간 세션 쿠키(continued) 중 하나라도 있으면 받는다.
func (m Method) NeedsPrep() bool {
	return len(m.OtherFields) > 0 || m.SessionSource == SourceContinued
}

// fromResponse는 칸이 응답에서 받는 칸인지다.
func (m Method) fromResponse(name string) bool {
	for _, f := range m.FieldsFromResponse {
		if f == name {
			return true
		}
	}
	return false
}

// Validate는 방법이 지원 범위 안이고 모호하지 않은지 본다. 통과하면 빈 사유다.
func (m Method) Validate() Reason {
	if m.Method != http.MethodPost {
		return ReasonFormatUnsupported
	}
	if !validPath(m.Path) {
		return ReasonRecordInvalid
	}
	switch m.Format {
	case FormatForm, FormatJSON, FormatMultipart:
	default:
		return ReasonFormatUnsupported
	}
	if m.PasswordField == "" || len(m.OtherFields) >= MaxFieldNames || len(m.FieldsFromResponse) > len(m.OtherFields) {
		return ReasonInputUnavailable
	}
	seen := map[string]bool{}
	names := append([]string{m.PasswordField}, m.OtherFields...)
	if m.IdentifierField != "" {
		names = append(names, m.IdentifierField)
	}
	for _, name := range names {
		if !validFieldName(name, m.Format) || seen[name] {
			return ReasonInputUnavailable
		}
		seen[name] = true
	}
	inResponse := map[string]bool{}
	for _, name := range m.FieldsFromResponse {
		if inResponse[name] || !m.isOther(name) {
			return ReasonInputUnavailable
		}
		inResponse[name] = true
	}
	if m.Format == FormatJSON && jsonPathsConflict(names) {
		return ReasonInputUnavailable
	}
	return m.validSession()
}

func (m Method) isOther(name string) bool {
	for _, f := range m.OtherFields {
		if f == name {
			return true
		}
	}
	return false
}

func (m Method) validSession() Reason {
	if len(m.SessionNames) == 0 || len(m.SessionNames) > maxSessionNames {
		return ReasonFormatUnsupported
	}
	seen := map[string]bool{}
	for _, name := range m.SessionNames {
		if seen[name] {
			return ReasonFormatUnsupported
		}
		seen[name] = true
	}
	switch m.SessionSource {
	case SourceSetCookie, SourceContinued:
		if m.SessionSentAs != SentAsCookie {
			return ReasonFormatUnsupported
		}
		for _, name := range m.SessionNames {
			if !validCookieName(name) {
				return ReasonFormatUnsupported
			}
		}
	case SourceJSONField, SourceHeader:
		// 판정기는 뒤 요청이 실어 보낸 토큰 하나로 좁힌다. 여럿이면 어느 것을 보냈는지 모른다.
		if len(m.SessionNames) != 1 {
			return ReasonFormatUnsupported
		}
		if _, _, ok := SentAsHeader(m.SessionSentAs); !ok {
			return ReasonFormatUnsupported
		}
		if m.SessionSource == SourceHeader && !validHeaderName(m.SessionNames[0]) ||
			m.SessionSource == SourceJSONField && !validJSONPath(m.SessionNames[0]) {
			return ReasonFormatUnsupported
		}
	default:
		return ReasonFormatUnsupported
	}
	return ""
}

// SentAsHeader는 "헤더이름:접두" 전달 방식을 나눈다. Cookie·Host 같은 제어 헤더는 받지 않는다(쿠키 세션은 SentAsCookie).
func SentAsHeader(sentAs string) (name, prefix string, ok bool) {
	name, prefix, ok = strings.Cut(sentAs, ":")
	if !ok || !validHeaderName(name) || len(prefix) > maxSentAsPrefix {
		return "", "", false
	}
	switch http.CanonicalHeaderKey(name) {
	case "Cookie", "Host", "Content-Length", "Content-Type", "Transfer-Encoding", "Connection", "Upgrade", "Te", "Trailer",
		"Proxy-Authorization", "Expect", "Keep-Alive":
		return "", "", false
	}
	for i := 0; i < len(prefix); i++ {
		if prefix[i] < 0x20 || prefix[i] > 0x7e {
			return "", "", false
		}
	}
	return http.CanonicalHeaderKey(name), prefix, true
}

// validPath는 질의·조각이 없는 정규 절대 경로다. 판정기가 남기는 이 origin 기준 경로(§3 path) 모양만 받는다.
func validPath(p string) bool {
	if p == "" || len(p) > maxPathBytes || p[0] != '/' || strings.ContainsAny(p, "?#\\") || strings.Contains(p, "//") || !utf8.ValidString(p) {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) || r == ' ' {
			return false
		}
	}
	return p == "/" || path.Clean(p) == strings.TrimSuffix(p, "/")
}

func validFieldName(name, format string) bool {
	if name == "" || len(name) > maxFieldNameBytes || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	if format == FormatJSON {
		return validJSONPath(name)
	}
	// multipart 칸 이름은 Content-Disposition 따옴표 안에 들어간다.
	return format != FormatMultipart || !strings.ContainsAny(name, "\"\r\n")
}

// validJSONPath는 판정기가 JSON 칸을 남기는 점 경로다(§3: user.email). 빈 조각은 없다.
func validJSONPath(p string) bool {
	if p == "" || len(p) > maxFieldNameBytes {
		return false
	}
	for _, segment := range strings.Split(p, ".") {
		if segment == "" {
			return false
		}
	}
	return true
}

// jsonPathsConflict는 한 경로가 다른 경로의 앞부분인지다(a와 a.b). 그 본문은 객체와 값이 겹쳐 다시 만들 수 없다.
func jsonPathsConflict(paths []string) bool {
	for i, a := range paths {
		for j, b := range paths {
			if i != j && strings.HasPrefix(b, a+".") {
				return true
			}
		}
	}
	return false
}

func validCookieName(name string) bool {
	if name == "" || len(name) > maxFieldNameBytes {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c <= 0x20 || c >= 0x7f || strings.IndexByte("()<>@,;:\\\"/[]?={}", c) >= 0 {
			return false
		}
	}
	return true
}

func validHeaderName(name string) bool {
	if name == "" || len(name) > maxFieldNameBytes {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0) {
			return false
		}
	}
	return true
}

func validRef(s string) bool {
	if s == "" || len(s) > maxRefBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':' || c == '/') {
			return false
		}
	}
	return true
}
