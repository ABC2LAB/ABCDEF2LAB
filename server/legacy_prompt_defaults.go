package server

import (
	"crypto/sha256"
	"fmt"
)

// e53a79f 및 언어 변경 직전 base 6551c59의 기본값을 정확히 대조한다. 원문은 해시로만 보관한다.
var legacyPromptHashes = map[string]string{
	"auto":      "b2a71b82243607337b19f0642317fed88c144d536412d80eaaf90807f2905a1d",
	"goals":     "72a7a0159443b41dde29dfba8a3de6a2c8a15b273935495a7b88561fa708aa68",
	"mainagent": "12bb69ee0886c4fb9beb936d882021a91104860f1da69a5e3eda3d9e345cd399",
	"pentest":   "a54bd5713b1c121d0f2ff3a72e0304820b1368c4b4b8159d5d651895663ac652",
	"planner":   "14e163f4a44da9fee20a214048a607adc3659b2025275569eb512532ca8c15d5",
	"reporter":  "8ba98208f6c5f2ccb103c88ef13239c4264733400c0e3dfec96d3a3572d78d39",
	"worker":    "80fbb1c610dd57e266c20b9145793339eee540b809f6db8f5336261b45879f08",
}
var legacyReporterTriggerHashes = []string{"2b766affdba89c92772698367e41fc6d570ee25f29964576c0aec96da4d176e6", "c10b22c2daf51a0e4998fe0e84a2e22baf70927a6f7368874e1e1e0b0eacb02b"}

func legacyTextHash(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
func isLegacyPromptDefault(key, value string) bool {
	want, ok := legacyPromptHashes[key]
	return ok && legacyTextHash(value) == want
}
func isLegacyReporterTrigger(value string) bool {
	got := legacyTextHash(value)
	for _, want := range legacyReporterTriggerHashes {
		if got == want {
			return true
		}
	}
	return false
}
