package db

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Autumn-27/artex/e2prompt"
)

var ErrE2Prompt = errors.New("e2_prompt_storage_unavailable")

// 새 실행의 단회 등록이다. run_ref 재사용은 과거 관측과의 혼합을 막는다.
func (s *ExplorationStore) RegisterE2PromptRun(ctx context.Context, task int64, run string) error {
	if s == nil || s.db == nil || ctx == nil || task <= 0 || !e2prompt.ValidRun(run) {
		return ErrE2Prompt
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO e2_prompt_runs(run_ref,task_id,exploration_id)
 SELECT $1,t.id,t.exploration_id FROM tasks t WHERE t.id=$2 AND t.exploration_id=$3 AND t.deleted_at IS NULL
 ON CONFLICT DO NOTHING`, run, task, s.expID)
	if err != nil {
		return ErrE2Prompt
	}
	n, err := result.RowsAffected()
	if err != nil {
		return ErrE2Prompt
	}
	if n != 1 {
		if _, err := s.db.ExecContext(ctx, `UPDATE e2_prompt_runs SET failed=true WHERE run_ref=$1 AND task_id=$2 AND exploration_id=$3`, run, task, s.expID); err != nil {
			return ErrE2Prompt
		}
		return ErrE2Prompt
	}
	return nil
}

// BeginE2PromptWork는 실제 E② 실행을 확인한 host만 호출한다. 모델 도구에 노출하지 않는다.
func (s *ExplorationStore) BeginE2PromptWork(ctx context.Context, task int64, run string, intent int64) (int64, error) {
	if s == nil || s.db == nil || ctx == nil || task <= 0 || intent <= 0 || !e2prompt.ValidRun(run) {
		return 0, ErrE2Prompt
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, ErrE2Prompt
	}
	defer tx.Rollback()
	var seq int64
	err = tx.QueryRowContext(ctx, `UPDATE e2_prompt_runs r SET next_sequence=next_sequence+1
 WHERE run_ref=$1 AND task_id=$2 AND exploration_id=$3 AND NOT sealed AND next_sequence<$5
 AND EXISTS(SELECT 1 FROM exploration_nodes n WHERE n.id=$4 AND n.exploration_id=$3 AND n.kind='intent' AND n.state IN ('open','running'))
 RETURNING next_sequence`, run, task, s.expID, intent, e2prompt.MaxObservations).Scan(&seq)
	if err != nil {
		return 0, ErrE2Prompt
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO e2_prompt_observations(run_ref,sequence,intent_id) VALUES($1,$2,$3)`, run, seq, intent); err != nil {
		return 0, ErrE2Prompt
	}
	if tx.Commit() != nil {
		return 0, ErrE2Prompt
	}
	return seq, nil
}

// SaveE2PromptActivity는 Activity와 신뢰 실행의 출처 연결을 원자적으로 저장한다.
func (s *ExplorationStore) SaveE2PromptActivity(ctx context.Context, task int64, run string, seq, intent int64, worker string, detail []byte) error {
	if _, err := e2prompt.Parse(detail); err != nil || !e2prompt.ValidWorker(worker) {
		return ErrE2Prompt
	}
	if s == nil || s.db == nil || ctx == nil {
		return ErrE2Prompt
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrE2Prompt
	}
	defer tx.Rollback()
	var sealed bool
	if tx.QueryRowContext(ctx, `SELECT sealed FROM e2_prompt_runs WHERE run_ref=$1 AND task_id=$2 AND exploration_id=$3 FOR UPDATE`, run, task, s.expID).Scan(&sealed) != nil || sealed {
		return ErrE2Prompt
	}
	var existing sql.NullInt64
	if tx.QueryRowContext(ctx, `SELECT activity_id FROM e2_prompt_observations WHERE run_ref=$1 AND sequence=$2 AND intent_id=$3`, run, seq, intent).Scan(&existing) != nil || existing.Valid {
		return ErrE2Prompt
	}
	var activity int64
	err = tx.QueryRowContext(ctx, `INSERT INTO activity(exploration_id,node_id,worker,kind,tool,is_error,summary,detail,metadata)
 SELECT $1,n.id,$3,'e2_prompt','E2HTTP',false,'E② 워커 프롬프트 적용',$4,'{}' FROM exploration_nodes n
 WHERE n.id=$2 AND n.exploration_id=$1 AND n.kind='intent' RETURNING id`, s.expID, intent, worker, string(detail)).Scan(&activity)
	if err != nil {
		return ErrE2Prompt
	}
	if _, err = tx.ExecContext(ctx, `UPDATE e2_prompt_observations SET activity_id=$3,worker=$4 WHERE run_ref=$1 AND sequence=$2`, run, seq, activity, worker); err != nil {
		return ErrE2Prompt
	}
	if tx.Commit() != nil {
		return ErrE2Prompt
	}
	return nil
}

func (s *ExplorationStore) SealE2PromptRun(ctx context.Context, task int64, run string, healthy bool) error {
	if s == nil || s.db == nil || ctx == nil || task <= 0 || !e2prompt.ValidRun(run) {
		return ErrE2Prompt
	}
	result, err := s.db.ExecContext(ctx, `UPDATE e2_prompt_runs SET sealed=true,failed=failed OR NOT $4
 WHERE run_ref=$1 AND task_id=$2 AND exploration_id=$3`, run, task, s.expID, healthy)
	if err != nil {
		return ErrE2Prompt
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrE2Prompt
	}
	return nil
}

// E2PromptProjection는 페이지 API를 사용하지 않는다. 원장 잠금과 한 snapshot으로 전체를 읽는다.
func (s *ExplorationStore) E2PromptProjection(ctx context.Context, task int64, run string) (e2prompt.Projection, error) {
	if s == nil || s.db == nil {
		return e2prompt.Projection{}, ErrE2Prompt
	}
	p := e2prompt.Projection{Format: e2prompt.Format, TaskID: task, ExplorationID: s.expID, RunRef: run, Observations: []e2prompt.Observation{}}
	if ctx == nil || !e2prompt.ValidRun(run) || task <= 0 {
		return p, ErrE2Prompt
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return p, ErrE2Prompt
	}
	defer tx.Rollback()
	var sealed, failed bool
	err = tx.QueryRowContext(ctx, `SELECT r.sealed,r.failed,r.next_sequence FROM e2_prompt_runs r JOIN tasks t ON t.id=r.task_id AND t.exploration_id=r.exploration_id
 WHERE r.run_ref=$1 AND r.task_id=$2 AND r.exploration_id=$3 AND t.deleted_at IS NULL FOR SHARE OF r`, run, task, s.expID).Scan(&sealed, &failed, &p.ExpectedCount)
	if err != nil {
		return p, ErrE2Prompt
	}
	p.SnapshotSequence = p.ExpectedCount
	if p.ExpectedCount > e2prompt.MaxObservations {
		return p, ErrE2Prompt
	}
	rows, err := tx.QueryContext(ctx, `SELECT o.sequence,o.intent_id,o.activity_id,COALESCE(o.worker,''),
 COALESCE(a.exploration_id,0),COALESCE(a.node_id,0),COALESCE(a.worker,''),COALESCE(a.kind,''),COALESCE(a.tool,''),COALESCE(a.is_error,true),COALESCE(CASE WHEN octet_length(a.detail)<=$4 THEN a.detail ELSE '' END,''),COALESCE(n.kind,'')
 FROM e2_prompt_observations o LEFT JOIN activity a ON a.id=o.activity_id
 LEFT JOIN exploration_nodes n ON n.id=o.intent_id AND n.exploration_id=$2
 WHERE o.run_ref=$1 ORDER BY o.sequence LIMIT $3`, run, s.expID, e2prompt.MaxObservations+1, e2prompt.MaxDetailBytes)
	if err != nil {
		return p, ErrE2Prompt
	}
	var count int64
	for rows.Next() {
		var o e2prompt.Observation
		var activity sql.NullInt64
		var exp, node int64
		var aw, kind, tool, detail, nkind string
		var isError bool
		if rows.Scan(&o.Sequence, &o.IntentID, &activity, &o.Worker, &exp, &node, &aw, &kind, &tool, &isError, &detail, &nkind) != nil {
			rows.Close()
			return p, ErrE2Prompt
		}
		count++
		if o.Sequence != count || o.Sequence > p.ExpectedCount || nkind != "intent" {
			p.State = "invalid"
		}
		if !activity.Valid {
			continue
		}
		p.ObservedCount++
		o.ActivityID = activity.Int64
		identity, parseErr := e2prompt.Parse([]byte(detail))
		if parseErr != nil || exp != s.expID || node != o.IntentID || aw != o.Worker || !e2prompt.ValidWorker(o.Worker) || kind != "e2_prompt" || tool != "E2HTTP" || isError {
			p.State = "invalid"
			continue
		}
		o.Identity = identity
		p.Observations = append(p.Observations, o)
	}
	if rows.Err() != nil {
		rows.Close()
		return p, ErrE2Prompt
	}
	rows.Close()
	if count != p.ExpectedCount {
		p.State = "invalid"
	}
	if tx.Commit() != nil {
		return p, ErrE2Prompt
	}
	p.Finalize(sealed, failed)
	return p, nil
}
