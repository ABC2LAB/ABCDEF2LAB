package kgfixture

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
)

// maxDepth bounds nesting. The v1 registry is four levels deep (root → resources
// → policies/canaries → fields); anything deeper is not a v1 registry.
const maxDepth = 8

// integerLiteral is the only number form the decoder accepts. Fractions and
// exponents have no single canonical text across Go and Python, so they are
// refused before any digest exists. (The v1 schema has no numeric field, so an
// accepted integer still fails the schema check.)
var integerLiteral = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

var errDuplicateKey = errors.New("kgfixture: duplicate key")

// decodeStrict parses body into map[string]any / []any / string / bool / nil /
// json.Number values. encoding/json silently keeps the last of duplicate keys,
// so keys are compared after unescaping through Decoder.Token; an escaped alias
// of a key ("format" next to "format") is a duplicate. Every key and string
// must be printable ASCII so the canonical text is the same in Go and Python.
func decodeStrict(body []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := strictValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("kgfixture: trailing data")
	}
	return value, nil
}

func strictValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("kgfixture: nesting limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch t := token.(type) {
	case json.Delim:
		switch t {
		case '{':
			return strictObject(decoder, depth)
		case '[':
			return strictArray(decoder, depth)
		}
		return nil, errors.New("kgfixture: unexpected delimiter")
	case string:
		if !printableASCII(t) {
			return nil, errors.New("kgfixture: string outside printable ASCII")
		}
		return t, nil
	case json.Number:
		if !integerLiteral.MatchString(string(t)) {
			return nil, errors.New("kgfixture: non-integer number")
		}
		return t, nil
	case bool:
		return t, nil
	case nil:
		return nil, nil
	}
	return nil, errors.New("kgfixture: unexpected token")
}

func strictObject(decoder *json.Decoder, depth int) (any, error) {
	object := make(map[string]any)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok || !printableASCII(key) {
			return nil, errors.New("kgfixture: bad key")
		}
		if _, duplicate := object[key]; duplicate {
			return nil, errDuplicateKey
		}
		value, err := strictValue(decoder, depth+1)
		if err != nil {
			return nil, err
		}
		object[key] = value
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return nil, errors.New("kgfixture: unterminated object")
	}
	return object, nil
}

func strictArray(decoder *json.Decoder, depth int) (any, error) {
	array := []any{}
	for decoder.More() {
		value, err := strictValue(decoder, depth+1)
		if err != nil {
			return nil, err
		}
		array = append(array, value)
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
		return nil, errors.New("kgfixture: unterminated array")
	}
	return array, nil
}

// printableASCII admits 0x20..0x7E only: no control characters, no DEL, no
// non-ASCII (including the U+FFFD that encoding/json substitutes for invalid UTF-8).
func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// canonicalJSON is the digest input: object keys in byte order, no whitespace,
// and only `"` and `\` escaped. Because decodeStrict admits printable ASCII only,
// this is byte-identical to Python's
// json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).
func canonicalJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	if err := writeCanonical(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeCanonical(out *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			writeCanonicalString(out, key)
			out.WriteByte(':')
			if err := writeCanonical(out, v[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case string:
		writeCanonicalString(out, v)
	case bool:
		if v {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	default:
		// Numbers and null never pass the v1 schema; refusing them here keeps the
		// canonical form from depending on number formatting.
		return errors.New("kgfixture: value outside the canonical v1 types")
	}
	return nil
}

func writeCanonicalString(out *bytes.Buffer, s string) {
	out.WriteByte('"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			out.WriteByte('\\')
		}
		out.WriteByte(s[i])
	}
	out.WriteByte('"')
}
