// 인증된 팀 입력을 검사하며 실행 권한이나 관찰 결과를 만들지 않는다.
package server

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/internal/privateinput"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/taskmode"
	"github.com/Autumn-27/artex/transmit"
)

const (
	controlledPreparationTimeout         = 5 * time.Second
	controlledPreparationMinimumRequests = 2 // owner baseline and evaluated read
)

type ControlledReadAssemblyPreparation struct {
	SnapshotRef  string              `json:"-"`
	TargetOrigin string              `json:"-"`
	ModelTag     string              `json:"-"`
	Metadata     e3record.Source     `json:"-"`
	KG           idor.PolicyResolver `json:"-"`
}

func (ControlledReadAssemblyPreparation) Format(s fmt.State, _ rune) {
	fmt.Fprint(s, "controlled_read_assembly_preparation")
}
func (ControlledReadAssemblyPreparation) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

func CheckControlledReadAssemblyPreparation(ctx context.Context, manager *Manager, snapshotDirectory string, p ControlledReadAssemblyPreparation) error {
	if ctx == nil || ctx.Err() != nil || manager == nil || manager.pg == nil || manager.pg.DB == nil ||
		!validControlledPreparation(p) || privateinput.CheckDirectory(snapshotDirectory, true) != nil {
		return ErrControlledReadHostConfiguration
	}
	ctx, finish := context.WithTimeout(ctx, controlledPreparationTimeout)
	defer finish()
	expected := p.Metadata.Expected
	spec, err := (&controlledReadStore{snapshotDir: snapshotDirectory}).load(expected.Scope.TaskRef, expected.IntentID, p.SnapshotRef)
	if err != nil {
		return ErrControlledReadHostConfiguration
	}
	defer func() {
		clear(spec.Key)
		for i := range spec.HTTP.Sessions {
			clear(spec.HTTP.Sessions[i].Headers)
		}
	}()
	if checkControlledPreparationSnapshot(ctx, spec.HTTP, p) != nil {
		return ErrControlledReadHostConfiguration
	}
	taskID, err := strconv.ParseInt(expected.Scope.TaskRef, 10, 64)
	if err != nil || taskID <= 0 {
		return ErrControlledReadHostConfiguration
	}
	tx, err := manager.pg.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return ErrControlledReadHostConfiguration
	}
	defer tx.Rollback()
	var taskExploration, intentExploration int64
	var mode taskmode.Mode
	var version uint64
	var paused, queued bool
	var firstRun, completed sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT t.exploration_id,n.exploration_id,COALESCE(m.mode,''),COALESCE(m.version,0),t.paused,t.queued,t.first_run_at,t.completed_at
FROM tasks t
JOIN exploration_nodes n ON n.id=$2 AND n.kind='intent'
LEFT JOIN task_control_modes m ON m.task_id=t.id
WHERE t.id=$1`, taskID, expected.IntentID).Scan(&taskExploration, &intentExploration, &mode, &version, &paused, &queued, &firstRun, &completed)
	if err != nil || ctx.Err() != nil || taskExploration != p.Metadata.ExplorationID || intentExploration != taskExploration ||
		(!paused || queued || firstRun.Valid || completed.Valid) || (mode != taskmode.ModeControlled && mode != taskmode.ModeLegacy && !(mode == "" && version == 0)) || version != p.Metadata.ModeVersion {
		return ErrControlledReadHostConfiguration
	}
	if tx.Commit() != nil || ctx.Err() != nil {
		return ErrControlledReadHostConfiguration
	}
	return nil
}

func validControlledPreparation(p ControlledReadAssemblyPreparation) bool {
	s, e := p.Metadata, p.Metadata.Expected
	if !safeSnapshotRef.MatchString(p.SnapshotRef) || p.TargetOrigin == "" || !validControlledPreparationText(p.ModelTag, 256) || s.ModelTag != p.ModelTag ||
		nilPolicyResolver(p.KG) || !numericID.MatchString(e.Scope.TaskRef) || e.IntentID <= 0 || e.Scope.RunRef != "" ||
		s.ExplorationID <= 0 || s.KGSnapshotDigest == ([32]byte{}) || s.EvalRunID != "" ||
		e.Owner == e.Evaluated || e.Owner.Label == e.Evaluated.Label || e.Owner.Handle == e.Evaluated.Handle ||
		e.Owner.Label == "" || e.Owner.Handle == "" || e.Evaluated.Label == "" || e.Evaluated.Handle == "" ||
		e.Owner.Status != oracle.SubjectVerified || e.Evaluated.Status != oracle.SubjectVerified ||
		s.EvidenceRevision != 0 || s.EvidenceDigest != ([32]byte{}) || !s.RecordCreatedAt.IsZero() ||
		s.Completed || s.Finalized || s.FinalCheckpointRef != "" || s.FinalCheckpointDigest != ([32]byte{}) ||
		s.FinalCheckpointScope != (transmit.Scope{}) || s.FinalCheckpointSequence != 0 || len(s.TrafficEvidenceIDs) != 0 {
		return false
	}
	for _, ref := range []string{e.Scope.CaseRef, e.Scope.ResetRef, e.Scope.PolicyRef, e.ResourceRef, e.ReadApprovalRef, e.KGPolicyVersion,
		s.KGSnapshotRef, s.GenerationRef, s.InstanceRef, s.ResetGeneration} {
		if !safeRef.MatchString(ref) || strings.Contains(ref, "://") {
			return false
		}
	}
	return s.Severity == "critical" || s.Severity == "high" || s.Severity == "medium" || s.Severity == "low"
}

func checkControlledPreparationSnapshot(ctx context.Context, cfg transmit.Config, p ControlledReadAssemblyPreparation) error {
	if ctx == nil || ctx.Err() != nil || !validControlledPreparation(p) || cfg.Scope != p.Metadata.Expected.Scope ||
		cfg.Origin != p.TargetOrigin || !validFirstRegisteredLimits(cfg, time.Now()) {
		return ErrControlledReadHostConfiguration
	}
	e := p.Metadata.Expected
	for _, subject := range []idor.AccountRef{e.Owner, e.Evaluated} {
		matches := 0
		for _, session := range cfg.Sessions {
			if session.Account.Label == subject.Label || session.Account.Handle == subject.Handle {
				if session.Account != subject || !time.Now().Before(session.ExpiresAt) {
					return ErrControlledReadHostConfiguration
				}
				matches++
			}
		}
		if matches != 1 {
			return ErrControlledReadHostConfiguration
		}
	}
	resources := 0
	for _, resource := range cfg.Resources {
		if resource.Ref != e.ResourceRef {
			continue
		}
		if resource.ApprovalRef != e.ReadApprovalRef || !resource.ReadOnly {
			return ErrControlledReadHostConfiguration
		}
		owner, evaluated := false, false
		for _, account := range resource.Accounts {
			owner = owner || account == e.Owner.Label
			evaluated = evaluated || account == e.Evaluated.Label
		}
		if !owner || !evaluated {
			return ErrControlledReadHostConfiguration
		}
		resources++
	}
	if resources != 1 || checkControlledPreparationKG(ctx, p.KG, p.Metadata) != nil || ctx.Err() != nil {
		return ErrControlledReadHostConfiguration
	}
	return nil
}

func checkControlledPreparationKG(ctx context.Context, kg idor.PolicyResolver, source e3record.Source) error {
	if ctx == nil || ctx.Err() != nil || nilPolicyResolver(kg) {
		return ErrControlledReadHostConfiguration
	}
	e := source.Expected
	policy, ok := kg.ResolvePolicy(e.ResourceRef, e.Owner, e.Evaluated)
	if !ok || ctx.Err() != nil || !sameControlledPreparationPolicy(policy, source.Policy) {
		return ErrControlledReadHostConfiguration
	}
	now := time.Now()
	if !policy.HasRegisteredSource || !policy.HintMatchesCanonical || policy.ValidFrom.IsZero() || policy.ValidUntil.IsZero() ||
		now.Before(policy.ValidFrom) || !now.Before(policy.ValidUntil) ||
		(policy.ExpectedAccess != oracle.AccessAllow && policy.ExpectedAccess != oracle.AccessDeny) ||
		(policy.Sharing != oracle.ResolvedNone && policy.Sharing != oracle.ResolvedAllow) ||
		(policy.Delegation != oracle.ResolvedNone && policy.Delegation != oracle.ResolvedAllow) {
		return ErrControlledReadHostConfiguration
	}
	markers, ok := kg.Canaries(e.ResourceRef, e.Owner)
	if !ok || ctx.Err() != nil {
		return ErrControlledReadHostConfiguration
	}
	fields := make([]string, 0, len(markers))
	for _, marker := range markers {
		if !marker.Trivial && marker.Value != "" {
			fields = append(fields, marker.Field)
		}
	}
	if len(fields) == 0 || !sameControlledPreparationFields(fields, source.CanaryFields) {
		return ErrControlledReadHostConfiguration
	}
	return nil
}

func sameControlledPreparationPolicy(a, b oracle.PolicyAssertion) bool {
	return a.ExpectedAccess == b.ExpectedAccess && a.Sharing == b.Sharing && a.Delegation == b.Delegation &&
		a.HasRegisteredSource == b.HasRegisteredSource && a.HintMatchesCanonical == b.HintMatchesCanonical &&
		a.ValidFrom.Equal(b.ValidFrom) && a.ValidUntil.Equal(b.ValidUntil)
}

func sameControlledPreparationFields(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, field := range a {
		if !validControlledPreparationText(field, 256) || seen[field] {
			return false
		}
		seen[field] = true
	}
	for _, field := range b {
		if !seen[field] {
			return false
		}
		delete(seen, field)
	}
	return len(seen) == 0
}

func validControlledPreparationText(s string, limit int) bool {
	if len(s) == 0 || len(s) > limit || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// 첫 비평가 owner/cross 조회는 별도 두 GET 한도이며 일반 HTTP 예산·health를 섞지 않는다.
func validFirstRegisteredLimits(cfg transmit.Config, now time.Time) bool {
	return cfg.MaxRequests == controlledPreparationMinimumRequests && cfg.MinInterval >= 500*time.Millisecond && cfg.RequestTimeout > 0 && cfg.RequestTimeout <= 10*time.Second && cfg.MaxBodyBytes > 0 && cfg.MaxBodyBytes <= 1<<20 && cfg.Health == nil && cfg.CumulativeBudget == nil && cfg.ExpiresAt.After(now) && !cfg.ExpiresAt.After(now.Add(15*time.Minute))
}
