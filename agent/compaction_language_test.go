package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode"

	"github.com/Autumn-27/artex/db"
)

func hasHan(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0
}

func compressionFixture(memberSummary string) (*coldGraph, block, map[int64]*db.Node) {
	node := func(id int64, kind, summary string) *db.Node {
		payload, _ := json.Marshal(map[string]string{"summary": summary})
		return &db.Node{ID: id, Kind: kind, State: "done", Payload: payload}
	}
	nodes := map[int64]*db.Node{
		1: node(1, db.KindIntent, "probe the listing"),
		2: node(2, db.KindFact, memberSummary),
		3: node(3, db.KindIntent, "upstream intent"),
	}
	return &coldGraph{children: map[int64][]int64{1: {2}}}, block{Members: []int64{1, 2}, Anchors: []int64{3}}, nodes
}

// 압축 지시와 입력 라벨은 프로그램이 쓰는 글이라 영어여야 한다.
func TestCompressionPromptAndLabelsAreEnglish(t *testing.T) {
	if hasHan(compressionSystemPrompt) {
		t.Fatal("compression system prompt contains Han characters")
	}
	graph, b, nodes := compressionFixture("listing returned two rows")
	input := buildCompressionInput(graph, b, nodes)
	if hasHan(input) {
		t.Fatalf("compression input labels contain Han characters: %q", input)
	}
	for _, want := range []string{"Member nodes", "#1 produced or derived -> #2", "Lineage edges", "Common parent", "#3"} {
		if !strings.Contains(input, want) {
			t.Fatalf("compression input lost %q: %q", want, input)
		}
	}
}

// 노드 요약은 프로그램 글이 아니다. 대상 응답에서 온 문자열을 번역하거나 지우지 않는다.
func TestCompressionInputKeepsNodeTextAsIs(t *testing.T) {
	const fromTarget = "응답 제목은 用户列表 이다"
	graph, b, nodes := compressionFixture(fromTarget)
	if input := buildCompressionInput(graph, b, nodes); !strings.Contains(input, fromTarget) {
		t.Fatalf("node text changed: %q", input)
	}
}
