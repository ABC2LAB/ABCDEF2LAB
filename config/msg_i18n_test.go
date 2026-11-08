package config

import "testing"

// 표시 언어는 한국어 하나다. 환경 변수와 설정 파일에 다른 언어가 있어도 따르지 않는다.
func TestLanguageIsFixedToKorean(t *testing.T) {
	for _, value := range []string{"", "ko", "en", "zh", "ja", "es", "bad"} {
		t.Setenv("ARTEX_LANG", value)
		if got := Language(); got != "ko" {
			t.Errorf("Language() with ARTEX_LANG=%q = %q, want ko", value, got)
		}
	}
	for _, code := range []string{"en", "zh", "es", "ja", "bad", ""} {
		if got := NormalizeLanguage(code); got != "" {
			t.Errorf("NormalizeLanguage(%q) = %q, want empty", code, got)
		}
	}
	if got := NormalizeLanguage(" KO "); got != "ko" {
		t.Errorf("NormalizeLanguage(KO) = %q", got)
	}
}

func TestMessagesPassThrough(t *testing.T) {
	const message = "[config] 설정 파일: %s"
	if T(message) != message || trCfg(message) != message {
		t.Fatal("message changed")
	}
}
