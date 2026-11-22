package llmpolicy

import (
	"context"
	"time"
)

// 연속 일시 오류 상한(C06)에 닿으면 바로 잠그지 않고 쉬었다가 시험 호출 하나를 보낸다(2026-11-22 사용자 결정).
// 제공자가 잠깐 버벅여도 점검 전체가 끝나지 않게 하되, 제공자를 계속 두드리지 않는다는 C06의 목적은 그대로다:
// 쉬는 동안에는 호출을 보내지 않고, 쉬는 시간이 끝날 때마다 호출 하나만 보낸다.
//
// 값의 근거:
//   - openRouterCooldown 60초: 평가 러너의 제공자 멈춤 기준(180초 동안 모델 호출 없음)보다 충분히 짧아서, 쉬는 동안에도
//     시험 호출의 시작·끝이 60초마다 기록된다. 429 구간은 지금까지의 회차에서 수 초~수십 초였다.
//   - openRouterCooldownTotal 5분: 회차 시간 예산(1500초)의 20%. 처음 쉬기 시작한 때부터 이만큼 지나도록 정상 응답이
//     없으면 이전처럼 잠근다(연속 일시 오류 상한으로 잠김, 회계는 그대로 믿을 수 있음). 이 시간이 지난 뒤에는 새 호출을
//     보내지 않는다: 실패한 시험 호출의 결과에서도, 새 호출의 대기·예약 경계에서도 검사한다(expireCooldownLocked).
//
// 시험에서 바꿀 수 있게 변수로 둔다.
var (
	openRouterCooldown      = 60 * time.Second
	openRouterCooldownTotal = 5 * time.Minute
	// openRouterCooldownPoll은 다른 시험 호출이 끝나기를 기다릴 때 상태를 다시 보는 간격이다.
	openRouterCooldownPoll = 200 * time.Millisecond
)

// enterCooldownLocked는 연속 일시 오류가 상한에 닿았을 때 부른다(p.mu를 쥔 채). 쉬기로 했으면 참을 돌려준다.
// 처음 쉰 때부터 총 시간을 넘겼거나 v1 승인(첫 일시 오류에 잠그는 이전 동작)이면 거짓이고, 호출자가 잠근다.
func (p *OpenRouterPolicy) enterCooldownLocked(now time.Time) bool {
	if p.manifest.Version < 2 {
		return false
	}
	if p.coolSince.IsZero() {
		p.coolSince = now
	}
	if now.Sub(p.coolSince) >= openRouterCooldownTotal {
		return false
	}
	p.coolUntil = now.Add(openRouterCooldown)
	return true
}

// expireCooldownLocked는 쉬는 중인데 처음 쉰 때부터 총 시간이 지났으면 잠근다(p.mu를 쥔 채). 새 호출을 보내기 전의
// 경계(대기·예약)에서 부른다: 쉬는 시간이 끝난 뒤 호출이 한동안 없다가 들어와도, 총 시간이 지났으면 시험 호출을 보내지
// 않고 잠근다. 이미 보낸 시험 호출이 진행 중이면(probeID != 0) 여기서 잠그지 않고 그 결과가 정한다: 성공이면 풀리고,
// 실패면 enterCooldownLocked가 총 시간을 보고 잠근다. 그동안 다른 호출은 보내지 않는다.
func (p *OpenRouterPolicy) expireCooldownLocked(now time.Time) {
	if p.blocked || p.coolUntil.IsZero() || p.probeID != 0 || now.Sub(p.coolSince) < openRouterCooldownTotal {
		return
	}
	p.blocked, p.transientLimited = true, true
	p.usage.FailureReason = "provider_cooldown_expired"
}

// clearCooldownLocked는 정상 응답 하나가 왔을 때 쉬는 상태를 지운다(p.mu를 쥔 채).
func (p *OpenRouterPolicy) clearCooldownLocked() {
	p.coolSince, p.coolUntil, p.probeID = time.Time{}, time.Time{}, 0
}

// CooldownUntil은 지금 쉬는 중이면 쉬는 시간이 끝나는 시각을 돌려준다. 쉬지 않으면 zero time이다.
func (p *OpenRouterPolicy) CooldownUntil() time.Time {
	if p == nil {
		return time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.blocked {
		return time.Time{}
	}
	return p.coolUntil
}

// AwaitCooldown은 쉬는 중이면 쉬는 시간이 끝날 때까지 기다린 뒤 시험 호출 자리 하나를 준다. 쉬지 않거나 이미 잠겼으면
// 바로 돌아온다(잠김은 예약 단계가 거절한다). 시험 호출 자리는 한 번에 하나라서, 다른 호출이 시험 중이면 그 결과를 기다린다:
// 성공하면 그대로 지나가고, 실패하면 다음 쉬는 시간을 다시 기다린다. 기다리는 동안에는 예약도 호출 기록도 남기지 않는다.
// 돌려준 release는 호출이 끝났거나 보내지 못했을 때 반드시 부른다. 자리는 release만 비운다: 쉬기 전에 이미 보낸 다른 호출이
// 끝나면서 자리를 비우면 시험 호출이 둘이 될 수 있어서다(성공한 시험 호출은 쉬는 상태 자체를 지운다).
func (p *OpenRouterPolicy) AwaitCooldown(ctx context.Context) (release func(), err error) {
	noop := func() {}
	if p == nil || ctx == nil {
		return noop, nil
	}
	for {
		p.mu.Lock()
		now := time.Now()
		p.expireCooldownLocked(now)
		if p.blocked || p.coolUntil.IsZero() {
			p.mu.Unlock()
			return noop, nil
		}
		wait := p.coolUntil.Sub(now)
		if wait <= 0 && p.probeID == 0 {
			p.probeSeq++
			id := p.probeSeq
			p.probeID = id
			p.mu.Unlock()
			return func() {
				p.mu.Lock()
				if p.probeID == id {
					p.probeID = 0
				}
				p.mu.Unlock()
			}, nil
		}
		p.mu.Unlock()
		if wait <= 0 {
			wait = openRouterCooldownPoll
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return noop, ctx.Err()
		case <-timer.C:
		}
	}
}
