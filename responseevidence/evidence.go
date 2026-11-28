// Package responseevidence는 E② 진단 응답 하나에서 선택 후보 검증에 필요한 구조 근거만 뽑는다.
//
// 본문 원문·값·쿠키·자격값은 담지 않는다. 같은 응답을 다시 넣으면 같은 결과가 나오는 순수 함수다(시각·난수·전역 상태 없음).
// 추출하지 못한 정보는 빈 값이나 정상 값으로 바꾸지 않고 상태(State)와 상한 표시(Limits)로 남긴다. 응답이 없다는 것과
// 응답에 필드가 없다는 것을 구별한다. 이 결과는 관찰 사실의 요약이지 취약점 판정이 아니다.
package responseevidence

import (
	"bytes"
	"mime"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"
)

// Schema는 저장·모델 입력에 쓰는 근거 형식 이름이다. 의미가 바뀌면 새 판본을 만든다.
const Schema = "jindo.candidate-response-evidence/v1"

// State는 추출 결과 상태다. complete와 limited만 구조 정보를 가진다.
type State string

const (
	// StateComplete는 처리 상한 안에서 끝까지 추출했다는 뜻이다.
	StateComplete State = "complete"
	// StateLimited는 끝까지 읽었지만 필드·폼·깊이 등 상한 때문에 일부만 담았다는 뜻이다(Limits에 이유).
	StateLimited State = "limited"
	// StateUnsupportedMedia는 구조를 해석하지 않는 형식(이진·압축·XML 등)이다. 길이와 빈 응답 여부만 있다.
	StateUnsupportedMedia State = "unsupported_media"
	// StateTruncated는 대상 응답을 끝까지 받지 못했거나 E②가 앞부분만 줬다는 뜻이다.
	StateTruncated State = "truncated"
	// StateParseFailed는 선언한 형식(JSON 등)으로 해석하지 못했다는 뜻이다.
	StateParseFailed State = "parse_failed"
	// StateNotReceived는 대상 응답(상태 줄·헤더)을 받지 못했다는 뜻이다.
	StateNotReceived State = "not_received"
	// StateIncomplete는 응답은 받았지만 본문을 끝까지 확인하지 못했거나 E②가 표현을 보류했다는 뜻이다.
	StateIncomplete State = "incomplete"
	// StateNotRecorded는 근거를 만들지 않은 관찰(옛 회차·크기 초과로 생략)이다. 추출기가 아니라 저장 쪽이 쓴다.
	StateNotRecorded State = "not_recorded"
)

// Structured는 구조 정보를 비교에 쓸 수 있는 상태인지다.
func (s State) Structured() bool { return s == StateComplete || s == StateLimited }

// Valid는 알려진 상태인지다.
func (s State) Valid() bool {
	switch s {
	case StateComplete, StateLimited, StateUnsupportedMedia, StateTruncated, StateParseFailed,
		StateNotReceived, StateIncomplete, StateNotRecorded:
		return true
	}
	return false
}

// 상한 표시 값이다. 어떤 상한 때문에 일부만 담았는지 고정 코드로 남긴다.
const (
	LimitBodyBytes   = "body_bytes"
	LimitDepth       = "depth"
	LimitFields      = "fields"
	LimitArrayItems  = "array_items"
	LimitStringBytes = "string_bytes"
	LimitHTMLTokens  = "html_tokens"
	LimitForms       = "forms"
	LimitInputs      = "inputs"
	LimitEncoded     = "encoded_size"
	LimitDuplicate   = "duplicate_keys"
)

// Limits는 처리 자원 상한이다. 0 이하 값은 기본값으로 바꾸고, 기본값보다 큰 값은 기본 상한으로 묶는다.
type Limits struct {
	MaxBodyBytes   int // 구조를 해석할 최대 본문 길이
	MaxDepth       int // JSON·HTML 최대 중첩 깊이
	MaxFields      int // 담을 JSON 필드 경로 수
	MaxArrayItems  int // 배열 하나에서 하위 구조를 살펴볼 원소 수(길이는 끝까지 센다)
	MaxStringBytes int // 값 digest·모양 판단에 쓸 문자열 최대 길이
	MaxHTMLTokens  int // HTML 토큰 처리 상한
	MaxForms       int // 담을 폼 수
	MaxInputs      int // 폼 하나에 담을 입력 수
}

// DefaultLimits는 제품 기본 상한이다. 응답 본문은 E② 본문 상한을 이미 지난 값이다.
func DefaultLimits() Limits {
	return Limits{MaxBodyBytes: 1 << 20, MaxDepth: 32, MaxFields: 48, MaxArrayItems: 64, MaxStringBytes: 256,
		MaxHTMLTokens: 50000, MaxForms: 4, MaxInputs: 12}
}

func (l Limits) normalized() Limits {
	d := DefaultLimits()
	if l.MaxBodyBytes <= 0 || l.MaxBodyBytes > d.MaxBodyBytes {
		l.MaxBodyBytes = d.MaxBodyBytes
	}
	if l.MaxDepth <= 0 || l.MaxDepth > d.MaxDepth {
		l.MaxDepth = d.MaxDepth
	}
	if l.MaxFields <= 0 || l.MaxFields > d.MaxFields {
		l.MaxFields = d.MaxFields
	}
	if l.MaxArrayItems <= 0 || l.MaxArrayItems > d.MaxArrayItems {
		l.MaxArrayItems = d.MaxArrayItems
	}
	if l.MaxStringBytes <= 0 || l.MaxStringBytes > d.MaxStringBytes {
		l.MaxStringBytes = d.MaxStringBytes
	}
	if l.MaxHTMLTokens <= 0 || l.MaxHTMLTokens > d.MaxHTMLTokens {
		l.MaxHTMLTokens = d.MaxHTMLTokens
	}
	if l.MaxForms <= 0 || l.MaxForms > d.MaxForms {
		l.MaxForms = d.MaxForms
	}
	if l.MaxInputs <= 0 || l.MaxInputs > d.MaxInputs {
		l.MaxInputs = d.MaxInputs
	}
	return l
}

// Response는 추출기 입력이다. Body는 호출이 끝나면 호출자가 지운다(추출기는 사본을 남기지 않는다).
type Response struct {
	Received        bool // 상태 줄·헤더를 받았다
	Complete        bool // 본문을 끝까지 받았고 E②가 표현을 보류하지 않았다
	Truncated       bool // 본문 상한으로 앞부분만 받았다
	StatusCode      int
	Method          string // HEAD는 본문이 없으므로 구조를 해석하지 않는다
	ContentType     string
	ContentEncoding string
	// Header는 응답 헤더다. 보안 헤더 존재·쿠키 속성 수만 남기고 값은 남기지 않는다.
	Header http.Header
	Body   []byte
}

// Protected는 기록하면 안 되는 문자열(제어 비밀·E③ 표식·보호 세션 값)을 담고 있는지 알려 준다. nil이면 검사하지 않는다.
type Protected func(string) bool

// Evidence는 응답 하나의 구조 근거다. 값 원문은 없다.
type Evidence struct {
	Schema     string   `json:"schema"`
	State      State    `json:"state"`
	MediaClass string   `json:"media_class"`
	StatusCode int      `json:"status_code,omitempty"`
	BodyBytes  int      `json:"body_bytes"`
	Empty      bool     `json:"empty,omitempty"`
	Limits     []string `json:"limits,omitempty"`
	Signals    []string `json:"signals,omitempty"`
	// Headers는 응답을 받은 모든 상태에 있다(본문이 잘려도 헤더는 끝까지 받았다).
	Headers *HeaderStructure `json:"headers,omitempty"`
	JSON    *JSONStructure   `json:"json,omitempty"`
	HTML    *HTMLStructure   `json:"html,omitempty"`
	Text    *TextStructure   `json:"text,omitempty"`
}

// MediaClass는 Content-Type을 고정 범주로 줄인다. E² HTTPObservation의 범주와 같은 값을 쓴다.
func MediaClass(contentType string) string {
	if strings.TrimSpace(contentType) == "" {
		return "unknown"
	}
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "unknown"
	}
	media = strings.ToLower(media)
	switch {
	case media == "text/html" || media == "application/xhtml+xml":
		return "html"
	case media == "application/json" || strings.HasSuffix(media, "+json"):
		return "json"
	case media == "text/plain":
		return "plain_text"
	case media == "application/xml" || media == "text/xml" || strings.HasSuffix(media, "+xml"):
		return "xml"
	case media == "application/octet-stream":
		return "binary"
	default:
		return "other"
	}
}

// Extract는 응답 하나의 구조 근거를 만든다. 같은 입력은 같은 출력을 낸다.
func Extract(r Response, limits Limits, protected Protected) Evidence {
	l := limits.normalized()
	ev := Evidence{Schema: Schema, MediaClass: "none"}
	if !r.Received {
		ev.State = StateNotReceived
		return ev
	}
	ev.StatusCode = r.StatusCode
	ev.MediaClass = MediaClass(r.ContentType)
	ev.Headers = extractHeaders(r.Header)
	ev.BodyBytes = len(r.Body)
	ev.Empty = len(r.Body) == 0
	switch {
	case r.Truncated:
		ev.State = StateTruncated
		return ev
	case !r.Complete:
		ev.State = StateIncomplete
		return ev
	}
	if strings.EqualFold(r.Method, "HEAD") {
		// HEAD는 표현 본문이 없다. 빈 본문을 '필드 없음'으로 해석하지 않도록 구조 없는 상태로 둔다.
		ev.State = StateUnsupportedMedia
		return ev
	}
	if enc := strings.TrimSpace(r.ContentEncoding); enc != "" && !strings.EqualFold(enc, "identity") {
		// 압축 본문은 풀지 않는다(해제 폭탄·자원 상한). 길이만 남긴다.
		ev.State = StateUnsupportedMedia
		return ev
	}
	if ev.Empty {
		ev.State = StateComplete
		return ev
	}
	if len(r.Body) > l.MaxBodyBytes {
		// 끝까지 받은 응답이지만 해석 상한을 넘었다. 일부 구조만 보고 '필드 없음'을 주장하지 않도록 구조를 만들지 않는다.
		ev.State, ev.Limits = StateLimited, []string{LimitBodyBytes}
		ev.Signals = detectSignals(r.Body[:l.MaxBodyBytes], ev.MediaClass)
		return ev
	}
	limited := map[string]bool{}
	switch ev.MediaClass {
	case "json":
		js, signals, ok := extractJSON(r.Body, l, protected, limited)
		if !ok {
			ev.State = StateParseFailed
			ev.Signals = detectSignals(r.Body, "plain_text")
			return ev
		}
		ev.JSON, ev.Signals = js, signals
	case "html":
		hs, text, ok := extractHTML(r.Body, l, protected, limited)
		if !ok {
			ev.State = StateParseFailed
			return ev
		}
		ev.HTML = hs
		ev.Signals = detectSignals(text, "html")
	case "plain_text":
		if !utf8.Valid(r.Body) {
			ev.State = StateParseFailed
			return ev
		}
		ev.Text = extractText(r.Body)
		ev.Signals = detectSignals(r.Body, "plain_text")
	default:
		ev.State = StateUnsupportedMedia
		return ev
	}
	ev.State = StateComplete
	if len(limited) != 0 {
		ev.State = StateLimited
		for code := range limited {
			ev.Limits = append(ev.Limits, code)
		}
		sort.Strings(ev.Limits)
	}
	return ev
}

// TextStructure는 일반 텍스트의 줄 수 지표다. 내용은 없다.
type TextStructure struct {
	Lines         int `json:"lines"`
	NonEmptyLines int `json:"non_empty_lines"`
}

func extractText(body []byte) *TextStructure {
	t := &TextStructure{}
	for _, line := range bytes.Split(body, []byte("\n")) {
		t.Lines++
		if len(bytes.TrimSpace(line)) != 0 {
			t.NonEmptyLines++
		}
	}
	return t
}
