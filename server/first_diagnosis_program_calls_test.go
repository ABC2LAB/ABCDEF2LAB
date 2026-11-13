package server

import (
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
)

// 서버가 연 호출(인증 준비 등)은 최종·감사 완료면 Worker 결과 없이 마감에 들어가고, 아니거나 Worker 결과가 붙어 있으면
// 막는다. 서버가 열지 않은 호출은 이 판정을 받지 않는다(C14, 11/13 S07 실제 회차).
func TestFirstDiagnosisProgramCall(t *testing.T) {
	program := map[string]bool{"auth-call": true}
	final := e2proxy.CallResult{CallRef: "auth-call", Final: true, AuditComplete: true}
	if settled, isProgram := firstDiagnosisProgramCall(final, program, nil); !settled || !isProgram {
		t.Fatal("final audited program call not settled")
	}
	for name, call := range map[string]e2proxy.CallResult{
		"not-final": {CallRef: "auth-call", AuditComplete: true},
		"no-audit":  {CallRef: "auth-call", Final: true},
	} {
		if settled, isProgram := firstDiagnosisProgramCall(call, program, nil); settled || !isProgram {
			t.Fatalf("%s: unsettled program call accepted", name)
		}
	}
	if settled, _ := firstDiagnosisProgramCall(final, program, map[string]e2proxy.CallResult{"auth-call": {CallRef: "auth-call"}}); settled {
		t.Fatal("program call with a worker result accepted")
	}
	if _, isProgram := firstDiagnosisProgramCall(e2proxy.CallResult{CallRef: "worker-call", Final: true, AuditComplete: true}, program, nil); isProgram {
		t.Fatal("worker call treated as a program call")
	}
}
