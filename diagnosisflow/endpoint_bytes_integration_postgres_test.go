package diagnosisflow

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2runner"
)

// 끝점 바이트 절단 프로세스 안 통합 시험(사용자 결정 2026-11-17: 공식 Juice Shop 회차에서 처음 확인하지 않는다).
// 합성 발견 약 300개를 제품이 쓰는 실제 입구 순서로 실제 컨트롤러에 넣는다.
//   - exploration: 탐색 프로필(server/exploration_pipeline.go) 순서. BindExploration → CrawlExploration(서버 정적 fetch,
//     HTML 링크) → 발견 보고(ReportStatic, 추정 출처) → 경계 관찰(ReportObserved, 브라우저 XHR) → FinishExploration.
//   - crawl: 탐색 없는 진단(agent/e2_http.go의 Workflow.Crawl). 발견 보고(추정 출처) → Crawl(HTML 링크).
//
// 받기(공개 목록 바이트 절단) → 공개 투영 → 자동 선택 → 모델 목록(SelectedData·Instructions) → PostgreSQL 저장 기록을
// 거치고, 다른 연결로 다시 읽어 확인한다.
//
//	(1) counts.omitted·counts.model_omitted가 저장 기록에 남고 실제로 빠진 수와 같다.
//	(2) 모델 목록이 #610 우선순위를 따른다: 관찰 출처를 앞에, 추정 출처를 뒤에 두고, 빠지는 것은 뒤쪽(추정)부터.
//	(3) 모델 입력(SelectedData)은 MaxSelectedDataBytes, 공개 투영은 MaxProjectionBytes, 저장 기록은 128KiB 안이다.
//
// counts.omitted·model_omitted는 진단 흐름 저장 기록(diagnosis_workflows)에 있다. 실행 결과 기록(first_diagnosis_run_v1)
// 에는 이 칸이 없다.
func TestEndpointByteCutThroughControllerProjectionModelListAndStore(t *testing.T) {
	dsn := os.Getenv("ARTEX_FIRST_DIAGNOSIS_ROLE_TEST_DSN")
	if dsn == "" || os.Getenv("ARTEX_FIRST_DIAGNOSIS_ROLE_TEST_OWNED") != "1" {
		t.Skip("owned PostgreSQL 16 required")
	}
	pool, err := db.Open(dsn)
	if err != nil {
		t.Fatal("owned database open failed")
	}
	defer pool.Close()
	if err := Migrate(t.Context(), pool.DB); err != nil {
		t.Fatal("workflow migration failed")
	}
	// 경로를 길게 해 공개 목록(48KiB)에 관찰·추정 출처가 함께 남고, 모델 목록(16KiB)만 넘치게 한다.
	pad := strings.Repeat("x", 50)
	linkPath := func(i int) string {
		return fmt.Sprintf("/read/catalog/sections/observed-section-entry-%s-%03d", pad, i)
	}
	inferred := func(n int) [][]Discovery {
		var batches [][]Discovery
		for start := 0; start < n; start += maxDiscoveriesPerReport {
			var batch []Discovery
			for i := start; i < min(start+maxDiscoveriesPerReport, n); i++ {
				batch = append(batch, Discovery{Method: "GET", Path: fmt.Sprintf("/read/api/v2/inventory-reports/inferred-collection-%s-%03d", pad, i), QueryKeys: []string{"page_size", "sort_order", "include_archived"}})
			}
			batches = append(batches, batch)
		}
		return batches
	}
	page := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, `<a href="%s">x</a>`, linkPath(i))
		}
		return b.String()
	}
	report := func(t *testing.T, c *Controller, n int) {
		for _, batch := range inferred(n) {
			if _, err := c.ReportStatic(t.Context(), batch); err != nil {
				t.Fatal("discovery report failed", err)
			}
		}
	}
	for _, tc := range []struct {
		name string
		// 받으려 한 서로 다른 발견 수(시작 페이지 끝점 포함)를 돌려준다.
		feed func(*testing.T, *Controller) int
	}{
		{"exploration", func(t *testing.T, c *Controller) int {
			const linksN, inferredN, xhrN = 50, 40, 210
			b := ExplorationBinding{SessionRef: "session-bytecut", RunRef: c.binding.RunRef, JobID: c.binding.JobID, TaskID: c.binding.TaskID, TargetRef: c.binding.TargetRef, TargetRevision: c.binding.TargetRevision, SourceDigest: c.binding.SourceDigest}
			reader := &privateDiscoveryReader{binding: b, rows: map[uint64]ExplorationObservation{}}
			if pinned, err := c.BindExploration(t.Context(), reader); err != nil || pinned != b {
				t.Fatal("exploration bind failed", err)
			}
			body := page(linksN)
			start := observationFor(b, 1, "/read")
			start.ResponseContentType, start.ResponseSHA256 = "text/html", sha256.Sum256([]byte(body))
			reader.rows[1] = start
			if _, err := c.CrawlExploration(t.Context(), b, func(_ context.Context, target string) (ExplorationFetchResult, error) {
				if target != "https://owned.test/read" {
					t.Fatalf("unexpected exploration fetch %q", target)
				}
				return ExplorationFetchResult{Seq: 1, Body: []byte(body), Complete: true}, nil
			}); err != nil {
				t.Fatal("exploration crawl failed", err)
			}
			report(t, c, inferredN)
			for i := 0; i < xhrN; i++ {
				seq := uint64(2 + i)
				reader.rows[seq] = observationFor(b, seq, fmt.Sprintf("/read/api/v2/live-browser-traffic/observed-request-%s-%03d", pad, i))
				if _, err := c.ReportObserved(t.Context(), b, seq); err != nil {
					t.Fatal("observed exploration report failed", err)
				}
			}
			if err := c.FinishExploration(t.Context(), b, "completed"); err != nil {
				t.Fatal("exploration finish failed", err)
			}
			return 1 + linksN + inferredN + xhrN
		}},
		{"crawl", func(t *testing.T, c *Controller) int {
			const inferredN, linksN = 40, 260
			report(t, c, inferredN)
			body := page(linksN)
			if _, err := c.Crawl(t.Context(), func(context.Context, string) (e2runner.Result, error) {
				return crawlResult(body), nil
			}); err != nil {
				t.Fatal("crawl failed", err)
			}
			return 1 + inferredN + linksN
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, err := pool.CreateTask("Owned endpoint byte cut", "Synthetic discoveries only", nil, 0, 0)
			if err != nil {
				t.Fatal("owned task create failed")
			}
			template, _ := flowFixture(t, Auto)
			binding := template.binding
			binding.TaskID = strconv.FormatInt(row.ID, 10)
			binding.JobID = "owned-bytecut-" + tc.name + "-" + binding.TaskID
			binding.TargetRef, binding.TargetRevision = "target-bytecut", 7
			binding.Methods = []string{"GET", "HEAD"}
			binding.ReversibleWrites = false
			binding.Limits.MaxEndpoints = MaxWorkflowEndpoints
			binding.Limits.MaxPages = 1
			binding.Limits.AllowReversibleWrites = false
			binding.Limits.MaxResponseBytes = 1 << 20
			c, err := New(t.Context(), binding, Postgres{DB: pool.DB})
			if err != nil {
				t.Fatal("workflow create failed", err)
			}
			discovered := tc.feed(t, c)
			checkByteCut(t, dsn, c, binding, discovered)
		})
	}
}

func checkByteCut(t *testing.T, dsn string, c *Controller, binding Binding, discovered int) {
	t.Helper()
	reopened, err := db.Open(dsn)
	if err != nil {
		t.Fatal("owned database reopen failed")
	}
	defer reopened.Close()
	saved, err := (Postgres{DB: reopened.DB}).Read(t.Context(), binding.Owner, binding.JobID)
	if err != nil {
		t.Fatal("saved workflow read failed", err)
	}
	s := saved.Snapshot
	if s.Stage != "diagnosing" {
		t.Fatalf("stage %q", s.Stage)
	}
	// (1) 잘린 수 = 받으려 한 서로 다른 발견 − 공개 목록.
	if s.Counts.Omitted == 0 || int(s.Counts.Omitted) != discovered-len(s.Endpoints) {
		t.Fatalf("counts.omitted=%d, discovered=%d, public=%d", s.Counts.Omitted, discovered, len(s.Endpoints))
	}
	var observedSel, inferredSel []string
	for _, e := range s.Endpoints {
		if !e.Selected {
			continue
		}
		if inferredSource(e.Source) {
			inferredSel = append(inferredSel, e.ID)
		} else {
			observedSel = append(observedSel, e.ID)
		}
	}
	var rows []map[string]any
	data := c.SelectedData()
	if err := json.Unmarshal([]byte(data), &rows); err != nil {
		t.Fatal("model endpoint list is not JSON", err)
	}
	selected := len(observedSel) + len(inferredSel)
	if s.Counts.ModelOmitted == 0 || int(s.Counts.ModelOmitted) != selected-len(rows) {
		t.Fatalf("counts.model_omitted=%d, selected=%d, model rows=%d", s.Counts.ModelOmitted, selected, len(rows))
	}
	if !strings.Contains(c.Instructions(), strconv.FormatUint(s.Counts.ModelOmitted, 10)+" more selected endpoints were left out") {
		t.Fatal("model instructions do not report the omitted endpoints")
	}
	// (2) 모델 목록은 (관찰 출처 ++ 추정 출처)의 앞부분이다. 저장 순서와 관계없이 관찰 출처가 앞선다.
	if len(observedSel) == 0 || len(inferredSel) == 0 || len(rows) <= len(observedSel) {
		t.Fatalf("fixture must keep every observed row and drop only inferred ones: observed=%d inferred=%d rows=%d", len(observedSel), len(inferredSel), len(rows))
	}
	want := append(append([]string{}, observedSel...), inferredSel...)
	for i, r := range rows {
		if r["endpoint_ref"] != want[i] {
			t.Fatalf("model row %d is %v, want %s (#610 order)", i, r["endpoint_ref"], want[i])
		}
	}
	// 저장 순서에서 추정 출처 뒤에 관찰 출처가 와야 순서 검사가 의미 있다(모델 목록이 다시 줄 세움).
	reordered, seenInferred := false, false
	for _, e := range s.Endpoints {
		if e.Selected && inferredSource(e.Source) {
			seenInferred = true
		} else if e.Selected && seenInferred {
			reordered = true
		}
	}
	if !reordered {
		t.Fatal("fixture must store an observed endpoint after an inferred one")
	}
	// (3) 바이트 상한.
	if len(data) > MaxSelectedDataBytes {
		t.Fatalf("model endpoint list %d bytes > %d", len(data), MaxSelectedDataBytes)
	}
	live, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for name, snap := range map[string]Snapshot{"live": live, "saved": Project(s)} {
		raw, _ := json.Marshal(snap)
		if len(raw) > MaxProjectionBytes {
			t.Fatalf("%s public projection %d bytes > %d", name, len(raw), MaxProjectionBytes)
		}
	}
	record, _ := json.Marshal(saved)
	if len(record) > 128<<10 {
		t.Fatalf("stored record %d bytes > 128KiB", len(record))
	}
	t.Logf("discovered=%d public=%d omitted=%d selected=%d(observed %d, inferred %d) model_rows=%d model_omitted=%d model_bytes=%d record_bytes=%d",
		discovered, len(s.Endpoints), s.Counts.Omitted, selected, len(observedSel), len(inferredSel), len(rows), s.Counts.ModelOmitted, len(data), len(record))
}
