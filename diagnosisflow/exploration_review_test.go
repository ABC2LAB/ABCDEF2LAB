package diagnosisflow

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCrawlExplorationErrorsPersistPartialBeforeCompleted(t *testing.T) {
	for _, failure := range []string{"unknown_seq", "hash_mismatch", "different_path", "foreign_binding", "observation_conflict", "nil_fetch", "fetch_completed", "fetch_error_completed"} {
		t.Run(failure, func(t *testing.T) {
			c, store, r, b := explorationFixture(t, Auto)
			body := []byte(`<a href="/read/fake">fake</a>`)
			o := observationFor(b, 1, "/read")
			o.ResponseContentType, o.ResponseSHA256 = "text/html", sha256.Sum256(body)
			r.rows[1] = o
			want := ErrExplorationObservation
			switch failure {
			case "unknown_seq":
				delete(r.rows, 1)
				want = ErrUnavailable
			case "hash_mismatch":
				o.ResponseSHA256 = sha256.Sum256([]byte("actual"))
				r.rows[1] = o
			case "different_path":
				o.Path = "/read/other"
				r.rows[1] = o
			case "foreign_binding":
				o.Binding.TargetRevision++
				r.rows[1] = o
			case "observation_conflict":
				previous := observationFor(b, 1, "/read/previous")
				r.rows[1] = previous
				if _, err := c.ReportObserved(t.Context(), b, 1); err != nil {
					t.Fatal(err)
				}
				r.rows[1] = o
				want = ErrConflict
			case "nil_fetch":
				want = ErrUnavailable
			}
			fetch := ExplorationFetch(func(context.Context, string) (ExplorationFetchResult, error) {
				if failure == "fetch_completed" {
					return ExplorationFetchResult{Reason: "completed"}, nil
				}
				if failure == "fetch_error_completed" {
					return ExplorationFetchResult{Reason: "completed"}, errors.New("request failed")
				}
				return ExplorationFetchResult{Seq: 1, Body: body, Complete: true}, nil
			})
			if failure == "nil_fetch" {
				fetch = nil
			}
			if _, err := c.CrawlExploration(t.Context(), b, fetch); !errors.Is(err, want) {
				t.Fatal("unexpected crawl failure", err)
			}
			if !store.records["prep-owned"].Snapshot.ExplorationPartial {
				t.Fatal("failure partial state was not persisted")
			}
			if err := c.FinishExploration(t.Context(), b, "completed"); err != nil {
				t.Fatal(err)
			}
			s, _ := c.Snapshot()
			if !s.ExplorationPartial || s.ExplorationStatus != "PARTIAL" {
				t.Fatal("failed crawl became complete")
			}
			if discoveredEndpoint(t, c, "GET", "/read/fake") != nil {
				t.Fatal("unbound failure body discovered an endpoint")
			}
		})
	}
}

func TestFinishExplorationChecksCurrentAndRemainingVisits(t *testing.T) {
	for _, reason := range []string{"completed", "cap_exhausted", "boundary_unavailable", "model_error", "runtime_unavailable", "time_budget", "page_limit"} {
		t.Run("revoked_"+reason, func(t *testing.T) {
			c, store, _, b := explorationFixture(t, Auto)
			before := store.records["prep-owned"].Snapshot.Revision
			c.binding.Current = func() error { return errors.New("revoked") }
			if err := c.FinishExploration(t.Context(), b, reason); !errors.Is(err, ErrExplorationBinding) {
				t.Fatal("revoked authority accepted finish", err)
			}
			if store.records["prep-owned"].Snapshot.Revision != before {
				t.Fatal("revoked finish persisted a transition")
			}
			for _, e := range c.record.Snapshot.Endpoints {
				if e.Selected {
					t.Fatal("revoked finish selected a diagnosis endpoint")
				}
			}
			if c.record.Snapshot.Stage != "crawling" {
				t.Fatal("revoked finish changed phase")
			}
		})
	}
	for _, reason := range []string{"cancelled", "target_ended"} {
		c, store, _, b := explorationFixture(t, Auto)
		c.binding.Current = func() error { return errors.New("revoked") }
		if err := c.FinishExploration(t.Context(), b, reason); err != nil {
			t.Fatal("revoked stop could not preserve discoveries")
		}
		if store.records["prep-owned"].Snapshot.Stage != "stopped" {
			t.Fatal("revoked stop did not persist stopped phase")
		}
	}
	c, _, _, b := explorationFixture(t, Auto)
	if err := c.FinishExploration(t.Context(), b, "completed"); err != nil {
		t.Fatal(err)
	}
	s, _ := c.Snapshot()
	if !s.ExplorationPartial || s.ExplorationStatus != "PARTIAL" {
		t.Fatal("unvisited attested endpoint disappeared from completion status")
	}
	complete, _, reader, pin := explorationFixture(t, Auto)
	reader.rows[1] = observationFor(pin, 1, "/read")
	if _, err := complete.ReportObserved(t.Context(), pin, 1); err != nil {
		t.Fatal(err)
	}
	if err := complete.FinishExploration(t.Context(), pin, "completed"); err != nil {
		t.Fatal(err)
	}
	s, _ = complete.Snapshot()
	if s.ExplorationPartial || s.ExplorationStatus != "COMPLETED" {
		t.Fatal("fully observed positive control was not complete")
	}
}

func TestFinishExplorationRejectsEveryForeignBinding(t *testing.T) {
	for _, change := range []func(*ExplorationBinding){func(b *ExplorationBinding) { b.SessionRef = "foreign" }, func(b *ExplorationBinding) { b.RunRef = "foreign" }, func(b *ExplorationBinding) { b.JobID = "foreign" }, func(b *ExplorationBinding) { b.TaskID = "foreign" }, func(b *ExplorationBinding) { b.TargetRef = "foreign" }, func(b *ExplorationBinding) { b.TargetRevision++ }, func(b *ExplorationBinding) { b.SourceDigest = strings.Repeat("b", 64) }} {
		c, store, _, b := explorationFixture(t, Auto)
		change(&b)
		before := store.records["prep-owned"].Snapshot.Revision
		if err := c.FinishExploration(t.Context(), b, "completed"); !errors.Is(err, ErrExplorationBinding) {
			t.Fatal("foreign finish binding accepted")
		}
		if store.records["prep-owned"].Snapshot.Revision != before {
			t.Fatal("foreign finish mutated persistence")
		}
	}
}

type notifyingDiscoveryReader struct {
	*privateDiscoveryReader
	read chan struct{}
	once sync.Once
}

func (r *notifyingDiscoveryReader) ReadObservation(ctx context.Context, b ExplorationBinding, seq uint64) (ExplorationObservation, error) {
	o, err := r.privateDiscoveryReader.ReadObservation(ctx, b, seq)
	r.once.Do(func() { close(r.read) })
	return o, err
}
func TestCrawlExplorationInflightPauseWaitsAndPreservesObservation(t *testing.T) {
	c, store, r, b := explorationFixture(t, Auto)
	notify := &notifyingDiscoveryReader{privateDiscoveryReader: r, read: make(chan struct{})}
	c.explorationReader = notify
	body := []byte("<p>observed</p>")
	o := observationFor(b, 1, "/read")
	o.ResponseContentType, o.ResponseSHA256 = "text/html", sha256.Sum256(body)
	r.rows[1] = o
	started, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.CrawlExploration(ctx, b, func(context.Context, string) (ExplorationFetchResult, error) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return ExplorationFetchResult{}, ctx.Err()
			}
			return ExplorationFetchResult{Seq: 1, Body: body, Complete: true}, nil
		})
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("owned fetch did not start")
	}
	s, _ := c.Snapshot()
	if _, err := c.Command(t.Context(), "ARTEX", "pause", "owned-pause", s.Revision, nil); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-notify.read:
	case <-ctx.Done():
		t.Fatal("private response was not read")
	}
	select {
	case err := <-done:
		t.Fatal("inflight pause aborted crawl", err)
	default:
	}
	s, _ = c.Snapshot()
	if !s.Paused || s.Counts.ExplorationRequests != 0 {
		t.Fatal("observation did not wait for resume")
	}
	if _, err := c.Command(t.Context(), "ARTEX", "resume", "owned-resume", s.Revision, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("resumed observation lost", err)
		}
	case <-ctx.Done():
		t.Fatal("resumed crawl did not finish")
	}
	if len(store.records["prep-owned"].DiscoveryInventory) != 1 || store.records["prep-owned"].Snapshot.Counts.ExplorationRequests != 1 {
		t.Fatal("resumed actual observation was not persisted")
	}
}

func TestCollectStaticDocumentNeverAttestsUnboundBytes(t *testing.T) {
	c, _ := flowFixture(t, Auto)
	if err := c.CollectStaticDocument(t.Context(), "/read", "text/html", []byte(`<a href="/read/fabricated">fake</a><script src="/read/fake.js"></script><script>fetch('/read/api/fake')</script>`)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/read/fabricated", "/read/fake.js", "/read/api/fake"} {
		e := discoveredEndpoint(t, c, "GET", path)
		if e == nil || e.DiscoveryConfidence != "inferred" || e.Change != "possible" || e.Source != SourceAPIReconStatic || e.Visited {
			t.Fatal("unbound document created an attested read", path)
		}
	}
	if err := c.CollectStaticDocument(t.Context(), "/read", "text/html", []byte(`<base href="https://other.test/">`)); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.documentBases["/read"]; ok {
		t.Fatal("unbound document changed trusted base resolution")
	}
}

func TestDiscoveryCurrentGuardsPrecedeEachMutation(t *testing.T) {
	c, store := flowFixture(t, Auto)
	c.binding.Current = func() error { return errors.New("revoked") }
	before := store.records["prep-owned"].Snapshot.Revision
	if _, err := c.ReportStatic(t.Context(), []Discovery{{Method: "GET", Path: "/read/forged"}}); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked static report accepted")
	}
	if len(c.DiscoveryInventory()) != 0 || store.records["prep-owned"].Snapshot.Revision != before {
		t.Fatal("revoked static report changed persistence")
	}
	observed, db, r, b := explorationFixture(t, Auto)
	r.rows[1] = observationFor(b, 1, "/read")
	checks := 0
	observed.binding.Current = func() error {
		checks++
		if checks >= 3 {
			return errors.New("revoked at commit")
		}
		return nil
	}
	if _, err := observed.ReportObserved(t.Context(), b, 1); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("observed commit skipped second authority check", err)
	}
	if len(db.records["prep-owned"].DiscoveryInventory) != 0 || observed.record.Exploration.LastSequence != 0 {
		t.Fatal("revoked observed commit changed provenance")
	}
}

func inventoryBytes(c *Controller) int {
	raw, _ := json.Marshal(c.DiscoveryInventory())
	return len(raw)
}
func largeShape(i int) Discovery {
	keys := []string{}
	for k := 0; k < 32; k++ {
		keys = append(keys, fmt.Sprintf("key%02d_%s", k, strings.Repeat("x", 54)))
	}
	return Discovery{Method: "GET", Path: fmt.Sprintf("/read/shape/%d", i), QueryKeys: keys, BodyKeys: keys}
}
func TestDiscoveryInventoryByteCapOnAppendAndMerge(t *testing.T) {
	t.Run("append", func(t *testing.T) {
		c, store := flowFixture(t, Manual)
		for i := 0; i < maxDiscoveryInventory; i++ {
			before := len(c.DiscoveryInventory())
			if _, err := c.ReportStatic(t.Context(), []Discovery{largeShape(i)}); err != nil {
				t.Fatal(err)
			}
			s, _ := c.Snapshot()
			if s.Counts.InventoryOmitted > 0 {
				if len(c.DiscoveryInventory()) != before || before >= maxDiscoveryInventory || inventoryBytes(c) > maxDiscoveryInventoryBytes {
					t.Fatal("byte cap did not reject new inventory entry independently")
				}
				if len(store.records["prep-owned"].DiscoveryInventory) != before {
					t.Fatal("oversize append reached persistence")
				}
				return
			}
		}
		t.Fatal("fixture never reached inventory byte bound")
	})
	t.Run("merge", func(t *testing.T) {
		c, store := flowFixture(t, Manual)
		if _, err := c.ReportStatic(t.Context(), []Discovery{{Method: "GET", Path: "/read/merge"}}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < maxDiscoveryInventory; i++ {
			if _, err := c.ReportStatic(t.Context(), []Discovery{largeShape(i)}); err != nil {
				t.Fatal(err)
			}
			s, _ := c.Snapshot()
			if s.Counts.InventoryOmitted > 0 {
				break
			}
		}
		before := c.DiscoveryInventory()
		old, _ := c.Snapshot()
		merge := largeShape(0)
		merge.Path = "/read/merge"
		if _, err := c.ReportStatic(t.Context(), []Discovery{merge}); err != nil {
			t.Fatal(err)
		}
		after, _ := c.Snapshot()
		if after.Counts.InventoryOmitted != old.Counts.InventoryOmitted+1 || !reflect.DeepEqual(c.DiscoveryInventory(), before) || inventoryBytes(c) > maxDiscoveryInventoryBytes {
			t.Fatal("oversize merge changed protected inventory")
		}
		if !reflect.DeepEqual(store.records["prep-owned"].DiscoveryInventory, before) {
			t.Fatal("oversize merge reached persistence")
		}
	})
}

func TestRepeatedObservedDiscoveryKeepsSourceTransportAndLegacyV1(t *testing.T) {
	c, _, r, b := explorationFixture(t, Manual)
	first := observationFor(b, 1, "/read/repeated")
	first.QueryKeys = []string{"first"}
	r.rows[1] = first
	if _, err := c.ReportObserved(t.Context(), b, 1); err != nil {
		t.Fatal(err)
	}
	// 기존 v1 저장 형태를 실제 JSON으로 만들고 다시 읽어 새 칸의 부재를 검사한다.
	raw, _ := json.Marshal(c.record)
	var document map[string]json.RawMessage
	if json.Unmarshal(raw, &document) != nil {
		t.Fatal("legacy record decode failed")
	}
	var legacyFacts []map[string]any
	if json.Unmarshal(document["discovery_inventory"], &legacyFacts) != nil {
		t.Fatal("legacy inventory decode failed")
	}
	for _, fact := range legacyFacts {
		delete(fact, "first_seq")
		delete(fact, "first_at")
		delete(fact, "observation_count")
	}
	document["discovery_inventory"], _ = json.Marshal(legacyFacts)
	raw, _ = json.Marshal(document)
	var restored Record
	if json.Unmarshal(raw, &restored) != nil {
		t.Fatal("persisted legacy record restore failed")
	}
	c.record = restored
	for seq := uint64(2); seq <= 260; seq++ {
		o := observationFor(b, seq, "/read/repeated")
		o.At = first.At.Add(time.Duration(seq) * time.Nanosecond)
		o.QueryKeys = []string{"last"}
		r.rows[seq] = o
		if _, err := c.ReportObserved(t.Context(), b, seq); err != nil {
			t.Fatal(err)
		}
	}
	facts := c.DiscoveryInventory()
	if len(facts) != 1 || facts[0].FirstSeq != 1 || facts[0].Seq != 260 || facts[0].ObservationCount != 260 || !facts[0].FirstAt.Equal(first.At) || !facts[0].At.Equal(r.rows[260].At) || strings.Join(facts[0].QueryKeys, ",") != "first,last" {
		t.Fatal("repeated observations lost legacy time/seq/key history")
	}
	for i, shape := range []struct{ source, transport, key string }{{SourceBrowserNavigation, "http", "navigation"}, {SourceBrowserXHR, "sse", "event"}, {SourceBrowserXHR, "websocket", "upgrade"}} {
		seq := uint64(261 + i)
		o := observationFor(b, seq, "/read/repeated")
		o.Source, o.Transport, o.QueryKeys = shape.source, shape.transport, []string{shape.key}
		r.rows[seq] = o
		if _, err := c.ReportObserved(t.Context(), b, seq); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.DiscoveryInventory()) != 4 {
		t.Fatal("source or transport provenance merged")
	}
	last := observationFor(b, 264, "/read/new-address")
	r.rows[264] = last
	if _, err := c.ReportObserved(t.Context(), b, 264); err != nil {
		t.Fatal(err)
	}
	s, _ := c.Snapshot()
	if len(c.DiscoveryInventory()) != 5 || s.Counts.InventoryOmitted != 0 || s.Counts.ExplorationRequests != 264 {
		t.Fatal("repeated address starved new discoveries")
	}
}

func TestCrawlExplorationPersistenceFailureCannotBecomeCompleted(t *testing.T) {
	c, store, _, b := explorationFixture(t, Auto)
	store.fail = true
	_, err := c.CrawlExploration(t.Context(), b, func(context.Context, string) (ExplorationFetchResult, error) {
		return ExplorationFetchResult{Seq: 99}, nil
	})
	if !errors.Is(err, ErrProjection) || !c.record.Snapshot.ExplorationPartial {
		t.Fatal("partial persistence failure was hidden")
	}
	if err := c.FinishExploration(t.Context(), b, "completed"); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("poisoned partial became complete")
	}
}

func TestObservedMergePreservesEachMediaTypeAndShape(t *testing.T) {
	c, _, r, b := explorationFixture(t, Manual)
	for i, shape := range []struct{ ct, key string }{{"application/json", "json_name"}, {"application/x-www-form-urlencoded", "form_name"}, {"application/json", "json_extra"}} {
		seq := uint64(i + 1)
		o := observationFor(b, seq, "/read/request")
		o.ContentType, o.BodyKeys = shape.ct, []string{shape.key}
		r.rows[seq] = o
		if _, err := c.ReportObserved(t.Context(), b, seq); err != nil {
			t.Fatal(err)
		}
	}
	facts := c.DiscoveryInventory()
	if len(facts) != 2 || facts[0].ContentType != "application/json" || strings.Join(facts[0].BodyKeys, ",") != "json_extra,json_name" || facts[0].FirstSeq != 1 || facts[0].Seq != 3 || facts[0].ObservationCount != 2 || facts[1].ContentType != "application/x-www-form-urlencoded" || strings.Join(facts[1].BodyKeys, ",") != "form_name" {
		t.Fatal("different request media types lost separate provenance")
	}
}

func TestCrawlExplorationCancellationStillPreservesPartial(t *testing.T) {
	c, store, _, b := explorationFixture(t, Auto)
	ctx, cancel := context.WithCancel(t.Context())
	_, err := c.CrawlExploration(ctx, b, func(context.Context, string) (ExplorationFetchResult, error) {
		cancel()
		return ExplorationFetchResult{}, ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || !store.records["prep-owned"].Snapshot.ExplorationPartial {
		t.Fatal("cancelled crawl lost partial failure")
	}
	if err := c.FinishExploration(ctx, b, "completed"); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("cancelled context permitted completed closure")
	}
}

func TestProtectedInventoryOmissionKeepsOtherwiseVisitedExplorationPartial(t *testing.T) {
	c, _, r, b := explorationFixture(t, Auto)
	r.rows[1] = observationFor(b, 1, "/read")
	if _, err := c.ReportObserved(t.Context(), b, 1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxDiscoveryInventory; i++ {
		if _, err := c.ReportStatic(t.Context(), []Discovery{largeShape(i)}); err != nil {
			t.Fatal(err)
		}
		if c.record.Snapshot.Counts.InventoryOmitted > 0 {
			break
		}
	}
	if err := c.FinishExploration(t.Context(), b, "completed"); err != nil {
		t.Fatal(err)
	}
	s, _ := c.Snapshot()
	if s.Counts.InventoryOmitted == 0 || !s.ExplorationPartial || s.ExplorationStatus != "PARTIAL" {
		t.Fatal("protected metadata omission became complete")
	}
}
