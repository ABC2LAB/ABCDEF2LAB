package main

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/server"
)

// 작업 목표는 승인 기록의 메서드·작업 방식으로 만든다. 읽기만 승인된 범위는 읽기 전용이라고 적는다. 쓰기가 승인된 범위는 그
// 메서드를 적고, 건별 확인 흐름이면 확인을 지어내지 말라고, 자동 모드면 사전 승인 범위라고 적는다(C21). 어느 방식이든 결과
// 불명 요청을 다시 보내지 말라고 적는다(루프 복원 결정안 §3).
func TestURLDiagnosisGoalFollowsApprovedMethods(t *testing.T) {
	const target = "https://owned.example"
	for name, tc := range map[string]struct {
		mode    string
		methods []string
		want    []string
		absent  []string
	}{
		"read-only":          {"", []string{"GET", "HEAD"}, []string{"approved read-only HTTPS target " + target, "Approved methods: GET, HEAD.", "Do not request state changes or other origins."}, []string{"Write methods", "per request"}},
		"read-only-workflow": {diagnosisflow.Auto, []string{"GET"}, []string{"approved read-only HTTPS target", "Workbench workflow mode: " + diagnosisflow.Auto + ".", "First prepare an initial discovery intent", "plan deep diagnostic intents from the endpoints the server lists", "State-changing requests require separately granted scope"}, []string{"Write methods", "exact selected endpoints"}},
		"selective-writes":   {diagnosisflow.Manual, []string{"GET", "POST", "PATCH"}, []string{"Inspect the approved HTTPS target " + target, "Approved methods: GET, POST, PATCH.", "Write methods (POST, PATCH) are approved only per request", "Never invent that confirmation", "never resend a request whose result is unknown", "Do not request other origins."}, []string{"read-only", "State-changing requests require separately granted scope", "pre-approved"}},
		"auto-writes":        {diagnosisflow.Auto, []string{"GET", "POST"}, []string{"Inspect the approved HTTPS target " + target, "Write methods (POST) are pre-approved for this automatic run", "without per-request confirmation", "Never resend a request whose result is unknown"}, []string{"read-only", "approved only per request", "operator confirms", "State-changing requests require separately granted scope"}},
		"cli-writes":         {"", []string{"GET", "PUT"}, []string{"Write methods (PUT) are approved only per request", "Do not request other origins."}, []string{"pre-approved", "Do not request state changes"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := urlDiagnosisGoal(target, tc.mode, tc.methods, nil)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Fatalf("missing %q in %q", w, got)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Fatalf("unexpected %q in %q", a, got)
				}
			}
		})
	}
}

// 목표의 목적은 취약점 찾기다. 관찰·후보 모으기만 말하면 목표 분해기가 "엔드포인트 정리"를 목표로 만들고 Planner가 탐색이
// 끝나자마자 달성으로 마감한다(2026-11-14 소유 Juice Shop 회차). 인가(IDOR·접근통제)를 먼저 적고, 탐색은 수단이라고 적는다.
func TestURLDiagnosisGoalStatesVulnerabilityObjective(t *testing.T) {
	for _, mode := range []string{"", diagnosisflow.Auto, diagnosisflow.Manual} {
		for _, methods := range [][]string{{"GET", "HEAD"}, {"GET", "POST"}} {
			got := urlDiagnosisGoal("https://owned.example", mode, methods, nil)
			for _, w := range []string{"find its security vulnerabilities", "Objective: find vulnerabilities", "actual request evidence", "Test authorization first", "IDOR", "broken access control", "other web vulnerability types", "Endpoint discovery and mapping are a means, not the objective", "not when it has been mapped", "unconfirmed review candidate"} {
				if !strings.Contains(got, w) {
					t.Fatalf("mode %q methods %v: missing %q in %q", mode, methods, w, got)
				}
			}
			// 관찰·후보만 모으라는 옛 문장(결정안 §3에서 지우기로 한 "후보만" 고정)이 남지 않는다.
			for _, a := range []string{"Collect only", "observations and manual-review candidates only", "labels:"} {
				if strings.Contains(got, a) {
					t.Fatalf("mode %q methods %v: unexpected %q", mode, methods, a)
				}
			}
			// 제품 fixture 모델(cmd/product-fixture)이 목표에서 대상을 찾는 문장 머리는 그대로다.
			if !regexp.MustCompile(`Inspect the approved (?:read-only )?HTTPS target (https://owned\.example)`).MatchString(got) {
				t.Fatalf("goal prefix changed: %q", got)
			}
		}
	}
}

// 결속된 시험 계정이 있으면 Planner가 계정을 바꿔 보는 인가 intent를 세울 수 있게 라벨(이름만)을 적는다. 세션·자격 값은 없다.
func TestURLDiagnosisGoalNamesBoundTestAccounts(t *testing.T) {
	auth := &server.E2HTTPFileAuth{RuleRef: "auth-prep", Accounts: []server.E2HTTPFileAuthAccount{
		{Label: "tester-b", Credential: server.E2HTTPFilePin{File: "/run/credential-b", SHA256: strings.Repeat("b", 64)}},
		{Label: "tester-a", Credential: server.E2HTTPFilePin{File: "/run/credential-a", SHA256: strings.Repeat("a", 64)}},
	}}
	labels := urlGoalAccountLabels(auth)
	if !slices.Equal(labels, []string{"tester-a", "tester-b"}) || urlGoalAccountLabels(nil) != nil {
		t.Fatalf("labels %v", labels)
	}
	got := urlDiagnosisGoal("https://owned.example", diagnosisflow.Auto, []string{"GET"}, labels)
	if !strings.Contains(got, "labels: tester-a, tester-b") || !strings.Contains(got, "compare what each account can access") {
		t.Fatalf("accounts not named: %q", got)
	}
	for _, a := range []string{"/run/credential", strings.Repeat("a", 64), "auth-prep"} {
		if strings.Contains(got, a) {
			t.Fatalf("goal carries account binding detail %q", a)
		}
	}
}
