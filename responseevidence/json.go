package responseevidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
)

// JSONStructure는 JSON 응답의 경로·자료형 요약이다. 값 원문은 없고, 한 번만 나온 짧은 스칼라에만 비교용 digest가 있다.
type JSONStructure struct {
	RootType      string      `json:"root_type"`
	RootLength    int         `json:"root_length"`
	ErrorShape    bool        `json:"error_shape,omitempty"`
	DuplicateKeys bool        `json:"duplicate_keys,omitempty"`
	Fields        []JSONField `json:"fields"`
	FieldsOmitted int         `json:"fields_omitted,omitempty"`
}

// JSONField는 경로 하나의 관찰이다. 배열 원소 경로는 []로 합친다(items[].id).
type JSONField struct {
	Path string `json:"path"`
	// Type은 object·array·string·number·bool·null 또는 원소마다 달랐으면 mixed다.
	Type  string `json:"type"`
	Count int    `json:"count"`
	// Length는 배열·객체의 원소 수다. 여러 번 나온 경로는 가장 큰 값이다.
	Length int  `json:"length,omitempty"`
	Empty  bool `json:"empty,omitempty"`
	// Shape는 문자열 값의 모양 범주다(email·uuid 등). 값은 없다.
	Shape string `json:"shape,omitempty"`
	Bool  *bool  `json:"bool,omitempty"`
	// ValueDigest는 한 번만 나온 짧은 스칼라 값의 비교용 digest다. 원본·재현 응답의 값 같음만 알려 준다.
	ValueDigest string `json:"value_digest,omitempty"`
	// Protected는 값이나 이름에 제어 비밀·표식·보호 세션 값이 들어 있어 모양·digest를 남기지 않았다는 뜻이다.
	Protected bool `json:"protected,omitempty"`
}

var jsonKeyPattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$\-]{0,63}$`)

type jsonField struct {
	JSONField
	types map[string]bool
	order int
}

type jsonWalker struct {
	dec       *json.Decoder
	l         Limits
	protected Protected
	limited   map[string]bool
	fields    map[string]*jsonField
	omitted   map[string]bool
	strings   bytes.Buffer
	dup       bool
	stack     bool
}

const jsonSignalTextBytes = 256 << 10

// 기록 깊이를 넘긴 값도 문법 검사를 하므로 재귀 깊이에 별도 절대 상한을 둔다.
// 이보다 깊은 입력은 일부 구조가 확인된 것처럼 다루지 않는다.
const jsonMaximumParserDepth = 256

var errJSONShape = errors.New("responseevidence: invalid json")

func extractJSON(body []byte, l Limits, protected Protected, limited map[string]bool) (*JSONStructure, []string, bool) {
	w := &jsonWalker{dec: json.NewDecoder(bytes.NewReader(body)), l: l, protected: protected, limited: limited,
		fields: map[string]*jsonField{}, omitted: map[string]bool{}}
	w.dec.UseNumber()
	rootType, rootLength, err := w.value("", 0, true, false)
	if err != nil {
		return nil, nil, false
	}
	if _, err := w.dec.Token(); err != io.EOF {
		// 값 뒤에 다른 JSON 값이나 쓰레기가 있으면 하나의 응답 구조로 보지 않는다.
		return nil, nil, false
	}
	out := &JSONStructure{RootType: rootType, RootLength: rootLength, DuplicateKeys: w.dup, FieldsOmitted: len(w.omitted)}
	if w.dup {
		limited[LimitDuplicate] = true
	}
	if len(w.omitted) != 0 {
		limited[LimitFields] = true
	}
	keys := make([]string, 0, len(w.fields))
	for path := range w.fields {
		keys = append(keys, path)
	}
	sort.Strings(keys)
	for _, path := range keys {
		f := w.fields[path]
		if len(f.types) > 1 {
			f.Type, f.Shape, f.Bool, f.ValueDigest = "mixed", "", nil, ""
		}
		if f.Count != 1 {
			f.ValueDigest, f.Bool = "", nil
		}
		out.Fields = append(out.Fields, f.JSONField)
		if rootType == "object" && !strings.Contains(path, ".") && !strings.Contains(path, "[") {
			switch strings.ToLower(path) {
			case "error", "errors", "exception", "fault":
				out.ErrorShape = true
			}
		}
	}
	if out.Fields == nil {
		out.Fields = []JSONField{}
	}
	// 오류 신호는 풀어 쓴 문자열 값에서 찾는다(JSON 이스케이프된 줄바꿈 때문에 원문으로는 스택 모양을 못 본다).
	signals := detectSignals(w.strings.Bytes(), "plain_text")
	if w.stack {
		signals = addSignal(signals, SignalStackTraceField)
	}
	return out, signals, true
}

// value는 값 하나를 읽고 자료형·길이를 돌려준다. record가 거짓이면 문법만 확인하고 경로를 담지 않는다.
func (w *jsonWalker) value(path string, depth int, record, repeated bool) (string, int, error) {
	if depth > jsonMaximumParserDepth {
		return "", 0, errJSONShape
	}
	tok, err := w.dec.Token()
	if err != nil {
		return "", 0, err
	}
	if depth > w.l.MaxDepth && record {
		w.limited[LimitDepth] = true
		record = false
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			seen := map[string]bool{}
			n := 0
			for w.dec.More() {
				kt, err := w.dec.Token()
				if err != nil {
					return "", 0, err
				}
				key, ok := kt.(string)
				if !ok {
					return "", 0, errJSONShape
				}
				if seen[key] {
					// 같은 키가 둘이면 해석기마다 다른 값을 고른다. 어느 쪽도 고르지 않고 상한 표시로 남긴다.
					w.dup = true
				}
				seen[key] = true
				n++
				child := w.childPath(path, key, record)
				if strings.EqualFold(key, "stack") || strings.EqualFold(key, "stacktrace") || strings.EqualFold(key, "stack_trace") {
					w.stack = true
				}
				if _, _, err := w.value(child, depth+1, record, repeated); err != nil {
					return "", 0, err
				}
			}
			if end, err := w.dec.Token(); err != nil || end != json.Delim('}') {
				return "", 0, errJSONShape
			}
			w.note(path, "object", record, repeated, n, n == 0, "", nil, "", false)
			return "object", n, nil
		case '[':
			n := 0
			for w.dec.More() {
				deep := record
				if n >= w.l.MaxArrayItems && record {
					// 길이는 끝까지 세고, 상한 뒤 원소의 하위 구조만 담지 않는다.
					w.limited[LimitArrayItems] = true
					deep = false
				}
				if _, _, err := w.value(path+"[]", depth+1, deep, true); err != nil {
					return "", 0, err
				}
				n++
			}
			if end, err := w.dec.Token(); err != nil || end != json.Delim(']') {
				return "", 0, errJSONShape
			}
			w.note(path, "array", record, repeated, n, n == 0, "", nil, "", false)
			return "array", n, nil
		}
		return "", 0, errJSONShape
	case string:
		if w.strings.Len() < jsonSignalTextBytes {
			w.strings.WriteString(v)
			w.strings.WriteByte('\n')
		}
		if !record {
			return "string", 0, nil
		}
		// 담을 경로의 값만 가림 검사를 한다(검사 비용을 담는 필드 수로 묶는다).
		protected := strings.Contains(path, "{protected}") || w.protected != nil && v != "" && w.protected(v)
		shape, digest := "", ""
		if !protected {
			shape = stringShape(v)
			// 자격·세션 모양의 값은 digest도 남기지 않는다(같은 세션인지 비교할 필요가 없고 값 대신 쓰일 수 있다).
			if len(v) > w.l.MaxStringBytes {
				w.limited[LimitStringBytes] = true
			} else if shape != "jwt" && !credentialPath(path) {
				digest = valueDigest("string", v)
			}
		}
		w.note(path, "string", record, repeated, 0, v == "", shape, nil, digest, protected)
		return "string", 0, nil
	case json.Number:
		if !record {
			return "number", 0, nil
		}
		value := v.String()
		protected := strings.Contains(path, "{protected}") || w.protected != nil && w.protected(value)
		digest := ""
		if !protected && !credentialPath(path) && len(value) <= w.l.MaxStringBytes {
			digest = valueDigest("number", value)
		} else if len(value) > w.l.MaxStringBytes {
			w.limited[LimitStringBytes] = true
		}
		w.note(path, "number", record, repeated, 0, false, "", nil, digest, protected)
		return "number", 0, nil
	case bool:
		if !record {
			return "bool", 0, nil
		}
		b := v
		protected := strings.Contains(path, "{protected}") || w.protected != nil && w.protected(map[bool]string{true: "true", false: "false"}[v])
		digest := ""
		if !protected && !credentialPath(path) {
			digest = valueDigest("bool", map[bool]string{true: "true", false: "false"}[v])
		}
		w.note(path, "bool", record, repeated, 0, false, "", &b, digest, protected)
		return "bool", 0, nil
	case nil:
		w.note(path, "null", record, repeated, 0, false, "", nil, "", false)
		return "null", 0, nil
	}
	return "", 0, errJSONShape
}

func (w *jsonWalker) childPath(parent, key string, record bool) string {
	name := key
	if !jsonKeyPattern.MatchString(key) {
		// 숫자 ID·이메일·점이 든 키는 데이터일 수 있고 경로 구분자와 섞인다. 이름 대신 자리표시로 남긴다.
		name = "{key}"
	} else if record && w.protected != nil && w.protected(key) {
		name = "{protected}"
	}
	if parent == "" {
		return name
	}
	return parent + "." + name
}

func (w *jsonWalker) note(path, typ string, record, repeated bool, length int, empty bool, shape string, b *bool, digest string, protected bool) {
	if !record {
		return
	}
	if path == "" {
		if typ == "object" || typ == "array" {
			return
		}
		path = "$"
	}
	f := w.fields[path]
	if f == nil {
		if len(w.fields) >= w.l.MaxFields {
			w.omitted[path] = true
			return
		}
		f = &jsonField{JSONField: JSONField{Path: path, Type: typ, Shape: shape, Bool: b, ValueDigest: digest, Protected: protected},
			types: map[string]bool{}, order: len(w.fields)}
		w.fields[path] = f
	} else if f.Shape != shape {
		f.Shape = "mixed"
	}
	f.types[typ] = true
	f.Count++
	if length > f.Length {
		f.Length = length
	}
	// 여러 번 나온 경로는 한 번이라도 비어 있지 않으면 비어 있지 않다고 본다.
	f.Empty = (f.Count == 1 || f.Empty) && empty
	f.Protected = f.Protected || protected
	if f.Protected {
		f.Shape, f.ValueDigest, f.Bool = "", "", nil
	}
	if repeated {
		f.ValueDigest, f.Bool = "", nil
	}
}

// credentialPath는 마지막 이름이 자격·세션 값을 담는 칸으로 보이는지다.
func credentialPath(path string) bool {
	name := path
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToLower(strings.TrimSuffix(name, "[]"))
	for _, word := range []string{"token", "password", "passwd", "secret", "session", "cookie", "auth", "apikey", "api_key", "credential", "jwt", "bearer"} {
		if strings.Contains(name, word) {
			return true
		}
	}
	return name == "sid"
}

func valueDigest(kind, value string) string {
	sum := sha256.Sum256([]byte("jindo-response-value/v1\x00" + kind + "\x00" + value))
	return hex.EncodeToString(sum[:8])
}

var (
	shapeNumeric  = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
	shapeUUID     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	shapeHex      = regexp.MustCompile(`^[0-9a-fA-F]{16,}$`)
	shapeDateTime = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}([T ][0-9]{2}:[0-9]{2}(:[0-9]{2}(\.[0-9]+)?)?(Z|[+-][0-9]{2}:?[0-9]{2})?)?$`)
	shapeEmail    = regexp.MustCompile(`^[^@\s]{1,64}@[^@\s]+\.[A-Za-z]{2,}$`)
	shapeURL      = regexp.MustCompile(`^(https?://|/)[^\s]*$`)
	shapeJWT      = regexp.MustCompile(`^[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*$`)
)

// stringShape는 값의 모양 범주만 돌려준다. 긴 값은 앞부분 모양으로 판단하지 않고 text다.
func stringShape(v string) string {
	switch {
	case v == "":
		return "empty"
	case len(v) > 4096:
		return "text"
	case shapeNumeric.MatchString(v):
		return "numeric"
	case shapeUUID.MatchString(v):
		return "uuid"
	case shapeDateTime.MatchString(v):
		return "datetime"
	case shapeJWT.MatchString(v):
		return "jwt"
	case shapeHex.MatchString(v):
		return "hex"
	case shapeEmail.MatchString(v):
		return "email"
	case shapeURL.MatchString(v):
		return "url"
	}
	return "text"
}
