package diagnosisflow

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

// C23: 합성어를 camelCase나 구분자로 나눠 쓴 상태 변경 경로는 붙여서 본다. 앞뒤 일치는 하지 않아 정상 조회 경로는 막지 않는다
// (탐색을 깎는 통제는 줄인다).
func TestRiskWordsJoinSplitCompounds(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		risky        bool
	}{
		{"GET", "/api/logOut", true},
		{"GET", "/api/log-out", true},
		{"GET", "/api/signOut", true},
		{"GET", "/api/sign_out", true},
		{"GET", "/api/logoff", true},
		{"POST", "/api/shutDown", true},
		{"POST", "/api/basket/checkOut", true},
		{"GET", "/api/basket/checkOut", false},
		{"GET", "/api/userdelete", false},
		{"GET", "/api/deleteall", false},
		{"GET", "/api/resetpassword", false},
		{"GET", "/api/reset2fa", true},
		{"GET", "/api/v2/delete3", true},
		{"GET", "/api/oauth2/users", false},
		{"GET", "/api/DELETEUSER", false},
		{"GET", "/api/items/deleted", false},
		{"GET", "/api/sessions/logouts", false},
		{"GET", "/api/payments-history", false},
		{"GET", "/api/undeleted-items", false},
		{"GET", "/rest/products/search", false},
	} {
		if got := riskyRequest(tc.method, tc.path, nil); got != tc.risky {
			t.Errorf("%s %s risky=%v, want %v", tc.method, tc.path, got, tc.risky)
		}
	}
}

// C23: query 값의 위험 단어는 동작을 고르는 이름일 때만 본다. ;를 구분자로 쓰는 대상도 같은 이름으로 본다.
func TestBeforeQueryActionValues(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	for _, tc := range []struct {
		raw    string
		denied bool
	}{
		{"https://owned.test/read?action=delete", true},
		{"https://owned.test/read?x=1;action=delete", true},
		{"https://owned.test/read?action=delete;", true},
		{"https://owned.test/read?a=%zz&action=delete", true},
		{"https://owned.test/read?action=del%65te", true},
		{"https://owned.test/read?action=logOut", true},
		{"https://owned.test/read?do=user.delete", true},
		{"https://owned.test/read?delete=1", true},
		{"https://owned.test/read?action=deleteall", false},
		{"https://owned.test/read?q=delete", false},
		{"https://owned.test/read?q=how+to+reset+password", false},
		{"https://owned.test/read?sort=remove_date", false},
	} {
		err := c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: tc.raw})
		if (err != nil) != tc.denied {
			t.Errorf("Before %s -> %v, want denied=%v", tc.raw, err, tc.denied)
		}
	}
}

// C23: 크롤링은 camelCase 로그아웃 링크를 자동 방문하지 않고 막힌 끝점으로만 남긴다.
func TestCrawlSkipsCamelCaseLogout(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	var visited []string
	_, err := c.Crawl(t.Context(), func(ctx context.Context, target string) (e2runner.Result, error) {
		visited = append(visited, target)
		view := e2proxy.RequestView{Method: "GET", URL: target}
		if err := c.Before(ctx, view); err != nil {
			t.Fatalf("crawl sent a request Before refuses: %s: %v", target, err)
		}
		receipt := e2proxy.Receipt{AttemptID: statusText(len(visited)), CallRef: "owned-call", Decision: "allowed", ForwardAttempted: true, RequestWritten: true, ResponseReceived: true, ResponseComplete: true, StatusCode: 200}
		if err := c.After(ctx, view, receipt); err != nil {
			t.Fatal(err)
		}
		body := `<a href="/read/logOut">sign out</a><a href="/read/items">items</a>`
		return e2runner.Result{ToolOutput: "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n" + body, Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Final: true, ProcessDrained: true, ProxyDrained: true, Call: e2proxy.CallResult{Final: true, AuditComplete: true, Counts: e2proxy.Counts{ForwardAttempted: 1}}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range visited {
		if v == "https://owned.test/read/logOut" {
			t.Fatalf("crawl visited logout link: %q", visited)
		}
	}
	s, _ := c.Snapshot()
	var blocked, items bool
	for _, e := range s.Endpoints {
		switch e.Path {
		case "/read/logOut":
			blocked = !e.InScope && e.Change == "blocked"
		case "/read/items":
			items = true
		}
	}
	if !blocked || !items {
		t.Fatalf("logout endpoint not kept as blocked (blocked=%v items=%v): %+v", blocked, items, s.Endpoints)
	}
}

// 계정을 닫거나 지우는 쓰기(탈퇴·해지)는 쓰기 메서드에서만 정확한 단어로 막는다(2026-11-26 팀 합의, 모델 직접 로그인의
// 남는 위험 보조). 읽기와 부분 문자열(closet·disclose·eraser)은 막지 않는다. 탐색 경계 위험 단어(DestructiveWords)에는
// 넣지 않는다.
func TestAccountClosureWordsOnlyForWrites(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		risky        bool
	}{
		{"POST", "/account/close", true},
		{"POST", "/account/closeAccount", true},
		{"PUT", "/users/me/deactivate", true},
		{"POST", "/membership/withdraw", true},
		{"POST", "/api/unregister", true},
		{"PATCH", "/subscription/terminate", true},
		{"DELETE", "/data/erase", true},
		{"POST", "/account/deactivate-now", true},
		{"GET", "/account/close", false},
		{"HEAD", "/membership/withdraw", false},
		{"POST", "/closet/items", false},
		{"POST", "/disclose/report", false},
		{"POST", "/tools/eraser", false},
		{"POST", "/login", false},
	} {
		if got := riskyRequest(tc.method, tc.path, nil); got != tc.risky {
			t.Errorf("%s %s risky=%v, want %v", tc.method, tc.path, got, tc.risky)
		}
	}
	for _, word := range DestructiveWords() {
		for _, closure := range accountClosureWords {
			if word == closure {
				t.Fatalf("계정 닫기 단어 %q가 탐색 경계 위험 단어로 넘어감", word)
			}
		}
	}
}
