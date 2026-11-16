package e2proxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// sessionRedactionValues는 호스트가 세션으로 추출한 값에만 사용한다. JWS의 JSON 객체 머리·claims와 정규 base64url
// 세 조각을 확인한 경우 payload·signature도 출력 가림에 넣는다. 서명 검증이나 계정 인증 판정에는 쓰지 않는다.
// JWT 머리는 공통 알고리즘 정보라 가리지 않고, 짧은 조각은 기존 가림 하한 때문에 더하지 않는다.
func sessionRedactionValues(values [][]byte) [][]byte {
	out := make([][]byte, 0, len(values))
	seen := map[string]bool{}
	add := func(v []byte) {
		if !seen[string(v)] {
			seen[string(v)] = true
			out = append(out, v)
		}
	}
	for _, value := range values {
		add(value)
		if len(value) > maxRedactValueBytes || bytes.Count(value, []byte(".")) != 2 {
			continue
		}
		parts := bytes.Split(value, []byte("."))
		valid := true
		algorithm := ""
		for i, part := range parts {
			if i == 2 && strings.EqualFold(algorithm, "none") {
				valid = len(part) == 0
				break
			}
			decoded, err := base64.RawURLEncoding.Strict().DecodeString(string(part))
			if err != nil || len(decoded) == 0 {
				valid = false
				clear(decoded)
				break
			}
			if i < 2 {
				object := bytes.TrimSpace(decoded)
				if !utf8.Valid(object) || len(object) < 2 || object[0] != '{' || !json.Valid(object) {
					valid = false
				}
				if i == 0 {
					var header struct {
						Algorithm string `json:"alg"`
					}
					if json.Unmarshal(object, &header) != nil || header.Algorithm == "" || strings.TrimSpace(header.Algorithm) != header.Algorithm {
						valid = false
					}
					algorithm = header.Algorithm
				}
			}
			clear(decoded)
			if !valid {
				break
			}
		}
		if !valid {
			continue
		}
		for _, part := range parts[1:] {
			if len(part) >= minRedactValueBytes && len(part) <= maxRedactValueBytes {
				add(part)
			}
		}
	}
	return out
}

// freshSessionRedactions는 기존 값과 배치 안의 중복을 빼고, 전부 등록할 수 있을 때만 사본을 돌려준다.
func freshSessionRedactions(existing, values [][]byte, limit int) ([][]byte, bool) {
	values = sessionRedactionValues(values)
	seen := make(map[string]bool, len(existing)+len(values))
	for _, value := range existing {
		seen[string(value)] = true
	}
	var fresh [][]byte
	for _, value := range values {
		if !seen[string(value)] {
			seen[string(value)] = true
			fresh = append(fresh, value)
		}
	}
	if len(existing)+len(fresh) > limit {
		return nil, false
	}
	for i := range fresh {
		fresh[i] = bytes.Clone(fresh[i])
	}
	return fresh, true
}

// redactEscapedJSONValues는 유효한 JSON의 문자열 값만 한 번 해석한다. 알려진 값이 가려지는 문자열만 다시 쓰므로
// 숫자·구조·다른 필드의 공백은 보존한다. 임의 인코딩이나 중첩 JSON을 해석하는 범용 DLP는 아니다.
func redactEscapedJSONValues(input []byte, values [][]byte) []byte {
	if !bytes.ContainsRune(input, '\\') || !json.Valid(input) {
		return input
	}
	var out bytes.Buffer
	copied := 0
	for i := 0; i < len(input); i++ {
		if input[i] != '"' {
			continue
		}
		start := i
		for i++; i < len(input); i++ {
			if input[i] == '\\' {
				i++
				continue
			}
			if input[i] == '"' {
				break
			}
		}
		literal := input[start : i+1]
		if !bytes.ContainsRune(literal, '\\') {
			continue
		}
		var decoded string
		if json.Unmarshal(literal, &decoded) != nil {
			continue
		} // json.Valid로 확인한 문자열이다.
		masked := []byte(decoded)
		for _, value := range values {
			if len(value) > 0 {
				masked = bytes.ReplaceAll(masked, value, []byte("[REDACTED]"))
			}
		}
		if string(masked) == decoded {
			continue
		}
		// 문자열의 JSON 인코딩은 실패하지 않는다. 실패하더라도 원문을 돌려주지 않고 이 값만 보류한다.
		encoded, err := json.Marshal(string(masked))
		if err != nil {
			encoded = []byte(`"[REDACTED]"`)
		}
		out.Write(input[copied:start])
		out.Write(encoded)
		copied = i + 1
	}
	if copied == 0 {
		return input
	}
	out.Write(input[copied:])
	return out.Bytes()
}
