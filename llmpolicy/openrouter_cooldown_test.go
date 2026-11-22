package llmpolicy

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// openRouterNoCooldown은 쉬는 총 시간을 0으로 둬 연속 일시 오류 상한에서 바로 잠그게 한다(이전 동작을 보는 시험용).
func openRouterNoCooldown(t *testing.T) {
	t.Helper()
	openRouterCooldownTimes(t, time.Hour, 0)
}

func openRouterCooldownTimes(t *testing.T, cooldown, total time.Duration) {
	t.Helper()
	c, tt := openRouterCooldown, openRouterCooldownTotal
	openRouterCooldown, openRouterCooldownTotal = cooldown, total
	t.Cleanup(func() { openRouterCooldown, openRouterCooldownTotal = c, tt })
}

// 제공자 일시 오류가 연속 상한에 닿으면 잠그지 않고 쉰다. 쉬는 동안의 호출은 보내지 않고 예약 없이 거절한다.
// 쉬는 시간 뒤 시험 호출 하나가 성공하면 쉬는 상태가 풀리고 연속 수도 0이 된다.
func TestOpenRouterCooldownThenProbeSucceeds(t *testing.T) {
	openRouterCooldownTimes(t, 40*time.Millisecond, time.Hour)
	p := openRouterV2Fixture(t, 2)
	var hits atomic.Int64
	fail := atomic.Bool{}
	fail.Store(true)
	client := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) {
		hits.Add(1)
		if fail.Load() {
			return openRouterStatus(429), nil
		}
		return openRouterResponse(openRouterReply), nil
	}))
	for i := 0; i < 2; i++ {
		if _, err := client.Do(openRouterRequest()); err == nil {
			t.Fatalf("call %d succeeded", i)
		}
	}
	snap := p.Snapshot()
	if snap.Blocked || p.CooldownUntil().IsZero() {
		t.Fatalf("상한에서 쉬지 않음: blocked=%v until=%v", snap.Blocked, p.CooldownUntil())
	}
	// 쉬는 중에 자리 없이 온 호출: 보내지 않고, 예약·요청 수도 늘지 않는다.
	if _, err := client.Do(openRouterRequest()); err == nil || hits.Load() != 2 {
		t.Fatalf("쉬는 중에 보냄: err=%v hits=%d", err, hits.Load())
	}
	if after := p.Snapshot(); after.Requests != snap.Requests || after.ReservedCostMicros != snap.ReservedCostMicros || after.Blocked || after.FailureReason != "provider_cooldown" {
		t.Fatalf("쉬는 중 거절이 회계를 바꿈: %+v", after)
	}
	fail.Store(false)
	start := time.Now()
	release, err := p.AwaitCooldown(context.Background())
	if err != nil || time.Since(start) < 20*time.Millisecond {
		t.Fatalf("쉬는 시간을 기다리지 않음: err=%v waited=%v", err, time.Since(start))
	}
	resp, err := client.Do(openRouterRequest())
	release()
	if err != nil || hits.Load() != 3 {
		t.Fatalf("시험 호출: err=%v hits=%d", err, hits.Load())
	}
	_ = resp.Body.Close()
	if !p.CooldownUntil().IsZero() || p.Snapshot().Blocked {
		t.Fatal("성공한 시험 호출 뒤에도 쉬는 중")
	}
	// 풀린 뒤에는 기다리지 않고, 연속 수는 0부터 다시 센다(실패 하나로는 쉬지 않는다).
	if release, err := p.AwaitCooldown(context.Background()); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	fail.Store(true)
	if _, err := client.Do(openRouterRequest()); err == nil || !p.CooldownUntil().IsZero() {
		t.Fatal("연속 수가 0으로 돌아가지 않음")
	}
}

// 시험 호출이 계속 실패하면 쉬는 시간마다 하나씩만 보내고, 처음 쉰 때부터 총 시간을 넘기면 일시 오류 상한으로 잠근다.
func TestOpenRouterCooldownLocksAfterTotal(t *testing.T) {
	openRouterCooldownTimes(t, 30*time.Millisecond, 100*time.Millisecond)
	p := openRouterV2Fixture(t, 2)
	var hits atomic.Int64
	client := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) {
		hits.Add(1)
		return openRouterStatus(503), nil
	}))
	for i := 0; i < 2; i++ {
		_, _ = client.Do(openRouterRequest())
	}
	began := time.Now()
	probes := 0
	for !p.Snapshot().Blocked {
		if probes > 20 {
			t.Fatal("총 시간을 넘겨도 잠기지 않음")
		}
		release, err := p.AwaitCooldown(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		before := hits.Load()
		_, _ = client.Do(openRouterRequest())
		release()
		if p.Snapshot().Blocked && hits.Load() == before {
			break
		}
		if hits.Load() != before+1 {
			t.Fatalf("시험 호출이 한 번이 아님: %d → %d", before, hits.Load())
		}
		probes++
	}
	snap := p.Snapshot()
	if !snap.TransientLimited || time.Since(began) < 100*time.Millisecond || probes < 2 || probes > 5 {
		t.Fatalf("잠김: limited=%v elapsed=%v probes=%d", snap.TransientLimited, time.Since(began), probes)
	}
	// 잠긴 뒤에는 기다리지 않고 예약 단계가 거절한다.
	release, err := p.AwaitCooldown(context.Background())
	release()
	if err != nil || !p.CooldownUntil().IsZero() {
		t.Fatalf("잠긴 뒤 대기: err=%v", err)
	}
	if _, err := client.Do(openRouterRequest()); err == nil {
		t.Fatal("잠긴 뒤 보냄")
	}
}

// 시험 호출 자리는 한 번에 하나다. 기다리던 호출 문맥이 끝나면 오류로 돌아오고 자리를 쥐지 않는다.
func TestOpenRouterCooldownSingleProbeAndContext(t *testing.T) {
	openRouterCooldownTimes(t, 30*time.Millisecond, time.Hour)
	p := openRouterV2Fixture(t, 2)
	client := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) { return openRouterStatus(429), nil }))
	for i := 0; i < 2; i++ {
		_, _ = client.Do(openRouterRequest())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := p.AwaitCooldown(ctx); err == nil {
		t.Fatal("문맥이 끝났는데 자리를 받음")
	}
	first, err := p.AwaitCooldown(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 첫 자리가 비기 전에는 두 번째가 받지 못한다.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel2()
	if _, err := p.AwaitCooldown(ctx2); err == nil {
		t.Fatal("시험 호출 자리가 둘")
	}
	first() // 보내지 못하고 자리를 돌려줌
	second, err := p.AwaitCooldown(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second()
}

// 요청 내용 거부(400)가 이어져 상한에 닿은 것은 제공자 일시 오류가 아니라서 쉬지 않고 바로 잠근다.
func TestOpenRouterRejectionStreakDoesNotCooldown(t *testing.T) {
	openRouterCooldownTimes(t, time.Hour, time.Hour)
	p := openRouterV2Fixture(t, 2)
	client := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) { return openRouterStatus(400), nil }))
	for i := 0; i < 2; i++ {
		_, _ = client.Do(openRouterRequest())
	}
	if !p.Snapshot().Blocked || !p.CooldownUntil().IsZero() {
		t.Fatalf("거부 연속이 쉬는 상태로 감: blocked=%v", p.Snapshot().Blocked)
	}
}
