package e3contract

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
)

// 생성 요청 본문 조립과 요청 본문 칸 객체 ID(사용자 결정 2026-11-19, 선언 create.id_field).
//
// 확정 단계(E③)가 본문 틀에 표식과(있으면) 객체 ID를 넣어 본문을 만들고, E²는 그 본문을 그대로 보낸다. 본문에는 표식이
// 들어 있으므로 표식과 같은 방식으로 문자열·JSON·텍스트·바이너리·gob·slog 출력을 막은 RequestBody로만 넘긴다(③(가):
// 표식은 E③가 만들고 직렬화되지 않는 비밀 타입으로 넘긴다). E²는 그 회차에 표식을 보호 값으로 등록했으므로 저널·영수증·
// 승인 미리보기에서 본문 속 표식을 가린다.
//
// 객체 ID는 비밀이 아니다(조회 경로·기록·로그에 남는다). 그래서 표식과 다른 난수에서 따로 만들고 표식에서 파생하지 않는다.

// ObjectIDPrefix는 확정 단계가 만든 객체 ID의 머리말이다. 사람이 기록에서 우리 시험 객체임을 알아보게 한다.
const ObjectIDPrefix = "e3id-"

// objectIDRandomBytes는 객체 ID 난수 바이트 수다(128비트).
const objectIDRandomBytes = 16

// ErrObjectID는 객체 ID를 만들 수 없을 때다(난수 실패, 표식과 겹침).
var ErrObjectID = errors.New("e3contract: 객체 ID를 만들 수 없다")

// ErrBodyAssembly는 본문 틀에 표식·ID를 넣을 수 없을 때다(틀이 계약 모양이 아님, 칸 경로가 객체가 아닌 값을 지남).
var ErrBodyAssembly = errors.New("e3contract: 생성 요청 본문을 만들 수 없다")

// NewObjectID는 r에서 읽은 새 난수로 객체 ID를 만든다. 표식과 같은 난수원을 써도 표식 뒤의 새 바이트를 읽으므로 표식에서
// 파생하지 않는다. 표식과 같거나 한쪽이 다른 쪽에 들어 있으면 만들지 않는다.
func NewObjectID(r io.Reader, marker Marker) (string, error) {
	if r == nil || !marker.Valid() {
		return "", ErrObjectID
	}
	b := make([]byte, objectIDRandomBytes)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", ErrObjectID
	}
	id := ObjectIDPrefix + hex.EncodeToString(b)
	m := string(marker.value)
	if strings.Contains(id, m) || strings.Contains(m, strings.TrimPrefix(id, ObjectIDPrefix)) || ContainsMarker([]byte(id), marker) {
		return "", ErrObjectID
	}
	return id, nil
}

// RequestBody는 표식이 든 생성 요청 본문이다. 값은 비공개이고 직렬화·출력하면 가려지거나 오류다(Marker와 같음).
type RequestBody struct {
	value []byte
	kind  BodyKind
}

// Kind는 본문 모양(json·form)이다. E²가 Content-Type을 정할 때 쓴다.
func (b RequestBody) Kind() BodyKind { return b.kind }

// Valid는 조립한 본문인지다.
func (b RequestBody) Valid() bool {
	return len(b.value) > 0 && (b.kind == BodyJSON || b.kind == BodyForm)
}

// Reveal은 본문 바이트의 사본이다. E²가 요청에 실을 때만 쓴다. 로그·기록·오류 문장에 넣지 않는다.
func (b RequestBody) Reveal() []byte { return bytes.Clone(b.value) }

func (RequestBody) String() string                 { return redactedMarker }
func (RequestBody) GoString() string               { return redactedMarker }
func (RequestBody) Format(f fmt.State, _ rune)     { _, _ = io.WriteString(f, redactedMarker) }
func (RequestBody) LogValue() slog.Value           { return slog.StringValue(redactedMarker) }
func (RequestBody) MarshalJSON() ([]byte, error)   { return nil, ErrMarkerSerialization }
func (RequestBody) MarshalText() ([]byte, error)   { return nil, ErrMarkerSerialization }
func (RequestBody) MarshalBinary() ([]byte, error) { return nil, ErrMarkerSerialization }
func (RequestBody) GobEncode() ([]byte, error)     { return nil, ErrMarkerSerialization }
func (*RequestBody) UnmarshalJSON([]byte) error    { return ErrMarkerSerialization }
func (*RequestBody) UnmarshalText([]byte) error    { return ErrMarkerSerialization }

// AssembleBody는 본문 틀에 표식(MarkerField)과, objectID가 있으면 ID(IDField)를 넣은 본문을 만든다. json은 틀의 값(수
// 원문 포함)을 그대로 두고 칸을 더하며, form은 키를 더해 다시 인코딩한다. 선언 검사(Validate)를 통과한 틀만 넣는다.
func AssembleBody(spec CreateSpec, marker Marker, objectID string) (RequestBody, error) {
	if !marker.Valid() || (spec.IDField == "") != (objectID == "") {
		return RequestBody{}, ErrBodyAssembly
	}
	fields := [][2]string{{spec.MarkerField, string(marker.value)}}
	if spec.IDField != "" {
		fields = append(fields, [2]string{spec.IDField, objectID})
	}
	switch spec.Kind {
	case BodyJSON:
		dec := json.NewDecoder(strings.NewReader(spec.BodyTemplate))
		dec.UseNumber()
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil || obj == nil {
			return RequestBody{}, ErrBodyAssembly
		}
		for _, f := range fields {
			if err := setJSONPath(obj, strings.Split(f[0], "."), f[1]); err != nil {
				return RequestBody{}, err
			}
		}
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(obj); err != nil {
			return RequestBody{}, ErrBodyAssembly
		}
		return RequestBody{value: bytes.TrimSuffix(out.Bytes(), []byte("\n")), kind: BodyJSON}, nil
	case BodyForm:
		values, err := url.ParseQuery(spec.BodyTemplate)
		if err != nil {
			return RequestBody{}, ErrBodyAssembly
		}
		for _, f := range fields {
			if strings.Contains(f[0], ".") {
				return RequestBody{}, ErrBodyAssembly
			}
			values.Set(f[0], f[1])
		}
		return RequestBody{value: []byte(values.Encode()), kind: BodyForm}, nil
	}
	return RequestBody{}, ErrBodyAssembly
}

// setJSONPath는 obj의 점 경로에 value를 넣는다. 없는 중간 칸은 객체로 만들고, 객체가 아닌 값을 지나면 오류다. 이미
// 있는 끝 칸은 덮어쓰지 않는다(틀에 표식·ID 칸이 있으면 모호하다).
func setJSONPath(obj map[string]any, path []string, value string) error {
	for i, seg := range path {
		if i == len(path)-1 {
			if _, exists := obj[seg]; exists {
				return ErrBodyAssembly
			}
			obj[seg] = value
			return nil
		}
		next, exists := obj[seg]
		if !exists {
			child := map[string]any{}
			obj[seg] = child
			obj = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return ErrBodyAssembly
		}
		obj = child
	}
	return ErrBodyAssembly
}
