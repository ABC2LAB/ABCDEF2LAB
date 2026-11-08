package server

import "testing"

// 사람이 보는 서버 메시지는 한국어 하나다. 언어 설정이 어떤 값이어도 바뀌지 않아야 한다.
func TestTrMsgReturnsKorean(t *testing.T) {
	const message = "인증이 필요합니다"
	if got := trMsg(message); got != message {
		t.Fatalf("trMsg changed the message: %q", got)
	}
	if got := logT(message); got != message {
		t.Fatalf("logT changed the message: %q", got)
	}
}
