// Package loginattempt는 모델이 직접 로그인하는 회차(2026-11-26 사용자 결정)의 로그인 시도를 프로그램이 가려내고, 성공·실패를
// 판정하고, 아이디별 연속 실패 상한과 탐색 쓰기 예외 상태를 지키고, 시도마다 기록 행을 만든다. 계약은
// docs/contracts/model-login-attempts.md(v1)다. 대상 앱의 로그인 경로·칸 이름·쿠키 이름은 코드에 넣지 않고 요청·응답의
// 모양만 본다. 탐색 경계와 E②(진단)가 같은 규칙을 쓰도록 전송 경계 밖의 순수 판정만 둔다.
package loginattempt

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 본문 형식(§3 format).
const (
	FormatForm      = "form"
	FormatJSON      = "json"
	FormatMultipart = "multipart"
)

// 계약 §2-1의 이름 규칙이다. 앱 고유 이름이 아니라 흔한 일반어만 둔다.
var (
	passwordNames      = []string{"password", "passwd", "passphrase", "pass", "pwd"}
	identifierNames    = []string{"username", "user", "login", "email", "mail", "account", "id"}
	identifierEndNames = []string{"username", "user", "login", "email", "mail", "account"}
)

const (
	maxIdentifierBytes = 256
	maxFieldNames      = 32
	maxFieldNameBytes  = 64
	// minResponseValueBytes보다 짧은 값은 앞 응답에 우연히 들어 있을 수 있어 fields_from_response로 보지 않는다.
	minResponseValueBytes = 8
)

// Account는 등록 계정 하나다. 값은 회차 메모리에만 두고 기록·로그에 넣지 않는다.
type Account struct {
	Label    string
	Username string
	Password string
}

func (Account) String() string   { return "loginattempt.Account[protected]" }
func (Account) GoString() string { return "loginattempt.Account[protected]" }

type field struct {
	name   string
	value  string
	isText bool
}

// Candidate는 로그인 시도로 본 요청의 본문 판정이다(§2-1). 값 칸(identifier·password)은 회차 메모리에만 둔다.
type Candidate struct {
	Format             string
	IdentifierField    string
	PasswordField      string
	Identifier         string
	OtherFields        []string
	FieldsFromResponse []string
	AccountLabel       string
	PasswordRegistered bool
	otherValues        map[string]string
	// Pair는 아이디 칸과 비밀번호 칸 값이 같은 등록 계정 하나의 아이디·비밀번호와 정확히 같다는 뜻이다(§2-2 예외 조건).
	Pair     bool
	password string
}

func (Candidate) String() string { return "loginattempt.Candidate[protected]" }

// Classify는 요청이 §2-1의 본문 로그인인지 가린다. 아니면 false다. fromResponse는 값이 같은 회차의 앞선 대상 응답에
// 그대로 있었는지 알려 준다(CSRF 토큰 같은 칸, nil이면 없음으로 본다).
func Classify(method, contentType string, body []byte, accounts []Account, fromResponse func(string) bool) (Candidate, bool) {
	if method != "POST" {
		return Candidate{}, false
	}
	format, fields, ok := parseBody(contentType, body)
	if !ok || len(fields) == 0 {
		return Candidate{}, false
	}
	password := -1
	for i, f := range fields {
		if !isPasswordCandidate(f, accounts) {
			continue
		}
		if password >= 0 {
			// 비밀번호 후보가 둘 이상이면 비밀번호 바꾸기·가입이다(§2-1).
			return Candidate{}, false
		}
		password = i
	}
	if password < 0 || !fields[password].isText {
		return Candidate{}, false
	}
	responded := map[int]bool{}
	if fromResponse != nil {
		for i, f := range fields {
			if i != password && f.isText && len(f.value) >= minResponseValueBytes && fromResponse(f.value) {
				responded[i] = true
			}
		}
	}
	identifier := pickIdentifier(fields, password, responded, accounts)
	c := Candidate{Format: format, PasswordField: fields[password].name, password: fields[password].value}
	if identifier >= 0 {
		c.IdentifierField = fields[identifier].name
		c.Identifier = truncateUTF8(fields[identifier].value, maxIdentifierBytes)
		for _, a := range accounts {
			if a.Username != "" && a.Username == fields[identifier].value {
				c.AccountLabel = a.Label
				c.PasswordRegistered = a.Password != "" && a.Password == fields[password].value
				c.Pair = c.PasswordRegistered
				break
			}
		}
	}
	for i, f := range fields {
		if i == password || i == identifier {
			continue
		}
		if c.otherValues == nil {
			c.otherValues = map[string]string{}
		}
		c.otherValues[f.name] = f.value
		c.OtherFields = appendName(c.OtherFields, f.name)
		if responded[i] {
			c.FieldsFromResponse = appendName(c.FieldsFromResponse, f.name)
		}
	}
	return c, true
}

func appendName(out []string, name string) []string {
	if len(out) >= maxFieldNames || name == "" || len(name) > maxFieldNameBytes {
		return out
	}
	for _, n := range out {
		if n == name {
			return out
		}
	}
	return append(out, name)
}

func isPasswordCandidate(f field, accounts []Account) bool {
	if f.isText {
		for _, a := range accounts {
			if a.Password != "" && a.Password == f.value {
				return true
			}
		}
	}
	// 이름 규칙 후보는 값이 든 문자열 칸만이다. 로그인 폼의 rememberPassword=on·showPassword=false 같은 켜고 끄는 칸까지
	// 후보로 세면 후보가 둘이 돼 로그인으로 못 가린다(#963 재상 봇 P3).
	if !f.isText || switchValue(f.value) {
		return false
	}
	if has(passwordNames, strings.ToLower(f.name)) {
		return true
	}
	// 끝 조각만 보면 가입의 passwordConfirm(끝 조각 confirm)을 놓쳐 비밀번호 후보가 하나로 남는다. 조각 어느 것이든 본다
	// (계약 §2-1 예시: 가입 password·passwordConfirm은 후보 둘).
	for _, piece := range pieces(f.name) {
		if has(passwordNames, piece) {
			return true
		}
	}
	return false
}

// switchValues는 켜고 끄는 칸의 값이다. 비밀번호 값으로 보지 않는다.
var switchValues = []string{"", "on", "off", "true", "false", "yes", "no", "1", "0"}

func switchValue(v string) bool { return has(switchValues, strings.ToLower(strings.TrimSpace(v))) }

// pickIdentifier는 §2-1의 세 규칙을 차례로 보고 처음 맞는 칸을 고른다. 규칙이 먼저고, 같은 규칙 안에서는 칸 순서다.
func pickIdentifier(fields []field, password int, responded map[int]bool, accounts []Account) int {
	usable := func(i int) bool { return i != password && fields[i].isText && !responded[i] }
	for i, f := range fields {
		if !usable(i) {
			continue
		}
		for _, a := range accounts {
			if a.Username != "" && a.Username == f.value {
				return i
			}
		}
	}
	for i, f := range fields {
		if usable(i) && has(identifierNames, strings.ToLower(f.name)) {
			return i
		}
	}
	for i, f := range fields {
		if usable(i) && has(identifierEndNames, endPiece(f.name)) {
			return i
		}
	}
	return -1
}

// endPiece는 칸 이름을 . [ ] _ -와 소문자→대문자 경계로 나눈 마지막 조각(소문자)이다.
func endPiece(name string) string {
	p := pieces(name)
	if len(p) == 0 {
		return ""
	}
	return p[len(p)-1]
}

// pieces는 칸 이름을 . [ ] _ -와 소문자→대문자 경계로 나눈 조각들(소문자)이다.
func pieces(name string) []string {
	var pieces []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			pieces = append(pieces, string(cur))
			cur = cur[:0]
		}
	}
	var prev rune
	for _, r := range name {
		switch {
		case r == '.' || r == '[' || r == ']' || r == '_' || r == '-':
			flush()
		case unicode.IsUpper(r) && unicode.IsLower(prev):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
		prev = r
	}
	flush()
	for i := range pieces {
		pieces[i] = strings.ToLower(pieces[i])
	}
	return pieces
}

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func parseBody(contentType string, body []byte) (string, []field, bool) {
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", nil, false
	}
	switch media {
	case "application/x-www-form-urlencoded":
		fields, ok := parseForm(body)
		return FormatForm, fields, ok
	case "application/json":
		fields, ok := parseJSON(body)
		return FormatJSON, fields, ok
	case "multipart/form-data":
		fields, ok := parseMultipart(body, params["boundary"])
		return FormatMultipart, fields, ok
	}
	return "", nil, false
}

// parseForm은 본문 순서대로 칸을 읽는다(url.ParseQuery는 순서를 잃는다). 풀 수 없는 칸이 있으면 판정하지 않는다.
func parseForm(body []byte) ([]field, bool) {
	var out []field
	for _, part := range strings.Split(string(body), "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		key, err := url.QueryUnescape(k)
		if err != nil {
			return nil, false
		}
		value, err := url.QueryUnescape(v)
		if err != nil {
			return nil, false
		}
		out = append(out, field{name: key, value: value, isText: true})
	}
	return out, true
}

// parseJSON은 객체 본문의 칸을 키가 나온 순서대로 점 경로로 편다. 배열 안은 보지 않고 칸 이름만 남긴다.
func parseJSON(body []byte) ([]field, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, false
	}
	var out []field
	if !walkObject(dec, "", &out, 0) {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return out, true
}

const maxJSONDepth = 8

func walkObject(dec *json.Decoder, prefix string, out *[]field, depth int) bool {
	if depth > maxJSONDepth {
		return false
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		key, ok := tok.(string)
		if !ok {
			return false
		}
		name := key
		if prefix != "" {
			name = prefix + "." + key
		}
		val, err := dec.Token()
		if err != nil {
			return false
		}
		switch v := val.(type) {
		case json.Delim:
			if v == '{' {
				if !walkObject(dec, name, out, depth+1) {
					return false
				}
				continue
			}
			if v != '[' || !skipArray(dec) {
				return false
			}
			*out = append(*out, field{name: name})
		case string:
			*out = append(*out, field{name: name, value: v, isText: true})
		default:
			*out = append(*out, field{name: name})
		}
	}
	end, err := dec.Token()
	return err == nil && end == json.Delim('}')
}

func skipArray(dec *json.Decoder) bool {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '[', '{':
				depth++
			case ']', '}':
				depth--
			}
		}
	}
	return true
}

// parseMultipart는 파일 부분이 하나라도 있으면 로그인 시도로 보지 않는다(§2-1·§2-2).
func parseMultipart(body []byte, boundary string) ([]field, bool) {
	if boundary == "" {
		return nil, false
	}
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	var out []field
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			return out, true
		}
		if err != nil || p.FileName() != "" {
			return nil, false
		}
		value, err := io.ReadAll(p)
		if err != nil {
			return nil, false
		}
		out = append(out, field{name: p.FormName(), value: string(value), isText: utf8.Valid(value)})
	}
}
