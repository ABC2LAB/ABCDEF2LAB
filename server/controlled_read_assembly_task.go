package server

import (
	"context"
	"database/sql"
	"encoding/json"
	pgdb "github.com/Autumn-27/artex/db"
	"time"
)

type FirstDiagnosisPreparedTask struct {
	TaskID        int64 `json:"task_id"`
	ExplorationID int64 `json:"exploration_id"`
}

// 실제 DB ID를 발급받되 intent, 모델 호출, 엔진 실행은 시작하지 않는다.
// 생성과 paused 표시는 같은 트랜잭션이며 부분 task가 admission에 노출되지 않는다.
func PrepareFirstDiagnosisTask(ctx context.Context, pool *sql.DB, description, goal string, profileID int64, timeoutSeconds int) (FirstDiagnosisPreparedTask, error) {
	refuse := func() (FirstDiagnosisPreparedTask, error) {
		return FirstDiagnosisPreparedTask{}, ErrControlledReadHostConfiguration
	}
	if ctx == nil || ctx.Err() != nil || pool == nil || !validControlledPreparationText(description, 16<<10) || !validControlledPreparationText(goal, 16<<10) || profileID <= 0 || timeoutSeconds < 1 || timeoutSeconds > 900 {
		return refuse()
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := pool.BeginTx(bounded, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return refuse()
	}
	defer tx.Rollback()
	var id int64
	if tx.QueryRowContext(bounded, `SELECT id FROM llm_profiles WHERE id=$1`, profileID).Scan(&id) != nil || id != profileID {
		return refuse()
	}
	var out FirstDiagnosisPreparedTask
	if tx.QueryRowContext(bounded, `INSERT INTO explorations(description,goal) VALUES($1,$2) RETURNING id`, description, goal).Scan(&out.ExplorationID) != nil {
		return refuse()
	}
	payload, _ := json.Marshal(map[string]string{"summary": "Task origin: " + description + "; goal: " + goal, "description": description, "goal": goal})
	if _, err = tx.ExecContext(bounded, `INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin) VALUES($1,'fact',$2,0,'origin','system')`, out.ExplorationID, string(payload)); err != nil {
		return refuse()
	}
	if tx.QueryRowContext(bounded, `INSERT INTO tasks(description,goal,exploration_id,llm_profile_id,active_llm_profile_id,paused,queued,timeout_seconds,plan_heartbeat_seconds,coverage_enabled) VALUES($1,$2,$3,$4,$4,true,false,$5,$6,false) RETURNING id`, description, goal, out.ExplorationID, profileID, timeoutSeconds, pgdb.MinPlanHeartbeatSeconds).Scan(&out.TaskID) != nil {
		return refuse()
	}
	if _, err = tx.ExecContext(bounded, `INSERT INTO task_llm_profiles(task_id,profile_id,position) VALUES($1,$2,0)`, out.TaskID, profileID); err != nil {
		return refuse()
	}
	if bounded.Err() != nil || tx.Commit() != nil {
		return refuse()
	}
	return out, nil
}
