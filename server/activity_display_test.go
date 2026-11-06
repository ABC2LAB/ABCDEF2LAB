package server

import (
	"encoding/json"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// API·SSE 경계에서는 화면용 글로 바뀌고 metadata 는 내려가지 않아야 한다. 다른 종류의 metadata 는 그대로다.
func TestActivityDTOUsesDisplayText(t *testing.T) {
	dto := activityDTO(db.Activity{Kind: "result", Summary: "model summary", Detail: "model detail", Metadata: db.DisplayMetadata("화면 요약", "화면 상세")})
	if dto.Summary != "화면 요약" || dto.Detail != "화면 상세" || dto.Metadata != nil {
		t.Fatalf("result row: %+v", dto)
	}
	metadata := json.RawMessage(`{"llm_transition":{"from":"a"}}`)
	dto = activityDTO(db.Activity{Kind: "llm_switch", Summary: "요약", Metadata: metadata})
	if dto.Summary != "요약" || string(dto.Metadata) != string(metadata) {
		t.Fatalf("other row changed: %+v", dto)
	}
}
