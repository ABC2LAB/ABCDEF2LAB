package e2export

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestPublisherInputDoesNotSerializeKeyOrPrivatePaths(t *testing.T) {
	private := "synthetic-private-canary"
	cfg := Config{AuditKey: []byte(private), JournalDirectory: private, OutputDirectory: private, EngineTaskID: private}
	data, err := json.Marshal(cfg)
	if err != nil || string(data) != "{}" {
		t.Fatal("게시 공급 입력이 JSON으로 노출됨")
	}
	for _, value := range []string{fmt.Sprint(cfg), fmt.Sprintf("%#v", cfg)} {
		if strings.Contains(value, private) || !strings.Contains(value, "protected") {
			t.Fatal("게시 공급 입력이 진단 문자열로 노출됨")
		}
	}
}
