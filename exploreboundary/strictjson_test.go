package exploreboundary

import "testing"

// 중첩 객체(결속·대상)의 키도 태그와 정확히 같아야 하고, 어느 층에서든 중복 키·뒤에 붙은 값은 거부한다.
func TestStrictKeysNested(t *testing.T) {
	good := `{"op":"status","session_ref":"s","binding":{"session_ref":"s","limits":{"max_requests":1}}}`
	if err := strictKeys([]byte(good), &ControlRequest{}); err != nil {
		t.Fatalf("exact nested keys refused: %v", err)
	}
	for name, raw := range map[string]string{
		"nested-case":      `{"op":"status","binding":{"Session_Ref":"s"}}`,
		"nested-duplicate": `{"op":"status","binding":{"session_ref":"a","session_ref":"b"}}`,
		"deep-case":        `{"op":"status","binding":{"limits":{"MAX_REQUESTS":1}}}`,
		"unknown-nested":   `{"op":"status","binding":{"extra":1}}`,
		"trailing-value":   `{"op":"status"} {"op":"close"}`,
		"top-duplicate":    `{"op":"status","op":"close"}`,
	} {
		if strictKeys([]byte(raw), &ControlRequest{}) == nil {
			t.Fatalf("%s accepted: %s", name, raw)
		}
	}
}
