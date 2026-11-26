package learnedlogin

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/url"
	"strings"
)

// Credential은 등록 계정의 자격이다. 서버가 결속 자격 파일에서 읽어 넘기고, 본문을 만든 뒤 지운다. 출력·로그에 값이 나가지
// 않게 문자열 표현을 가린다.
type Credential struct {
	Username string
	Password string
}

func (Credential) String() string   { return "learnedlogin.Credential[protected]" }
func (Credential) GoString() string { return "learnedlogin.Credential[protected]" }

// LoginRequest는 다시 만든 로그인 요청이다. Body에는 자격 값이 들어 있으므로 호출자가 보낸 뒤 지운다.
type LoginRequest struct {
	Method      string
	Path        string
	ContentType string
	Body        []byte
}

func (LoginRequest) String() string   { return "learnedlogin.LoginRequest[protected]" }
func (LoginRequest) GoString() string { return "learnedlogin.LoginRequest[protected]" }

// Build는 학습한 방법과 등록 자격, 로그인 화면에서 받은 칸 값(values)으로 로그인 요청을 다시 만든다. 칸 순서는 아이디·
// 비밀번호·나머지 칸(기록 순서)이다. 나머지 칸은 모두 values에 있어야 한다: 값을 지어내지 않는다. 방법이 Validate를
// 통과하지 않았거나 값이 모자라면 사유를 돌려준다.
func Build(m Method, cred Credential, values map[string]string) (LoginRequest, Reason) {
	if reason := m.Validate(); reason != "" {
		return LoginRequest{}, reason
	}
	if cred.Password == "" || m.IdentifierField != "" && cred.Username == "" {
		return LoginRequest{}, ReasonInputUnavailable
	}
	type pair struct{ name, value string }
	var fields []pair
	if m.IdentifierField != "" {
		fields = append(fields, pair{m.IdentifierField, cred.Username})
	}
	fields = append(fields, pair{m.PasswordField, cred.Password})
	for _, name := range m.OtherFields {
		v, ok := values[name]
		if !ok {
			return LoginRequest{}, ReasonInputUnavailable
		}
		fields = append(fields, pair{name, v})
	}
	out := LoginRequest{Method: m.Method, Path: m.Path}
	switch m.Format {
	case FormatForm:
		parts := make([]string, 0, len(fields))
		for _, f := range fields {
			parts = append(parts, url.QueryEscape(f.name)+"="+url.QueryEscape(f.value))
		}
		out.Body, out.ContentType = []byte(strings.Join(parts, "&")), "application/x-www-form-urlencoded"
	case FormatJSON:
		root := &jsonObject{}
		for _, f := range fields {
			if !root.set(strings.Split(f.name, "."), f.value) {
				return LoginRequest{}, ReasonInputUnavailable
			}
		}
		var b bytes.Buffer
		root.write(&b)
		out.Body, out.ContentType = b.Bytes(), "application/json"
	case FormatMultipart:
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		for _, f := range fields {
			if w.WriteField(f.name, f.value) != nil {
				return LoginRequest{}, ReasonInputUnavailable
			}
		}
		if w.Close() != nil {
			return LoginRequest{}, ReasonInputUnavailable
		}
		out.Body, out.ContentType = b.Bytes(), w.FormDataContentType()
	default:
		return LoginRequest{}, ReasonFormatUnsupported
	}
	return out, ""
}

// jsonObject는 칸 순서를 지키는 JSON 객체다. 점 경로(user.email)는 중첩 객체로 만든다. 같은 자리에 값과 객체가 겹치면
// 만들지 않는다(Validate가 먼저 막지만 여기서도 확인한다).
type jsonObject struct {
	keys   []string
	values map[string]any
}

func (o *jsonObject) set(path []string, value string) bool {
	if len(path) == 0 || path[0] == "" {
		return false
	}
	if o.values == nil {
		o.values = map[string]any{}
	}
	key := path[0]
	existing, ok := o.values[key]
	if len(path) == 1 {
		if ok {
			return false
		}
		o.keys = append(o.keys, key)
		o.values[key] = value
		return true
	}
	child, isObject := existing.(*jsonObject)
	if ok && !isObject {
		return false
	}
	if !ok {
		child = &jsonObject{}
		o.keys = append(o.keys, key)
		o.values[key] = child
	}
	return child.set(path[1:], value)
}

func (o *jsonObject) write(b *bytes.Buffer) {
	b.WriteByte('{')
	for i, key := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONString(b, key)
		b.WriteByte(':')
		switch v := o.values[key].(type) {
		case *jsonObject:
			v.write(b)
		case string:
			writeJSONString(b, v)
		}
	}
	b.WriteByte('}')
}

func writeJSONString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	// Encode가 붙인 줄바꿈을 뗀다.
	b.Truncate(b.Len() - 1)
}
