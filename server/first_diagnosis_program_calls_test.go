package server

import (
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
)

// 서버가 연 호출(인증 준비 등)은 최종·감사 완료면 Worker 결과 없이 마감에 들어가고, 아니거나 Worker 결과가 붙어 있으면
// 막는다. 서버가 열지 않은 호출은 이 판정을 받지 않는다(C14, 11/13 S07 실제 회차).
func TestFirstDiagnosisProgramCall(t *testing.T) {
	program := map[string]bool{"auth-call": true}
	final := e2proxy.CallResult{CallRef: "auth-call", Program: e2proxy.ProgramAuthPrep, Final: true, AuditComplete: true}
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
	for _, call := range []e2proxy.CallResult{
		{CallRef: "auth-call", IntentID: 1, Final: true, AuditComplete: true},
		{CallRef: "auth-call", IntentID: 1, Program: e2proxy.ProgramAuthPrep, Final: true, AuditComplete: true},
		{CallRef: "unregistered-program", Program: e2proxy.ProgramAuthPrep, Final: true, AuditComplete: true},
	} {
		if settled, _ := firstDiagnosisProgramCall(call, program, nil); settled {
			t.Fatal("귀속·등록 대조 없이 프로그램 호출을 마감함")
		}
	}
}

// E③ 확정 단계 호출(신원·생성·조회)은 제품 확정 단계가 E② 실행에 직접 보내 서버 등록부에 없다. 최종·감사 완료이고 intent가
// 없으면 마감에 들어간다(실제 제품 합성 A/B 회차에서 closure_program_call_open으로 마감이 막혔음, 11/20). 서버 호출 종류는
// 여전히 등록부가 있어야 하고, E③ 종류라도 미완료·intent 귀속·Worker 결과가 붙으면 막는다.
func TestFirstDiagnosisProgramCallE3(t *testing.T) {
	program := map[string]bool{}
	for _, kind := range []e2proxy.ProgramCallKind{e2proxy.ProgramE3Identity, e2proxy.ProgramE3Create, e2proxy.ProgramE3Read} {
		call := e2proxy.CallResult{CallRef: "e3-call", Program: kind, Final: true, AuditComplete: true}
		if settled, isProgram := firstDiagnosisProgramCall(call, program, nil); !settled || !isProgram {
			t.Fatalf("%s: 끝난 E③ 호출이 마감에 안 들어감", kind)
		}
		for name, bad := range map[string]e2proxy.CallResult{
			"not-final": {CallRef: "e3-call", Program: kind, AuditComplete: true},
			"no-audit":  {CallRef: "e3-call", Program: kind, Final: true},
			"intent":    {CallRef: "e3-call", Program: kind, IntentID: 3, Final: true, AuditComplete: true},
		} {
			if settled, isProgram := firstDiagnosisProgramCall(bad, program, nil); settled || !isProgram {
				t.Fatalf("%s %s: 받음", kind, name)
			}
		}
		if settled, _ := firstDiagnosisProgramCall(call, program, map[string]e2proxy.CallResult{"e3-call": {CallRef: "e3-call"}}); settled {
			t.Fatalf("%s: Worker 결과가 붙은 호출을 받음", kind)
		}
		// 등록부에 같은 참조가 있으면 서버 호출 규칙(Program.Valid)을 따른다(E③ 종류는 서버 호출 종류가 아님).
		if settled, _ := firstDiagnosisProgramCall(call, map[string]bool{"e3-call": true}, nil); settled {
			t.Fatalf("%s: 서버 등록부의 호출로 E③ 종류를 받음", kind)
		}
	}
	if settled, _ := firstDiagnosisProgramCall(e2proxy.CallResult{CallRef: "x", Program: "e3_other", Final: true, AuditComplete: true}, program, nil); settled {
		t.Fatal("모르는 프로그램 종류를 받음")
	}
}
