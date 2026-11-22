package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// 프롬프트 관측 저장 실패는 단계 코드를 싣되 errors.Is(err, ErrE2Prompt)는 그대로 참이다(호출자의 판정은 바뀌지 않음).
// 문맥이 끝난 뒤의 실패는 코드 끝에 _timeout·_canceled가 붙는다. 로그는 이 코드만 쓴다(리허설 F9).
func TestE2PromptFailureReason(t *testing.T) {
	live := context.Background()
	expired, cancelExpired := context.WithDeadline(live, time.Now().Add(-time.Second))
	defer cancelExpired()
	canceled, cancel := context.WithCancel(live)
	cancel()
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"stage":          {e2PromptFail(live, "commit_failed"), "commit_failed"},
		"stage-timeout":  {e2PromptFail(expired, "begin_tx_failed"), "begin_tx_failed_timeout"},
		"stage-canceled": {e2PromptFail(canceled, "begin_tx_failed"), "begin_tx_failed_canceled"},
		"nil-context":    {e2PromptFail(nil, "invalid_input"), "invalid_input"},
		"wrapped":        {fmt.Errorf("server: %w", e2PromptFail(live, "sequence_unavailable")), "sequence_unavailable"},
		"plain":          {ErrE2Prompt, "unspecified"},
		"other":          {errors.New("x"), "other"},
		"none":           {nil, ""},
	} {
		if got := E2PromptFailureReason(tc.err); got != tc.want {
			t.Errorf("%s: %q, 기대 %q", name, got, tc.want)
		}
		if tc.want != "" && tc.want != "other" && !errors.Is(tc.err, ErrE2Prompt) {
			t.Errorf("%s: ErrE2Prompt 갈래가 아님", name)
		}
	}
}
