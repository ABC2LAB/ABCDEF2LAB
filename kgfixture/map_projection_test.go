package kgfixture

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestMapResourcesReferencesOnlyAndDetached(t *testing.T) {
	s := mustParse(t, encode(t, validRegistry()))
	resources, err := s.MapResources()
	if err != nil || len(resources) != 2 || resources[0].OwnerRef != "account-a" || len(resources[0].Principals) != 1 || resources[0].Principals[0] != "account-b" || len(resources[1].Principals) != 0 {
		t.Fatal("소유·정책 등록 projection 불일치")
	}
	raw, err := json.Marshal(resources)
	if err != nil || bytes.Contains(raw, []byte(sentinel)) || bytes.Contains(raw, []byte("canaries")) || bytes.Contains(raw, []byte("expected_access")) {
		t.Fatal("지도 projection에 비공개 입력 포함")
	}
	resources[0].OwnerRef, resources[0].Principals[0] = "changed", "changed"
	again, err := s.MapResources()
	if err != nil || again[0].OwnerRef != "account-a" || again[0].Principals[0] != "account-b" {
		t.Fatal("호출자가 동결 snapshot을 변경함")
	}
	s.Invalidate()
	if _, err := s.MapResources(); err == nil {
		t.Fatal("무효 snapshot을 projection으로 복구함")
	}
}
