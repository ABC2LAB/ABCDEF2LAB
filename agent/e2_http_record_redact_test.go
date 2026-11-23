package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
	actool "github.com/Autumn-27/norma/tool"
)

// recordSplitScope는 model_visible_v1처럼 모델 출력은 비밀번호를 두고 저장 기록만 가리는 합성 범위다.
type recordSplitScope struct{ value []byte }

func (recordSplitScope) SnapshotForIntent(int64) e2proxy.ExecutionView {
	return e2proxy.ExecutionView{}
}
func (recordSplitScope) HasIntent(int64) bool              { return true }
func (s recordSplitScope) RedactOutput(data []byte) []byte { return bytes.Clone(data) }
func (s recordSplitScope) RedactRecord(data []byte) []byte {
	return bytes.ReplaceAll(data, s.value, []byte("[REDACTED]"))
}

// 그래프 도구 입력은 탐색 그래프에 저장되므로 저장 기록 가림을 거친다. 모델이 받은 비밀번호를 사실 기록에 넣어도 남지 않는다.
func TestE2HTTPGraphInputUsesRecordRedaction(t *testing.T) {
	scope := recordSplitScope{value: []byte("visible-pass-77")}
	var stored string
	base := e2WorkerFixtureTool("record_fact", func(_ context.Context, input json.RawMessage) (actool.Result, error) {
		stored = string(input)
		return actool.Text("fact recorded: 1"), nil
	})
	tool := e2HTTPGraphWrap(scope, 22)(base)
	result, err := tool.Call(context.Background(), json.RawMessage(`{"intent_id":22,"summary":"login as alice with visible-pass-77"}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("기록 실패: %v %s", err, result.Flatten())
	}
	if strings.Contains(stored, "visible-pass-77") || !strings.Contains(stored, "[REDACTED]") {
		t.Fatalf("그래프에 비밀번호가 저장됨: %q", stored)
	}
}
