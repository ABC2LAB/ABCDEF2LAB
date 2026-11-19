package db

// KG 조회 v1 읽기 어댑터(docs/contracts/kg-query-v1.md §3·§8). 호출자가 연 read-only
// repeatable-read 트랜잭션 하나에서만 읽는다 — 기존 helper를 여러 번 불러 snapshot을 섞지 않는다.
// 모든 조회는 상한+1 행까지만 읽고, 넘으면 자르지 않고 ErrKGTooLarge다(§5.3).
// source_summary·URL 원문·payload는 공개 칸으로 읽지 않는다. URL은 포함 규칙(endpoint_origin) 계산에만 쓴다.

import (
	"context"
	"database/sql"
	"errors"
)

// kgScopeMatchCond는 contextCoverageCTE의 범위 일치 조건 그대로다(§3.1-2).
// scopeTargetCTE의 IP-literal domain 확장과 합치지 않는다. 두 문자열이 같은지는 시험이 지킨다.
const kgScopeMatchCond = `
       (ts.kind='company'     AND a.company_id = ts.company_id)
    OR (ts.kind='root_domain' AND a.root_domain = ts.domain)
    OR (ts.kind='subdomain'   AND a.domain = ts.domain)
    OR (ts.kind IN ('ip','cidr') AND ts.net >>= try_inet(a.ip))
    OR (ts.kind='icp' AND (
         lower(regexp_replace(COALESCE(a.icp,''), '[[:space:]]+', '', 'g')) = ts.value
         OR lower(regexp_replace(COALESCE(a.app_icp,''), '[[:space:]]+', '', 'g')) = ts.value
       ))`

type kgQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// QueryKGTx는 권한을 확인한 작업 문맥(tasks[0]=현재, 나머지=직접 원천 ID 오름차순)의 v1 그래프를 만든다.
// 문맥은 호출자가 같은 트랜잭션에서 읽은 것이어야 한다. 이 함수는 쓰기를 하지 않는다.
func QueryKGTx(ctx context.Context, tx *sql.Tx, tasks []KGContextTask) (*KGGraph, error) {
	rows, err := readKGRows(ctx, tx, tasks)
	if err != nil {
		return nil, err
	}
	return buildKGGraph(rows)
}

func readKGRows(ctx context.Context, q kgQuerier, tasks []KGContextTask) (kgRows, error) {
	in := kgRows{Tasks: tasks}
	if len(tasks) == 0 {
		return in, errors.New("kg: empty task context")
	}
	taskIDs := make([]int64, 0, len(tasks))
	explorations := make([]int64, 0, len(tasks))
	for _, t := range tasks {
		taskIDs = append(taskIDs, t.ID)
		explorations = append(explorations, t.Exploration)
	}
	capRows := KGLimits.Provenance + 1
	var err error

	// 명시적 연결 — 배열 쌍마다 출처 하나(link 또는 array)가 생기므로 출처 상한으로 묶는다.
	in.Arrays, err = kgScan(ctx, q, capRows, func(r *sql.Rows) (kgPair, error) {
		var p kgPair
		return p, r.Scan(&p.Task, &p.Asset)
	}, `SELECT ctx.id, a.id FROM unnest($1::bigint[]) AS ctx(id) JOIN assets a ON a.task_ids @> ARRAY[ctx.id]
ORDER BY 1, 2 LIMIT $2`, taskIDs, capRows)
	if err != nil {
		return in, err
	}
	in.Links, err = kgScan(ctx, q, capRows, func(r *sql.Rows) (kgLinkRow, error) {
		var l kgLinkRow
		var node sql.NullInt64
		err := r.Scan(&l.Task, &l.Asset, &l.Source, &node)
		if node.Valid {
			l.SourceNode = &node.Int64
		}
		return l, err
	}, `SELECT l.task_id, l.asset_id, l.source, l.source_node_id FROM task_asset_links l JOIN assets a ON a.id=l.asset_id
WHERE l.task_id = ANY($1::bigint[]) AND a.task_ids @> ARRAY[l.task_id] ORDER BY 1, 2 LIMIT $2`, taskIDs, capRows)
	if err != nil {
		return in, err
	}
	in.ScopeMatches, err = kgScan(ctx, q, capRows, func(r *sql.Rows) (kgScopeMatchRow, error) {
		var m kgScopeMatchRow
		return m, r.Scan(&m.Scope, &m.Task, &m.Source, &m.Asset)
	}, `SELECT ts.id, ts.task_id, ts.source, a.id FROM task_scope ts JOIN assets a ON (`+kgScopeMatchCond+`
) WHERE ts.task_id = ANY($1::bigint[]) ORDER BY 1, 4 LIMIT $2`, taskIDs, capRows)
	if err != nil {
		return in, err
	}
	in.KeywordTasks, err = kgScan(ctx, q, len(tasks)+1, func(r *sql.Rows) (int64, error) {
		var t int64
		return t, r.Scan(&t)
	}, `SELECT DISTINCT task_id FROM task_scope WHERE task_id = ANY($1::bigint[]) AND kind='keyword' ORDER BY 1 LIMIT $2`, taskIDs, len(tasks)+1)
	if err != nil {
		return in, err
	}
	// 공개 가능한 앵커만 읽는다 — 원천의 끝나지 않은 intent·현재의 삭제 intent는 상한 계산에도 넣지 않는다.
	in.Anchors, err = kgScan(ctx, q, capRows, func(r *sql.Rows) (kgAnchorRow, error) {
		var a kgAnchorRow
		var conf sql.NullString
		err := r.Scan(&a.Node.ID, &a.Asset, &a.Node.Exploration, &a.Node.Kind, &a.Node.State, &conf)
		a.Node.Confidence = conf.String
		return a, err
	}, `SELECT ea.node_id, ea.asset_id, n.exploration_id, n.kind, n.state, CASE WHEN n.kind='fact' THEN n.payload->>'confidence' END
FROM exploration_nodes n JOIN exploration_anchors ea ON ea.node_id=n.id
WHERE n.exploration_id = ANY($1::bigint[]) AND n.kind IN ('intent','fact','finding')
  AND (n.kind<>'intent' OR n.state IN ('done','blocked','exhausted','stopped') OR (n.exploration_id=$2 AND n.state<>'deleted'))
ORDER BY 1, 2 LIMIT $3`, explorations, tasks[0].Exploration, capRows)
	if err != nil {
		return in, err
	}

	selected := map[int64]bool{}
	for _, p := range in.Arrays {
		selected[p.Asset] = true
	}
	for _, m := range in.ScopeMatches {
		selected[m.Asset] = true
	}
	for _, a := range in.Anchors {
		selected[a.Asset] = true
	}
	if len(tasks)+len(selected) > KGLimits.Nodes {
		return in, ErrKGTooLarge
	}
	assetIDs := make([]int64, 0, len(selected))
	for id := range selected {
		assetIDs = append(assetIDs, id)
	}
	in.Assets, err = kgScan(ctx, q, KGLimits.Nodes+1, func(r *sql.Rows) (kgAssetRow, error) {
		var a kgAssetRow
		return a, r.Scan(&a.ID, &a.Type, &a.Domain, &a.RootDomain, &a.IP, &a.URL, &a.Kind)
	}, `SELECT id, type, COALESCE(domain,''), COALESCE(root_domain,''), COALESCE(ip,''), COALESCE(url,''), COALESCE(service_type,'')
FROM assets WHERE id = ANY($1::bigint[]) ORDER BY id LIMIT $2`, assetIDs, KGLimits.Nodes+1)
	if err != nil {
		return in, err
	}
	// 배열에 없는 연결 행 — 포함된 자산에 대해서만 상충 문제로 쓴다(숨은 자산을 드러내지 않음).
	in.Mismatches, err = kgScan(ctx, q, KGLimits.Issues+1, func(r *sql.Rows) (kgPair, error) {
		var p kgPair
		return p, r.Scan(&p.Task, &p.Asset)
	}, `SELECT l.task_id, l.asset_id FROM task_asset_links l JOIN assets a ON a.id=l.asset_id
WHERE l.task_id = ANY($1::bigint[]) AND l.asset_id = ANY($2::bigint[]) AND NOT (a.task_ids @> ARRAY[l.task_id])
ORDER BY 1, 2 LIMIT $3`, taskIDs, assetIDs, KGLimits.Issues+1)
	if err != nil {
		return in, err
	}
	// 연결 행의 source_node_id 검증 재료. 노드 내용은 종류·상태·fact confidence만 읽는다.
	var linkTasks, linkAssets, linkNodes []int64
	for _, l := range in.Links {
		if l.SourceNode != nil {
			linkTasks, linkAssets, linkNodes = append(linkTasks, l.Task), append(linkAssets, l.Asset), append(linkNodes, *l.SourceNode)
		}
	}
	if len(linkNodes) > 0 {
		in.SourceNodes, err = kgScan(ctx, q, capRows, func(r *sql.Rows) (kgSourceNodeRow, error) {
			var s kgSourceNodeRow
			var conf sql.NullString
			err := r.Scan(&s.Link.Task, &s.Link.Asset, &s.Node.ID, &s.Node.Exploration, &s.Node.Kind, &s.Node.State, &conf, &s.Anchored)
			s.Node.Confidence = conf.String
			return s, err
		}, `SELECT p.task, p.asset, n.id, n.exploration_id, n.kind, n.state, CASE WHEN n.kind='fact' THEN n.payload->>'confidence' END,
       EXISTS (SELECT 1 FROM exploration_anchors ea WHERE ea.node_id=n.id AND ea.asset_id=p.asset)
FROM unnest($1::bigint[], $2::bigint[], $3::bigint[]) AS p(task, asset, node) JOIN exploration_nodes n ON n.id=p.node
ORDER BY 1, 2 LIMIT $4`, linkTasks, linkAssets, linkNodes, capRows)
		if err != nil {
			return in, err
		}
	}
	return in, nil
}

// kgScan은 limit 행까지 읽는다. limit에 닿으면(= 상한+1) 자르지 않고 ErrKGTooLarge다.
func kgScan[T any](ctx context.Context, q kgQuerier, limit int, scan func(*sql.Rows) (T, error), query string, args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		if len(out) >= limit {
			return nil, ErrKGTooLarge
		}
	}
	return out, rows.Err()
}
