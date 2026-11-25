package server

import (
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/pathmask"
)

// PathDetailState는 운영자 조회에서 확인할 수 있는 경로 상세 수준이다.
type PathDetailState string

const (
	PathDetailExact       PathDetailState = "exact"
	PathDetailPatternOnly PathDetailState = "pattern_only"
	PathDetailUnavailable PathDetailState = "unavailable"
)

// 공개 응답 전용 필드다. DB 그래프의 노드와 간선 식별에는 사용하지 않는다.
type coverageGraphPublicNode struct {
	db.CoverageGraphNode
	DisplayPath     string          `json:"display_path,omitempty"`
	PathDetailState PathDetailState `json:"path_detail_state,omitempty"`
}

type coverageGraphPublic struct {
	Nodes []coverageGraphPublicNode `json:"nodes"`
	Edges []db.CoverageGraphEdge    `json:"edges"`
}

// 그래프 행에는 관찰과 결속된 승인 틀이 없어, 이름처럼 보이는 조각도 전부 가린다.
func coverageGraphDisplayPath(escaped string) (string, PathDetailState) {
	if !strings.HasPrefix(escaped, "/") || strings.Contains(escaped, "//") ||
		strings.Count(escaped, "/") > pathmask.MaxSegments+1 {
		return "", PathDetailUnavailable
	}
	if escaped == "/" {
		return escaped, PathDetailExact
	}
	shape := pathmask.DisplayShape(escaped)
	if shape == pathmask.LongPath {
		return "", PathDetailUnavailable
	}
	return shape, PathDetailPatternOnly
}
