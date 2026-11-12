package e2proxy

import "net/http"

// AttemptPathObserver는 시도마다 한 번, 결과 영수증이 감사 기록된 뒤 Execution 잠금 밖에서 불린다.
// escapedPath는 정규화한 요청의 url.URL.EscapedPath()로, 계측기가 전달 요청에서 보는 표현과 같다.
// 거부된 요청도 같은 규칙으로 만들지만 보낸 것으로 표시하지 않는다(영수증의 decision·forward_attempted 그대로).
// 요청 URL을 만들 수 없던 거부(형식 오류·CONNECT 단계)는 ""다.
// 평가 전용 경로 투영(진단 요청 반복 차단 수, browser-acceptance.md §6)을 위한 것이고 허용 판단·송신·감사·계수에
// 영향을 주지 않는다. 오류를 돌려주지 않으며 짧게 끝나야 한다. 경로 원문을 감사 저널·영수증·trusted에 넣지 않는다.
type AttemptPathObserver func(r Receipt, escapedPath string)

func (c *Call) observeAttemptPath(req *http.Request, r Receipt) {
	observer := c.e.cfg.AttemptPathObserver
	if observer == nil {
		return
	}
	path := ""
	if req != nil && req.URL != nil {
		path = req.URL.EscapedPath()
	}
	observer(r, path)
}
