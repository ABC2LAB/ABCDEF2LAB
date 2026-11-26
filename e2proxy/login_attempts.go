package e2proxy

import (
	"bytes"

	"github.com/Autumn-27/artex/loginattempt"
)

// AttachLoginAttempts는 모델 직접 로그인 회차의 로그인 시도 판정기를 붙인다(docs/contracts/model-login-attempts.md, 사용자
// 결정 2026-11-26). 모델 요청(진단 단계)마다 본문을 읽은 뒤 판정기에 묻고, 아이디별 연속 실패 상한에 닿은 로그인은 보내지
// 않는다. 그 밖의 모델 요청·응답은 성공 판정의 관찰로 넘긴다. 프로그램 요청(인증 준비·E③)은 넘기지 않는다. Worker가 첫
// 요청을 보내기 전에 붙인다.
func (e *Execution) AttachLoginAttempts(t *loginattempt.Tracker) {
	if e != nil {
		e.loginAttempts.Store(t)
	}
}

// loginAttemptRef는 로그인 시도 기록의 transport_ref다(E② call_ref·attempt_id, 계약 §3).
func loginAttemptRef(r Receipt) string { return r.CallRef + "/" + r.AttemptID }

// observedLoginResponse는 판정에 쓸 응답 사본이다. 본문은 판정기 상한까지의 앞부분만 복사한다(모델에 가는 응답과 따로).
func observedLoginResponse(status int, header map[string][]string, body []byte) *loginattempt.Response {
	if len(body) > loginattempt.MaxObservedBodyBytes {
		body = body[:loginattempt.MaxObservedBodyBytes]
	}
	h := make(map[string][]string, len(header))
	for k, v := range header {
		h[k] = append([]string(nil), v...)
	}
	return &loginattempt.Response{Status: status, Header: h, Body: bytes.Clone(body)}
}

// finishLoginAttempt는 모델 요청 하나가 끝난 뒤 결과를 판정기에 알린다. 보낸 로그인은 응답으로 판정하고, 보냈는지 모르면
// 결과 불명, 다른 경계 규칙으로 못 보냈으면 미전송이다. 로그인이 아닌 요청은 응답을 받았을 때만 관찰로 넘긴다.
func finishLoginAttempt(t *loginattempt.Tracker, p loginattempt.Plan, req loginattempt.Request, resp *loginattempt.Response, r Receipt) {
	if t == nil {
		return
	}
	switch {
	case p.Login && p.Refusal != "":
		// 판정기가 이미 거부 행을 남겼다.
	case p.Login && resp != nil:
		t.Complete(p, loginAttemptRef(r), req, *resp)
	case p.Login && r.Reason == Unknown:
		t.Unknown(p, loginAttemptRef(r))
	case p.Login:
		t.NotSent(p, loginAttemptRef(r), string(r.Reason))
	case resp != nil:
		t.Observe(req, *resp)
	}
}
