package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/taskmode"
	"github.com/jackc/pgx/v5/pgconn"
)

func e3FeedbackPrepared(t *testing.T, in e3record.Input, source e3record.Source) e3record.Prepared {
	t.Helper()
	prepared, err := e3record.Prepare(t.Context(), in, func(context.Context, evidencebind.Context) (e3record.Source, error) {
		return source, nil
	})
	if err != nil {
		t.Fatal("valid synthetic Prepare failed", err)
	}
	return prepared
}

func e3FeedbackStored(t *testing.T, d *DB, f e3Fixture) ControlledFindingResult {
	t.Helper()
	out, err := d.RecordControlledFinding(t.Context(), f.prepared, f.markers)
	if err != nil || !out.Created || out.FindingID == 0 {
		t.Fatalf("valid controlled fixture: out=%+v err=%v", out, err)
	}
	return out
}

func e3FeedbackCounts(t *testing.T, d *DB, f e3Fixture, want [5]int) {
	t.Helper()
	if got := e3Counts(t, d, f); got != want {
		t.Fatalf("stored rows changed: got=%v want=%v", got, want)
	}
}

func TestE3FeedbackImmutableTables(t *testing.T) {
	d := e3TestDB(t)
	f := e3FixtureFor(t, d, oracle.Confirmed, true)
	out := e3FeedbackStored(t, d, f)
	snapshot, _ := f.prepared.Snapshot()
	tables := []struct {
		table, trigger string
		id             int64
	}{
		{"e3_evidence_manifests", "manifest", out.ManifestID},
		{"e3_oracle_verdicts", "verdict", out.VerdictID},
		{"e3_verdict_issuances", "issuance", out.IssuanceID},
		{"e3_verdict_links", "link", 0},
	}
	if err := d.QueryRow(`SELECT id FROM e3_verdict_links WHERE issuance_id=$1`, out.IssuanceID).Scan(&tables[3].id); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		for _, operation := range []string{"update", "delete", "truncate"} {
			t.Run(table.trigger+"/"+operation, func(t *testing.T) {
				tx, err := d.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				// CASCADE avoids an FK-only rejection. Disable the other tables'
				// truncate guards inside this rollback-only transaction so they
				// cannot hide a missing guard on the table under test.
				if operation == "truncate" {
					for _, other := range tables {
						if other.table != table.table {
							if _, err := tx.Exec(`ALTER TABLE ` + other.table + ` DISABLE TRIGGER trg_e3_` + other.trigger + `_truncate`); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				if _, err := tx.Exec(`SAVEPOINT immutable_attempt`); err != nil {
					t.Fatal(err)
				}
				query, args := `UPDATE `+table.table+` SET id=id WHERE id=$1`, []any{table.id}
				switch operation {
				case "delete":
					query = `DELETE FROM ` + table.table + ` WHERE id=$1`
				case "truncate":
					query, args = `TRUNCATE `+table.table+` CASCADE`, nil
				}
				_, err = tx.Exec(query, args...)
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.Message != "e3_immutable_record" {
					t.Fatalf("expected immutable guard, not an FK/other rejection: %v", err)
				}
				if operation == "truncate" {
					if _, err := tx.Exec(`ROLLBACK TO SAVEPOINT immutable_attempt`); err != nil {
						t.Fatal(err)
					}
					if _, err := tx.Exec(`ALTER TABLE ` + table.table + ` DISABLE TRIGGER trg_e3_` + table.trigger + `_truncate`); err != nil {
						t.Fatal(err)
					}
					if _, err := tx.Exec(query); err != nil {
						t.Fatal("truncate positive control was not executable", err)
					}
				}
				if err := tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				e3FeedbackCounts(t, d, f, [5]int{1, 1, 1, 1, 1})
				payload, err := d.ControlledFindingRecord(t.Context(), out.FindingID)
				if err != nil || !bytes.Equal(payload, snapshot.Canonical) {
					t.Fatal("failed immutable mutation changed the original record", err)
				}
			})
		}
	}
}

func TestE3FeedbackVersionAndIntentGuards(t *testing.T) {
	d := e3TestDB(t)
	t.Run("mode version", func(t *testing.T) {
		f := e3FixtureFor(t, d, oracle.Confirmed, true)
		source := f.source
		source.ModeVersion++
		prepared := e3FeedbackPrepared(t, f.input, source)
		out, err := d.RecordControlledFinding(t.Context(), prepared, f.markers)
		if !errors.Is(err, ErrControlledFindingConflict) || out != (ControlledFindingResult{}) {
			t.Fatal("stale/wrong mode version stored", out, err)
		}
		e3FeedbackCounts(t, d, f, [5]int{})
		e3FeedbackStored(t, d, f) // Correct DB version is accepted with the same evidence.
	})
	t.Run("intent kind", func(t *testing.T) {
		f := e3FixtureFor(t, d, oracle.Confirmed, true)
		var originalState string
		if err := d.QueryRow(`SELECT state FROM exploration_nodes WHERE id=$1`, f.intentID).Scan(&originalState); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`UPDATE exploration_nodes SET kind='fact',state='confirmed' WHERE id=$1`, f.intentID); err != nil {
			t.Fatal(err)
		}
		out, err := d.RecordControlledFinding(t.Context(), f.prepared, f.markers)
		if !errors.Is(err, ErrControlledFindingInput) || out != (ControlledFindingResult{}) {
			t.Fatal("non-intent node used as issuance intent", out, err)
		}
		e3FeedbackCounts(t, d, f, [5]int{})
		if _, err := d.Exec(`UPDATE exploration_nodes SET kind='intent',state=$2 WHERE id=$1`, f.intentID, originalState); err != nil {
			t.Fatal(err)
		}
		e3FeedbackStored(t, d, f)
	})
}

func TestE3FeedbackManifestIdentityAndContent(t *testing.T) {
	d := e3TestDB(t)
	t.Run("another case with the same manifest key", func(t *testing.T) {
		f := e3FixtureFor(t, d, oracle.Confirmed, true)
		first := e3FeedbackStored(t, d, f)
		in, source := f.input, f.source
		c := in.Expected
		c.Scope.CaseRef = "other-synthetic-case"
		in.Expected, source.Expected, source.FinalCheckpointScope = c, c, c.Scope
		in.Evidence.Policy.Context, in.Evidence.Owner.Context, in.Evidence.Cross.Context, in.Evidence.Result.Context = c, c, c, c
		in.Evidence.Owner.Receipt.Scope, in.Evidence.Cross.Receipt.Scope = c.Scope, c.Scope
		in.Evidence.Owner.Evidence.Scope, in.Evidence.Cross.Evidence.Scope = c.Scope, c.Scope
		in.Evidence.Result.OwnerEvidence, in.Evidence.Result.CrossEvidence = in.Evidence.Owner.Evidence, in.Evidence.Cross.Evidence
		var err error
		source.EvidenceDigest, err = e3record.EvidenceDigest(in.Evidence)
		if err != nil {
			t.Fatal(err)
		}
		other := e3FeedbackPrepared(t, in, source)
		a, _ := f.prepared.Snapshot()
		b, _ := other.Snapshot()
		if a.OperationKey == b.OperationKey || bytes.Equal(a.Manifest, b.Manifest) ||
			a.Source.Expected.Scope.RunRef != b.Source.Expected.Scope.RunRef || a.Source.EvidenceRevision != b.Source.EvidenceRevision {
			t.Fatal("fixture does not isolate same-key, different-case manifest reuse")
		}
		if out, err := d.RecordControlledFinding(t.Context(), other, f.markers); !errors.Is(err, ErrControlledFindingConflict) || out != (ControlledFindingResult{}) {
			t.Fatal("different case reused a frozen manifest", out, err)
		}
		e3FeedbackCounts(t, d, f, [5]int{1, 1, 1, 1, 1})
		if retry, err := d.RecordControlledFinding(t.Context(), f.prepared, f.markers); err != nil || retry.Created || retry.FindingID != first.FindingID {
			t.Fatal("conflict changed the original operation", retry, err)
		}
	})
	for _, mismatch := range []string{"none", "canonical", "digest"} {
		t.Run("persisted "+mismatch, func(t *testing.T) {
			f := e3FixtureFor(t, d, oracle.Confirmed, true)
			snapshot, _ := f.prepared.Snapshot()
			canonical := bytes.Clone(snapshot.Manifest)
			digest := snapshot.ManifestDigest
			if mismatch == "canonical" {
				canonical = []byte(`{"different_synthetic_case":true}`)
			} else if mismatch == "digest" {
				digest[0] ^= 1
			}
			if _, err := d.Exec(`INSERT INTO e3_evidence_manifests(task_id,execution_ref,evidence_revision,canonical,digest) VALUES($1,$2,$3,$4,$5)`,
				f.taskID, f.source.Expected.Scope.RunRef, int64(f.source.EvidenceRevision), canonical, digest[:]); err != nil {
				t.Fatal(err)
			}
			out, err := d.RecordControlledFinding(t.Context(), f.prepared, f.markers)
			if mismatch == "none" {
				if err != nil || !out.Created || out.FindingID == 0 {
					t.Fatal("identical preexisting manifest rejected", out, err)
				}
				e3FeedbackCounts(t, d, f, [5]int{1, 1, 1, 1, 1})
			} else {
				if !errors.Is(err, ErrControlledFindingConflict) || out != (ControlledFindingResult{}) {
					t.Fatal("inconsistent stored manifest accepted", out, err)
				}
				e3FeedbackCounts(t, d, f, [5]int{1, 0, 0, 0, 0})
			}
		})
	}
}

// Only synthetic corruption fixtures use this helper. The trigger is restored
// before commit; callers restore the data after checking fail-closed readers.
func e3FeedbackCorrupt(t *testing.T, d *DB, table, trigger, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE ` + table + ` DISABLE TRIGGER ` + trigger); err != nil {
		t.Fatal(err)
	}
	result, err := tx.Exec(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatal("corruption fixture must affect exactly one row", n, err)
	}
	if _, err := tx.Exec(`ALTER TABLE ` + table + ` ENABLE TRIGGER ` + trigger); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestE3FeedbackConfirmedReplayRequiresItsLink(t *testing.T) {
	d := e3TestDB(t)
	f := e3FixtureFor(t, d, oracle.Confirmed, true)
	out := e3FeedbackStored(t, d, f)
	var linkID int64
	if err := d.QueryRow(`SELECT id FROM e3_verdict_links WHERE issuance_id=$1`, out.IssuanceID).Scan(&linkID); err != nil {
		t.Fatal(err)
	}
	e3FeedbackCorrupt(t, d, "e3_verdict_links", "trg_e3_link_immutable", `DELETE FROM e3_verdict_links WHERE id=$1`, linkID)
	restore := func() {
		if _, err := d.Exec(`INSERT INTO e3_verdict_links(id,issuance_id,task_id,finding_id,purpose,evidence_version) VALUES($1,$2,$3,$4,'initial',1) ON CONFLICT(id) DO NOTHING`,
			linkID, out.IssuanceID, f.taskID, out.FindingID); err != nil {
			t.Fatal("restore synthetic missing link", err)
		}
	}
	t.Cleanup(restore)
	if replay, err := d.RecordControlledFinding(t.Context(), f.prepared, f.markers); !errors.Is(err, ErrControlledFindingConflict) || replay != (ControlledFindingResult{}) {
		t.Fatal("linkless confirmed replay reported success", replay, err)
	}
	e3FeedbackCounts(t, d, f, [5]int{1, 1, 0, 1, 1})
	restore()
	if replay, err := d.RecordControlledFinding(t.Context(), f.prepared, f.markers); err != nil || replay.Created || replay.FindingID != out.FindingID {
		t.Fatal("restored link did not restore valid replay", replay, err)
	}
}

func TestE3FeedbackReadRejectsDigestMismatch(t *testing.T) {
	d := e3TestDB(t)
	f := e3FixtureFor(t, d, oracle.Confirmed, true)
	out := e3FeedbackStored(t, d, f)
	snapshot, _ := f.prepared.Snapshot()
	wrong := snapshot.PayloadDigest
	wrong[0] ^= 1
	e3FeedbackCorrupt(t, d, "e3_oracle_verdicts", "trg_e3_verdict_immutable", `UPDATE e3_oracle_verdicts SET payload_digest=$1 WHERE id=$2`, wrong[:], out.VerdictID)
	restore := func() {
		e3FeedbackCorrupt(t, d, "e3_oracle_verdicts", "trg_e3_verdict_immutable", `UPDATE e3_oracle_verdicts SET payload_digest=$1 WHERE id=$2`, snapshot.PayloadDigest[:], out.VerdictID)
	}
	t.Cleanup(restore)
	if payload, err := d.ControlledFindingRecord(t.Context(), out.FindingID); !errors.Is(err, ErrControlledFindingConflict) || payload != nil {
		t.Fatal("unverified persisted bytes escaped the reader", err)
	}
	e3FeedbackCounts(t, d, f, [5]int{1, 1, 1, 1, 1})
	restore()
	if payload, err := d.ControlledFindingRecord(t.Context(), out.FindingID); err != nil || !bytes.Equal(payload, snapshot.Canonical) {
		t.Fatal("valid canonical bytes failed the reader control", err)
	}
}

func TestE3FeedbackRunningRetestTransitionGuards(t *testing.T) {
	d := e3TestDB(t)
	for _, scenario := range []struct {
		controlled bool
		verdict    string
	}{{false, "fixed"}, {true, "fixed"}, {false, "reproduced"}, {true, "reproduced"}} {
		controlled := scenario.controlled
		t.Run(fmt.Sprintf("controlled=%t/%s", controlled, scenario.verdict), func(t *testing.T) {
			task, err := d.CreateTask("synthetic running retest", "synthetic only", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			node, err := d.Exploration(task.ExplorationID).AddNode(KindFinding, map[string]any{}, 5, "confirmed", "worker", nil)
			if err != nil {
				t.Fatal(err)
			}
			findingID, err := d.AddFinding(task.ID, node, "synthetic-legacy", "legacy", "low", "", "", "worker", nil)
			if err != nil {
				t.Fatal(err)
			}
			retest, _, _, err := d.CreateFindingRetest(t.Context(), findingID, "before controlled transition")
			if err != nil || retest == nil || retest.ConversationID == nil {
				t.Fatal("retest fixture", err)
			}
			if started, err := d.StartFindingRetest(t.Context(), retest.ID); err != nil || !started {
				t.Fatal("running retest fixture", started, err)
			}
			const summary, evidence = "synthetic staged result", "synthetic non-sensitive evidence"
			if err := d.RecordFindingRetestResult(t.Context(), *retest.ConversationID, scenario.verdict, summary, evidence); err != nil {
				t.Fatal("valid legacy staged result", err)
			}
			if controlled {
				if _, err := d.RegisterTaskMode(t.Context(), task.ID, taskmode.ModeControlled, nil); err != nil {
					t.Fatal(err)
				}
			}
			// An identical retry is valid for an ordinary running retest, so a
			// removed controlled guard cannot hide behind a changed-verdict error.
			// Reproduced completion has no finding-status write, isolating the
			// completed-only retest guard from the separate status-update guard.
			err = d.RecordFindingRetestResult(t.Context(), *retest.ConversationID, scenario.verdict, summary, evidence)
			if controlled && !errors.Is(err, ErrControlledFindingGeneric) || !controlled && err != nil {
				t.Fatal("running result transition guard", err)
			}
			err = d.FinishFindingRetest(retest.ID, "completed", "synthetic completion")
			if controlled && !errors.Is(err, ErrControlledFindingGeneric) || !controlled && err != nil {
				t.Fatal("running completion transition guard", err)
			}
			after, err := d.FindingRetestForConversation(t.Context(), *retest.ConversationID)
			if err != nil || after == nil || after.Verdict != scenario.verdict || after.Summary != summary || after.Evidence != evidence || after.StartedAt == nil {
				t.Fatal("staged retest state changed", err)
			}
			finding, err := d.GetFinding(findingID)
			if err != nil {
				t.Fatal(err)
			}
			if controlled {
				if after.Status != "running" || after.FinishedAt != nil || finding.Status != FindingPending {
					t.Fatal("controlled rejection promoted or sealed a running retest")
				}
				if err := d.FinishFindingRetest(retest.ID, "stopped", "synthetic cleanup"); err != nil {
					t.Fatal("controlled stopped cleanup rejected", err)
				}
				stopped, err := d.FindingRetestForConversation(t.Context(), *retest.ConversationID)
				if err != nil || stopped == nil || stopped.Status != "stopped" || stopped.FinishedAt == nil {
					t.Fatal("controlled cleanup did not seal as stopped", err)
				}
			} else {
				wantStatus := FindingPending
				if scenario.verdict == "fixed" {
					wantStatus = FindingFixed
				}
				if after.Status != "completed" || after.FinishedAt == nil || finding.Status != wantStatus {
					t.Fatal("ordinary legacy completion semantics changed")
				}
			}
		})
	}
}

func TestE3FeedbackDatabaseCanonicalSizeBoundary(t *testing.T) {
	d := e3TestDB(t)
	// This direct SQL fixture isolates the DB byte limit. The typed Prepare
	// contract is tested separately; this is not an accepted oracle record.
	for _, size := range []int{65536, 65537} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			const prefix, suffix = `{"synthetic":"`, `"}`
			payload := []byte(prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix)
			if len(payload) != size {
				t.Fatal("incorrect boundary fixture size")
			}
			digest := sha256.Sum256(payload)
			tx, err := d.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var id int64
			err = tx.QueryRow(`INSERT INTO e3_oracle_verdicts(record_version,payload_canonical,payload,payload_digest,oracle_value) VALUES(1,$1,$2::jsonb,$3,'undecidable') RETURNING id`,
				payload, string(payload), digest[:]).Scan(&id)
			if size == 65536 {
				if err != nil || id == 0 {
					t.Fatal("65536-byte canonical payload rejected", err)
				}
				var stored int
				if err := tx.QueryRow(`SELECT octet_length(payload_canonical) FROM e3_oracle_verdicts WHERE id=$1`, id).Scan(&stored); err != nil || stored != size {
					t.Fatal("boundary payload changed", stored, err)
				}
			} else {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "e3_oracle_verdicts_payload_canonical_check" {
					t.Fatal("65537-byte payload did not fail its byte constraint", err)
				}
			}
		})
	}
}
