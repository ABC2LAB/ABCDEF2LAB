package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/taskmode"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/google/uuid"
)

const controlledReadInputLimit = 4096

var (
	errControlledReadUnavailable = errors.New("controlled_read_unavailable")
	errControlledReadNotPaused   = errors.New("controlled_read_requires_paused_task")
	errControlledReadBusy        = errors.New("controlled_read_already_active")
	errControlledReadInput       = errors.New("controlled_read_invalid_input")
)

type controlledWorkKey struct {
	task   string
	intent int64
}

// The registry is server-owned, never supplied in task/intent JSON. Disk markers
// distinguish a normal task from a controlled task missing its one-use lease.
type controlledReadRuntime struct {
	store        *controlledReadStore
	mu           sync.Mutex
	pending      map[controlledWorkKey]controlledReadSpec
	active       map[controlledWorkKey]bool
	closed       bool
	verification *controlledReadVerificationConfig
	mode         *controlledTaskModeConfig
	publisher    *controlledReadPublisherConfig
	quiescing    map[string]bool
	tokens       map[controlledWorkKey]*controlledReadTransition
}

func newControlledReadRuntime(store *controlledReadStore) *controlledReadRuntime {
	return &controlledReadRuntime{store: store, pending: make(map[controlledWorkKey]controlledReadSpec), active: make(map[controlledWorkKey]bool), quiescing: make(map[string]bool), tokens: make(map[controlledWorkKey]*controlledReadTransition)}
}

func (r *controlledReadRuntime) discardTask(taskID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.tokens {
		if key.task == taskID {
			delete(r.tokens, key)
		}
	}
	for key, spec := range r.pending {
		if key.task == taskID {
			clear(spec.Key)
			delete(r.pending, key)
		}
	}
	for key := range r.active {
		if key.task == taskID {
			delete(r.active, key)
		}
	}
}

func (r *controlledReadRuntime) discardAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, spec := range r.pending {
		clear(spec.Key)
		delete(r.pending, key)
	}
	clear(r.active)
	clear(r.tokens)
	r.closed = true
}

// cancelPendingWork serializes intent deletion with registration's state CAS.
// Revocation is conservative even when the database mutation later fails.
func (r *controlledReadRuntime) cancelPendingWork(key controlledWorkKey, mutate func() error) error {
	r.mu.Lock()
	if r.mode != nil {
		delete(r.tokens, key)
		if spec, exists := r.pending[key]; exists {
			clear(spec.Key)
			delete(r.pending, key)
		}
		r.mu.Unlock()
		return mutate()
	}
	defer r.mu.Unlock()
	if spec, exists := r.pending[key]; exists {
		clear(spec.Key)
		delete(r.pending, key)
	}
	return mutate()
}

func (s *Server) initControlledReads(_ string, dataDir string) error {
	snapshotDir, stateDir := os.Getenv("ARTEX_E2_SNAPSHOT_DIR"), os.Getenv("ARTEX_E2_STATE_DIR")
	// The data directory is the persisted runtime store and is git-ignored in
	// source runs. keyDir can be the checkout root or an unpersisted image path.
	if snapshotDir == "" && dataDir != "" {
		snapshotDir = filepath.Join(dataDir, "e2-snapshots")
	}
	if stateDir == "" && dataDir != "" {
		stateDir = filepath.Join(dataDir, "e2-state")
	}
	store, err := openControlledReadStore(snapshotDir, stateDir)
	if err != nil {
		// Startup must fail explicitly: an unreadable store cannot distinguish an
		// ordinary task from a persisted controlled task without its authority.
		return errControlledReadUnavailable
	}
	s.engine.controlledReads = newControlledReadRuntime(store)
	context.AfterFunc(s.ctx, s.engine.controlledReads.discardAll)
	return nil
}

func (e *Engine) controlledTask(taskID string) bool {
	if e == nil || e.controlledReads == nil {
		return false
	}
	if e.controlledReads.mode != nil {
		decision, err := e.resolveControlledTaskMode(e.controlledReads.mode.lifecycle, taskID)
		return err != nil || decision.Route() != taskmode.Legacy
	}
	if e.controlledReads.store == nil {
		return true
	}
	marked, err := e.controlledReads.store.taskMarked(taskID)
	return err != nil || marked
}

// registerControlledRead is called only by the authenticated API with an opaque
// snapshot reference. Pause, active-work check and publication serialize with
// acquire, so a racing resume cannot enter an ordinary worker past the marker.
func (e *Engine) registerControlledRead(ctx context.Context, task *Task, intentID int64, snapshotRef string, makeRunnable ...func() error) (err error) {
	defer func() {
		if err != nil {
			reason := "runtime_unavailable"
			if errors.Is(err, errControlledReadNotPaused) {
				reason = "requires_paused_task"
			}
			log.Printf("[controlled-read] registration_rejected reason=%s", reason)
		}
	}()
	r := e.controlledReads
	if r == nil || r.store == nil || task == nil {
		return errControlledReadUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errControlledReadUnavailable
	}
	key := controlledWorkKey{task.ID, intentID}
	canArm := func() bool {
		lifecycle := task.lifecycleSnapshot()
		if ctx.Err() != nil || !e.IsPaused(task.ID) || !lifecycle.Paused || isTerminalStatus(lifecycle.Status) || e.IsDeleting(task.ID) {
			return false
		}
		e.workMu.Lock()
		_, running := e.work[intentID]
		e.workMu.Unlock()
		return !running && !r.active[key]
	}
	if !canArm() {
		return errControlledReadNotPaused
	}
	// Persist the downgrade barrier before any fallible secret/profile load.
	if err := r.store.mark(task.ID, intentID); err != nil {
		log.Print("[controlled-read] registration_failed reason=marker_write_failed")
		return errControlledReadUnavailable
	}
	if previous, exists := r.pending[key]; exists {
		clear(previous.Key)
		delete(r.pending, key)
	}
	spec, err := r.store.load(task.ID, intentID, snapshotRef)
	if err != nil {
		log.Print("[controlled-read] registration_failed reason=snapshot_load_failed")
		return errControlledReadUnavailable
	}
	if err := validateControlledReadSpec(spec); err != nil {
		log.Print("[controlled-read] registration_failed reason=snapshot_validation_failed")
		clear(spec.Key)
		return errControlledReadUnavailable
	}
	if !canArm() {
		clear(spec.Key)
		return errControlledReadNotPaused
	}
	if len(makeRunnable) > 0 && makeRunnable[0] != nil {
		if err := makeRunnable[0](); err != nil {
			log.Print("[controlled-read] registration_failed reason=intent_transition_failed")
			clear(spec.Key)
			return errControlledReadUnavailable
		}
		if !canArm() {
			clear(spec.Key)
			return errControlledReadNotPaused
		}
	}
	r.pending[key] = spec
	return nil
}

type controlledReadValidationRecorder struct{}

func (controlledReadValidationRecorder) Record(context.Context, transmit.Event) error {
	return errControlledReadUnavailable // validation can never authorize a send
}

func validateControlledReadSpec(spec controlledReadSpec) error {
	for _, ref := range []string{spec.CaseRef, spec.ResetRef, spec.PolicyRef, spec.KeyRef} {
		if !safeRef.MatchString(ref) || strings.Contains(ref, "://") {
			return errControlledReadUnavailable
		}
	}
	scope := transmit.Scope{RunRef: "validation", TaskRef: spec.TaskID, CaseRef: spec.CaseRef, ResetRef: spec.ResetRef, PolicyRef: spec.PolicyRef}
	cfg := controlledReadHTTPConfig(spec, scope, controlledReadValidationRecorder{})
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		return errControlledReadUnavailable
	}
	tx.Stop()
	return nil
}

type controlledReadLease struct {
	tx           *transmit.HTTPTransmitter
	journal      *auditlog.Journal
	recorder     *controlledCheckpointRecorder
	store        *controlledReadStore
	scope        transmit.Scope
	sessions     map[string]idor.AccountRef
	accounts     []string
	resources    []string
	timeout      time.Duration
	runtime      *controlledReadRuntime
	work         controlledWorkKey
	once         sync.Once
	closeErr     error
	resultMu     sync.Mutex
	observed     bool
	denied       bool
	verification *controlledReadVerificationState
}

// prepareControlledRead is invoked once per runIntent, outside model retries.
// A tombstone without a pending registration is a hard refusal, never fallback.
func (e *Engine) prepareControlledRead(ctx context.Context, task *Task, intent *db.Node) (context.Context, func(...bool) error, error) {
	r := e.controlledReads
	if r == nil {
		return ctx, nil, nil
	}
	if r.store == nil || task == nil || intent == nil {
		return ctx, nil, errControlledReadUnavailable
	}
	if r.mode != nil {
		decision, err := e.resolveControlledTaskMode(ctx, task.ID)
		if err != nil || decision.Route() == taskmode.Block {
			return ctx, nil, errControlledReadUnavailable
		}
		if decision.Route() == taskmode.Legacy {
			return ctx, nil, nil
		}
	}
	key := controlledWorkKey{task.ID, intent.ID}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ctx, nil, errControlledReadUnavailable
	}
	marked, err := r.store.marked(task.ID, intent.ID)
	if err != nil {
		r.mu.Unlock()
		return ctx, nil, errControlledReadUnavailable
	}
	if !marked {
		r.mu.Unlock()
		return ctx, nil, nil
	}
	spec, exists := r.pending[key]
	if !exists || r.active[key] || ctx.Err() != nil {
		r.mu.Unlock()
		return ctx, nil, errControlledReadUnavailable
	}
	delete(r.pending, key) // consumed once, including initialization failures
	r.active[key] = true
	verificationHost := r.verification
	publisherHost := r.publisher
	r.mu.Unlock()
	defer clear(spec.Key)
	release := func() { r.mu.Lock(); delete(r.active, key); r.mu.Unlock() }
	if spec.TaskID != task.ID || spec.IntentID != intent.ID {
		release()
		return ctx, nil, errControlledReadUnavailable
	}
	runRef := "e2-" + uuid.NewString()
	scope := transmit.Scope{RunRef: runRef, TaskRef: task.ID, CaseRef: spec.CaseRef, ResetRef: spec.ResetRef, PolicyRef: spec.PolicyRef}
	var journal *auditlog.Journal
	var published *controlledPublishedJournal
	if publisherHost != nil {
		published, err = r.openPublishedJournal(ctx, publisherHost, spec, scope)
		if err != nil {
			release()
			return ctx, nil, errControlledReadUnavailable
		}
	} else {
		journal, err = auditlog.Create(r.store.journalDirectory(), runRef+".jsonl", auditlog.Config{Scope: scope, KeyRef: spec.KeyRef, Key: spec.Key})
		if err != nil {
			release()
			return ctx, nil, errControlledReadUnavailable
		}
		if err := r.store.saveCheckpoint(ctx, journal.Checkpoint()); err != nil {
			_ = journal.Close()
			release()
			return ctx, nil, errControlledReadUnavailable
		}
	}
	recorder := &controlledCheckpointRecorder{journal: journal, published: published, store: r.store}
	cfg := controlledReadHTTPConfig(spec, scope, recorder)
	httpCtx := ctx
	if published != nil {
		httpCtx = published.httpContext(ctx)
	}
	tx, err := transmit.NewHTTP(httpCtx, cfg)
	if err != nil || httpCtx.Err() != nil {
		if tx != nil {
			tx.StopAndWait()
		}
		if published != nil {
			published.abort()
		} else {
			_ = journal.Close()
		}
		release()
		return ctx, nil, errControlledReadUnavailable
	}
	if published != nil {
		published.drain.bind(tx)
	}
	lease := &controlledReadLease{tx: tx, journal: journal, recorder: recorder, store: r.store, scope: scope,
		timeout: cfg.RequestTimeout, runtime: r, work: key, sessions: make(map[string]idor.AccountRef)}
	for _, session := range cfg.Sessions {
		lease.sessions[session.Account.Label] = session.Account
		lease.accounts = append(lease.accounts, session.Account.Label)
	}
	for _, resource := range cfg.Resources {
		lease.resources = append(lease.resources, resource.Ref)
	}
	sort.Strings(lease.accounts)
	sort.Strings(lease.resources)
	taskID, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil || taskID <= 0 {
		lease.close()
		return ctx, nil, errControlledReadUnavailable
	}
	if verificationHost != nil {
		lease.verification = &controlledReadVerificationState{host: verificationHost, runCtx: ctx, taskID: taskID, intentID: intent.ID}
	}
	ctx = agent.WithControlledReadExecution(ctx, agent.ControlledReadExecution{
		TaskID: taskID, IntentID: intent.ID, RunRef: runRef, Tool: lease.tool(), Verification: lease.verification != nil,
	})
	return ctx, lease.close, nil
}

// Journal and protected checkpoint acknowledgement form one admission record.
// Failure in either half is sticky and denies subsequent attempts.
type controlledCheckpointRecorder struct {
	mu        sync.Mutex
	journal   *auditlog.Journal
	published *controlledPublishedJournal
	store     *controlledReadStore
	failed    bool
}

func (r *controlledCheckpointRecorder) Record(ctx context.Context, event transmit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed {
		return errControlledReadUnavailable
	}
	if r.published != nil {
		if err := r.published.record(ctx, event); err != nil {
			r.failed = true
			return errControlledReadUnavailable
		}
		return nil
	}
	if err := r.journal.Record(ctx, event); err != nil {
		r.failed = true
		return errControlledReadUnavailable
	}
	if err := r.store.saveCheckpoint(ctx, r.journal.Checkpoint()); err != nil {
		r.failed = true
		return errControlledReadUnavailable
	}
	return nil
}

func (l *controlledReadLease) close(disposition ...bool) error {
	l.once.Do(func() {
		if l.recorder.published == nil {
			l.tx.StopAndWait()
		} else if !l.recorder.published.drain.stopAndWait(l.tx) {
			l.tx.StopAndWait()
			l.closeErr = errControlledReadUnavailable
		}
		halted := l.tx.HealthStatus().Halted
		l.resultMu.Lock()
		observedOK := l.observed && !l.denied && !halted
		l.resultMu.Unlock()
		l.recorder.mu.Lock()
		var checkpoint auditlog.Checkpoint
		if l.recorder.published != nil {
			completed := observedOK && len(disposition) == 1 && disposition[0]
			var err error
			checkpoint, err = l.closePublishedAudit(completed)
			if err != nil {
				l.closeErr = errControlledReadUnavailable
			}
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
			defer cancel()
			if err := l.journal.Close(); err != nil {
				l.closeErr = errControlledReadUnavailable
			}
			checkpoint = l.journal.Checkpoint()
			if err := l.store.saveCheckpoint(ctx, checkpoint); err != nil || l.recorder.failed {
				l.closeErr = errControlledReadUnavailable
			}
		}
		l.recorder.mu.Unlock()
		if !observedOK {
			l.closeErr = errControlledReadUnavailable
		}
		l.runtime.mu.Lock()
		delete(l.runtime.active, l.work)
		l.runtime.mu.Unlock()
		// Cleanup alone never commits a staged verdict. The engine supplies its
		// successful disposition, and storage runs outside journal/runtime locks.
		if l.closeErr == nil && l.verification != nil {
			completed := len(disposition) == 1 && disposition[0]
			if completed {
				claim, ok := l.verification.runCtx.Value(controlledReadCompletionClaimKey{}).(func() bool)
				completed = ok && claim()
			}
			l.closeErr = l.verification.finish(l, completed, checkpoint)
		}
	})
	return l.closeErr
}

func (l *controlledReadLease) tool() actool.CoreTool {
	if l.verification != nil {
		return l.verificationTool()
	}
	return roTool(agent.ControlledHTTPToolName, "Perform one approved read using registered account and resource references. No arbitrary URL, headers or methods. If health.Halted is true, stop requesting and report its reason codes. Continue/below_threshold or insufficient_samples is not a normal or safe result and does not authorize another request. ok only reports capture completion.", map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"account_ref":  map[string]any{"type": "string", "enum": l.accounts},
			"resource_ref": map[string]any{"type": "string", "enum": l.resources},
		}, "required": []string{"account_ref", "resource_ref"},
	}, func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
		account, resource, err := controlledReadInput(input)
		if err != nil || ctx.Err() != nil {
			l.resultMu.Lock()
			l.denied = true
			l.resultMu.Unlock()
			return actool.Errorf("controlled_read_invalid_input"), nil
		}
		// Tool cancellation (SDK deadline) must also stop the parent-bound sender.
		stop := context.AfterFunc(ctx, l.tx.Stop)
		defer stop()
		capture, receipt, ok := l.tx.SendDetailed(idor.AccessRequest{As: l.sessions[account], ResourceRef: resource, Method: "GET"})
		var health *transmit.HealthStatus
		if status := l.tx.HealthStatus(); status.Enabled {
			health = &status
		}
		halted := health != nil && health.Halted
		l.resultMu.Lock()
		l.observed = l.observed || ok
		l.denied = l.denied || !ok || halted
		l.resultMu.Unlock()
		out, err := json.Marshal(struct {
			OK      bool                   `json:"ok"`
			Receipt transmit.Receipt       `json:"receipt"`
			Capture idor.Capture           `json:"capture"`
			Health  *transmit.HealthStatus `json:"health,omitempty"`
		}{ok, receipt, capture, health})
		if err != nil {
			return actool.Errorf("controlled_read_result_unavailable"), nil
		}
		result := actool.Text(string(out))
		result.IsError = !ok || halted
		return result, nil
	})
}

func controlledReadInput(input json.RawMessage) (account, resource string, err error) {
	if len(input) == 0 || len(input) > controlledReadInputLimit {
		return "", "", errControlledReadInput
	}
	d := json.NewDecoder(bytes.NewReader(input))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return "", "", errControlledReadInput
	}
	seen := make(map[string]bool, 2)
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return "", "", errControlledReadInput
		}
		name, ok := key.(string)
		if !ok || seen[name] || name != "account_ref" && name != "resource_ref" {
			return "", "", errControlledReadInput
		}
		seen[name] = true
		var value string
		if d.Decode(&value) != nil || !safeRef.MatchString(value) || strings.Contains(value, "://") {
			return "", "", errControlledReadInput
		}
		if name == "account_ref" {
			account = value
		} else {
			resource = value
		}
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || len(seen) != 2 {
		return "", "", errControlledReadInput
	}
	if _, err := d.Token(); err != io.EOF {
		return "", "", errControlledReadInput
	}
	return account, resource, nil
}
