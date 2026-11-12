package diagnosisflow

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"testing"
	"time"
)

// Go CI에서도 페이지 어댑터 시험을 실행한다. 브라우저·새 의존성·외부 통신은 필요 없다.
func TestAPIReconMetadataOnlyNodeAdapter(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("metadata-only adapter requires Node in CI")
		}
		t.Skip("local Node runtime unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, node, "--test", "../skills/api-recon/scripts/metadata_only.test.js").CombinedOutput()
	// 원문 assertion의 대상 값은 모델·공유 로그에 복사하지 않고 TAP 건수만 남긴다.
	counts := regexp.MustCompile(`(?m)^# (?:tests|pass|fail|skipped) [0-9]+$`).FindAll(output, -1)
	for _, count := range counts {
		t.Log(string(count))
	}
	if err != nil {
		t.Fatal("metadata-only Node adapter tests failed")
	}
}
