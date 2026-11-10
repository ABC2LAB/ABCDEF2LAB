package diagnosisflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/Autumn-27/artex/dlp"
	"strconv"
	"time"
)

type Postgres struct{ DB *sql.DB }

// Migrate는 설치자만 호출한다. API·실행 프로세스는 DDL 권한을 받지 않는다.
func Migrate(ctx context.Context, pool *sql.DB) error {
	if pool == nil {
		return ErrUnavailable
	}
	_, err := pool.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS diagnosis_workflows (
 job_id text PRIMARY KEY, owner_ref text NOT NULL, task_id bigint NOT NULL REFERENCES tasks(id), revision bigint NOT NULL CHECK(revision>0), record jsonb NOT NULL);
 CREATE UNIQUE INDEX IF NOT EXISTS diagnosis_workflows_task_idx ON diagnosis_workflows(task_id);
 CREATE TABLE IF NOT EXISTS diagnosis_workflow_events (job_id text NOT NULL REFERENCES diagnosis_workflows(job_id), revision bigint NOT NULL, record jsonb NOT NULL, PRIMARY KEY(job_id,revision));`)
	return err
}
func (p Postgres) Create(ctx context.Context, r Record) error           { return p.write(ctx, 0, r) }
func (p Postgres) Save(ctx context.Context, old uint64, r Record) error { return p.write(ctx, old, r) }
func (p Postgres) write(ctx context.Context, old uint64, r Record) error {
	if p.DB == nil || r.Snapshot.Revision != old+1 || r.Owner == "" {
		return ErrUnavailable
	}
	n, err := strconv.ParseInt(r.Snapshot.TaskID, 10, 64)
	if err != nil || n <= 0 {
		return ErrUnavailable
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > 128<<10 {
		return ErrUnavailable
	}
	call, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, err := p.DB.BeginTx(call, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	var result sql.Result
	if old == 0 {
		result, err = tx.ExecContext(call, `INSERT INTO diagnosis_workflows(job_id,owner_ref,task_id,revision,record) VALUES($1,$2,$3,$4,$5::jsonb)`, r.Snapshot.JobID, r.Owner, n, r.Snapshot.Revision, string(raw))
	} else {
		result, err = tx.ExecContext(call, `UPDATE diagnosis_workflows SET revision=$1,record=$2::jsonb WHERE job_id=$3 AND owner_ref=$4 AND task_id=$5 AND revision=$6`, r.Snapshot.Revision, string(raw), r.Snapshot.JobID, r.Owner, n, old)
	}
	if err != nil {
		return ErrUnavailable
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(call, `INSERT INTO diagnosis_workflow_events(job_id,revision,record) VALUES($1,$2,$3::jsonb)`, r.Snapshot.JobID, r.Snapshot.Revision, string(raw)); err != nil {
		return ErrUnavailable
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}
func (p Postgres) Read(ctx context.Context, owner, id string) (Record, error) {
	var r Record
	var raw []byte
	if p.DB == nil || p.DB.QueryRowContext(ctx, `SELECT record FROM diagnosis_workflows WHERE job_id=$1 AND owner_ref=$2`, id, owner).Scan(&raw) != nil || len(raw) > 128<<10 || json.Unmarshal(raw, &r) != nil || r.Owner != owner || r.Snapshot.JobID != id || r.Snapshot.Format != Version {
		return r, ErrUnavailable
	}
	return r, nil
}

// Display는 workflow가 결속한 실제 task에서 제한된 계획·활동만 조회한다.
// 도구 입력/원문 detail/자격값은 공개 projection으로 복사하지 않는다.
func (p Postgres) Display(ctx context.Context, owner, id string) ([]Plan, []Activity, error) {
	plans := []Plan{}
	activities := []Activity{}
	rows, err := p.DB.QueryContext(ctx, `SELECT n.id,n.state,COALESCE(n.payload->>'summary',n.payload->>'description',n.payload->>'title','설명 미제공') FROM diagnosis_workflows w JOIN tasks t ON t.id=w.task_id JOIN exploration_nodes n ON n.exploration_id=t.exploration_id WHERE w.job_id=$1 AND w.owner_ref=$2 AND n.kind='intent' ORDER BY n.id LIMIT 20`, id, owner)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	for rows.Next() {
		var v Plan
		if rows.Scan(&v.ID, &v.State, &v.Title) != nil {
			rows.Close()
			return nil, nil, ErrUnavailable
		}
		v.Title = displayText(v.Title, 240)
		plans = append(plans, v)
	}
	if rows.Err() != nil {
		rows.Close()
		return nil, nil, ErrUnavailable
	}
	rows.Close()
	rows, err = p.DB.QueryContext(ctx, `SELECT a.id,a.created_at,COALESCE(a.worker,''),COALESCE(a.kind,''),COALESCE(a.summary,'') FROM diagnosis_workflows w JOIN tasks t ON t.id=w.task_id JOIN activity a ON a.exploration_id=t.exploration_id WHERE w.job_id=$1 AND w.owner_ref=$2 ORDER BY a.id DESC LIMIT 20`, id, owner)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var v Activity
		if rows.Scan(&v.ID, &v.At, &v.Worker, &v.Kind, &v.Summary) != nil {
			return nil, nil, ErrUnavailable
		}
		v.Worker = displayText(v.Worker, 64)
		v.Kind = displayText(v.Kind, 64)
		v.Summary = displayText(v.Summary, 240)
		activities = append(activities, v)
	}
	return plans, activities, rows.Err()
}

func displayText(raw string, limit int) string {
	r := []rune(dlp.DiagnosticText(raw))
	if len(r) > limit {
		return string(r[:limit]) + "…"
	}
	out := string(r)
	for {
		encoded, _ := json.Marshal(out)
		if len(encoded) <= 512 || len(out) == 0 {
			return out
		}
		r = []rune(out)
		out = string(r[:len(r)-1])
	}
}
