package idor

import (
	"testing"
	"time"

	"github.com/Autumn-27/artex/oracle"
)

// 정책 유효 기간은 판정 대상인 교차 조회 시각으로 본다. 소유자 조회 시각이 대신하면 안 된다.
func TestVerifyJudgesPolicyAtCrossReadTime(t *testing.T) {
	now := time.Now()
	t.Run("cross after window", func(t *testing.T) {
		cand, tx, kg := cleanScenario(now)
		key := "evaluated|GET|" + testResource
		entry := tx.byKey[key]
		entry.cap.ObservedAt = now.Add(2 * time.Hour)
		tx.byKey[key] = entry
		if got := Verify(cand, tx, kg); got.Verdict.Value != oracle.Undecidable {
			t.Fatalf("verdict = %q, want undecidable (reasons %v)", got.Verdict.Value, got.Verdict.ReasonCodes)
		}
	})
	t.Run("owner before window", func(t *testing.T) {
		cand, tx, kg := cleanScenario(now)
		key := "owner|GET|" + testResource
		entry := tx.byKey[key]
		entry.cap.ObservedAt = now.Add(-2 * time.Hour)
		tx.byKey[key] = entry
		if got := Verify(cand, tx, kg); got.Verdict.Value != oracle.Confirmed {
			t.Fatalf("verdict = %q, want confirmed (reasons %v)", got.Verdict.Value, got.Verdict.ReasonCodes)
		}
	})
}

// 전송기가 실패를 보고하면 응답이 채워져 있어도 증거로 쓰지 않는다.
func TestVerifyIgnoresPopulatedCaptureFromFailedSend(t *testing.T) {
	now := time.Now()
	for _, key := range []string{"owner|GET|" + testResource, "evaluated|GET|" + testResource} {
		t.Run(key, func(t *testing.T) {
			cand, tx, kg := cleanScenario(now)
			entry := tx.byKey[key]
			entry.ok = false
			tx.byKey[key] = entry
			if got := Verify(cand, tx, kg); got.Verdict.Value == oracle.Confirmed {
				t.Fatalf("failed send confirmed (reasons %v)", got.Verdict.ReasonCodes)
			}
		})
	}
}
