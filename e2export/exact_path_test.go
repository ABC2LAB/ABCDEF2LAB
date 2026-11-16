package e2export

import (
	"bytes"
	"testing"
)

func TestExactPathFingerprintDistinctFromLegacyPrefix(t *testing.T) {
	cfg, b := policyFixture()
	cfg.Rules = cfg.Rules[:1]
	cfg.Rules[0].PathPrefix = "/"
	prefix, err := canonicalPolicy(cfg, 100, b)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Rules[0].PathMatch = "exact"
	exact, err := canonicalPolicy(cfg, 100, b)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(prefix, exact) || !bytes.Contains(exact, []byte(`"path_match":"exact_path_no_query_v1"`)) || !bytes.Contains(prefix, []byte(`"path_match":"clean_segment_prefix_v1"`)) {
		t.Fatal("접두·정확 지문이 구분되지 않음")
	}
	cfg.Rules[0].PathMatch = "unknown"
	if _, err := canonicalPolicy(cfg, 100, b); err == nil {
		t.Fatal("알 수 없는 모드 지문 생성")
	}
}
