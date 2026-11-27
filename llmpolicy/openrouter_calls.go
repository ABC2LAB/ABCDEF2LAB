package llmpolicy

import (
	"context"
	"encoding/json"
	"regexp"
	"time"
)

// 호출별 기록(탐색 분리 배치 결정안 D(b), 2026-11-16 사용자 결정). 평가 러너가 제품(O)의 모델 호출을 원본(B)의 평가
// 게이트웨이 기록과 같은 기준으로 사후 검증하는 근거다. 그래서 행의 칸 이름·결정·끝 모양 어휘는 게이트웨이
// (cmd/eval-meter/gateway.go)의 llm-requests 행과 같다(게이트웨이 시험이 아래 상수와 대조한다). 전송은 바꾸지 않는다:
// 이미 읽은 응답 JSON에서 id를 읽어 남길 뿐이고 다른 곳에 요청하지 않는다. 기록은 승인 장부와 같은 보호 디렉터리에만
// 쓰고 모델 입력·사용량 요약(OpenRouterUsage)·보고서에는 넣지 않는다.
const (
	// OpenRouterCallForwarded는 응답 본문을 끝까지 읽은 호출이다(상태 코드와 상관없이).
	OpenRouterCallForwarded = "forwarded"
	// OpenRouterCallUpstreamError는 응답을 끝까지 받지 못한 호출이다. 끝 모양은 UnfinishedEnd에 있다.
	OpenRouterCallUpstreamError = "upstream_error"
	// OpenRouterEndClientClosedBeforeDone은 호출자 쪽(문맥 취소, 응답 상한 초과로 읽기 중단)이 먼저 멈춘 것이다.
	OpenRouterEndClientClosedBeforeDone = "client_closed_before_done"
	// OpenRouterEndUpstreamCutBeforeDone은 연결 실패·본문 끊김처럼 위쪽에서 끝까지 오지 않은 것이다.
	OpenRouterEndUpstreamCutBeforeDone = "upstream_cut_before_done"
	// OpenRouterEndClientClosedAfterDone·OpenRouterEndUpstreamCutAfterDone은 게이트웨이가 SSE 종료 줄 뒤 끊김에 쓰는
	// 값이다. 제품 transport는 스트리밍을 받지 않아(본문 전체가 응답이다) 이 두 값을 만들지 않는다. 어휘는 같게 둔다.
	OpenRouterEndClientClosedAfterDone = "client_closed_after_done"
	OpenRouterEndUpstreamCutAfterDone  = "upstream_cut_after_done"
)

// OpenRouterCallRecordFields는 호출 행의 JSON 칸 이름이다. 게이트웨이 llm-requests 행과 같아야 한다.
var OpenRouterCallRecordFields = []string{"seq", "decision", "method", "status", "started_at", "completed_at",
	"request_bytes", "response_bytes", "generation_id", "unfinished_end"}

// OpenRouterCallRecord는 보낸 호출 하나의 기록이다. Status 0은 HTTP 응답을 받지 못했다는 뜻이다(게이트웨이는 같은 경우를
// 자기 502로 적는다). GenerationID는 제공자 응답 JSON의 id이고 없거나 모양이 다르면 빈 값이다.
type OpenRouterCallRecord struct {
	Seq           uint64 `json:"seq"`
	Decision      string `json:"decision"`
	Method        string `json:"method"`
	Status        int    `json:"status"`
	StartedAt     string `json:"started_at"`
	CompletedAt   string `json:"completed_at"`
	RequestBytes  int64  `json:"request_bytes"`
	ResponseBytes int64  `json:"response_bytes"`
	GenerationID  string `json:"generation_id"`
	UnfinishedEnd string `json:"unfinished_end,omitempty"`
}

// openRouterGenerationIDPattern은 기록에 남길 generation id 모양이다. 제공자 문자열을 그대로 쓰지 않고 이 모양만 받는다.
var openRouterGenerationIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// openRouterGenerationID는 이미 끝까지 읽은 응답 JSON의 최상위 id를 읽는다.
func openRouterGenerationID(data []byte) string {
	var head struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &head) != nil || !openRouterGenerationIDPattern.MatchString(head.ID) {
		return ""
	}
	return head.ID
}

// openRouterCallerStopped는 끝까지 받지 못한 호출을 이쪽(호출자)이 멈춘 것인지다: 요청 문맥이 끝났거나 문맥 기한이 지났다.
// 호출 시간 상한(http.Client.Timeout)은 문맥 기한과 따로 도는 타이머로도 요청을 끊는다. 그 타이머가 먼저 끊으면 문맥이 아직
// 끝나지 않았을 수 있어, 같은 시한 끊김의 끝 모양이 호출마다 '이쪽이 멈춤'과 '위쪽 끊김'으로 갈렸다. 평가 러너는 위쪽 끊김만
// 제공자 장애로 세므로(runner_fault.py·runner_early_end.py) 한 값으로 남아야 한다(2026-11-27 사용자 결정): 기한이 지났으면
// 이쪽이 멈춘 것이다.
func openRouterCallerStopped(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	if ctx.Err() != nil {
		return true
	}
	deadline, ok := ctx.Deadline()
	return ok && !time.Now().Before(deadline)
}

// openRouterCallOutcome은 결정과 끝 모양을 게이트웨이와 같은 어휘로 정한다.
func openRouterCallOutcome(complete, callerStopped bool) (decision, end string) {
	switch {
	case complete:
		return OpenRouterCallForwarded, ""
	case callerStopped:
		return OpenRouterCallUpstreamError, OpenRouterEndClientClosedBeforeDone
	default:
		return OpenRouterCallUpstreamError, OpenRouterEndUpstreamCutBeforeDone
	}
}

// openRouterStamp는 게이트웨이 행과 같은 시각 형식이다.
func openRouterStamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// recordCall은 호출 기록을 남긴다. 남기지 못해도 이미 끝난 전송 결과는 바꾸지 않고 실패 수만 센다. 러너는 이 수가 0이
// 아니면 사후 검증 근거가 불완전하다고 본다.
func (p *OpenRouterPolicy) recordCall(rec OpenRouterCallRecord) {
	if p == nil || p.callLog == nil {
		return
	}
	if p.callLog(rec) != nil {
		p.mu.Lock()
		p.callLogFailures++
		p.mu.Unlock()
	}
}

// CallRecordFailures는 호출 기록을 남기지 못한 수다. 기록을 켜지 않은 정책(보호 파일 로더를 거치지 않음)은 0이다.
func (p *OpenRouterPolicy) CallRecordFailures() int64 {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.callLogFailures
}
