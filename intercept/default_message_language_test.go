package intercept

import (
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// 운영자 원문과 달리 프로그램이 채운 기본 메시지는 모델·표시 경계를 나눈다.
func TestRuleDefaultMessageSeparatesModelAndDisplay(t *testing.T) {
	for _, action := range []string{"deny", "ask", "allow"} {
		for _, message := range []string{"", "운영자가 직접 쓴 메시지"} {
			t.Run(action+"/"+message, func(t *testing.T) {
				ic := &Interceptor{cached: []compiledRule{{InterceptRule: db.InterceptRule{
					ID: 37, Name: "운영자 규칙", MatchTarget: "tool_name", MatchType: "string",
					Pattern: "fixture", Action: action, Message: message,
				}}}}
				dec, matched := ic.Match("fixture", []byte(`{}`))
				if !matched || dec.Action != action || dec.RuleID != 37 || dec.ConfigDigest == "" {
					t.Fatalf("rule identity or action changed: %+v matched=%v", dec, matched)
				}
				if message != "" {
					if dec.Message != message || dec.ModelMessage != "" || dec.ReasonCode != "" {
						t.Fatalf("operator message was changed: %+v", dec)
					}
					return
				}
				if dec.Message != defaultMessage(action, "운영자 규칙") {
					t.Fatalf("display message changed: %q", dec.Message)
				}
				if action == "allow" {
					if dec.ModelMessage != "" || dec.ReasonCode != "" {
						t.Fatalf("allow gained a block message: %+v", dec)
					}
					return
				}
				if !strings.Contains(dec.ModelMessage, "reason_code=rule_"+action) || dec.ReasonCode != "rule_"+action {
					t.Fatalf("model reason code missing: %+v", dec)
				}
				for _, r := range dec.ModelMessage {
					if r > 127 {
						t.Fatalf("default model message is not English: %q", dec.ModelMessage)
					}
				}
			})
		}
	}
}

func TestConfigurationFailureMatchSeparatesModelAndDisplay(t *testing.T) {
	dec, matched := New(nil).Match("fixture", []byte(`{}`))
	if !matched || dec.Action != "deny" || dec.Message != ErrConfigurationUnavailable.Error() ||
		!strings.Contains(dec.ModelMessage, "reason_code=configuration_unavailable") ||
		strings.Contains(dec.ModelMessage, dec.Message) || dec.ReasonCode != "configuration_unavailable" ||
		dec.RuleID != 0 || dec.ConfigDigest != "" {
		t.Fatalf("configuration failure language or denial changed: %+v matched=%v", dec, matched)
	}
}
