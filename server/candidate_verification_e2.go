package server

import (
	"context"
	"errors"
	"regexp"
	"strconv"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2runner"
)

var errCandidateE2Execution = errors.New("candidate E2 execution unavailable")

// Only a static exact path reconstructed from a server-checked observation is
// accepted. No caller-supplied shell metacharacter or query reaches Bash.
var candidateFixedGETURL = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(?::[0-9]{1,5})?/(?:[A-Za-z-]+(?:/[A-Za-z-]+)*)?$`)

// runSelectedCandidateGET is called by the protected host only after a new
// candidate-specific task, policy, lease and approved plan have been bound.
// PrepareWork performs the normal E² admission and records both fixed Bash
// commands through the same trusted-result observer used by model workers.
// The caller must drain and seal the runtime before assessing the results.
func (r *E2HTTPRuntime) runSelectedCandidateGET(ctx context.Context, task *Task, intent *db.Node, exactURL string) (results []e2runner.TrustedResult, resultErr error) {
	if r == nil || task == nil || intent == nil || intent.Kind != db.KindIntent || !candidateFixedGETURL.MatchString(exactURL) {
		return nil, errCandidateE2Execution
	}
	bound, finish, err := r.PrepareWork(ctx, task, intent)
	if err != nil || finish == nil {
		return nil, errCandidateE2Execution
	}
	defer func() {
		if closeErr := finish(); closeErr != nil {
			resultErr = errors.Join(resultErr, errCandidateE2Execution)
		}
	}()
	t, err := r.taskFor(task)
	if err != nil {
		return nil, errCandidateE2Execution
	}
	t.mu.Lock()
	lease := t.lease
	valid := t.validLease && lease != nil && lease.Runner != nil
	t.mu.Unlock()
	if !valid {
		return nil, errCandidateE2Execution
	}
	command := "curl --silent --show-error --max-time 15 --output /dev/null --url " + strconv.Quote(exactURL)
	for i := 0; i < 2; i++ {
		if bound.Err() != nil || ctx.Err() != nil {
			return nil, errCandidateE2Execution
		}
		result, runErr := lease.Runner.RunForIntent(bound, intent.ID, command)
		if err := r.observeResult(bound, t, lease, intent.ID, result); err != nil {
			return nil, errCandidateE2Execution
		}
		if runErr != nil {
			return nil, errCandidateE2Execution
		}
	}
	return r.Results(task.ID), nil
}
