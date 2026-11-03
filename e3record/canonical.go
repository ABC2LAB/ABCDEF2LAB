package e3record

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"
)

// canonical converts already typed and validated v1 input. It agrees with
// Python sort_keys=True/ensure_ascii=False for our integer-only schema.
func canonical(v any) ([]byte, error) {
	if !canonicalInputValid(reflect.ValueOf(v)) {
		return nil, ErrInvalid
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return nil, ErrInvalid
	}
	out, err := appendCanonical(nil, value)
	if err != nil || len(out) == 0 || len(out) > MaxCanonicalBytes {
		return nil, ErrInvalid
	}
	return out, nil
}

func appendCanonical(out []byte, value any) ([]byte, error) {
	switch v := value.(type) {
	case nil:
		return append(out, "null"...), nil
	case bool:
		return strconv.AppendBool(out, v), nil
	case string:
		if !utf8.ValidString(v) {
			return nil, ErrInvalid
		}
		out = append(out, '"')
		const hex = "0123456789abcdef"
		for _, r := range v {
			switch r {
			case '"', '\\':
				out = append(out, '\\', byte(r))
			case '\b':
				out = append(out, '\\', 'b')
			case '\f':
				out = append(out, '\\', 'f')
			case '\n':
				out = append(out, '\\', 'n')
			case '\r':
				out = append(out, '\\', 'r')
			case '\t':
				out = append(out, '\\', 't')
			default:
				if r < 0x20 {
					out = append(out, '\\', 'u', '0', '0', hex[byte(r)>>4], hex[byte(r)&15])
				} else {
					out = utf8.AppendRune(out, r)
				}
			}
		}
		return append(out, '"'), nil
	case json.Number:
		n, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil {
			return nil, ErrInvalid
		}
		return strconv.AppendInt(out, n, 10), nil
	case []any:
		out = append(out, '[')
		for i, item := range v {
			if i > 0 {
				out = append(out, ',')
			}
			var err error
			out, err = appendCanonical(out, item)
			if err != nil {
				return nil, err
			}
		}
		return append(out, ']'), nil
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out = append(out, '{')
		for i, key := range keys {
			if i > 0 {
				out = append(out, ',')
			}
			var err error
			out, err = appendCanonical(out, key)
			if err != nil {
				return nil, err
			}
			out = append(out, ':')
			out, err = appendCanonical(out, v[key])
			if err != nil {
				return nil, err
			}
		}
		return append(out, '}'), nil
	default:
		return nil, ErrInvalid
	}
}

func canonicalInputValid(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	if v.Type() == reflect.TypeOf(time.Time{}) {
		return true
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		return v.IsNil() || canonicalInputValid(v.Elem())
	case reflect.String:
		return utf8.ValidString(v.String())
	case reflect.Float32, reflect.Float64:
		return false
	case reflect.Uint, reflect.Uint64:
		return v.Uint() <= math.MaxInt64
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return false
		}
		it := v.MapRange()
		for it.Next() {
			if !canonicalInputValid(it.Key()) || !canonicalInputValid(it.Value()) {
				return false
			}
		}
	case reflect.Array, reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if !canonicalInputValid(v.Index(i)) {
				return false
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath == "" && !canonicalInputValid(v.Field(i)) {
				return false
			}
		}
	}
	return true
}
