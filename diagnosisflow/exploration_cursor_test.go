package diagnosisflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestExplorationCursorZeroAndClosedStatesAreReadonly(t *testing.T) {
	for _, reason := range []string{"", "completed", "cap_exhausted", "cancelled", "target_ended"} {
		t.Run("close_"+reason, func(t *testing.T) {
			c, store, reader, b := explorationFixture(t, Manual)
			if seq, err := c.ExplorationCursor(t.Context(), b); err != nil || seq != 0 {
				t.Fatal("empty bound cursor was not zero", err)
			}
			reader.rows[7] = observationFor(b, 7, "/read/actual")
			if _, err := c.ReportObserved(t.Context(), b, 7); err != nil {
				t.Fatal(err)
			}
			if reason != "" {
				if err := c.FinishExploration(t.Context(), b, reason); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := c.Snapshot()
			if seq, err := c.ExplorationCursor(t.Context(), b); err != nil || seq != 7 {
				t.Fatal("active or closed private cursor lost", err)
			}
			after, _ := c.Snapshot()
			if before.Revision != after.Revision || store.records["prep-owned"].Snapshot.Revision != before.Revision {
				t.Fatal("cursor read wrote a projection")
			}
		})
	}
}

func TestExplorationCursorRejectsForeignBindings(t *testing.T) {
	for _, change := range []func(*ExplorationBinding){func(b *ExplorationBinding) { b.SessionRef = "foreign" }, func(b *ExplorationBinding) { b.RunRef = "foreign" }, func(b *ExplorationBinding) { b.JobID = "foreign" }, func(b *ExplorationBinding) { b.TaskID = "foreign" }, func(b *ExplorationBinding) { b.TargetRef = "foreign" }, func(b *ExplorationBinding) { b.TargetRevision++ }, func(b *ExplorationBinding) { b.SourceDigest = strings.Repeat("b", 64) }} {
		c, _, r, b := explorationFixture(t, Manual)
		r.rows[7] = observationFor(b, 7, "/read/actual")
		if _, err := c.ReportObserved(t.Context(), b, 7); err != nil {
			t.Fatal(err)
		}
		bad := b
		change(&bad)
		if seq, err := c.ExplorationCursor(t.Context(), bad); seq != 0 || !errors.Is(err, ErrExplorationBinding) {
			t.Fatal("foreign binding read private cursor")
		}
	}
	c, _, _, b := explorationFixture(t, Manual)
	c.record.Exploration.Binding.SessionRef = "other-private-session"
	if seq, err := c.ExplorationCursor(t.Context(), b); seq != 0 || !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("private session mismatch read cursor")
	}
	c, _, _, b = explorationFixture(t, Manual)
	c.binding.TargetRevision++
	if seq, err := c.ExplorationCursor(t.Context(), b); seq != 0 || !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("changed protected target read cursor")
	}
}

func TestExplorationCursorRejectsRevocationPoisonAndContext(t *testing.T) {
	c, store, r, b := explorationFixture(t, Manual)
	r.rows[1] = observationFor(b, 1, "/read/actual")
	store.fail = true
	if _, err := c.ReportObserved(t.Context(), b, 1); !errors.Is(err, ErrProjection) {
		t.Fatal("fixture did not fail persistence")
	}
	if seq, err := c.ExplorationCursor(t.Context(), b); seq != 0 || !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("unsaved poisoned cursor was returned")
	}
	c, _, _, b = explorationFixture(t, Manual)
	c.binding.Current = func() error { return errors.New("authority revoked") }
	if seq, err := c.ExplorationCursor(t.Context(), b); seq != 0 || !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("revoked authority read cursor")
	}
	c.binding.Current = func() error { return nil }
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if seq, err := c.ExplorationCursor(cancelled, b); seq != 0 || !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("cancelled context read cursor")
	}
	late, cancelLate := context.WithCancel(t.Context())
	defer cancelLate()
	c.binding.Current = func() error { cancelLate(); return nil }
	if seq, err := c.ExplorationCursor(late, b); seq != 0 || !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("cursor ignored cancellation during authority check")
	}
	if seq, err := c.ExplorationCursor(nil, b); seq != 0 || !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("nil context read cursor")
	}
	if seq, err := (*Controller)(nil).ExplorationCursor(t.Context(), b); seq != 0 || !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("nil controller read cursor")
	}
	unbound, _ := flowFixture(t, Manual)
	if seq, err := unbound.ExplorationCursor(t.Context(), b); seq != 0 || !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("unbound controller read cursor")
	}
}

func TestExplorationCursorSurvivesPublicAndInventoryOmission(t *testing.T) {
	c, store, r, b := explorationFixture(t, Manual)
	for batch := 0; batch < 10; batch++ {
		rows := []Discovery{}
		for i := 0; i < 30; i++ {
			rows = append(rows, Discovery{Method: "GET", Path: fmt.Sprintf("/read/cursor/%d", batch*30+i)})
		}
		if _, err := c.ReportStatic(t.Context(), rows); err != nil {
			t.Fatal(err)
		}
	}
	r.rows[500] = observationFor(b, 500, "/read/unlisted-observation")
	if _, err := c.ReportObserved(t.Context(), b, 500); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := c.Snapshot()
	if len(snapshot.Endpoints) != 32 || len(c.DiscoveryInventory()) != maxDiscoveryInventory || snapshot.Counts.InventoryOmitted == 0 {
		t.Fatal("fixture did not omit both bounded views")
	}
	for _, endpoint := range snapshot.Endpoints {
		if endpoint.ExplorationSeq >= 500 {
			t.Fatal("private latest seq unexpectedly present in public view")
		}
	}
	for _, fact := range c.DiscoveryInventory() {
		if fact.Seq >= 500 {
			t.Fatal("private latest seq unexpectedly present in inventory")
		}
	}
	if store.records["prep-owned"].Exploration.LastSequence != 500 {
		t.Fatal("private watermark did not persist")
	}
	if seq, err := c.ExplorationCursor(t.Context(), b); err != nil || seq != 500 {
		t.Fatal("cursor guessed from omitted presentation records", err)
	}
	if err := c.FinishExploration(t.Context(), b, "cap_exhausted"); err != nil {
		t.Fatal(err)
	}
	if seq, err := c.ExplorationCursor(t.Context(), b); err != nil || seq != 500 {
		t.Fatal("closed omitted cursor was not readable", err)
	}
}

func TestExplorationCursorUsesPrivateWatermarkWithMergedObservations(t *testing.T) {
	c, _, r, b := explorationFixture(t, Manual)
	for seq := uint64(1); seq <= 40; seq++ {
		r.rows[seq] = observationFor(b, seq, "/read/repeated")
		if _, err := c.ReportObserved(t.Context(), b, seq); err != nil {
			t.Fatal(err)
		}
	}
	facts := c.DiscoveryInventory()
	if len(facts) != 1 || facts[0].FirstSeq != 1 || facts[0].Seq != 40 || facts[0].ObservationCount != 40 {
		t.Fatal("fixture did not merge repeated observations")
	}
	if seq, err := c.ExplorationCursor(t.Context(), b); err != nil || seq != 40 {
		t.Fatal("merged cursor lost private watermark", err)
	}
}
