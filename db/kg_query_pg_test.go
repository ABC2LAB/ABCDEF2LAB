package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// 실제 PostgreSQL에서 §3.1 세 근거·직접 원천 한정·공개 상태·비밀 비공개를 확인한다(Q01~Q06·Q14·Q17).
// 모든 값은 합성 fixture다. DB 설정이 없으면 건너뛴다.
func TestKGQueryPostgres(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	// 정리(t.Cleanup)는 defer 뒤에 돈다 — 연결 닫기를 가장 먼저 등록해 가장 나중에 닫는다.
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	sfx := fmt.Sprintf("kg%d.test", time.Now().UnixNano())

	newTask := func() *Task {
		task, err := d.CreateTask("synthetic kg-v1", "fixture", nil, 0, 300)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
		return task
	}
	cur, src, srcsrc := newTask(), newTask(), newTask()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO task_relations(task_id, source_task_id) VALUES ($1,$2),($2,$3)`, cur.ID, src.ID, srcsrc.ID)

	var assetIDs []int64
	asset := func(typ string, taskIDs []int64, cols map[string]any) int64 {
		t.Helper()
		names, holders, args := []string{"type", "task_ids"}, []string{"$1", "$2::bigint[]"}, []any{typ, taskIDs}
		for _, k := range []string{"domain", "root_domain", "ip", "url", "service_type"} {
			if v, ok := cols[k]; ok {
				names = append(names, k)
				args = append(args, v)
				holders = append(holders, fmt.Sprintf("$%d", len(args)))
			}
		}
		var id int64
		if err := d.QueryRowContext(ctx, `INSERT INTO assets(`+strings.Join(names, ",")+`) VALUES (`+strings.Join(holders, ",")+`) RETURNING id`, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		assetIDs = append(assetIDs, id)
		return id
	}
	t.Cleanup(func() {
		if _, err := d.Exec(`DELETE FROM assets WHERE id = ANY($1::bigint[])`, assetIDs); err != nil {
			t.Error(err)
		}
	})
	node := func(exploration int64, kind, state, payload string) int64 {
		t.Helper()
		var id int64
		if err := d.QueryRowContext(ctx, `INSERT INTO exploration_nodes(exploration_id, kind, state, payload) VALUES ($1,$2,$3,$4::jsonb) RETURNING id`, exploration, kind, state, payload).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	anchor := func(n, a int64) { exec(`INSERT INTO exploration_anchors(node_id, asset_id) VALUES ($1,$2)`, n, a) }

	none := []int64{}
	// 현재 작업: 수동 연결(관측 참조 있음, 비밀 문구), 출처 행 없는 배열, 배열 없는 연결 행 두 개
	a1 := asset("endpoint", []int64{cur.ID}, map[string]any{"url": "https://user:canary-password@" + sfx + "/reset?token=canary-token"})
	f1 := node(cur.ExplorationID, "fact", "confirmed", `{"confidence":"observed","summary":"canary-payload"}`)
	anchor(f1, a1)
	exec(`UPDATE task_asset_links SET source='manual', source_summary='canary-summary', source_node_id=$3 WHERE task_id=$1 AND asset_id=$2`, cur.ID, a1, f1)
	a2 := asset("endpoint", []int64{cur.ID}, nil)
	exec(`DELETE FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, cur.ID, a2)
	a3 := asset("endpoint", none, nil)
	exec(`INSERT INTO task_asset_links(task_id, asset_id, source) VALUES ($1,$2,'api')`, cur.ID, a3)
	f3 := node(cur.ExplorationID, "finding", "confirmed", `{}`)
	anchor(f3, a3)
	a4 := asset("endpoint", none, nil) // 연결 행만 있고 다른 근거 없음 — 응답에 나타나지 않는다
	exec(`INSERT INTO task_asset_links(task_id, asset_id, source) VALUES ($1,$2,'api')`, cur.ID, a4)
	// 범위: 루트 도메인·하위 도메인, cidr(ip 칸만 — IP-literal domain은 아님), keyword
	a5 := asset("root_domain", none, map[string]any{"domain": sfx, "root_domain": sfx})
	a6 := asset("subdomain", none, map[string]any{"domain": "app." + sfx, "root_domain": sfx})
	a7 := asset("subdomain", none, map[string]any{"domain": "198.51.100.77"})
	a8 := asset("ip", none, map[string]any{"ip": "198.51.100.78"})
	var rootScope int64
	if err := d.QueryRowContext(ctx, `INSERT INTO task_scope(task_id, kind, domain, source) VALUES ($1,'root_domain',$2,'manual') RETURNING id`, cur.ID, sfx).Scan(&rootScope); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO task_scope(task_id, kind, net, source) VALUES ($1,'cidr','198.51.100.0/24','agent')`, cur.ID)
	exec(`INSERT INTO task_scope(task_id, kind, value, source) VALUES ($1,'keyword','canary-keyword','auto')`, cur.ID)
	// 현재 작업의 삭제 intent 앵커는 공개하지 않는다
	a13 := asset("endpoint", none, nil)
	anchor(node(cur.ExplorationID, "intent", "deleted", `{}`), a13)
	// 직접 원천: 연결(현재 작업 노드를 source_node로 — 다른 작업 노드라 검증 실패), 끝난/진행 중 intent 앵커
	a9 := asset("endpoint", []int64{src.ID}, nil)
	exec(`UPDATE task_asset_links SET source_node_id=$3 WHERE task_id=$1 AND asset_id=$2`, src.ID, a9, f1)
	a10 := asset("endpoint", none, nil)
	anchor(node(src.ExplorationID, "intent", "running", `{}`), a10)
	a11 := asset("endpoint", none, nil)
	done := node(src.ExplorationID, "intent", "done", `{}`)
	anchor(done, a11)
	// 원천의 원천은 넓히지 않는다
	a12 := asset("endpoint", []int64{srcsrc.ID}, nil)
	// 문맥 밖(원천의 원천) 탐색의 공개 상태 앵커도 읽지 않는다
	a14 := asset("endpoint", none, nil)
	anchor(node(srcsrc.ExplorationID, "fact", "confirmed", `{}`), a14)

	tx, err := d.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	g, err := QueryKGTx(ctx, tx, []KGContextTask{{cur.ID, cur.ExplorationID}, {src.ID, src.ExplorationID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(g)
	body := string(raw)

	var nodes []string
	for _, n := range g.Nodes {
		switch v := n.(type) {
		case KGTaskNode:
			nodes = append(nodes, v.ID)
		case KGAssetNode:
			nodes = append(nodes, v.ID)
		}
	}
	ref := func(prefix string, id int64) string { return fmt.Sprintf("%s%d", prefix, id) }
	wantNodes := []string{ref("t:", cur.ID), ref("t:", src.ID)}
	for _, a := range []int64{a1, a2, a3, a5, a6, a8, a9, a11} {
		wantNodes = append(wantNodes, ref("a:", a))
	}
	if !slices.Equal(nodes, wantNodes) {
		t.Fatalf("nodes=%v want %v", nodes, wantNodes)
	}
	for _, hidden := range []int64{a4, a7, a10, a12, a13, a14} {
		if strings.Contains(body, ref(`"a:`, hidden)+`"`) {
			t.Fatalf("포함되면 안 되는 자산 %d가 응답에 있음", hidden)
		}
	}
	if strings.Contains(body, ref(`"t:`, srcsrc.ID)+`"`) || slices.Contains(g.Scope.TaskIDs, fmt.Sprint(srcsrc.ID)) {
		t.Fatal("원천의 원천이 응답에 있음")
	}
	for _, secret := range []string{"canary", "user:", sfx, "198.51.100"} {
		if strings.Contains(body, secret) {
			t.Fatalf("원문 값 %q가 응답에 남음", secret)
		}
	}

	prov := map[string]KGProvenance{}
	for _, p := range g.Provenance {
		prov[p.ID] = p
	}
	l1 := prov[fmt.Sprintf("link:%d:%d", cur.ID, a1)]
	if *l1.Source != "manual" || l1.Observation == nil || l1.Observation.NodeID != fmt.Sprint(f1) || l1.Observation.Authority != "model_claim" || l1.Observation.ClaimedConfidence != "observed" {
		t.Fatalf("수동 연결·관측 참조: %+v %+v", l1, l1.Observation)
	}
	if p, ok := prov[fmt.Sprintf("array:%d:%d", cur.ID, a2)]; !ok || *p.Source != "unknown" {
		t.Fatal("출처 행 없는 배열 연결")
	}
	if _, ok := prov[fmt.Sprintf("scope:%d:%d", rootScope, a6)]; !ok {
		t.Fatal("루트 도메인 범위 일치")
	}
	l9 := prov[fmt.Sprintf("link:%d:%d", src.ID, a9)]
	if !l9.Inherited || l9.Observation != nil {
		t.Fatalf("원천 연결: %+v", l9)
	}
	if p := prov[fmt.Sprintf("anchor:%d:%d", done, a11)]; !p.Inherited || p.Observation == nil || p.Observation.SourceState != "done" {
		t.Fatalf("원천의 끝난 intent 앵커: %+v", p)
	}
	var issues []string
	for _, x := range g.Issues {
		issues = append(issues, x.Code+"|"+strings.Join(x.Refs, ","))
	}
	wantIssues := []string{
		fmt.Sprintf("legacy_link_missing|array:%d:%d", cur.ID, a2),
		fmt.Sprintf("membership_mismatch|a:%d,t:%d", a3, cur.ID),
		fmt.Sprintf("scope_kind_not_projected|t:%d", cur.ID),
		fmt.Sprintf("source_reference_unverified|link:%d:%d", src.ID, a9),
	}
	if !slices.Equal(issues, wantIssues) {
		t.Fatalf("issues=%v want %v", issues, wantIssues)
	}
	var contained []string
	for _, e := range g.Edges {
		if e.Kind == "contained_by" {
			contained = append(contained, e.ID)
		}
	}
	if !slices.Equal(contained, []string{fmt.Sprintf("contained_by:a:%d:a:%d", a6, a5)}) {
		t.Fatalf("contained_by=%v", contained)
	}

	// 숨은 앵커(원천의 진행 중 intent·현재의 삭제 intent·문맥 밖 탐색)는 노드 상한 계산에도 들어가지 않는다.
	// 노드 상한을 응답 노드 수와 같게 두면 성공, 하나 줄이면 413 — 앵커 SQL 필터를 빼면 같을 때도 413이 된다.
	query := func() error {
		tx, err := d.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		_, err = QueryKGTx(ctx, tx, []KGContextTask{{cur.ID, cur.ExplorationID}, {src.ID, src.ExplorationID}})
		return err
	}
	saved := KGLimits
	KGLimits.Nodes = len(g.Nodes)
	if err := query(); err != nil {
		KGLimits = saved
		t.Fatalf("노드 상한 경계(숨은 앵커 제외): %v", err)
	}
	KGLimits.Nodes = len(g.Nodes) - 1
	err = query()
	KGLimits = saved
	if err != ErrKGTooLarge {
		t.Fatalf("노드 상한-1: %v", err)
	}

	// Q13: 출처 상한+1 행은 자르지 않고 실패한다(범위 출처 상한+1개).
	exec(`INSERT INTO assets(type, ip, url) SELECT 'endpoint', '203.0.113.' || (g % 250 + 1), 'http://203.0.113.1/p' || g FROM generate_series(1, $1) g`, KGLimits.Provenance+1)
	t.Cleanup(func() {
		if _, err := d.Exec(`DELETE FROM assets WHERE type='endpoint' AND ip LIKE '203.0.113.%'`); err != nil {
			t.Error(err)
		}
	})
	big := newTask()
	exec(`INSERT INTO task_scope(task_id, kind, net, source) VALUES ($1,'cidr','203.0.113.0/24','manual')`, big.ID)
	tx, err = d.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := QueryKGTx(ctx, tx, []KGContextTask{{big.ID, big.ExplorationID}}); err != ErrKGTooLarge {
		t.Fatalf("상한+1: %v", err)
	}
}
