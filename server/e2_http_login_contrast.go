package server

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/url"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/loginattempt"
)

// 프로그램 대조 요청(docs/contracts/model-login-attempts.md §4). 모델이 로그인 세션으로 연 URL에 세션 없는 응답이 이 회차에
// 없으면, 그 정확한 URL을 세션 없이 한 번 다시 열어 막힘이면 로그인 성공을 세운다. E② 프로그램 호출(login_contrast)로
// 보내므로 승인 규칙 범위·제외 경로·예산·감사를 그대로 받고, 작업대 target_requests의 login 칸에 든다.

// loginContrastProber는 판정기가 쌓은 대조 요청을 하나씩 보낸다.
type loginContrastProber struct {
	execution *e2proxy.Execution
	tracker   *loginattempt.Tracker
	calls     *e2HTTPProgramCalls
	origin    *url.URL
	signal    chan struct{}
}

func newLoginContrastProber(origin *url.URL) *loginContrastProber {
	return &loginContrastProber{origin: origin, signal: make(chan struct{}, 1)}
}

// notify는 판정기 잠금 안에서 불린다. 막지 않는다.
func (p *loginContrastProber) notify() {
	select {
	case p.signal <- struct{}{}:
	default:
	}
}

// run은 문맥이 끝날 때까지 알림마다 쌓인 대조 요청을 보낸다. 모델 단계가 마감되면 E²가 새 프로그램 호출을 열지 않으므로
// 그 뒤 요청은 보내지 않은 것으로 끝난다.
func (p *loginContrastProber) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.signal:
		}
		for {
			probe, ok := p.tracker.NextProbe()
			if !ok {
				break
			}
			p.send(ctx, probe)
		}
	}
}

func (p *loginContrastProber) send(ctx context.Context, probe loginattempt.Probe) {
	call, err := p.execution.BeginProgramCall(ctx, e2proxy.ProgramLoginContrast, "login-contrast")
	if err != nil {
		p.tracker.ProbeDone(probe, nil)
		return
	}
	p.calls.add(call.Ref())
	defer func() {
		if _, closeErr := call.Close(context.WithoutCancel(ctx)); closeErr != nil {
			log.Print("[e2-http] 로그인 대조 요청 호출을 닫지 못함")
		}
	}()
	target := *p.origin
	target.RawPath, target.Path = "", ""
	full := target.String() + probe.Path
	if probe.RawQuery != "" {
		full += "?" + probe.RawQuery
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		p.tracker.ProbeDone(probe, nil)
		return
	}
	resp, _, err := call.Do(ctx, req)
	if err != nil || resp == nil {
		p.tracker.ProbeDone(probe, nil)
		return
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, loginattempt.MaxObservedBodyBytes))
	if closeErr := resp.Body.Close(); closeErr != nil {
		log.Print("[e2-http] 로그인 대조 요청 응답을 닫지 못함")
	}
	if readErr != nil {
		body = nil
	}
	p.tracker.ProbeDone(probe, &loginattempt.Response{Status: resp.StatusCode, Header: resp.Header.Clone(), Body: body})
}
