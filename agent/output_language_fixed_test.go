package agent

import (
	"context"
	"reflect"
	"testing"
)

// 서버를 거치지 않은 옛 훅도 모델 지시문의 표시 언어를 바꿀 수 없다.
func TestOutputLanguageFixedWithoutInvokingLegacyHook(t *testing.T) {
	if !reflect.DeepEqual(SupportedOutputLangs, []string{"ko"}) {
		t.Fatalf("supported output languages = %v", SupportedOutputLangs)
	}
	for _, code := range []string{"ko", "en", "zh", "es", "ja", "fr", ""} {
		t.Run(code, func(t *testing.T) {
			called := false
			ctx := WithRuntimeHooks(context.Background(), RuntimeHooks{OutputLanguage: func() string {
				called = true
				return code
			}})
			if got := resolveOutputLang(ctx); got != "ko" || called {
				t.Fatalf("language=%q legacy hook invoked=%v", got, called)
			}
			for _, prompt := range []string{"", " \n", "synthetic wrapup"} {
				if got := withWrapupLang(ctx, prompt); got != prompt || called {
					t.Fatalf("wrapup changed or invoked legacy hook: %q, called=%v", got, called)
				}
			}
		})
	}
}
