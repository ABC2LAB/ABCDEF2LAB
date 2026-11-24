package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestReportGraphReadRouteClosure(t *testing.T) {
	for _, leaf := range []string{"coverage-graph", "access-map"} {
		for _, tc := range []struct {
			method, path string
			allowed      bool
		}{
			{"GET", "/api/tasks/42/" + leaf, true},
			{"HEAD", "/api/tasks/42/" + leaf, false},
			{"POST", "/api/tasks/42/" + leaf, false},
			{"GET", "/api/tasks/042/" + leaf, false},
			{"GET", "/api/tasks/0/" + leaf, false},
			{"GET", "/api/tasks/-1/" + leaf, false},
			{"GET", "/api/tasks/+42/" + leaf, false},
			{"GET", "/api/tasks/9223372036854775808/" + leaf, false},
			{"GET", "/api/tasks/42/" + leaf + "?", false},
			// KG 조회 v1 선택자는 coverage-graph에서 정확한 원문 하나만 받는다(kg-query-v1 §3).
			{"GET", "/api/tasks/42/" + leaf + "?view=kg-v1", leaf == "coverage-graph"},
			{"HEAD", "/api/tasks/42/" + leaf + "?view=kg-v1", false},
			{"GET", "/api/tasks/42/" + leaf + "?view=kg-v1&view=kg-v1", false},
			{"GET", "/api/tasks/42/" + leaf + "?view=kg-v1&task=42", false},
			{"GET", "/api/tasks/42/" + leaf + "?view=kg-v2", false},
			{"GET", "/api/tasks/42/" + leaf + "?view=kg%2Dv1", false},
			{"GET", "/api/tasks/42/" + leaf + "?View=kg-v1", false},
			{"GET", "/api/tasks/42/" + leaf + "?view=", false},
			{"GET", "/api/tasks/042/" + leaf + "?view=kg-v1", false},
			{"GET", "/api/tasks/42/" + leaf + "?task=42&task=43", false},
			{"GET", "/api/tasks/42/" + leaf + "?token=secret", false},
			{"GET", "/api/tasks/%34%32/" + leaf, false},
			{"GET", "/api/tasks/42%2F" + leaf, false},
			{"GET", "/api/tasks/42//" + leaf, false},
			{"GET", "/api/tasks/42/" + leaf + "/extra", false},
		} {
			t.Run(tc.method+tc.path, func(t *testing.T) {
				r := httptest.NewRequest(tc.method, tc.path, nil)
				id, ok := reportGraphRequestTask(r)
				if ok != tc.allowed || ok && id != 42 {
					t.Fatal("보고서 경로 범위 불일치")
				}
				_, bridge := managementBridgeTask(r)
				if bridge != tc.allowed {
					t.Fatal("관리 중계 경로 검사 불일치")
				}
			})
		}
	}
}

func TestReportGraphPublicProjectionOmitsUntrustedValues(t *testing.T) {
	stored := &db.CoverageGraphData{Nodes: []db.CoverageGraphNode{
		{Key: "a:2", Kind: "endpoint", AssetID: 2, InScope: true, Tested: true, URL: "https://user:password@owned.test/reset/private-token?token=secret#secret", Label: "secret", PageTitle: "secret", AppName: "secret", ServiceType: "secret", Domain: "?secret", RootDomain: "secret/parameter", IP: "secret"},
		{Key: "r:secret/parameter", Kind: "root_domain", Domain: "secret/parameter", Label: "secret"},
		{Key: "c:1", Kind: "company", CompanyID: 1, Label: "secret"},
	}, Edges: []db.CoverageGraphEdge{{Src: "a:2", Dst: "r:secret/parameter"}}}
	g := publicCoverageGraph(stored)
	raw, _ := json.Marshal(g)
	for _, value := range []string{"secret", "password", "private-token", "user:"} {
		if strings.Contains(string(raw), value) {
			t.Fatal("원문 비밀이 공개 DTO에 남음")
		}
	}
	if len(g.Nodes) != 3 || len(g.Edges) != 1 || g.Nodes[0].URL != "https://owned.test" || g.Nodes[0].DisplayPath != "/{id}/{id}" || !g.Nodes[0].Tested || !g.Nodes[0].InScope || g.Edges[0].Dst == "r:secret/parameter" {
		t.Fatal("기존 노드·관계·검사 표시 유실")
	}
	before := string(raw)
	raw, _ = json.Marshal(publicCoverageGraph(stored))
	if before != string(raw) {
		t.Fatal("같은 저장 자료의 공개 투영이 달라짐")
	}
}
