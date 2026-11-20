package db

import (
	"errors"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/oracle"
)

func sealRecordFor(taskID int64) SealCheckRecord {
	return SealCheckRecord{TaskID: taskID, RunRef: "product-run-seal",
		Head:   ProductCheckpoint{JournalID: "journal-synthetic", Sequence: 20, MAC: strings.Repeat("e", 64)},
		Checks: []SealCheck{{Sequence: 12, MAC: strings.Repeat("c", 64), Included: true}, {Sequence: 15, MAC: strings.Repeat("d", 64), Included: false}}}
}

// 봉인 확인 기록은 회차마다 한 번만 INSERT하고 바꾸지 않는다(E1 계약 §6). 같은 기록을 다시 쓰면 그대로, 다른 기록이면 거부한다.
func TestRecordSealCheck(t *testing.T) {
	d := e3TestDB(t)
	f := e3FixtureFor(t, d, oracle.Allowed, true)
	if _, ok, err := d.SealCheck(t.Context(), f.taskID, "product-run-seal"); ok || err != nil {
		t.Fatal("기록 전", ok, err)
	}
	rec := sealRecordFor(f.taskID)
	if err := d.RecordSealCheck(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.SealCheck(t.Context(), f.taskID, rec.RunRef)
	if err != nil || !ok || got.Head != rec.Head || !equalSealChecks(got.Checks, rec.Checks) || got.RecordedAt.IsZero() {
		t.Fatalf("읽기: %+v %v %v", got, ok, err)
	}
	if err := d.RecordSealCheck(t.Context(), rec); err != nil {
		t.Fatal("같은 기록 다시 쓰기", err)
	}
	changed := sealRecordFor(f.taskID)
	changed.Checks[1].Included = true
	if err := d.RecordSealCheck(t.Context(), changed); !errors.Is(err, ErrSealCheckInput) {
		t.Fatal("다른 기록으로 바꿈", err)
	}
	if got, _, _ := d.SealCheck(t.Context(), f.taskID, rec.RunRef); got.Checks[1].Included {
		t.Fatal("기록이 바뀜")
	}
	for name, stmt := range map[string]string{
		"update":   `UPDATE e3_seal_checks SET head_sequence=21 WHERE task_id=$1`,
		"delete":   `DELETE FROM e3_seal_checks WHERE task_id=$1`,
		"truncate": `TRUNCATE e3_seal_checks`,
	} {
		args := []any{f.taskID}
		if name == "truncate" {
			args = nil
		}
		if _, err := d.ExecContext(t.Context(), stmt, args...); err == nil || !strings.Contains(err.Error(), "e3_immutable_record") {
			t.Fatalf("%s가 막히지 않음: %v", name, err)
		}
	}
}

func TestRecordSealCheckRejectsInvalid(t *testing.T) {
	d := e3TestDB(t)
	f := e3FixtureFor(t, d, oracle.Allowed, true)
	for name, mutate := range map[string]func(*SealCheckRecord){
		"no task":            func(r *SealCheckRecord) { r.TaskID = 0 },
		"bad run":            func(r *SealCheckRecord) { r.RunRef = "../run" },
		"bad journal":        func(r *SealCheckRecord) { r.Head.JournalID = "" },
		"upper head mac":     func(r *SealCheckRecord) { r.Head.MAC = strings.Repeat("E", 64) },
		"nil checks":         func(r *SealCheckRecord) { r.Checks = nil },
		"zero check":         func(r *SealCheckRecord) { r.Checks[0].Sequence = 0 },
		"bad check mac":      func(r *SealCheckRecord) { r.Checks[0].MAC = "x" },
		"included past head": func(r *SealCheckRecord) { r.Checks[0].Sequence = 21 },
		"duplicate check":    func(r *SealCheckRecord) { r.Checks[1].Sequence = r.Checks[0].Sequence },
		"too many": func(r *SealCheckRecord) {
			r.Checks = make([]SealCheck, maxSealChecks+1)
			for i := range r.Checks {
				r.Checks[i] = SealCheck{Sequence: uint64(i + 1), MAC: strings.Repeat("c", 64)}
			}
		},
	} {
		rec := sealRecordFor(f.taskID)
		mutate(&rec)
		if err := d.RecordSealCheck(t.Context(), rec); !errors.Is(err, ErrSealCheckInput) {
			t.Fatalf("%s 받음: %v", name, err)
		}
	}
	// 포함되지 않은 체크포인트는 봉인 head보다 뒤여도 기록할 수 있다(다른 분기).
	rec := sealRecordFor(f.taskID)
	rec.Checks = []SealCheck{{Sequence: 30, MAC: strings.Repeat("c", 64), Included: false}}
	if err := d.RecordSealCheck(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	// 저장된 기록이 계약 모양이 아니면(위조) 읽을 때 따로 알린다.
	tx, err := d.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	for _, forged := range []string{`[{"sequence":0,"mac":"x","included":true}]`, `[{"sequence":3,"mac":"` + strings.Repeat("c", 64) + `","included":true,"extra":1}]`} {
		if _, err := tx.ExecContext(t.Context(), `UPDATE e3_seal_checks SET checks=$2::jsonb WHERE task_id=$1`, f.taskID, forged); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := ReadSealCheck(t.Context(), tx, f.taskID, rec.RunRef); ok || !errors.Is(err, ErrSealCheckStored) {
			t.Fatalf("위조 기록 %s: %v %v", forged, ok, err)
		}
	}
}

// 근거 체크포인트가 없는 판정도 저장한다(manifest의 evidence_checkpoint는 null).
func TestRecordProductVerdictWithoutCheckpoint(t *testing.T) {
	d := e3TestDB(t)
	f := e3FixtureFor(t, d, oracle.Allowed, true)
	v := productVerdictFor(f.taskID, "product-run-nocp", "document", oracle.Undecidable)
	v.Checkpoint = nil
	out, err := d.RecordProductVerdict(t.Context(), v)
	if err != nil || !out.Created {
		t.Fatal(err)
	}
	var manifest string
	if err := d.QueryRowContext(t.Context(), `SELECT convert_from(canonical,'UTF8') FROM e3_evidence_manifests WHERE id=$1`, out.ManifestID).Scan(&manifest); err != nil {
		t.Fatal(err)
	}
	if manifest != `{"format":"e3-product-manifest/v2","run_ref":"product-run-nocp","evidence_checkpoint":null}` {
		t.Fatal("manifest", manifest)
	}
}
