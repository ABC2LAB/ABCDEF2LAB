package server

import (
	"context"
	"errors"
	"log"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2host"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

var errCandidateE2Execution = errors.New("candidate E2 execution unavailable")

// Only a static exact path reconstructed from a server-checked observation is
// accepted. No caller-supplied shell metacharacter or query reaches Bash.
var candidateFixedGETURL = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(?::[0-9]{1,5})?/(?:[A-Za-z-]+(?:/[A-Za-z-]+)*)?$`)
var candidateVerificationOrigin = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(?::[0-9]{1,5})?$`)

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
			log.Print("[candidate-e2] work drain unavailable")
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

// runSelectedCandidateActions executes only the immutable approved read plan.
// Every curl call uses the same protected Runner and trusted-result observer as
// ordinary model work. The model has no shell, browser or direct HTTP tool.
func (r *E2HTTPRuntime) runSelectedCandidateActions(ctx context.Context, task *Task, intent *db.Node,
	origin string, actions []CandidateLLMPlanAction) (results []e2runner.TrustedResult, resultErr error) {
	if r == nil || task == nil || intent == nil || intent.Kind != db.KindIntent || len(actions) < 1 || len(actions) > 3 {
		return nil, errCandidateE2Execution
	}
	for _, action := range actions {
		if action.Method != "GET" && action.Method != "HEAD" || !validSelectedExactPath(action.ExactPath) ||
			!validSelectedActionInputs(action.Query, action.Headers) ||
			!candidateVerificationOrigin.MatchString(origin) {
			return nil, errCandidateE2Execution
		}
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
	results = make([]e2runner.TrustedResult, 0, len(actions))
	for _, action := range actions {
		if bound.Err() != nil || ctx.Err() != nil {
			return nil, errCandidateE2Execution
		}
		url := origin + action.ExactPath
		if query := selectedActionQuery(action.Query); query != "" {
			url += "?" + query
		}
		command := "curl --silent --show-error --max-time 15 --output /dev/null --request " + action.Method + " --url " + strconv.Quote(url)
		names := make([]string, 0, len(action.Headers))
		for name := range action.Headers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			command += " --header " + strconv.Quote(name+": "+action.Headers[name])
		}
		result, runErr := lease.Runner.RunForIntent(bound, intent.ID, command)
		observeErr := r.observeResult(bound, t, lease, intent.ID, result)
		if observeErr != nil || runErr != nil {
			log.Printf("[candidate-e2] action result unavailable: observed=%t runner=%t", observeErr == nil, runErr == nil)
			return nil, errCandidateE2Execution
		}
		if lease.VerifySelected != nil {
			if len(result.Trusted.Call.Observations) != 1 {
				log.Print("[candidate-e2] action observation count invalid")
				return nil, errCandidateE2Execution
			}
			observation := result.Trusted.Call.Observations[0]
			verified, err := lease.VerifySelected(bound, e2host.ObservationRef{
				CallRef: result.Trusted.Call.CallRef, AttemptID: observation.AttemptRef, Sequence: observation.Sequence,
			})
			matched := err == nil && selectedVerifiedResult(result.Trusted, verified)
			if !matched {
				log.Printf("[candidate-e2] host verification unavailable: proof=%t matched=%t", err == nil, matched)
				if err == nil {
					e, c := verified.Event, verified.Checkpoint
					log.Printf("[candidate-e2] proof comparison: stage=%t sequence=%t version=%t scope=%t policy=%t intent=%t ordinary=%t references=%t method=%t rule=%t decision=%t reason=%t status=%t forward=%t written=%t response=%t complete=%t audit=%t untruncated=%t sent=%t time=%t",
						e.Stage == "result", e.Index > 0 && e.Index <= c.Sequence, e.Version == e2proxy.JournalVersionV3 && c.Version == e2proxy.JournalVersionV3,
						e.Scope == result.Trusted.Execution.Scope && c.Scope == e.Scope, e.PolicyRevision == result.Trusted.Execution.PolicyRevision,
						e.IntentID == result.Trusted.Call.IntentID, e.Program == "" && e.E3Audit == nil,
						e.CallRef == result.Trusted.Call.CallRef && e.AttemptID == observation.AttemptRef && e.Sequence == observation.Sequence,
						e.Method == observation.Method, e.RuleRef == observation.RuleRef, e.Decision == observation.Decision,
						e.Reason == observation.Reason, e.StatusCode == observation.StatusCode,
						e.ForwardAttempted == observation.ForwardAttempted, e.RequestWritten,
						e.ResponseReceived, e.ResponseComplete == observation.ResponseComplete, e.AuditComplete == observation.AuditComplete,
						!e.ResponseTruncated, !e.NotSent, selectedAuditTimeFollowsReceipt(e.At, observation.ObservedAt))
				}
				return nil, errCandidateE2Execution
			}
			result.Trusted.SelectedProof = &e2runner.SelectedAuditProof{Event: verified.Event, Checkpoint: verified.Checkpoint}
		}
		results = append(results, result.Trusted)
	}
	return results, nil
}

func selectedVerifiedResult(trusted e2runner.TrustedResult, verified e2host.Verification) bool {
	if len(trusted.Call.Observations) != 1 {
		return false
	}
	o, e, c := trusted.Call.Observations[0], verified.Event, verified.Checkpoint
	return e.Stage == "result" && e.Index > 0 && e.Index <= c.Sequence && e.Version == e2proxy.JournalVersionV3 &&
		e.Scope == trusted.Execution.Scope && c.Scope == e.Scope && c.Version == e2proxy.JournalVersionV3 && c.JournalID != "" &&
		e.PolicyRevision == trusted.Execution.PolicyRevision && e.IntentID == trusted.Call.IntentID && e.Program == "" && e.E3Audit == nil &&
		e.CallRef == trusted.Call.CallRef && e.AttemptID == o.AttemptRef && e.Sequence == o.Sequence &&
		e.Method == o.Method && e.RuleRef == o.RuleRef && e.Decision == o.Decision && e.Reason == o.Reason &&
		e.StatusCode == o.StatusCode && e.ForwardAttempted == o.ForwardAttempted && e.RequestWritten && e.ResponseReceived &&
		e.ResponseComplete == o.ResponseComplete && e.AuditComplete == o.AuditComplete && !e.ResponseTruncated && !e.NotSent &&
		selectedAuditTimeFollowsReceipt(e.At, o.ObservedAt)
}

// The receipt is timestamped before it is appended; the v3 Host journal
// stamps the authenticated event when it records it. Exact equality is not
// valid even for the same call. A bounded, forward-only interval retains the
// time consistency check without conflating those two clocks.
func selectedAuditTimeFollowsReceipt(journalAt, receiptAt time.Time) bool {
	if journalAt.IsZero() || receiptAt.IsZero() {
		return false
	}
	delta := journalAt.Sub(receiptAt)
	return delta >= 0 && delta <= 15*time.Second
}
