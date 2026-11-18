package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/Autumn-27/artex/dlp"
)

// redact-text 역할: 러너가 근거에 남길 짧은 원문 줄을 제품과 같은 자격 가림(dlp.CredentialText)에 통과시킨다.
// 표준 입력의 JSON 문자열 배열을 같은 길이의 가린 배열로 표준 출력에 쓴다. 망·파일을 쓰지 않는다.
// 러너(Python)에 같은 규칙을 따로 두지 않으려고 계측기 이미지의 Go 함수를 그대로 쓴다(B 전용 CLI 관측 줄, 2026-11-19 결정).
const (
	roleRedactText     = "redact-text"
	maxRedactInput     = 1 << 20
	maxRedactLines     = 256
	maxRedactLineBytes = 16 << 10
)

func runRedactText(stdin io.Reader, stdout, stderr io.Writer) int {
	raw, err := io.ReadAll(io.LimitReader(stdin, maxRedactInput+1))
	if err != nil || len(raw) > maxRedactInput {
		return fail(stderr, invalidInput)
	}
	var lines []string
	if decodeOneJSON(raw, &lines) != nil || lines == nil || len(lines) > maxRedactLines {
		return fail(stderr, invalidInput)
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		if len(line) > maxRedactLineBytes {
			return fail(stderr, invalidInput)
		}
		out[i] = dlp.CredentialText(line)
	}
	if err := json.NewEncoder(stdout).Encode(out); err != nil {
		return fail(stderr, storageUnavailable)
	}
	return 0
}

// decodeOneJSON은 값 하나만 받는다(뒤에 다른 값이 붙으면 거부).
func decodeOneJSON(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}
