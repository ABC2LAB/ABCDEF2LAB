package diagnosisflow

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2runner"
)

type privateDiscoveryReader struct {
	binding ExplorationBinding
	rows    map[uint64]ExplorationObservation
	closed  bool
	reads   int
}

func (r *privateDiscoveryReader) Session(context.Context) (ExplorationBinding, error) {
	if r.closed {
		return ExplorationBinding{}, ErrExplorationClosed
	}
	return r.binding, nil
}
func (r *privateDiscoveryReader) ReadObservation(_ context.Context, b ExplorationBinding, seq uint64) (ExplorationObservation, error) {
	r.reads++
	if b != r.binding {
		return ExplorationObservation{}, ErrExplorationBinding
	}
	v, ok := r.rows[seq]
	if !ok {
		return v, ErrUnavailable
	}
	return v, nil
}
func explorationFixture(t *testing.T, mode string) (*Controller, *memoryStore, *privateDiscoveryReader, ExplorationBinding) {
	t.Helper()
	c, store := flowFixture(t, mode)
	c.binding.TargetRef = "target-owned"
	c.binding.TargetRevision = 7
	b := ExplorationBinding{SessionRef: "session-owned", RunRef: c.binding.RunRef, JobID: c.binding.JobID, TaskID: c.binding.TaskID, TargetRef: c.binding.TargetRef, TargetRevision: 7, SourceDigest: c.binding.SourceDigest}
	r := &privateDiscoveryReader{binding: b, rows: map[uint64]ExplorationObservation{}}
	if pinned, err := c.BindExploration(t.Context(), r); err != nil || pinned != b {
		t.Fatal("host pin failed", err)
	}
	return c, store, r, b
}
func observationFor(b ExplorationBinding, seq uint64, path string) ExplorationObservation {
	return ExplorationObservation{Binding: b, Seq: seq, At: time.Now().UTC(), Method: "GET", Path: path, Source: SourceBrowserXHR, Kind: "api", Transport: "http", Status: 200, Forwarded: true, ResponseReceived: true}
}
func TestObservedDiscoveryRequiresPinnedHostBinding(t *testing.T) {
	c, _, r, b := explorationFixture(t, Manual)
	for _, change := range []func(*ExplorationBinding){func(v *ExplorationBinding) { v.SessionRef = "other" }, func(v *ExplorationBinding) { v.RunRef = "other" }, func(v *ExplorationBinding) { v.JobID = "other" }, func(v *ExplorationBinding) { v.TaskID = "2" }, func(v *ExplorationBinding) { v.TargetRef = "other" }, func(v *ExplorationBinding) { v.TargetRevision++ }, func(v *ExplorationBinding) { v.SourceDigest = strings.Repeat("b", 64) }} {
		bad := b
		change(&bad)
		before := r.reads
		if _, err := c.ReportObserved(t.Context(), bad, 1); !errors.Is(err, ErrExplorationBinding) || r.reads != before {
			t.Fatal("caller binding was trusted", err)
		}
	}
	foreign := observationFor(b, 1, "/read/actual")
	foreign.Binding.TargetRevision++
	r.rows[1] = foreign
	if _, err := c.ReportObserved(t.Context(), b, 1); !errors.Is(err, ErrExplorationObservation) {
		t.Fatal("foreign reader row accepted", err)
	}
	r.rows[1] = observationFor(b, 1, "/read/actual")
	c.binding.Current = func() error { return errors.New("revoked") }
	if _, err := c.ReportObserved(t.Context(), b, 1); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("revoked authority accepted", err)
	}
	c.binding.Current = func() error { return nil }
	r.binding.TargetRevision++
	if _, err := c.ReportObserved(t.Context(), b, 1); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("changed host session accepted", err)
	}
}
func TestObservedMetadataDoesNotTrustStaticNamesOrReplay(t *testing.T) {
	c, store, r, b := explorationFixture(t, Manual)
	if _, err := c.ReportStatic(t.Context(), []Discovery{{Method: "GET", Path: "/read/api", QueryKeys: []string{"inferred_key"}}}); err != nil {
		t.Fatal(err)
	}
	row := observationFor(b, 1, "/read/api")
	row.QueryKeys = []string{"observed_key"}
	row.ContentType = "application/json"
	r.rows[1] = row
	if n, err := c.ReportObserved(t.Context(), b, 1); err != nil || n != 1 {
		t.Fatal("actual observation refused", err)
	}
	if _, err := c.ReportStatic(t.Context(), []Discovery{{Method: "GET", Path: "/read/api", QueryKeys: []string{"pretend_runtime_key"}}}); err != nil {
		t.Fatal(err)
	}
	e := discoveredEndpoint(t, c, "GET", "/read/api")
	if e.DiscoveryConfidence != "observed" || e.Source != SourceBrowserXHR || !e.Visited || strings.Join(e.QueryKeys, ",") != "observed_key" || e.ExplorationSeq != 1 {
		t.Fatal("static data polluted observed metadata", e)
	}
	facts := c.DiscoveryInventory()
	if len(facts) != 2 || facts[0].Confidence != "inferred" || facts[1].Confidence != "observed" {
		t.Fatal("provenance not retained")
	}
	if !strings.Contains(strings.Join(facts[0].QueryKeys, ","), "pretend_runtime_key") {
		t.Fatal("static supplement lost")
	}
	saved := store.records["prep-owned"].DiscoveryInventory
	if len(saved) != 2 {
		t.Fatal("inventory not persisted")
	}
	if n, err := c.ReportObserved(t.Context(), b, 1); err != nil || n != 0 {
		t.Fatal("same record replay changed state", err)
	}
	row.BodyKeys = []string{"changed"}
	r.rows[1] = row
	if _, err := c.ReportObserved(t.Context(), b, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("changed sequence replay accepted", err)
	}
	if len(c.DiscoveryInventory()) != 2 {
		t.Fatal("replay wrote extra fact")
	}
}
func TestObservedDiscoveryChecksScopeAndRequestShapes(t *testing.T) {
	for name, change := range map[string]func(*ExplorationObservation){"outside": func(o *ExplorationObservation) { o.Path = "/other" }, "query value": func(o *ExplorationObservation) { o.Path = "/read?q=secret" }, "foreign origin": func(o *ExplorationObservation) { o.Path = "https://foreign.test/read" }, "method": func(o *ExplorationObservation) { o.Method = "DELETE" }, "denied": func(o *ExplorationObservation) { o.Forwarded = false }, "missing time": func(o *ExplorationObservation) { o.At = time.Time{} }, "bad source": func(o *ExplorationObservation) { o.Source = "model" }, "body value": func(o *ExplorationObservation) { o.BodyKeys = []string{"q=secret"} }, "header value": func(o *ExplorationObservation) { o.ContentType = "application/json; token=secret" }, "raw websocket": func(o *ExplorationObservation) { o.Transport = "raw_frames" }} {
		t.Run(name, func(t *testing.T) {
			c, _, r, b := explorationFixture(t, Manual)
			o := observationFor(b, 1, "/read/api")
			change(&o)
			r.rows[1] = o
			if _, err := c.ReportObserved(t.Context(), b, 1); !errors.Is(err, ErrExplorationObservation) {
				t.Fatal("invalid observation accepted", err)
			}
			if len(c.DiscoveryInventory()) != 0 {
				t.Fatal("bad metadata persisted")
			}
		})
	}
}
func TestCrawlExplorationSeparatesE2AndKeepsPartialDiscoveries(t *testing.T) {
	c, _, reader, b := explorationFixture(t, Manual)
	calls := 0
	handled, err := c.CrawlExploration(t.Context(), b, func(_ context.Context, target string) (ExplorationFetchResult, error) {
		calls++
		path := strings.TrimPrefix(target, c.origin)
		body := []byte(`<script src="/read/app.js"></script><form action="/read/search"><input name="query" value="PRIVATE_VALUE"></form>`)
		kind := "page"
		ct := "text/html"
		if strings.HasSuffix(path, ".js") {
			kind = "script"
			ct = "application/javascript"
			body = []byte(`fetch("/read/api/runtime");const route={path:"/read/dashboard"}; const api="/graphql";`)
		}
		o := observationFor(b, uint64(calls), path)
		o.Kind = kind
		o.Source = SourceBrowserNavigation
		o.ResponseContentType = ct
		o.ResponseSHA256 = sha256.Sum256(body)
		reader.rows[o.Seq] = o
		return ExplorationFetchResult{Seq: o.Seq, Body: body, Complete: true}, nil
	})
	if !handled || err != nil || calls != 2 {
		t.Fatal("exploration crawl failed", calls, err)
	}
	if err = c.FinishExploration(t.Context(), b, "cap_exhausted"); err != nil {
		t.Fatal(err)
	}
	s, _ := c.Snapshot()
	if s.Stage != "awaiting_selection" || !s.InitialCompleted || !s.ExplorationPartial || s.Counts.ExplorationRequests != 2 || s.Counts.Sent != 0 || s.Counts.Attempted != 0 {
		t.Fatal("exploration mixed with diagnosis", s.Counts)
	}
	search := discoveredEndpoint(t, c, "GET", "/read/search")
	if search == nil || strings.Join(search.QueryKeys, ",") != "query" || search.Change != "possible" {
		t.Fatal("form metadata not restored")
	}
	if discoveredEndpoint(t, c, "GET", "/read/dashboard") == nil {
		t.Fatal("frontend route not restored")
	}
	if _, err = c.ReportObserved(t.Context(), b, 3); !errors.Is(err, ErrExplorationClosed) {
		t.Fatal("closed session accepted", err)
	}
	raw, _ := json.Marshal(c.DiscoveryInventory())
	if strings.Contains(string(raw), "PRIVATE_VALUE") {
		t.Fatal("form value persisted")
	}
}
func TestExplorationBodyMustMatchActualRecord(t *testing.T) {
	c, _, r, b := explorationFixture(t, Manual)
	o := observationFor(b, 1, "/read")
	o.ResponseContentType = "text/html"
	o.ResponseSHA256 = sha256.Sum256([]byte("actual"))
	r.rows[1] = o
	if _, err := c.CrawlExploration(t.Context(), b, func(context.Context, string) (ExplorationFetchResult, error) {
		return ExplorationFetchResult{Seq: 1, Body: []byte(`<a href="/read/forged">fake</a>`), Complete: true}, nil
	}); !errors.Is(err, ErrExplorationObservation) {
		t.Fatal("unbound body parsed", err)
	}
	if discoveredEndpoint(t, c, "GET", "/read/forged") != nil {
		t.Fatal("forged body added endpoint")
	}
}
func TestDiscoveryInventoryRetainsProjectionOverflow(t *testing.T) {
	c, store := flowFixture(t, Manual)
	for batch := 0; batch < 3; batch++ {
		rows := []Discovery{}
		for i := 0; i < 30; i++ {
			rows = append(rows, Discovery{Method: "GET", Path: fmt.Sprintf("/read/api/%d", batch*30+i), QueryKeys: []string{"page"}})
		}
		if _, err := c.ReportStatic(t.Context(), rows); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := c.Snapshot()
	facts := c.DiscoveryInventory()
	if len(s.Endpoints) != 32 || len(facts) != 90 || s.Counts.Omitted != 59 || s.Counts.InventoryStored != 90 || s.Counts.InventoryOmitted != 0 {
		t.Fatal("discovery loss not retained/measured", len(s.Endpoints), len(facts), s.Counts)
	}
	if len(store.records["prep-owned"].DiscoveryInventory) != 90 {
		t.Fatal("overflow only retained in memory")
	}
	t.Logf("measurement input=90 public_api=31 protected_inventory=90 public_omitted=%d protected_omitted=%d", s.Counts.Omitted, s.Counts.InventoryOmitted)
}
func TestPrivateDiscoveryShapeSurvivesPublicCompaction(t *testing.T) {
	c, store := flowFixture(t, Manual)
	rows := []Discovery{}
	for i := 0; i < 13; i++ {
		keys := []string{}
		for k := 0; k < 32; k++ {
			keys = append(keys, fmt.Sprintf("key%02d_%s", k, strings.Repeat("x", 54)))
		}
		rows = append(rows, Discovery{Method: "POST", Path: fmt.Sprintf("/read/api/%d", i), QueryKeys: keys, BodyKeys: keys})
	}
	if _, err := c.ReportStatic(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	s, _ := c.Snapshot()
	if !s.DetailTruncated {
		t.Fatal("fixture did not reach public projection bound")
	}
	facts := store.records["prep-owned"].DiscoveryInventory
	if len(facts) != 13 || len(facts[0].QueryKeys) != 32 || len(facts[12].BodyKeys) != 32 {
		t.Fatal("private shape discarded by public compaction")
	}
	raw, _ := json.Marshal(store.records["prep-owned"])
	if len(raw) > 128<<10 {
		t.Fatal("private inventory exceeds persistent record budget")
	}
	publicKeys, privateKeys := 0, 0
	for _, e := range s.Endpoints {
		publicKeys += len(e.QueryKeys) + len(e.BodyKeys)
	}
	for _, e := range facts {
		privateKeys += len(e.QueryKeys) + len(e.BodyKeys)
	}
	projection, _ := json.Marshal(s)
	t.Logf("measurement input_keys=832 public_keys=%d protected_keys=%d public_bytes=%d record_bytes=%d", publicKeys, privateKeys, len(projection), len(raw))
}

func TestRuntimeModelReportsStayInferredAndKeepTransportOrigins(t *testing.T) {
	c, _ := flowFixture(t, Auto)
	for _, transport := range []string{"http", "sse", "websocket"} {
		if _, err := c.ReportStatic(t.Context(), []Discovery{{Method: "GET", Path: "/read/runtime", Source: SourceAPIReconRuntime, Transport: transport, QueryKeys: []string{"topic"}}}); err != nil {
			t.Fatal(err)
		}
	}
	e := discoveredEndpoint(t, c, "GET", "/read/runtime")
	if e.Source != SourceAPIReconRuntime || e.DiscoveryConfidence != "inferred" || e.Visited || e.Change != "possible" || e.ExplorationSeq != 0 {
		t.Fatal("runtime text was promoted", e)
	}
	facts := c.DiscoveryInventory()
	if len(facts) != 3 {
		t.Fatal("runtime transport provenance lost")
	}
	for _, fact := range facts {
		if !fact.InScope || fact.Confidence != "inferred" || fact.Seq != 0 {
			t.Fatal("runtime text gained boundary authority")
		}
	}
	if n, err := c.ReportStatic(t.Context(), []Discovery{{Method: "GET", Path: "/read/forged", Source: SourceBrowserXHR}, {Method: "GET", Path: "/read/forged2", Transport: "frames"}}); err != nil || n != 0 {
		t.Fatal("forged origin accepted", err)
	}
}

func TestStaticReconKeepsFieldCandidatesAndUnknownMethodsSeparate(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	body := []byte(`const endpoint="/service/catalog?search=QUERY_SECRET"; const route={path:"/read/dashboard"};axios.post("/read/search?q=QUERY_SECRET",{filter:"BODY_SECRET",page:3});fetch("/read/filter",{method:"POST",body:JSON.stringify({status:"BODY_SECRET",limit:5})});axios.post("/read/uncertain",{...data,pretend:"BODY_SECRET"});`)
	if err := c.CollectStaticDocument(t.Context(), "/read", "application/javascript", body); err != nil {
		t.Fatal(err)
	}
	post := discoveredEndpoint(t, c, "POST", "/read/search")
	if post == nil || strings.Join(post.BodyKeys, ",") != "filter,page" || post.DiscoveryConfidence != "inferred" || post.Visited {
		t.Fatal("static field candidates missing")
	}
	filter := discoveredEndpoint(t, c, "POST", "/read/filter")
	if filter == nil || strings.Join(filter.BodyKeys, ",") != "limit,status" {
		t.Fatal("static fetch field candidates missing")
	}
	unknown := discoveredEndpoint(t, c, "UNKNOWN", "/service/catalog")
	if unknown == nil || unknown.InScope || unknown.Visited {
		t.Fatal("unknown method candidate gained scope")
	}
	for _, fact := range c.DiscoveryInventory() {
		if fact.Path == "/read/uncertain" && len(fact.BodyKeys) > 0 {
			t.Fatal("spread parameters guessed")
		}
	}
	raw, _ := json.Marshal(c.DiscoveryInventory())
	if strings.Contains(string(raw), "SECRET") {
		t.Fatal("static values persisted")
	}
}

func TestObservedGETDoesNotEraseEarlierFormSideEffectSignal(t *testing.T) {
	c, _, r, b := explorationFixture(t, Auto)
	if err := c.CollectStaticDocument(t.Context(), "/read", "text/html", []byte(`<form action="/read/search"><input name="q"></form>`)); err != nil {
		t.Fatal(err)
	}
	o := observationFor(b, 1, "/read/search")
	r.rows[1] = o
	if _, err := c.ReportObserved(t.Context(), b, 1); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishExploration(t.Context(), b, "completed"); err != nil {
		t.Fatal(err)
	}
	e := discoveredEndpoint(t, c, "GET", "/read/search")
	if e.Kind != "form" || e.Change != "possible" || e.Selected {
		t.Fatal("observed GET erased side-effect signal")
	}
}

func TestDiscoveryInventoryCapsRemainExplicitWithoutGrantingMoreSelection(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	for batch := 0; batch < 10; batch++ {
		rows := []Discovery{}
		for i := 0; i < 30; i++ {
			rows = append(rows, Discovery{Method: "GET", Path: fmt.Sprintf("/read/bounded/%d", batch*30+i)})
		}
		if _, err := c.ReportStatic(t.Context(), rows); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := c.Snapshot()
	if len(c.DiscoveryInventory()) != 256 || s.Counts.InventoryOmitted != 44 || len(s.Endpoints) != 32 {
		t.Fatal("protected inventory is not bounded independently")
	}
	if s.Counts.Sent != 0 || s.Counts.ExplorationRequests != 0 {
		t.Fatal("discovery storage created request authority")
	}
}

func TestExplorationRequestFailureKeepsOtherPagesAndPartialResult(t *testing.T) {
	c, _, r, b := explorationFixture(t, Manual)
	// 이미 검증한 실제 응답에서 나온 별도 방문 후보를 준비한다. 임의 정적 문서는 추정 후보뿐이다.
	c.mu.Lock()
	c.discoverBodyLocked([]byte(`<a href="/read/next">next</a>`), "text/html", "/read")
	c.mu.Unlock()
	calls := 0
	_, err := c.CrawlExploration(t.Context(), b, func(_ context.Context, target string) (ExplorationFetchResult, error) {
		calls++
		if calls == 1 {
			return ExplorationFetchResult{}, errors.New("target connection failed")
		}
		body := []byte("<p>ok</p>")
		o := observationFor(b, 2, strings.TrimPrefix(target, c.origin))
		o.ResponseContentType, o.ResponseSHA256 = "text/html", sha256.Sum256(body)
		r.rows[2] = o
		return ExplorationFetchResult{Seq: 2, Body: body, Complete: true}, nil
	})
	if err != nil || calls != 2 {
		t.Fatal("one request stopped exploration", err)
	}
	if err := c.FinishExploration(t.Context(), b, "completed"); err != nil {
		t.Fatal(err)
	}
	s, _ := c.Snapshot()
	if !s.ExplorationPartial || s.ExplorationReason != "completed" || s.Counts.ExplorationRequests != 1 || discoveredEndpoint(t, c, "GET", "/read").ObservationError != "request_failed" || !discoveredEndpoint(t, c, "GET", "/read/next").Visited {
		t.Fatal("partial observations lost")
	}
}

func TestStaticReconRespectsForeignDocumentBase(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	if err := c.CollectStaticDocument(t.Context(), "/read", "text/html", []byte(`<base href="https://other.test/"><script>const x="/service/foreign"; const r={path:"/read/foreign"}</script>`)); err != nil {
		t.Fatal(err)
	}
	for _, fact := range c.DiscoveryInventory() {
		if fact.Path == "/service/foreign" || fact.Path == "/read/foreign" {
			t.Fatal("foreign document base became owned metadata")
		}
	}
}

func TestStaticFormShapeSurvivesEndpointSelectionLimit(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	var body strings.Builder
	for i := 0; i < 36; i++ {
		fmt.Fprintf(&body, `<form method="POST" enctype="multipart/form-data" action="/read/forms/%d"><input name="upload" value="PRIVATE_VALUE"></form>`, i)
	}
	// 문서 바이트 상한은 별도 승인 구성이고 이 fixture는 이름 보존만 검사한다.
	c.binding.Limits.MaxResponseBytes = 16 << 10
	if err := c.CollectStaticDocument(t.Context(), "/read", "text/html", []byte(body.String())); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, fact := range c.DiscoveryInventory() {
		if fact.Kind == "form" && strings.Join(fact.BodyKeys, ",") == "upload" && fact.ContentType == "multipart/form-data" {
			found++
		}
	}
	if found != 36 || len(c.DiscoveryInventory()) != 36 {
		t.Fatal("form metadata lost at endpoint cap", found)
	}
	raw, _ := json.Marshal(c.DiscoveryInventory())
	if strings.Contains(string(raw), "PRIVATE_VALUE") {
		t.Fatal("form value persisted")
	}
}

func TestExplorationDoesNotShareItsCrawlWithLegacyE2Path(t *testing.T) {
	c, _, _, _ := explorationFixture(t, Manual)
	called := false
	if _, err := c.Crawl(t.Context(), func(context.Context, string) (e2runner.Result, error) { called = true; return e2runner.Result{}, nil }); !errors.Is(err, ErrExplorationBinding) || called {
		t.Fatal("bound exploration used legacy E2 crawl", err)
	}
	old, _ := flowFixture(t, Manual)
	old.binding.TargetRef, old.binding.TargetRevision = "target-owned", 7
	old.claimed = true
	b := ExplorationBinding{SessionRef: "session-owned", RunRef: old.binding.RunRef, JobID: old.binding.JobID, TaskID: old.binding.TaskID, TargetRef: old.binding.TargetRef, TargetRevision: 7, SourceDigest: old.binding.SourceDigest}
	if _, err := old.BindExploration(t.Context(), &privateDiscoveryReader{binding: b}); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("exploration joined an already claimed legacy crawl")
	}
}

func TestExplorationUnavailableIsExplicitAndDoesNotClaimLegacyCrawl(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	before, _ := c.Snapshot()
	if err := c.RecordExplorationUnavailable(t.Context(), "boundary_unavailable"); err != nil {
		t.Fatal(err)
	}
	s, _ := c.Snapshot()
	if s.ExplorationStatus != "NOT_RUN" || s.ExplorationReason != "boundary_unavailable" || !s.ExplorationPartial || s.InitialCompleted || c.claimed || s.Stage != "crawling" || len(s.Endpoints) != len(before.Endpoints) || s.Endpoints[0].Selected {
		t.Fatal("unavailable status changed execution authority")
	}
	if err := c.RecordExplorationUnavailable(t.Context(), "boundary_unavailable"); err != nil {
		t.Fatal("identical preflight is not idempotent")
	}
	afterReplay, _ := c.Snapshot()
	if afterReplay.Revision != s.Revision {
		t.Fatal("identical preflight repeated event")
	}
	if err := c.RecordExplorationUnavailable(t.Context(), "model_unavailable"); !errors.Is(err, ErrConflict) {
		t.Fatal("unavailable reason changed silently")
	}
	syntheticCrawl(t, c)
	s, _ = c.Snapshot()
	if s.ExplorationStatus != "NOT_RUN" || s.Stage != "awaiting_selection" || !s.InitialCompleted {
		t.Fatal("legacy fallback was claimed or misreported")
	}
	if err := c.RecordExplorationUnavailable(t.Context(), "boundary_unavailable"); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("late preflight accepted")
	}
}

func TestExplorationUnavailableRequiresCurrentUnboundHostState(t *testing.T) {
	for _, reason := range []string{"", "completed", "raw provider error SECRET"} {
		c, _ := flowFixture(t, Manual)
		if err := c.RecordExplorationUnavailable(t.Context(), reason); !errors.Is(err, ErrDenied) {
			t.Fatal("untrusted availability reason stored")
		}
	}
	c, _, _, _ := explorationFixture(t, Manual)
	if err := c.RecordExplorationUnavailable(t.Context(), "model_unavailable"); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("bound session overwritten as NOT_RUN")
	}
	other, _ := flowFixture(t, Manual)
	other.binding.Current = func() error { return errors.New("authority revoked") }
	if err := other.RecordExplorationUnavailable(t.Context(), "model_unavailable"); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("revoked authority stored availability")
	}
}

func TestModelOrRuntimeFailurePreservesPartialDiscoveryForSelection(t *testing.T) {
	for _, reason := range []string{"model_error", "runtime_unavailable"} {
		t.Run(reason, func(t *testing.T) {
			c, store, _, b := explorationFixture(t, Manual)
			if _, err := c.ReportStatic(t.Context(), []Discovery{{Method: "GET", Path: "/read/partial"}}); err != nil {
				t.Fatal(err)
			}
			if err := c.FinishExploration(t.Context(), b, reason); err != nil {
				t.Fatal(err)
			}
			s, _ := c.Snapshot()
			if s.ExplorationStatus != "PARTIAL" || s.ExplorationReason != reason || !s.ExplorationPartial || s.Stage != "awaiting_selection" || len(c.DiscoveryInventory()) != 1 || !s.InitialCompleted {
				t.Fatal("model or runtime failure lost partial discoveries")
			}
			if store.records["prep-owned"].Snapshot.ExplorationReason != reason {
				t.Fatal("failure reason not persisted")
			}
		})
	}
}

func TestExplorationTargetCheckRejectsRebindingBeforeFactory(t *testing.T) {
	c, store := flowFixture(t, Manual)
	c.binding.TargetRef, c.binding.TargetRevision = "target-owned", 7
	b := ExplorationBinding{SessionRef: "session-owned", RunRef: c.binding.RunRef, JobID: c.binding.JobID, TaskID: c.binding.TaskID, TargetRef: c.binding.TargetRef, TargetRevision: 7, SourceDigest: c.binding.SourceDigest}
	before, _ := c.Snapshot()
	if err := c.CheckExplorationTarget(t.Context(), b, c.binding.TargetURL); err != nil {
		t.Fatal("exact protected target rejected")
	}
	for _, target := range []string{"https://owned.test/read/", "https://owned.test:443/read", "https://owned.test/read?x=1", "https://other.test/read", "https://owned.test/read/../read", "https://owned.test/read#fragment"} {
		if err := c.CheckExplorationTarget(t.Context(), b, target); !errors.Is(err, ErrExplorationBinding) {
			t.Fatal("different URL accepted by normalization")
		}
	}
	for _, change := range []func(*ExplorationBinding){func(v *ExplorationBinding) { v.SessionRef = "" }, func(v *ExplorationBinding) { v.RunRef = "foreign" }, func(v *ExplorationBinding) { v.JobID = "foreign" }, func(v *ExplorationBinding) { v.TaskID = "foreign" }, func(v *ExplorationBinding) { v.TargetRef = "foreign" }, func(v *ExplorationBinding) { v.TargetRevision++ }, func(v *ExplorationBinding) { v.SourceDigest = strings.Repeat("b", 64) }} {
		bad := b
		change(&bad)
		if err := c.CheckExplorationTarget(t.Context(), bad, c.binding.TargetURL); !errors.Is(err, ErrExplorationBinding) {
			t.Fatal("foreign protected binding accepted")
		}
	}
	c.binding.Current = func() error { return errors.New("revoked") }
	if err := c.CheckExplorationTarget(t.Context(), b, c.binding.TargetURL); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("revoked target accepted")
	}
	c.binding.Current = func() error { return nil }
	after, _ := c.Snapshot()
	if after.Revision != before.Revision || store.records["prep-owned"].Snapshot.Revision != before.Revision || c.claimed {
		t.Fatal("read-only target check changed workflow")
	}
	if _, err := c.BindExploration(t.Context(), &privateDiscoveryReader{binding: b}); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckExplorationTarget(t.Context(), b, c.binding.TargetURL); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("factory check reopened a bound session")
	}
}
