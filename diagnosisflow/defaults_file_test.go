package diagnosisflow

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 제품 워크플로 기본값 파일(deploy/product/workflow-defaults.json)은 설치 입력 target_policy.workflow의 정본이다(2026-11-17
// 사용자 결정). 제품 검사(Limits.Valid)를 통과해야 하고, 모르는 칸이 없어야 하며, 고객 기본값은 쓰기를 끈다. README 예시·평가
// 러너가 이 파일을 그대로 쓴다(scripts/ci/test_product_workflow_defaults.py).
func TestProductWorkflowDefaultsFileIsValid(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "deploy", "product", "workflow-defaults.json"))
	if err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var l Limits
	if err := d.Decode(&l); err != nil || d.More() {
		t.Fatalf("defaults file does not decode strictly: %v", err)
	}
	if !l.Valid() || l.AllowReversibleWrites {
		t.Fatalf("defaults %+v valid=%v", l, l.Valid())
	}
	if l.MaxPages != MaxWorkflowPages || l.MaxEndpoints != MaxWorkflowEndpoints {
		t.Fatalf("defaults %+v are not the code caps the decision fixed", l)
	}
}
