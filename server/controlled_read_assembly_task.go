package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	pgdb "github.com/Autumn-27/artex/db"
	"net/url"
	"strconv"
	"strings"
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

// SeedFirstDiagnosisTarget은 원본 작업 시작의 대상 자산 등록(seed)을 첫 진단 준비에서 한다(C24, 루프 복원 결정안
// §9-5). 진단 Planner의 list_assets·list_untested_assets가 대상에서 시작하게 한다. 작업 설명 글을 파싱하지 않고,
// 승인 범위로 검사한 대상 URL의 origin 하나만 등록한다. 실행 역할의 자산 쓰기 권한(#481)이 필요하다.
func SeedFirstDiagnosisTarget(ctx context.Context, pool *sql.DB, prepared FirstDiagnosisPreparedTask, targetURL string) error {
	if ctx == nil || ctx.Err() != nil || pool == nil || prepared.TaskID <= 0 || prepared.ExplorationID <= 0 {
		return ErrControlledReadHostConfiguration
	}
	u, err := url.Parse(targetURL)
	if err != nil || u.User != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ErrControlledReadHostConfiguration
	}
	port := 443
	if u.Scheme == "http" {
		port = 80
	}
	if raw := u.Port(); raw != "" {
		if port, err = strconv.Atoi(raw); err != nil || port < 1 || port > 65535 {
			return ErrControlledReadHostConfiguration
		}
	}
	store := &pgdb.DB{DB: pool}
	if _, err := seedTargetAsset(store.Assets(), store.Exploration(prepared.ExplorationID), prepared.TaskID, u.Scheme, strings.ToLower(u.Hostname()), port); err != nil {
		return errors.Join(ErrControlledReadHostConfiguration, err)
	}
	return nil
}
