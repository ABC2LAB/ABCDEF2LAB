package server

import (
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/pathmask"
)

// 공개 응답 전용 필드다. DB 그래프의 노드와 간선 식별에는 사용하지 않는다.
type coverageGraphPublicNode struct {
	db.CoverageGraphNode
	DisplayPath string `json:"display_path,omitempty"`
}

type coverageGraphPublic struct {
	Nodes []coverageGraphPublicNode `json:"nodes"`
	Edges []db.CoverageGraphEdge    `json:"edges"`
}

// 그래프 행에는 관찰과 결속된 승인 틀이 없어, 이름처럼 보이는 조각도 전부 가린다.
func coverageGraphDisplayPath(escaped string) string {
	if !strings.HasPrefix(escaped, "/") || strings.Contains(escaped, "//") ||
		strings.Count(escaped, "/") > pathmask.MaxSegments+1 {
		return ""
	}
	shape := pathmask.DisplayShape(escaped)
	if shape == pathmask.LongPath {
		return ""
	}
	return shape
}
