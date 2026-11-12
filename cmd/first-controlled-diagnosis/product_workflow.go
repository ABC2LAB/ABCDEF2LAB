package main

import (
	"context"
	"database/sql"
	"errors"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"strconv"
	"strings"
)

func (b *urlPreparationRuntime) createWorkflow(ctx context.Context, l *urlLoadedProfile, e diagnosisprep.Execution, taskID string, current func() error) (*diagnosisflow.Controller, *sql.DB, error) {
	if e.Job.Input.WorkflowMode == "" {
		return nil, nil, nil
	}
	if b.product == nil || b.product.targets.policy.Workflow == nil || l.productTarget == nil {
		return nil, nil, errPreparation
	}
	pin, err := l.databasePin()
	if err != nil {
		return nil, nil, errPreparation
	}
	pool, err := urlOpenDatabase(ctx, b.config, pin)
	if err != nil {
		return nil, nil, errPreparation
	}
	limits := *b.product.targets.policy.Workflow
	limits.MaxPages = min(limits.MaxPages, max(1, int(l.profile.Budget.Requests/2)))
	excluded := []string{}
	if e.Job.Input.ExcludedPaths != "" {
		excluded = strings.Split(e.Job.Input.ExcludedPaths, "\n")
	}
	binding := diagnosisflow.Binding{TargetRef: l.productTarget.Target.ID, TargetRevision: l.productTarget.Target.Revision, JobID: e.Job.ID, TaskID: taskID, RunRef: "run-" + e.Job.ID, SourceDigest: e.Job.SourceDigest, InputDigest: workflowInputDigest(e.Job.Input), Owner: e.Principal, Mode: e.Job.Input.WorkflowMode, TargetURL: e.Job.Input.TargetURL, PathPrefixes: l.productTarget.Target.PathPrefixes, ReadonlyPrefixes: l.productTarget.Target.PathPrefixes, Methods: l.productTarget.Target.Methods, Excluded: excluded, ReversibleWrites: l.productTarget.ReversibleWrites && limits.AllowReversibleWrites, Limits: limits, Current: current}
	flow, err := diagnosisflow.New(ctx, binding, diagnosisflow.Postgres{DB: pool})
	if err != nil {
		pool.Close()
		return nil, nil, errPreparation
	}
	b.product.flowMu.Lock()
	b.product.flows[e.Job.ID] = flow
	b.product.flowMu.Unlock()
	return flow, pool, nil
}
func workflowTerminal(stage diagnosisprep.State) string {
	switch stage {
	case diagnosisprep.Completed:
		return "completed"
	case diagnosisprep.Cancelled:
		return "stopped"
	case diagnosisprep.Unknown:
		return "unknown"
	default:
		return "failed"
	}
}
func (e productPreparationEndpoint) workflowControl(ctx context.Context, r diagnosisprep.Request) (*diagnosisflow.Snapshot, error) {
	request := r.Product
	bound, err := e.base.Call(ctx, diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: r.RequestID, Principal: r.Principal, Command: diagnosisprep.Status, JobID: request.JobID})
	if err != nil || bound.Error != "" || bound.Job == nil || bound.Job.Input.WorkflowMode == "" || bound.Job.TaskID == "" {
		return nil, errors.New("workflow_not_started")
	}
	job := bound.Job
	e.product.flowMu.RLock()
	flow := e.product.flows[job.ID]
	e.product.flowMu.RUnlock()
	var snapshot diagnosisflow.Snapshot
	if flow != nil {
		if request.Operation == "workflow_status" {
			snapshot, err = flow.SnapshotWithDisplay(ctx)
		} else {
			if job.State != diagnosisprep.Running && job.State != diagnosisprep.Preparing {
				return nil, errors.New("workflow_closed")
			}
			snapshot, err = flow.Command(ctx, r.Principal, strings.TrimPrefix(request.Operation, "workflow_"), request.CommandKey, request.ExpectedRevision, request.SelectedIDs)
		}
	} else {
		if request.Operation != "workflow_status" {
			return nil, errors.New("workflow_closed")
		}
		_, cfg, _, _, loadErr := e.product.model(job.Input.ProfileID)
		if loadErr != nil {
			return nil, errPreparation
		}
		raw, pin, loadErr := urlReadPin(cfg.RuntimeDSNFile, 16<<10, false)
		clear(raw)
		if loadErr != nil {
			return nil, errPreparation
		}
		pool, loadErr := urlOpenDatabase(ctx, e.product.config, pin)
		if loadErr != nil {
			return nil, errPreparation
		}
		defer pool.Close()
		record, readErr := (diagnosisflow.Postgres{DB: pool}).Read(ctx, r.Principal, job.ID)
		err = readErr
		snapshot = diagnosisflow.Project(record.Snapshot)
		if err == nil {
			snapshot.Plan, snapshot.Activity, err = (diagnosisflow.Postgres{DB: pool}).Display(ctx, r.Principal, job.ID)
		}
	}
	if err != nil {
		return nil, err
	}
	if snapshot.JobID != job.ID || snapshot.TaskID != job.TaskID || snapshot.SourceDigest != job.SourceDigest || snapshot.Mode != job.Input.WorkflowMode {
		return nil, errPreparation
	}
	switch job.State {
	case diagnosisprep.Completed, diagnosisprep.Cancelled, diagnosisprep.Failed, diagnosisprep.Unknown:
		snapshot.Stage = workflowTerminal(job.State)
		snapshot.Paused = false
	}
	if _, err = strconv.ParseInt(snapshot.TaskID, 10, 64); err != nil {
		return nil, errPreparation
	}
	snapshot = diagnosisflow.Project(snapshot)
	return &snapshot, nil
}
