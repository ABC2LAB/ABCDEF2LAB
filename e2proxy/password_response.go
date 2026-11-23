package e2proxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"html"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// PasswordResponseMaskSpans는 호스트가 등록한 비밀번호의 일치 구간만 가리는 정책이다.
// 비어 있는 이전 정책은 전체 보류 동작을 유지한다. 일반 보호 값·세션·E③ 표식 정책과 구별한다.
const PasswordResponseMaskSpans = "mask_spans_v1"

// PasswordResponseModelVisible은 대상 응답에 비친 시험 계정 비밀번호를 보류·가림 없이 도구(모델)에 주는 정책이다
// (사용자 결정 2026-11-23: 대상에서 온 정보는 외부 모델로 나가도 되고 그 책임은 사용자에게 있다고 동의 문장으로 공지한다).
// 비밀번호는 저장 기록(도구 출력 기록·근거·활동·보고서·화면 API)에서만 가린다(RedactRecord). 일반 보호 값(우리 제어 비밀)은
// 지금처럼 응답을 통째로 보류하고, 로그인은 여전히 프로그램(인증 준비)만 한다.
const PasswordResponseModelVisible = "model_visible_v1"
const maxPasswordTransforms = 1 << 16
const maxPasswordSpans = 4096

type passwordSpan struct{ begin, end int }
type passwordEdit struct{ source, sourceEnd, decoded, decodedEnd int }
type passwordView struct {
	data  []byte
	edits []passwordEdit
}

func (v passwordView) sourceIndex(index int, end bool) int {
	i := sort.Search(len(v.edits), func(i int) bool {
		if end {
			return v.edits[i].decoded >= index
		}
		return v.edits[i].decoded > index
	}) - 1
	if i < 0 {
		return index
	}
	edit := v.edits[i]
	if end && index <= edit.decodedEnd {
		return edit.sourceEnd
	}
	if !end && index < edit.decodedEnd {
		return edit.source
	}
	return index + edit.sourceEnd - edit.decodedEnd
}
func passwordHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

// passwordNormalize는 단일 JSON escape·HTML entity·URL percent 해석의 원문 위치를 보존한다.
// 변경된 조각만 위치표에 넣고, 과도한 변환 수·잘못된 인코딩은 불명으로 돌려준다.
func passwordNormalize(input []byte, kind int, form bool) (passwordView, bool) {
	view := passwordView{data: input}
	var out []byte
	copied := 0
	replace := func(start, end int, value []byte) bool {
		if len(view.edits) >= maxPasswordTransforms {
			return false
		}
		if out == nil {
			out = make([]byte, 0, len(input))
		}
		out = append(out, input[copied:start]...)
		begin := len(out)
		out = append(out, value...)
		view.edits = append(view.edits, passwordEdit{start, end, begin, len(out)})
		copied = end
		return true
	}
	for i := 0; i < len(input); i++ {
		switch kind {
		case 0:
			if input[i] != '\\' {
				continue
			}
			if i+1 >= len(input) {
				continue
			}
			var value []byte
			end := i + 2
			switch input[i+1] {
			case '"', '\\', '/':
				value = []byte{input[i+1]}
			case 'b':
				value = []byte{'\b'}
			case 'f':
				value = []byte{'\f'}
			case 'n':
				value = []byte{'\n'}
			case 'r':
				value = []byte{'\r'}
			case 't':
				value = []byte{'\t'}
			case 'u':
				if i+6 > len(input) {
					return passwordView{}, false
				}
				n, err := strconv.ParseUint(string(input[i+2:i+6]), 16, 16)
				if err != nil {
					return passwordView{}, false
				}
				r := rune(n)
				end = i + 6
				if utf16.IsSurrogate(r) {
					if r < 0xD800 || r > 0xDBFF || i+12 > len(input) || string(input[i+6:i+8]) != "\\u" {
						return passwordView{}, false
					}
					next, err := strconv.ParseUint(string(input[i+8:i+12]), 16, 16)
					if err != nil || next < 0xDC00 || next > 0xDFFF {
						return passwordView{}, false
					}
					r = utf16.DecodeRune(r, rune(next))
					end = i + 12
				}
				value = utf8.AppendRune(nil, r)
			default:
				// JSON 밖의 리터럴 역슬래시다. JSON 본문 문법은 별도로 검사한다.
				continue
			}
			if !replace(i, end, value) {
				return passwordView{}, false
			}
			i = end - 1
		case 1:
			if input[i] != '&' {
				continue
			}
			end := i + 1
			for end < len(input) && end-i <= 34 && (input[end] >= 'a' && input[end] <= 'z' || input[end] >= 'A' && input[end] <= 'Z' || input[end] >= '0' && input[end] <= '9' || input[end] == '#') {
				end++
			}
			if end == i+1 {
				continue
			}
			semicolon := end < len(input) && input[end] == ';'
			if semicolon {
				end++
			}
			token := string(input[i:end])
			decoded := html.UnescapeString(token)
			if decoded == token {
				if semicolon {
					return passwordView{}, false
				}
				continue
			}
			if !semicolon || utf8.RuneCountInString(decoded) > 2 {
				return passwordView{}, false
			}
			if !replace(i, end, []byte(decoded)) {
				return passwordView{}, false
			}
			i = end - 1
		case 2:
			if input[i] == '+' && form {
				if !replace(i, i+1, []byte{' '}) {
					return passwordView{}, false
				}
				continue
			}
			if input[i] != '%' {
				continue
			}
			if i+1 == len(input) || !passwordHex(input[i+1]) {
				continue
			}
			if i+2 >= len(input) || !passwordHex(input[i+2]) {
				return passwordView{}, false
			}
			value, _ := strconv.ParseUint(string(input[i+1:i+3]), 16, 8)
			if !replace(i, i+3, []byte{byte(value)}) {
				return passwordView{}, false
			}
			i += 2
		}
	}
	if out != nil {
		out = append(out, input[copied:]...)
		view.data = out
	}
	if !utf8.Valid(view.data) {
		return passwordView{}, false
	}
	return view, true
}

// passwordFold는 Unicode simple fold의 같은 순환에 속한 rune을 한 값으로 모은다.
// UTF-8 길이가 바뀐 자리만 위치표에 넣어, 검색 사본의 위치를 원문 바이트로 되돌린다.
// ASCII 대소문자처럼 길이가 같은 치환은 위치표/변환 상한을 쓰지 않는다.
func passwordFold(input []byte) (passwordView, bool) {
	if !utf8.Valid(input) {
		return passwordView{}, false
	}
	view := passwordView{data: input}
	var out []byte
	copied := 0
	for at := 0; at < len(input); {
		r, size := utf8.DecodeRune(input[at:])
		fold := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			fold = min(fold, next)
		}
		if fold >= 'A' && fold <= 'Z' {
			fold += 'a' - 'A'
		}
		if fold != r {
			if out == nil {
				out = make([]byte, 0, len(input))
			}
			out = append(out, input[copied:at]...)
			begin := len(out)
			out = utf8.AppendRune(out, fold)
			if utf8.RuneLen(fold) != size {
				if len(view.edits) >= maxPasswordTransforms {
					return passwordView{}, false
				}
				view.edits = append(view.edits, passwordEdit{at, at + size, begin, len(out)})
			}
			copied = at + size
		}
		at += size
	}
	if out != nil {
		view.data = append(out, input[copied:]...)
	}
	return view, true
}

func passwordSpans(input []byte, passwords [][]byte, form bool) ([]passwordSpan, bool) {
	if !utf8.Valid(input) {
		return nil, false
	}
	foldedPasswords := make([][]byte, 0, len(passwords))
	for _, password := range passwords {
		if len(password) < minProtectedValueBytes || len(password) > maxRedactValueBytes {
			return nil, false
		}
		folded, ok := passwordFold(password)
		if !ok {
			return nil, false
		}
		foldedPasswords = append(foldedPasswords, folded.data)
	}
	spans := []passwordSpan{}
	collect := func(view passwordView) bool {
		folded, ok := passwordFold(view.data)
		if !ok {
			return false
		}
		for i, password := range passwords {
			// 지원하지 않는 알려진 base64 표현도 일치 없음으로 처리하지 않는다. 범용 인코딩 DLP는 아니다.
			for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
				if bytes.Contains(view.data, []byte(encoding.EncodeToString(password))) {
					return false
				}
			}
			offset := 0
			for offset < len(folded.data) {
				index := bytes.Index(folded.data[offset:], foldedPasswords[i])
				if index < 0 {
					break
				}
				start := offset + index
				end := start + len(foldedPasswords[i])
				spans = append(spans, passwordSpan{view.sourceIndex(folded.sourceIndex(start, false), false), view.sourceIndex(folded.sourceIndex(end, true), true)})
				if len(spans) > maxPasswordSpans {
					return false
				}
				offset = end
			}
		}
		return true
	}
	if !collect(passwordView{data: input}) {
		return nil, false
	}
	// 이미 일치한 값의 리터럴 특수문자는 추가 인코딩으로 오인하지 않는다.
	// 같은 길이의 NUL 사본만 검사하므로 다른 구간의 원문 위치는 그대로다.
	blankKnown := func(value []byte) ([]byte, bool) {
		folded, ok := passwordFold(value)
		if !ok {
			return nil, false
		}
		out := bytes.Clone(value)
		for _, password := range foldedPasswords {
			for at := 0; at < len(folded.data); {
				found := bytes.Index(folded.data[at:], password)
				if found < 0 {
					break
				}
				at += found
				clear(out[folded.sourceIndex(at, false):folded.sourceIndex(at+len(password), true)])
				at += len(password)
			}
		}
		return out, true
	}
	inputWithoutKnown, ok := blankKnown(input)
	if !ok {
		return nil, false
	}
	for kind := 0; kind < 3; kind++ {
		view, ok := passwordNormalize(input, kind, form)
		if !ok {
			view, ok = passwordNormalize(inputWithoutKnown, kind, form)
		}
		if !ok {
			return nil, false
		}
		if len(view.edits) == 0 {
			continue
		}
		if !collect(view) {
			return nil, false
		}
		// 첫 변환이 새 escape/entity/percent 표현을 만들면 이중 해석이다. 독립된 다른 필드와 구별한다.
		withoutKnown, ok := blankKnown(view.data)
		if !ok {
			return nil, false
		}
		for nextKind := 0; nextKind < 3; nextKind++ {
			next, ok := passwordNormalize(withoutKnown, nextKind, form)
			if !ok {
				return nil, false
			}
			at := 0
			for _, edit := range next.edits {
				for at < len(view.edits) && view.edits[at].decodedEnd <= edit.source {
					at++
				}
				if at < len(view.edits) && view.edits[at].decoded < edit.sourceEnd {
					return nil, false
				}
			}
		}
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].begin == spans[j].begin {
			return spans[i].end < spans[j].end
		}
		return spans[i].begin < spans[j].begin
	})
	merged := spans[:0]
	for _, span := range spans {
		if len(merged) > 0 && span.begin <= merged[len(merged)-1].end {
			merged[len(merged)-1].end = max(merged[len(merged)-1].end, span.end)
		} else {
			merged = append(merged, span)
		}
	}
	return merged, true
}

// prepareResponse는 보호 세대·비밀번호 가림·기존 응답 검사를 같은 잠금 아래 수행한다.
func (e *Execution) prepareResponse(response *http.Response, body []byte, generation uint64, limit int64, partial, oversized bool) ([]byte, bool, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.secretsGen != generation {
		return nil, false, false
	}
	// 일반 제어 비밀이 비밀번호와 같은 값이어도 구간 가림으로 보류를 우회하지 않는다.
	if !e.protectedHTTPValuesSafeLocked(response, body, e.secrets) {
		return nil, false, false
	}
	masked := false
	if e.cfg.PasswordResponse == PasswordResponseMaskSpans && len(e.passwords) > 0 {
		if partial || oversized || response == nil || response.Uncompressed || !identityEncoding(response.Header, "Content-Encoding") || !identityEncoding(response.Trailer, "Content-Encoding") {
			return nil, false, false
		}
		// 이름의 기존 ToLower 대조는 simple fold와 일부 문자(İ 등)에서 다르다.
		// 메타데이터의 기존 검출을 먼저 보존하고, 본문은 아래에서 구간 가림한다.
		if !e.protectedHTTPValuesSafeLocked(response, nil, e.passwords) {
			return nil, false, false
		}
		for _, fields := range []http.Header{response.Header, response.Trailer} {
			for name, values := range fields {
				if strings.EqualFold(name, "Content-Transfer-Encoding") {
					return nil, false, false
				}
				for _, value := range append([]string{name}, values...) {
					spans, ok := passwordSpans([]byte(value), e.passwords, false)
					if !ok || len(spans) > 0 {
						return nil, false, false
					}
				}
			}
		}
		for _, value := range []string{response.Status, response.Proto} {
			spans, ok := passwordSpans([]byte(value), e.passwords, false)
			if !ok || len(spans) > 0 {
				return nil, false, false
			}
		}
		if len(body) > 0 {
			media, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
			if err != nil || len(response.Header.Values("Content-Type")) != 1 || params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8") && !strings.EqualFold(params["charset"], "us-ascii") {
				return nil, false, false
			}
			jsonBody := media == "application/json" || strings.HasSuffix(media, "+json")
			if !jsonBody && media != "text/plain" && media != "text/html" && media != "application/x-www-form-urlencoded" || jsonBody && !json.Valid(body) {
				return nil, false, false
			}
			spans, ok := passwordSpans(body, e.passwords, media == "application/x-www-form-urlencoded")
			if !ok {
				return nil, false, false
			}
			if len(spans) > 0 {
				out := make([]byte, 0, len(body))
				at := 0
				replacement := []byte("[REDACTED]")
				if media == "application/x-www-form-urlencoded" {
					replacement = []byte("%5BREDACTED%5D")
				}
				for _, span := range spans {
					out = append(out, body[at:span.begin]...)
					out = append(out, replacement...)
					at = span.end
				}
				out = append(out, body[at:]...)
				if int64(len(out)) > limit || jsonBody && !json.Valid(out) {
					clear(out)
					return nil, false, false
				}
				remaining, checked := passwordSpans(out, e.passwords, media == "application/x-www-form-urlencoded")
				if !checked || len(remaining) != 0 {
					clear(out)
					return nil, false, false
				}
				body = out
				masked = true
			}
		}
	}
	if !e.safeHTTPResponseLocked(response, body) {
		if masked {
			clear(body)
		}
		return nil, false, false
	}
	return body, masked, true
}

// authSessionSafe는 모델에 넘길 세션만 검사한다. 비밀번호와 겹친 값은 세션으로 승격하지 않는다.
func (e *Execution) authSessionSafe(headers http.Header) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.passwords) == 0 {
		return true
	}
	for name, values := range headers {
		for _, value := range append([]string{name}, values...) {
			spans, ok := passwordSpans([]byte(value), e.passwords, false)
			if !ok || len(spans) > 0 {
				return false
			}
		}
	}
	return true
}
