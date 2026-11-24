package server

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/pathmask"
)

func TestReportGraphDisplayPathProjection(t *testing.T) {
	const origin = "https://api.example.test"
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"escaped slash stays in one segment", origin + "/api/al%2Fice/%2574oken", "/{id}/{id}/{id}"},
		{"free text and IDs", origin + "/sam/42/00000000-0000-4000-8000-000000000001/alice%40example.test/", "/{id}/{id}/{id}/{id}/"},
		{"URL credentials and query", "https://synthetic-user:synthetic-password@api.example.test/a/short-secret?synthetic-key=synthetic-value#synthetic-fragment", "/{id}/{id}"},
		{"untrusted template", origin + "/books/v1/{title}", "/{id}/{id}/{id}"},
		{"malformed escape", origin + "/%xx", ""},
		{"no authority", "https:///api/sam", ""},
		{"unsupported scheme", "file://api.example.test/a/b", ""},
		{"no path", origin + "?synthetic-key=synthetic-value", ""},
		{"root path", origin + "/#synthetic-fragment", "/"},
		{"ambiguous slashes", origin + "/api//name", ""},
		{"maximum", origin + strings.Repeat("/x", pathmask.MaxSegments) + "/", strings.Repeat("/{id}", pathmask.MaxSegments) + "/"},
		{"too many segments", origin + strings.Repeat("/x", pathmask.MaxSegments+1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &db.CoverageGraphData{Nodes: []db.CoverageGraphNode{
				{Key: "a:1", Kind: "endpoint", AssetID: 1, URL: tc.raw},
			}}
			node := publicCoverageGraph(g).Nodes[0]
			if node.DisplayPath != tc.want {
				t.Fatalf("표시 경로 = %q, 기대값 %q", node.DisplayPath, tc.want)
			}
			if g.Nodes[0].URL != tc.raw {
				t.Fatal("저장 그래프가 변경됨")
			}
			raw, err := json.Marshal(node)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "display_path") != (tc.want != "") {
				t.Fatal("선택 필드의 생략 규칙 불일치")
			}
			for _, private := range []string{"synthetic-user", "synthetic-password", "short-secret", "synthetic-key", "synthetic-value", "synthetic-fragment"} {
				if strings.Contains(string(raw), private) {
					t.Fatal("원문 URL의 일부가 공개 응답에 남음")
				}
			}
		})
	}
}

func TestReportGraphDisplayPathHandoffFixtures(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		raw, err := os.ReadFile("../docs/contracts/fixtures/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	var want coverageGraphPublic
	fixture := read("coverage-graph-display-path")
	if err := json.Unmarshal(fixture, &want); err != nil {
		t.Fatal(err)
	}

	// 공개 식별자·범위·검사 상태가 경로 투영과 무관한지 합성 저장 자료를 통해 비교한다.
	suffixes := map[string]string{
		"a:14": "/raw-service-path",
		"a:21": "/api/status",
		"a:22": "/api/users/123",
		"a:23": "/api/users/sam",
		"a:24": "/",
	}
	stored := db.CoverageGraphData{Edges: want.Edges}
	for _, n := range want.Nodes {
		row := n.CoverageGraphNode
		row.URL += suffixes[row.Key]
		stored.Nodes = append(stored.Nodes, row)
	}
	if got := publicCoverageGraph(&stored); !reflect.DeepEqual(got, want) {
		t.Fatalf("공개 투영과 합성 인계 자료가 다름: %+v", got)
	}

	var oldReader db.CoverageGraphData
	if err := json.Unmarshal(fixture, &oldReader); err != nil || len(oldReader.Nodes) != len(want.Nodes) ||
		!reflect.DeepEqual(oldReader.Edges, want.Edges) {
		t.Fatal("기존 DTO가 추가 필드가 있는 응답을 읽지 못함", err)
	}
	var newReader coverageGraphPublic
	if err := json.Unmarshal(read("coverage-graph-legacy"), &newReader); err != nil || len(newReader.Nodes) != 2 ||
		newReader.Nodes[1].DisplayPath != "" || newReader.Nodes[1].URL != "https://api.example.test" {
		t.Fatal("새 DTO가 기존 응답을 읽지 못함", err)
	}
}
