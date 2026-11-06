package guard

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

// 실제 파서와 서버가 쓰는 판정 변환을 거쳐 모델 결과와 감사 기록의 소비 경계를 확인한다.
func TestJudgeVerdictToGuardSeparatesDisplayReason(t *testing.T) {
	const reason = "실제 동작: 파일 삭제; 성공 후 결과: 업무 데이터 손실; 적중 규칙: D4"
	for _, action := range []string{"deny", "ask"} {
		t.Run(action, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"decision": action, "comment": reason})
			dec := intercept.JudgeDecision(intercept.ParseVerdict(string(raw)), 42)
			g := New()
			got := g.blockDecision("Bash", dec)
			if got.Decision != "block" || !strings.Contains(got.Message, "reason_code=judge_"+action) || strings.Contains(got.Message, reason) {
				t.Fatalf("model result leaked display reason or lost decision: %+v", got)
			}
			if dec.ProfileID != 42 || dec.Message != reason || g.Audit()[0].Reason != reason {
				t.Fatal("display reason or profile was changed")
			}
		})
	}
	const custom = "운영자가 작성한 규칙 설명"
	if got := New().blockDecision("Bash", intercept.Decision{Message: custom}); !strings.Contains(got.Message, custom) {
		t.Fatal("custom rule message fallback was lost")
	}
}

// 전용 시험 DB가 있을 때는 Judge와 Guard의 실제 훅·감사 저장 경로까지 연결한다.
func TestJudgeToGuardHookKeepsKoreanInAuditOnly(t *testing.T) {
	dsn := os.Getenv("ARTEX_INTERCEPT_TEST_DSN")
	if dsn == "" {
		t.Skip("ARTEX_INTERCEPT_TEST_DSN 전용 PostgreSQL 필요")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for key, value := range map[string]string{"llm_judge_enabled": "true", "intercept_enabled_tools": `["language_fixture"]`} {
		previous, exists, err := d.GetSetting(key)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if exists {
				_ = d.SetSetting(key, previous)
			} else {
				_, _ = d.Exec(`DELETE FROM settings WHERE key=$1`, key)
			}
		})
		if err := d.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	ic := intercept.New(d)
	const reason = "실제 동작: 파일 삭제； 성공 후 결과: 데이터 손실； 적중 규칙: D4"
	ic.SetReviewer(func(context.Context, int64, string, intercept.ReviewInput) (intercept.Decision, error) {
		raw, _ := json.Marshal(map[string]string{"decision": "deny", "comment": reason})
		return intercept.JudgeDecision(intercept.ParseVerdict(string(raw)), 0), nil
	})
	g := NewWithInterceptor(ic)
	blocked, message, _ := g.Hooks().PreToolUse(context.Background(), "language_fixture", json.RawMessage(`{}`))
	if !blocked || !strings.Contains(message, "reason_code=judge_deny") || strings.Contains(message, reason) {
		t.Fatalf("judge to hook result = %v %q", blocked, message)
	}
	if audit := g.Audit(); len(audit) != 1 || !strings.Contains(audit[0].Reason, reason) {
		t.Fatal("Korean audit reason was lost")
	}
}
