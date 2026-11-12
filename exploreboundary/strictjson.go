package exploreboundary

import (
	"bytes"
	"encoding"
	"encoding/json"
	"io"
	"reflect"
	"strings"
)

// strictKeys는 제어 소켓 JSON을 디코드하기 전에 키를 엄격히 본다. encoding/json은 키를 대소문자 구분 없이 맞추고
// 중복 키는 마지막 값을 쓰며, Decoder는 뒤에 붙은 `]`·`}`를 다음 값으로 남긴다(#488 리뷰). 그래서 객체마다 중복 키를
// 거부하고, 구조체로 가는 객체는 json 태그 이름과 정확히 같은 키만 받고, 값 하나 뒤에는 공백만 허용한다.
func strictKeys(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := walkValue(d, reflect.TypeOf(v)); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrConfig
	}
	return nil
}

var textUnmarshaler = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
var jsonUnmarshaler = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()

// fieldsOf는 구조체의 JSON 키 이름과 필드 형식이다. 직접 디코드하는 형식이나 구조체가 아니면 nil이다(키 이름은 안 보고
// 중복·형식만 본다).
func fieldsOf(t reflect.Type) map[string]reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct || reflect.PointerTo(t).Implements(jsonUnmarshaler) || reflect.PointerTo(t).Implements(textUnmarshaler) {
		return nil
	}
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" || !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			for k, ft := range fieldsOf(f.Type) {
				out[k] = ft
			}
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	return out
}

func elemOf(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map) {
		return t.Elem()
	}
	return nil
}

func walkValue(d *json.Decoder, t reflect.Type) error {
	tok, err := d.Token()
	if err != nil {
		return ErrConfig
	}
	switch tok {
	case json.Delim('{'):
		fields := fieldsOf(t)
		mapElem := elemOf(t)
		seen := map[string]bool{}
		for d.More() {
			keyTok, err := d.Token()
			key, ok := keyTok.(string)
			if err != nil || !ok || seen[key] {
				return ErrConfig
			}
			seen[key] = true
			next := mapElem
			if fields != nil {
				ft, known := fields[key]
				if !known {
					return ErrConfig
				}
				next = ft
			}
			if err := walkValue(d, next); err != nil {
				return err
			}
		}
		if end, err := d.Token(); err != nil || end != json.Delim('}') {
			return ErrConfig
		}
	case json.Delim('['):
		for d.More() {
			if err := walkValue(d, elemOf(t)); err != nil {
				return err
			}
		}
		if end, err := d.Token(); err != nil || end != json.Delim(']') {
			return ErrConfig
		}
	}
	return nil
}
