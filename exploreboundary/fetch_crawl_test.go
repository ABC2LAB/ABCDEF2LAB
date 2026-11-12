package exploreboundary

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
)

type crawlMemoryStore struct {
	mu      sync.Mutex
	records map[string]diagnosisflow.Record
}

func (m *crawlMemoryStore) Create(_ context.Context, r diagnosisflow.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[r.Snapshot.JobID] = r
	return nil
}
func (m *crawlMemoryStore) Save(_ context.Context, _ uint64, r diagnosisflow.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[r.Snapshot.JobID] = r
	return nil
}
func (m *crawlMemoryStore) Read(_ context.Context, _, id string) (diagnosisflow.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.records[id], nil
}

// 서버 정적 크롤은 경계의 직접 입구로 나간다. 경계 관찰의 출처는 직접 입구라 API Recon 실행으로 붙지만, 서버가 자기 fetch의
// 순번으로 읽은 관찰은 발견한 끝점의 출처·종류를 유지해야 실제로 받은 시작 페이지가 사전 승인 읽기·자동 선택에서 빠지지
// 않는다(#503 리뷰).
func TestServerCrawlThroughDirectIngressKeepsDiscoverySource(t *testing.T) {
	f := newIngressFixture(t, DirectIngress, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/app" {
			io.WriteString(w, `<a href="/app/item">item</a>`)
			return
		}
		io.WriteString(w, "item")
	}, nil)
	flowBinding := f.session.flow
	c, err := diagnosisflow.New(t.Context(), diagnosisflow.Binding{TargetRef: flowBinding.TargetRef, TargetRevision: flowBinding.TargetRevision, JobID: flowBinding.JobID, TaskID: flowBinding.TaskID,
		RunRef: flowBinding.RunRef, SourceDigest: flowBinding.SourceDigest, Owner: "ARTEX", Mode: diagnosisflow.Auto, TargetURL: f.target + "/app", PathPrefixes: []string{"/app"},
		ReadonlyPrefixes: []string{"/app"}, Methods: []string{"GET", "HEAD"}, Limits: diagnosisflow.Limits{Format: diagnosisflow.Version, MaxPages: 4, MaxEndpoints: 32, MaxResponseBytes: 4096},
		Current: func() error { return nil }}, &crawlMemoryStore{records: map[string]diagnosisflow.Record{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.BindExploration(t.Context(), f.session.Reader()); err != nil {
		t.Fatal(err)
	}
	handled, err := c.CrawlExploration(t.Context(), flowBinding, fixtureFetch(f).Fetch)
	if !handled || err != nil || f.hits.Load() != 2 {
		t.Fatalf("crawl through direct ingress: handled=%v err=%v hits=%d", handled, err, f.hits.Load())
	}
	if err := c.FinishExploration(t.Context(), flowBinding, "completed"); err != nil {
		t.Fatal(err)
	}
	s, _ := c.Snapshot()
	seen := 0
	for _, e := range s.Endpoints {
		if e.Path != "/app" && e.Path != "/app/item" {
			continue
		}
		seen++
		if e.Source == diagnosisflow.SourceAPIReconRuntime || e.Change != "attested_read" || !e.Visited || !e.Selected || e.DiscoveryConfidence != "observed" {
			t.Errorf("%s fetched by the server lost its read evidence: source=%s change=%s visited=%v selected=%v confidence=%s", e.Path, e.Source, e.Change, e.Visited, e.Selected, e.DiscoveryConfidence)
		}
	}
	if seen != 2 {
		t.Fatalf("endpoints: %+v", s.Endpoints)
	}
}
