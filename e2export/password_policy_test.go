package e2export

import (
	"github.com/Autumn-27/artex/e2proxy"
	"testing"
)

func TestPasswordResponsePolicyFingerprintSeparatesLegacy(t *testing.T) {
	cfg, behavior := policyFixture()
	cfg.HeaderMode = e2proxy.HeaderTestAccountPassthrough
	auth := cfg.Rules[0]
	auth.Ref = "auth-login"
	auth.PathPrefix = "/login"
	auth.Methods = []string{"POST"}
	auth.Headers = nil
	auth.Purpose = e2proxy.PurposeAuthPrep
	cfg.Rules = append(cfg.Rules, auth)
	legacy := policyDigest(t, cfg, behavior)
	cfg.PasswordResponse = e2proxy.PasswordResponseMaskSpans
	if got := policyDigest(t, cfg, behavior); got == legacy {
		t.Fatal("가림 의미가 다른 정책의 지문이 같음")
	}
	cfg.PasswordResponse = "unsupported"
	if _, err := PolicyFingerprint(cfg, 20, behavior); err == nil {
		t.Fatal("모르는 가림 정책 수용")
	}
	cfg.PasswordResponse = e2proxy.PasswordResponseMaskSpans
	cfg.HeaderMode = e2proxy.HeaderPreserve
	if _, err := PolicyFingerprint(cfg, 20, behavior); err == nil {
		t.Fatal("실계정 모드에서 시험 계정 가림 정책 수용")
	}
}
