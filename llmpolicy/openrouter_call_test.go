package llmpolicy

import (
	"context"
	"net/http"
	"sync"
	"testing"
)

type testReplyKey struct{}

// contextWithReply는 fixture가 고를 응답 이름을 문맥에 붙인다(전송 본문·헤더는 바꾸지 않는다).
func contextWithReply(r *http.Request, name string) context.Context {
	return context.WithValue(r.Context(), testReplyKey{}, name)
}

// 겹친 두 요청은 각자 자기 호출 칸에 예약·사용량을 남기고, transport 누적 값은 두 칸의 합이다(C28 결정 2).
func TestOpenRouterCallSlotsSeparateOverlappingRequests(t *testing.T) {
	p := openRouterFixture(t)
	p.manifest.MaxConcurrent = 2
	inside, release := make(chan struct{}, 2), make(chan struct{})
	replies := map[string]string{
		"first":  `{"model":"qwen/qwen3.6-35b-a3b","provider":"Fixture","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"cost":0.000012}}`,
		"second": `{"model":"qwen/qwen3.6-35b-a3b","provider":"Fixture","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"cost":0.000023}}`,
	}
	c := openRouterClient(p, openRouterRT(func(r *http.Request) (*http.Response, error) {
		inside <- struct{}{}
		<-release // 두 요청이 모두 transport 안에 있을 때 응답한다.
		return openRouterResponse(replies[r.Context().Value(testReplyKey{}).(string)]), nil
	}))
	slots := map[string]*OpenRouterCall{"first": {}, "second": {}}
	var wg sync.WaitGroup
	for name, slot := range slots {
		wg.Add(1)
		go func(name string, slot *OpenRouterCall) {
			defer wg.Done()
			req := openRouterRequest()
			req = req.WithContext(WithOpenRouterCall(contextWithReply(req, name), slot))
			resp, err := c.Do(req)
			if err != nil {
				t.Error(name, err)
				return
			}
			_ = resp.Body.Close()
		}(name, slot)
	}
	<-inside
	<-inside
	close(release)
	wg.Wait()
	a, b := slots["first"].Result(), slots["second"].Result()
	if !a.Reserved || !a.Succeeded || a.InputTokens != 10 || a.OutputTokens != 2 || a.CostMicros != 12 ||
		!b.Reserved || !b.Succeeded || b.InputTokens != 20 || b.OutputTokens != 3 || b.CostMicros != 23 {
		t.Fatalf("slots: first=%+v second=%+v", a, b)
	}
	u := p.Snapshot()
	if u.Requests != 2 || u.InputTokens != 30 || u.OutputTokens != 5 || u.CostMicros != 35 ||
		u.ReservedInputTokens != a.ReservedInputTokens+b.ReservedInputTokens || u.ReservedCostMicros != a.ReservedCostMicros+b.ReservedCostMicros {
		t.Fatalf("transport total differs from slots: %+v", u)
	}
}

// 한 칸은 요청 하나에만 쓴다. 같은 칸으로 온 두 번째 요청은 예약 전에 거부되고 송신되지 않는다. 칸이 없는
// 요청은 지금처럼 처리된다.
func TestOpenRouterCallSlotSingleUse(t *testing.T) {
	p := openRouterFixture(t)
	hits := 0
	c := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) {
		hits++
		return openRouterResponse(openRouterReply), nil
	}))
	slot := &OpenRouterCall{}
	for i, want := range []bool{true, false} {
		req := openRouterRequest()
		resp, err := c.Do(req.WithContext(WithOpenRouterCall(req.Context(), slot)))
		if (err == nil) != want {
			t.Fatalf("request %d with the same slot: err=%v", i, err)
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
	}
	if hits != 1 || p.Snapshot().Requests != 1 || slot.Result().InputTokens != 10 {
		t.Fatalf("slot reuse reached the wire or the reservation: hits=%d usage=%+v", hits, p.Snapshot())
	}
	resp, err := c.Do(openRouterRequest())
	if err != nil || hits != 2 {
		t.Fatal("request without a slot rejected", err)
	}
	_ = resp.Body.Close()
}
