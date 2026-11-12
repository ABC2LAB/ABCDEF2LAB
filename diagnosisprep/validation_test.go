package diagnosisprep

import (
	"encoding/json"
	"github.com/Autumn-27/artex/diagnosis"
	"strings"
	"testing"
)

func TestCanonicalInputStrictLocalValidation(t *testing.T) {
	good := map[string]string{"https://EXAMPLE.test:443": "https://example.test/", "https://example.test/read": "https://example.test/read", "https://[2001:db8::1]:8443/read": "https://[2001:db8::1]:8443/read"}
	for input, want := range good {
		t.Run(input, func(t *testing.T) {
			got, e := CanonicalInput(Input{TargetURL: input, ProfileID: "local"})
			if e != nil || got.TargetURL != want {
				t.Fatalf("%#v %v", got, e)
			}
		})
	}
	bad := []string{"http://example.test/", "https://[owned.example]/", "https://[127.0.0.1]/", "https://2001:db8::1:443/", "file:///etc/passwd", "https://user@example.test/", "https://example.test/#", "https://example.test/?", "https://example.test/?x=1", "https://example.test/%2fread", "https://example.test/%252fread", "https://example.test/a/../b", "https://example.test/./b", "https://example.test//read", "https://example.test/\\read", "https://example.test./", "https://예제.test/", "https://127.1/", "https://2130706433/", "https://0177.0.0.1/", "https://0x7f000001/", "https://[::ffff:127.0.0.1]/", "https://[fe80::1%25eth0]/", "https://example.test:0443/", "https://example.test:/", "https://example.test:0/", "https://example.test:65536/", " https://example.test/", "https://example.test/\n", "https://example.test/a b"}
	for _, input := range bad {
		t.Run(input, func(t *testing.T) {
			if _, e := CanonicalInput(Input{TargetURL: input, ProfileID: "local"}); e == nil {
				t.Fatal("ambiguous URL accepted")
			}
		})
	}
}
func TestStrictJSONRejectsNonCanonicalFields(t *testing.T) {
	valid := `{"version":"diagnosis-preparation.v1","request_id":"r","command":"create","principal":"owner","idempotency_key":"key","input":{"target_url":"https://example.test/","profile_id":"local"}}`
	var request Request
	if e := strictDecode([]byte(valid), &request); e != nil {
		t.Fatal(e)
	}
	bad := []string{
		strings.Replace(valid, `"version"`, `"Version"`, 1),
		strings.Replace(valid, `"request_id":"r"`, `"request_id":"r","request_id":"r"`, 1),
		strings.Replace(valid, `"profile_id":"local"`, `"profile_id":"local","profile_id":"other"`, 1),
		strings.Replace(valid, `"profile_id":"local"`, `"profile_id":"local","file_path":"/secret"`, 1),
		strings.Replace(valid, `"profile_id":"local"`, `"profile_id":null`, 1),
		valid + " {}", valid + "\n{}",
	}
	for i, raw := range bad {
		if e := strictDecode([]byte(raw), new(Request)); e == nil {
			t.Fatalf("accepted malformed JSON %d", i)
		}
	}
}
func TestResponseFactsAndUnknownAreIndependent(t *testing.T) {
	base := Job{ID: "prep-job", Revision: 1, SourceDigest: strings.Repeat("a", 64), Input: Input{TargetURL: "https://example.test/", ProfileID: "local"}, State: Unknown, Phase: Closure, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityNotIssued}
	for _, drain := range []bool{false, true} {
		for _, result := range []bool{false, true} {
			j := base
			j.DrainComplete = drain
			j.ResultReady = result
			if !validJob(j) {
				t.Fatalf("unknown facts collapsed: drain %v result %v", drain, result)
			}
		}
	}
	bad := base
	bad.Started = true
	if validJob(bad) {
		t.Fatal("started without consumed authority accepted")
	}
	bad = base
	bad.TaskID = ""
	bad.ResultReady = true
	if validJob(bad) {
		t.Fatal("result without task accepted")
	}
	bad = base
	bad.State = Completed
	bad.DrainComplete = true
	bad.ResultReady = true
	if validJob(bad) {
		t.Fatal("completed without actual start accepted")
	}
	base.State = Failed
	base.DrainComplete = true
	if !validJob(base) {
		t.Fatal("closed preparation failure rejected")
	}
}
func TestRequestAndResponseShape(t *testing.T) {
	r := Request{Version: Version, RequestID: "r", Principal: "owner", Command: Stop, JobID: "prep-job", ExpectedRevision: 2}
	if ValidateRequest(r) != nil {
		t.Fatal("valid revision rejected")
	}
	r.ExpectedRevision = 0
	if ValidateRequest(r) == nil {
		t.Fatal("missing stop revision accepted")
	}
	r.Command = Catalog
	r.JobID = ""
	r.Input = &Input{}
	if ValidateRequest(r) == nil {
		t.Fatal("catalog input accepted")
	}
	r.Input = nil
	response := Response{Version: Version, RequestID: "wrong"}
	if ValidateResponse(r, response) == nil {
		t.Fatal("wrong correlation accepted")
	}
	response.RequestID = r.RequestID
	response.Error = "private raw error /secret"
	if ValidateResponse(r, response) == nil {
		t.Fatal("arbitrary error accepted")
	}
	raw, _ := json.Marshal(response)
	if len(raw) == 0 {
		t.Fatal("marshal")
	}
}

func TestAuthorizeResponseRequiresMatchingOwnedJob(t *testing.T) {
	r := Request{Version: Version, RequestID: "r", Command: AuthorizeTask, Principal: "owner", TaskID: "42"}
	yes := true
	res := Response{Version: Version, RequestID: "r", Authorized: &yes}
	if ValidateResponse(r, res) == nil {
		t.Fatal("missing owned job accepted")
	}
	res.Job = &Job{ID: "prep-job", Revision: 1, SourceDigest: strings.Repeat("a", 64), Input: Input{TargetURL: "https://example.test/", ProfileID: "local"}, State: Unknown, Phase: Planning, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityNotIssued}
	if ValidateResponse(r, res) != nil {
		t.Fatal("owned job rejected")
	}
	res.Job.TaskID = "43"
	if ValidateResponse(r, res) == nil {
		t.Fatal("wrong task mapping accepted")
	}
	yes = false
	if ValidateResponse(r, res) == nil {
		t.Fatal("unauthorized task data leaked")
	}
}

func TestSourceDigestBindsCanonicalInputAndInstallation(t *testing.T) {
	input := Input{TargetURL: "https://example.test/read", ProfileID: "local"}
	installation := strings.Repeat("a", 64)
	first := SourceDigest(input, installation)
	if !digestPattern.MatchString(first) || SourceDigest(input, installation) != first {
		t.Fatal("unstable source digest")
	}
	if SourceDigest(input, strings.Repeat("b", 64)) == first {
		t.Fatal("installation is not bound")
	}
	input.ProfileID = "another"
	if SourceDigest(input, installation) == first {
		t.Fatal("profile is not bound")
	}
}

// 실행 결과 칸 규칙(docs/contracts/run-outcome-states.md §3.2): outcome·closing_reason은 completed만,
// intent_outcomes는 completed·cancelled만, outcome은 개수 대조식과 맞아야 한다.
func TestJobRunResultFields(t *testing.T) {
	counts := func(c diagnosis.IntentOutcomes) *diagnosis.IntentOutcomes { return &c }
	completed := Job{ID: "prep-job", Revision: 1, SourceDigest: strings.Repeat("a", 64), Input: Input{TargetURL: "https://example.test/", ProfileID: "local"}, State: Completed, Phase: Report, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityConsumed, Started: true, DrainComplete: true, ResultReady: true}
	for _, tc := range []struct {
		name   string
		change func(*Job)
		ok     bool
	}{
		{"legacy_completed_without_result", func(*Job) {}, true},
		{"complete", func(j *Job) {
			j.Outcome, j.IntentOutcomes = diagnosis.OutcomeComplete, counts(diagnosis.IntentOutcomes{Done: 2, PolicyLimited: 1})
		}, true},
		{"partial_budget_limited", func(j *Job) {
			j.Outcome, j.ClosingReason, j.IntentOutcomes = diagnosis.OutcomePartial, diagnosis.ClosingBudgetLimited, counts(diagnosis.IntentOutcomes{Done: 1, Incomplete: 2})
		}, true},
		{"no_intents_budget_limited", func(j *Job) {
			j.Outcome, j.ClosingReason, j.IntentOutcomes = diagnosis.OutcomeNoIntents, diagnosis.ClosingBudgetLimited, counts(diagnosis.IntentOutcomes{})
		}, true},
		{"cancelled_counts", func(j *Job) {
			j.State, j.IntentOutcomes = Cancelled, counts(diagnosis.IntentOutcomes{Done: 1, Stopped: 1})
		}, true},
		{"outcome_mismatch", func(j *Job) {
			j.Outcome, j.IntentOutcomes = diagnosis.OutcomeComplete, counts(diagnosis.IntentOutcomes{Done: 1, Incomplete: 1})
		}, false},
		{"outcome_without_counts", func(j *Job) { j.Outcome = diagnosis.OutcomeComplete }, false},
		{"counts_without_outcome", func(j *Job) { j.IntentOutcomes = counts(diagnosis.IntentOutcomes{Done: 1}) }, false},
		{"closing_without_outcome", func(j *Job) { j.ClosingReason = diagnosis.ClosingBudgetLimited }, false},
		{"unknown_closing_reason", func(j *Job) {
			j.Outcome, j.ClosingReason, j.IntentOutcomes = diagnosis.OutcomeComplete, "time_limited", counts(diagnosis.IntentOutcomes{Done: 1})
		}, false},
		{"negative_count", func(j *Job) {
			j.Outcome, j.IntentOutcomes = diagnosis.OutcomePartial, counts(diagnosis.IntentOutcomes{Done: 1, Incomplete: 2, Stopped: -1})
		}, false},
		{"outcome_on_cancelled", func(j *Job) {
			j.State, j.Outcome, j.IntentOutcomes = Cancelled, diagnosis.OutcomeComplete, counts(diagnosis.IntentOutcomes{Done: 1})
		}, false},
		{"counts_on_failed", func(j *Job) { j.State, j.IntentOutcomes = Failed, counts(diagnosis.IntentOutcomes{Done: 1}) }, false},
		{"counts_on_unknown", func(j *Job) { j.State, j.IntentOutcomes = Unknown, counts(diagnosis.IntentOutcomes{Done: 1}) }, false},
		{"log_warning_on_completed", func(j *Job) { j.DiagnosticLogIncomplete = true }, true},
		{"log_warning_with_reason", func(j *Job) { j.DiagnosticLogIncomplete, j.ReasonCode = true, "budget_limited" }, true},
		{"log_warning_on_failed", func(j *Job) { j.State, j.DiagnosticLogIncomplete = Failed, true }, false},
	} {
		j := completed
		tc.change(&j)
		if validJob(j) != tc.ok {
			t.Fatalf("%s: valid=%v", tc.name, !tc.ok)
		}
	}
}
