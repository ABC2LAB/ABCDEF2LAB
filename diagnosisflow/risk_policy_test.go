package diagnosisflow

import (
	"context"
	"net/url"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
)

// 위험 경로 판정(C23). 정상 읽기의 오탐을 줄이고 파괴 동작·쓰기 거래·위험 query는 계속 막는다.
func TestRiskyRequestUsesWholeWordsAndMethod(t *testing.T) {
	for _, tc := range []struct {
		method, path, query string
		risky               bool
	}{
		{"GET", "/api/payments-history", "", false},
		{"GET", "/checkout", "", false},
		{"HEAD", "/api/transfers", "", false},
		{"GET", "/api/undeleted-items", "", false},
		{"GET", "/api/resetting-guide", "", false},
		{"POST", "/api/payments", "", true},
		{"PUT", "/checkout/confirm", "", true},
		{"GET", "/logout", "", true},
		{"GET", "/api/users/7/delete", "", true},
		{"GET", "/api/deleteAccount", "", true},
		{"GET", "/password-reset", "", true},
		{"GET", "/api/items", "action=delete", true},
		{"GET", "/api/items", "logout=1", true},
		{"GET", "/api/items", "page=2&sort=name", false},
	} {
		query, _ := url.ParseQuery(tc.query)
		if got := riskyRequest(tc.method, tc.path, query); got != tc.risky {
			t.Errorf("%s %s?%s risky=%v want %v", tc.method, tc.path, tc.query, got, tc.risky)
		}
	}
}

func TestStaticDiscoveryKeepsReadsOfTransactionPages(t *testing.T) {
	c, _ := flowFixture(t, Auto)
	n, err := c.ReportStatic(context.Background(), []Discovery{{Method: "GET", Path: "/read/payments-history"}, {Method: "GET", Path: "/read/checkout"}})
	if err != nil || n != 2 {
		t.Fatal("read of a transaction page was dropped as risky", n, err)
	}
}

func TestBeforeRefusesDestructiveQueryOnApprovedPath(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	if err := c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read?page=2"}); err != nil {
		t.Fatal("ordinary query on the approved path was refused", err)
	}
	for _, raw := range []string{"https://owned.test/read?action=delete", "https://owned.test/read?logout=true"} {
		if err := c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: raw}); err == nil {
			t.Fatal("destructive query was admitted:", raw)
		}
	}
}
