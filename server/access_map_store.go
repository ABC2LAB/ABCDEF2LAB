package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/kgfixture"
	"github.com/Autumn-27/artex/transmit"
	"github.com/google/uuid"
)

const (
	accessMapOrigin     = "abc2lab.access-map.current/v1"
	accessMapTimeout    = 5 * time.Second
	maxAccessMapBytes   = 16 << 20
	maxAccessMapRecords = 2 * maxControlledSessions * maxControlledResources
)

var (
	errAccessMapUnavailable = errors.New("access_map_unavailable")
	errAccessMapScope       = errors.New("access_map_scope_unavailable")
)

type accessMapStore struct{ pool *sql.DB }

// 현재 범위는 등록 경로만 교체한다. 판정 시각이나 행 순서로 실행을 선택하지 않는다.
// 이 projection에는 비밀·표식 값·요청/응답 원문을 넣지 않는다.
type accessMapBinding struct {
	Version        int                     `json:"version"`
	TaskID         int64                   `json:"task_id"`
	ExplorationID  int64                   `json:"exploration_id"`
	IntentID       int64                   `json:"intent_id"`
	Registration   string                  `json:"registration"`
	Ready          bool                    `json:"ready"`
	ModeVersion    uint64                  `json:"mode_version"`
	Scope          transmit.Scope          `json:"scope"`
	SnapshotRef    string                  `json:"snapshot_ref"`
	SnapshotDigest [32]byte                `json:"snapshot_digest"`
	PolicyVersion  string                  `json:"policy_version"`
	GenerationRef  string                  `json:"generation_ref"`
	InstanceRef    string                  `json:"instance_ref"`
	Principals     []string                `json:"principals"`
	Resources      []kgfixture.MapResource `json:"resources"`
	Attempts       []accessMapAttempt      `json:"attempts"`
}

type accessMapAttempt struct {
	Principal string `json:"principal"`
	Resource  string `json:"resource_ref"`
}

// 기존 fact 저장소의 단일 현재 포인터를 task 행 잠금 아래 갱신한다.
// 실패한 재등록도 이전 세대를 현재 지도에 남겨 놓지 않는다.
func (s *accessMapStore) update(ctx context.Context, taskID int64, change func(*accessMapBinding) error) error {
	ctx, cancel := context.WithTimeout(ctx, accessMapTimeout)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return errAccessMapUnavailable
	}
	defer tx.Rollback()
	var explorationID int64
	if tx.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, taskID).Scan(&explorationID) != nil {
		return errAccessMapUnavailable
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,payload FROM exploration_nodes WHERE exploration_id=$1 AND origin=$2 FOR UPDATE`, explorationID, accessMapOrigin)
	if err != nil {
		return errAccessMapUnavailable
	}
	var nodeID int64
	var b accessMapBinding
	for rows.Next() {
		var raw []byte
		if nodeID != 0 || rows.Scan(&nodeID, &raw) != nil || len(raw) > maxAccessMapBytes || strictJSON(raw, &b) != nil {
			rows.Close()
			return errAccessMapUnavailable
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return errAccessMapUnavailable
	}
	if nodeID != 0 && (b.Version != 1 || b.TaskID != taskID || b.ExplorationID != explorationID) {
		return errAccessMapUnavailable
	}
	b.Version, b.TaskID, b.ExplorationID = 1, taskID, explorationID
	if err := change(&b); err != nil {
		return err
	}
	raw, err := json.Marshal(b)
	if err != nil || len(raw) > maxAccessMapBytes {
		return errAccessMapUnavailable
	}
	if nodeID == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO exploration_nodes(exploration_id,kind,state,origin,payload) VALUES($1,'fact','confirmed',$2,$3::jsonb)`, explorationID, accessMapOrigin, string(raw))
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE exploration_nodes SET payload=$1::jsonb WHERE id=$2 AND exploration_id=$3 AND origin=$4`, string(raw), nodeID, explorationID, accessMapOrigin)
	}
	if err != nil || tx.Commit() != nil {
		return errAccessMapUnavailable
	}
	return nil
}

func (s *accessMapStore) invalidate(ctx context.Context, task string) (string, error) {
	id, err := strconv.ParseInt(task, 10, 64)
	if err != nil || id <= 0 {
		return "", errAccessMapUnavailable
	}
	registration := uuid.NewString()
	err = s.update(ctx, id, func(b *accessMapBinding) error {
		*b = accessMapBinding{Version: 1, TaskID: id, ExplorationID: b.ExplorationID, Registration: registration}
		return nil
	})
	return registration, err
}

func (s *accessMapStore) register(ctx context.Context, spec controlledReadSpec) error {
	if spec.fixture == nil || spec.mapRegistration == "" {
		return errAccessMapUnavailable
	}
	resources, err := spec.fixture.MapResources()
	if err != nil {
		return errAccessMapUnavailable
	}
	id, err := strconv.ParseInt(spec.TaskID, 10, 64)
	if err != nil {
		return errAccessMapUnavailable
	}
	return s.update(ctx, id, func(b *accessMapBinding) error {
		if b.Registration != spec.mapRegistration || b.Ready {
			return errAccessMapScope
		}
		b.Ready, b.IntentID = true, spec.IntentID
		b.Scope = transmit.Scope{TaskRef: spec.TaskID, CaseRef: spec.CaseRef, ResetRef: spec.ResetRef, PolicyRef: spec.PolicyRef}
		b.SnapshotRef, b.SnapshotDigest, b.PolicyVersion = spec.fixture.Ref(), spec.fixture.Digest(), spec.fixture.PolicyVersion()
		b.GenerationRef, b.InstanceRef = spec.fixture.GenerationRef(), spec.fixture.InstanceRef()
		b.Principals, b.Resources, b.Attempts = spec.fixture.Principals(), resources, []accessMapAttempt{}
		return nil
	})
}

func (s *accessMapStore) start(ctx context.Context, spec controlledReadSpec, scope transmit.Scope) error {
	id, err := strconv.ParseInt(spec.TaskID, 10, 64)
	if err != nil {
		return errAccessMapUnavailable
	}
	var version uint64
	if s.pool.QueryRowContext(ctx, `SELECT COALESCE((SELECT version FROM task_control_modes WHERE task_id=$1),0)`, id).Scan(&version) != nil {
		return errAccessMapUnavailable
	}
	return s.update(ctx, id, func(b *accessMapBinding) error {
		if !b.Ready || b.Registration != spec.mapRegistration || b.IntentID != spec.IntentID || b.Scope.RunRef != "" ||
			b.Scope.TaskRef != scope.TaskRef || b.Scope.CaseRef != scope.CaseRef || b.Scope.ResetRef != scope.ResetRef || b.Scope.PolicyRef != scope.PolicyRef || scope.RunRef == "" {
			return errAccessMapScope
		}
		b.Scope, b.ModeVersion = scope, version
		return nil
	})
}

func (l *controlledReadLease) mapAttempt(ctx context.Context, resource string, principals ...string) error {
	if l.runtime.mapStore == nil || l.mapRegistration == "" {
		return nil
	}
	id, err := strconv.ParseInt(l.scope.TaskRef, 10, 64)
	if err != nil {
		return errAccessMapUnavailable
	}
	return l.runtime.mapStore.update(ctx, id, func(b *accessMapBinding) error {
		if !b.Ready || b.Registration != l.mapRegistration || b.Scope != l.scope {
			return errAccessMapScope
		}
		for _, principal := range principals {
			if !accessMapHas(b.Principals, principal) || !b.hasResource(resource) {
				return errAccessMapScope
			}
			a := accessMapAttempt{Principal: principal, Resource: resource}
			found := false
			for _, old := range b.Attempts {
				found = found || old == a
			}
			if !found {
				b.Attempts = append(b.Attempts, a)
			}
		}
		return nil
	})
}

func accessMapHas(values []string, value string) bool {
	for _, old := range values {
		if old == value {
			return true
		}
	}
	return false
}

func (b accessMapBinding) hasResource(ref string) bool {
	for _, r := range b.Resources {
		if r.ResourceRef == ref {
			return true
		}
	}
	return false
}

func (b accessMapBinding) hasPolicy(resource, owner, principal string) bool {
	for _, r := range b.Resources {
		if r.ResourceRef == resource && r.OwnerRef == owner {
			return accessMapHas(r.Principals, principal)
		}
	}
	return false
}
