package e2proxy

import (
	"bytes"
	"encoding/base64"
	"html"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 프로그램 표식은 공통 계약의 32자 소문자 영숫자 값만 받는다. 값은 실행 안에서만 보관한다.
const markerBytes = 32
const maxMarkerOutput = 1 << 20

type markerSource struct {
	begin, end int
	kind       byte
}

type markerSpan struct {
	begin, end int
	method     string
}

const (
	markerJSON byte = iota + 1
	markerURL
	markerHTMLNumeric
	markerHTMLNamed
)

// RegisterProgramMarker는 E③ 확정 단계가 만든 보호 표식을 이 실행의 출력 가림에 결속한다.
// 같은 값의 재등록만 허용한다. 값은 오류·저널·로그로 내보내지 않는다.
func (e *Execution) RegisterProgramMarker(marker []byte) error {
	if e == nil || len(marker) != markerBytes {
		return ErrConfig
	}
	for _, b := range marker {
		if (b < 'a' || b > 'z') && (b < '0' || b > '9') {
			return ErrConfig
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped || len(e.marker) != 0 && !bytes.Equal(e.marker, marker) {
		return ErrConfig
	}
	if len(e.marker) == 0 {
		e.marker = bytes.Clone(marker)
	}
	return nil
}

// decodeMarkerView는 원문을 정확히 한 번 훑어 세 인코딩을 함께 푼다.
// 각 출력 바이트의 원문 범위를 보존해 섞인 인코딩도 정확한 자리에서 가린다.
func decodeMarkerView(input []byte) ([]byte, []markerSource, bool) {
	if len(input) > maxMarkerOutput || !utf8.Valid(input) {
		return nil, nil, false
	}
	out := make([]byte, 0, len(input))
	sources := make([]markerSource, 0, len(input))
	appendValue := func(start, end int, kind byte, value []byte) {
		out = append(out, value...)
		for range value {
			sources = append(sources, markerSource{start, end, kind})
		}
	}
	for i := 0; i < len(input); {
		start := i
		if input[i] == '\\' && i+1 < len(input) {
			if input[i+1] == 'u' && i+6 <= len(input) {
				value, err := strconv.ParseUint(string(input[i+2:i+6]), 16, 16)
				if err == nil && value < utf8.RuneSelf {
					appendValue(i, i+6, markerJSON, []byte{byte(value)})
					i += 6
					continue
				}
			} else if input[i+1] == '/' || input[i+1] == '\\' || input[i+1] == '"' {
				appendValue(i, i+2, markerJSON, input[i+1:i+2])
				i += 2
				continue
			}
		} else if input[i] == '%' && i+2 < len(input) && passwordHex(input[i+1]) && passwordHex(input[i+2]) {
			value, _ := strconv.ParseUint(string(input[i+1:i+3]), 16, 8)
			appendValue(i, i+3, markerURL, []byte{byte(value)})
			i += 3
			continue
		} else if input[i] == '&' {
			end := bytes.IndexByte(input[i:min(len(input), i+35)], ';')
			if end > 1 && end <= 34 {
				end += i + 1
				encoded := string(input[i:end])
				decoded := html.UnescapeString(encoded)
				if decoded != encoded {
					kind := markerHTMLNamed
					if strings.HasPrefix(encoded, "&#") {
						kind = markerHTMLNumeric
					}
					appendValue(i, end, kind, []byte(decoded))
					i = end
					continue
				}
			}
		}
		appendValue(start, start+1, 0, input[i:i+1])
		i++
	}
	if !utf8.Valid(out) {
		return nil, nil, false
	}
	return out, sources, true
}

func markerSpansInView(decoded []byte, sources []markerSource, marker []byte) []markerSpan {
	spans := []markerSpan{}
	// 표식은 ASCII만 사용한다. Unicode fold는 바이트 길이를 바꿀 수 있어 원문 위치표를 깨뜨린다.
	lower := bytes.Clone(decoded)
	for i, b := range lower {
		if b >= 'A' && b <= 'Z' {
			lower[i] = b + ('a' - 'A')
		}
	}
	for offset := 0; offset+len(marker) <= len(decoded); {
		index := bytes.Index(lower[offset:], marker)
		if index < 0 {
			break
		}
		start := offset + index
		end := start + len(marker)
		method := "exact"
		if !bytes.Equal(decoded[start:end], marker) {
			method = "case_insensitive"
		}
		for _, source := range sources[start:end] {
			switch source.kind {
			case markerJSON:
				method = "decoded_json"
			case markerURL:
				method = "decoded_url"
			case markerHTMLNumeric:
				method = "decoded_html_numeric"
			case markerHTMLNamed:
				method = "decoded_html_named"
			}
			if source.kind != 0 {
				break
			}
		}
		spans = append(spans, markerSpan{sources[start].begin, sources[end-1].end, method})
		offset = end
	}
	return spans
}

// unsupportedMarkerForm은 표식의 지원 밖 표현을 아는 범위에서 감지한다.
// 이중 인코딩과 base64가 보이면 일치 없음으로 판정하지 않는다.
func unsupportedMarkerForm(input, once, marker []byte) bool {
	for _, codec := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if bytes.Contains(input, []byte(codec.EncodeToString(marker))) {
			return true
		}
	}
	if bytes.Equal(once, input) {
		return false
	}
	twice, _, ok := decodeMarkerView(once)
	if !ok || bytes.Equal(once, twice) {
		return !ok
	}
	for i, b := range twice {
		if b >= 'A' && b <= 'Z' {
			twice[i] = b + ('a' - 'A')
		}
	}
	return bytes.Contains(twice, marker)
}

// inspectProgramMarker는 구조화 응답 검사에서 사용할 동일 정규화 경계다.
// 호출자는 응답 완전성·content-type·압축 여부를 먼저 확인해야 한다.
func inspectProgramMarker(input, marker []byte) (bool, string, bool) {
	decoded, sources, ok := supportedMarkerView(input, marker)
	if !ok {
		return false, "", false
	}
	spans := markerSpansInView(decoded, sources, marker)
	if len(spans) == 0 {
		return false, "", true
	}
	return true, spans[0].method, true
}

// containsEmbeddedBase64Marker checks every possible byte alignment.
// Full groups of three marker bytes encode independently of adjacent bytes and padding.
// For the marker's alphanumeric alphabet, standard and URL-safe groups are identical.
func containsEmbeddedBase64Marker(input, decoded, marker []byte) bool {
	hasDecodedView := !bytes.Equal(input, decoded)
	for start := 0; start < 3; start++ {
		end := start + (len(marker)-start)/3*3
		interior := marker[start:end]
		signature := []byte(base64.RawStdEncoding.EncodeToString(interior))
		if bytes.Contains(input, signature) || hasDecodedView && bytes.Contains(decoded, signature) {
			return true
		}
	}
	return false
}

// supportedMarkerView establishes a shared rejection boundary before matching.
func supportedMarkerView(input, marker []byte) ([]byte, []markerSource, bool) {
	if len(marker) != markerBytes {
		return nil, nil, false
	}
	decoded, sources, ok := decodeMarkerView(input)
	if !ok || bytes.Contains(decoded, []byte("&#")) ||
		containsEmbeddedBase64Marker(input, decoded, marker) || unsupportedMarkerForm(input, decoded, marker) {
		return nil, nil, false
	}
	return decoded, sources, true
}

func redactMarker(input, marker []byte) []byte {
	decoded, sources, ok := supportedMarkerView(input, marker)
	if !ok {
		return []byte("[REDACTED]")
	}
	spans := markerSpansInView(decoded, sources, marker)
	if len(spans) == 0 {
		return input
	}
	out := make([]byte, 0, len(input))
	last := 0
	for _, span := range spans {
		if span.begin < last {
			last = max(last, span.end)
			continue
		}
		out = append(out, input[last:span.begin]...)
		out = append(out, "[REDACTED]"...)
		last = span.end
	}
	return append(out, input[last:]...)
}
