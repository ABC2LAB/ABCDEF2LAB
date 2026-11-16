package main

import (
	"os"
	"path/filepath"
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
			got := urlDiagnosisGoal(target, tc.mode, tc.methods, nil, nil)
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
			got := urlDiagnosisGoal("https://owned.example", mode, methods, nil, nil)
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
	got := urlDiagnosisGoal("https://owned.example", diagnosisflow.Auto, []string{"GET"}, nil, labels)
	if !strings.Contains(got, "labels: tester-a, tester-b") || !strings.Contains(got, "compare what each account can access") {
		t.Fatalf("accounts not named: %q", got)
	}
	for _, a := range []string{"/run/credential", strings.Repeat("a", 64), "auth-prep"} {
		if strings.Contains(got, a) {
			t.Fatalf("goal carries account binding detail %q", a)
		}
	}
}

// 목표 문장은 승인 경로 전체를 같은 origin의 절대 URL로 점검 주소 뒤에 알리고, 점검 주소는 그중 한 시작점이라고 적는다
// (VAmPI 2차 회차: 모델이 점검 주소 경로를 대상 전체로 여겨 다른 승인 경로를 한 번도 요청하지 않았다).
func TestURLDiagnosisGoalListsApprovedPaths(t *testing.T) {
	paths := []string{"/api/v1", "/openapi.json", "/static/main.js", "=/"}
	for _, mode := range []string{"", diagnosisflow.Auto, diagnosisflow.Manual} {
		got := urlDiagnosisGoal("https://owned.example:8443/api/v1", mode, []string{"GET", "HEAD"}, paths, nil)
		for _, w := range []string{
			"Approved paths on this origin, space-separated: https://owned.example:8443/api/v1 https://owned.example:8443/openapi.json https://owned.example:8443/static/main.js https://owned.example:8443/ (exact) .",
			"except a URL marked (exact), which covers only that path without a query string",
			"not the whole target: explore and test every approved path",
		} {
			if !strings.Contains(got, w) {
				t.Fatalf("mode=%q missing %q", mode, w)
			}
		}
		if strings.Index(got, "https://owned.example:8443/api/v1 through") > strings.Index(got, "Approved paths") {
			t.Fatal("the target URL must come before the approved path list")
		}
	}
	if strings.Contains(urlDiagnosisGoal("https://owned.example", "", []string{"GET"}, nil, nil), "Approved paths") {
		t.Fatal("no approved paths must not produce an empty list sentence")
	}
}

// 러너 작업 본문 검사(scripts/eval/runner_engine.py task_request)와 같은 정규식으로 볼 때, 점이 든 경로가 있어도 본문의
// URL은 모두 점검 주소와 같은 origin이고 URL 밖에는 IP·도메인 꼴이 없다. 원본 parseTarget(server/server.go)이 잡는 첫
// URL은 점검 주소다(엔진은 설명+" "+목표에서 첫 URL로 대상을 심는다).
func TestURLDiagnosisGoalTargetParsing(t *testing.T) {
	reURL := regexp.MustCompile(`https?://[^\s'"]+`)
	reIP := regexp.MustCompile(`\b((?:\d{1,3}\.){3}\d{1,3})(?::(\d{1,5}))?`)
	reDomain := regexp.MustCompile(`\b((?:[a-zA-Z0-9-]+\.)+[a-zA-Z]{2,})(?::(\d{1,5}))?`)
	target := "https://owned.example:8443/api/v1"
	paths := []string{"/api/v1", "/openapi.json", "/static/main.js", "/docs/v2.0", "=/"}
	for _, mode := range []string{"", diagnosisflow.Auto} {
		goal := urlDiagnosisGoal(target, mode, []string{"GET", "HEAD"}, paths, []string{"tester-a", "tester-b"})
		text := "승인된 URL 진단 " + goal
		urls := reURL.FindAllString(text, -1)
		if len(urls) == 0 || urls[0] != target {
			t.Fatalf("mode=%q first URL %q, want the target URL", mode, urls)
		}
		for _, raw := range urls {
			if urlGoalOrigin(raw) != "https://owned.example:8443" {
				t.Fatalf("mode=%q URL %q is not on the target origin", mode, raw)
			}
		}
		rest := reURL.ReplaceAllString(text, " ")
		if m := reIP.FindString(rest); m != "" {
			t.Fatalf("mode=%q IP-like text outside URLs: %q", mode, m)
		}
		if m := reDomain.FindString(rest); m != "" {
			t.Fatalf("mode=%q domain-like text outside URLs: %q", mode, m)
		}
		// 맨 경로로 렌더하면 이 검사에 걸린다는 것을 함께 고정한다(회귀 방지).
		if reDomain.FindString(" /openapi.json ") == "" {
			t.Fatal("bare dotted path must look like a domain to the runner regex")
		}
	}
}

// 시험했다고 볼 범위를 일반 문장으로 적는다(판정 아님, IDOR 확정은 E③ 몫). 대상 특정 경로·이름은 넣지 않는다.
func TestURLDiagnosisGoalStatesAuthorizationTestScope(t *testing.T) {
	got := urlDiagnosisGoal("https://owned.example", diagnosisflow.Auto, []string{"GET"}, []string{"/"}, []string{"tester-a", "tester-b"})
	for _, w := range []string{
		"IDOR is tested when the same object on an endpoint that requires authentication is requested with each test account's session and the responses are compared",
		"broken access control is tested when a function meant for another role is requested with a session that lacks that role and with no session",
		"Reading data that any unauthenticated client can read does not by itself test IDOR or access control",
	} {
		if !strings.Contains(got, w) {
			t.Fatalf("missing %q", w)
		}
	}
}

// 공통 부분(승인 경로·목적·보고)은 원본(B) 작업 본문에도 바이트까지 같게 들어간다. 제품 목표 = 제품 전용 앞부분 + 공통 +
// 제품 전용 끝 문장이고, 공통 부분은 E②·워크플로·메서드 같은 제품 전용 문장을 담지 않으며, 예시 입력의 결과가 고정 파일과
// 같다(평가 러너가 같은 파일로 대조).
func TestURLDiagnosisCommonGoalIsSharedAndPinned(t *testing.T) {
	target, paths, labels := "https://owned.example:8443/api/v1", []string{"/api/v1", "/openapi.json", "=/"}, []string{"tester-a", "tester-b"}
	for _, mode := range []string{"", diagnosisflow.Auto, diagnosisflow.Manual} {
		for _, methods := range [][]string{{"GET", "HEAD"}, {"GET", "POST"}} {
			full, common, product := urlDiagnosisGoalParts(target, mode, methods, paths, labels)
			if common != urlDiagnosisCommonGoal("https://owned.example:8443", paths, labels) || !strings.Contains(full, common) || strings.Replace(full, common, "", 1) != product {
				t.Fatalf("mode=%q methods=%v: goal is not product prefix + shared text + product tail", mode, methods)
			}
			for _, only := range []string{"E2", "Workbench", "workflow", "Approved methods"} {
				if strings.Contains(common, only) {
					t.Fatalf("shared text carries product-only text %q", only)
				}
			}
		}
	}
	want, err := os.ReadFile(filepath.Join("testdata", "goal-common-example.txt"))
	if got := urlDiagnosisCommonGoal("https://owned.example:8443", paths, labels); err != nil || string(want) != got+"\n" {
		t.Fatalf("shared goal text differs from testdata/goal-common-example.txt (update both the file and the evaluation runner's B task text together): err=%v", err)
	}
}
