package e3contract

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/url"
	"strings"
)

// MarkerLength는 표식 길이다. E②가 보호 값으로 받는 하한(6바이트)과 출력 가림 하한(8바이트)보다 길게 두어
// 흔한 문자열과 겹치지 않게 한다.
const MarkerLength = 32

// markerAlphabet은 소문자 영숫자다(#596 §6). 대소문자 무시 대조와 인코딩 풀기 뒤 대조가 한 가지 모양으로 끝난다.
const markerAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// redactedMarker는 표식을 사람이 보는 출력에 찍을 때 대신 쓰는 글이다.
const redactedMarker = "[REDACTED]"

// ErrMarkerSerialization은 표식 값을 직렬화하려 할 때의 오류다. 표식 값은 저널·로그·근거·보고서·화면·모델 입력
// 어디에도 남기지 않는다(CLAUDE.md 절대 규칙 2).
var ErrMarkerSerialization = errors.New("e3contract: 표식 값은 직렬화하지 않는다")

// ErrMarkerInvalid는 표식을 만들 수 없거나 모양이 계약과 다를 때의 오류다.
var ErrMarkerInvalid = errors.New("e3contract: 표식이 계약 모양이 아니다")

// Marker는 확정 단계 한 회의 표식이다. 값은 비공개 칸에만 있고, 문자열·JSON·텍스트·slog로 내보내면 가린 글이나
// 오류가 된다. 이 타입은 값 자체의 직렬화만 막는다. 요청 본문에 들어간 표식은 일반 바이트이므로, 확정 단계를 시작할 때
// Transport.RegisterMarker로 E² 실행의 보호 값에 올려 저널·영수증·승인 미리보기·로그·내보내기에서 가리게 한다.
type Marker struct {
	value []byte
}

// NewMarker는 r에서 읽은 난수로 표식을 만든다. 거부 표집으로 글자마다 고르게 뽑는다.
func NewMarker(r io.Reader) (Marker, error) {
	if r == nil {
		return Marker{}, ErrMarkerInvalid
	}
	// 256을 36으로 나눈 나머지 쏠림을 없애려고 252(=36×7) 이상은 버린다.
	const accept = byte(len(markerAlphabet) * (256 / len(markerAlphabet)))
	out := make([]byte, 0, MarkerLength)
	buf := make([]byte, MarkerLength)
	for len(out) < MarkerLength {
		if _, err := io.ReadFull(r, buf); err != nil {
			return Marker{}, fmt.Errorf("%w: 난수를 읽지 못함", ErrMarkerInvalid)
		}
		for _, b := range buf {
			if b >= accept {
				continue
			}
			out = append(out, markerAlphabet[int(b)%len(markerAlphabet)])
			if len(out) == MarkerLength {
				break
			}
		}
	}
	return Marker{value: out}, nil
}

// Valid는 표식이 길이·글자 모양을 지키는지다.
func (m Marker) Valid() bool {
	if len(m.value) != MarkerLength {
		return false
	}
	for _, b := range m.value {
		if strings.IndexByte(markerAlphabet, b) < 0 {
			return false
		}
	}
	return true
}

// Reveal은 표식 값의 사본이다. E²가 요청 본문에 넣고, 응답을 검사하고, 보호 값으로 등록할 때만 쓴다.
// 돌려받은 바이트를 로그·기록·오류 문장에 넣지 않는다.
func (m Marker) Reveal() []byte { return bytes.Clone(m.value) }

func (Marker) String() string   { return redactedMarker }
func (Marker) GoString() string { return redactedMarker }

// Format은 %v·%+v·%#v·%s·%q·%x 어느 동사로 찍어도 가린 글만 쓴다.
func (Marker) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redactedMarker) }

// LogValue는 slog가 표식을 가린 글로 남기게 한다.
func (Marker) LogValue() slog.Value { return slog.StringValue(redactedMarker) }

func (Marker) MarshalJSON() ([]byte, error)   { return nil, ErrMarkerSerialization }
func (Marker) MarshalText() ([]byte, error)   { return nil, ErrMarkerSerialization }
func (Marker) MarshalBinary() ([]byte, error) { return nil, ErrMarkerSerialization }
func (Marker) GobEncode() ([]byte, error)     { return nil, ErrMarkerSerialization }
func (*Marker) UnmarshalJSON([]byte) error    { return ErrMarkerSerialization }
func (*Marker) UnmarshalText([]byte) error    { return ErrMarkerSerialization }

// MarkerForms는 표식이 기록·출력에 나타날 수 있는 모양들이다: 원문, 대문자, 그리고 #596 §6 정규화기가 푸는 인코딩
// (JSON \u 이스케이프, URL 퍼센트 인코딩, HTML 숫자 엔티티 10진·16진)을 글자 전체에 적용한 모양. 새는지 확인하는 시험이
// 이 모양들을 찾는다. 돌려받은 값은 시험 안에서만 쓰고 출력하지 않는다.
func MarkerForms(m Marker) [][]byte {
	if !m.Valid() {
		return nil
	}
	raw := string(m.value)
	var jsonEsc, pct, htmlDec, htmlHex strings.Builder
	for _, r := range raw {
		fmt.Fprintf(&jsonEsc, `\u%04x`, r)
		fmt.Fprintf(&pct, "%%%02X", r)
		fmt.Fprintf(&htmlDec, "&#%d;", r)
		fmt.Fprintf(&htmlHex, "&#x%x;", r)
	}
	forms := []string{raw, strings.ToUpper(raw), jsonEsc.String(), pct.String(), htmlDec.String(), htmlHex.String()}
	// 사전 점검: 각 인코딩을 풀면 원문으로 돌아와야 한다. 아니면 시험이 엉뚱한 모양을 찾는다.
	if u, err := url.PathUnescape(pct.String()); err != nil || u != raw || html.UnescapeString(htmlDec.String()) != raw ||
		html.UnescapeString(htmlHex.String()) != raw {
		return nil
	}
	out := make([][]byte, 0, len(forms))
	for _, f := range forms {
		out = append(out, []byte(f))
	}
	return out
}

// ContainsMarker는 data에 표식의 어느 모양(MarkerForms, 대소문자 무시)이든 들어 있는지다.
func ContainsMarker(data []byte, m Marker) bool {
	lower := bytes.ToLower(data)
	for _, f := range MarkerForms(m) {
		if bytes.Contains(lower, bytes.ToLower(f)) {
			return true
		}
	}
	return false
}
