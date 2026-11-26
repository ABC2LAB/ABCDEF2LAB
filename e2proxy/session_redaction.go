package e2proxy

import (
	"bytes"
	"encoding/json"
)

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
