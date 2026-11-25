package diagnosisapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

func TestURLResponseFieldSetsRequireWebContractReview(t *testing.T) {
	// A new omitempty field can stay nil in every fixed example yet appear in
	// production and break the web's closed parsers. These explicit contracts
	// mirror parseURLJob/parseRun's exact allowlists, forcing a web deployment and
	// shared-example review even when the new field is absent from our fixtures.
	for _, contract := range []struct {
		name   string
		value  any
		fields []string
	}{
		{"Job", diagnosisprep.Job{}, []string{
			"id", "revision", "source_digest", "input", "state", "phase", "reason_code", "scope", "task_id",
			"preparation_started", "authority_state", "started", "drain_complete", "started_at", "finished_at",
			"result_ready", "model_usage", "outcome", "closing_reason", "intent_outcomes", "diagnostic_log_incomplete", "account_auth",
			// 2026-11-25 #936: 실행 중 account_auth의 계정별 변경 시각. 웹 parseURLJob은 #937에서 받는다. 공유 예시에는 계정이
			// 없어 이 칸이 나오지 않는다(omitempty).
			"account_auth_updated_at",
		}},
		{"RunSummary", diagnosisprep.RunSummary{}, []string{
			"id", "revision", "display_name", "state", "phase", "reason_code", "task_id", "started", "drain_complete",
			"started_at", "finished_at", "result_ready", "report_available", "approval_status", "action_required",
		}},
	} {
		t.Run(contract.name, func(t *testing.T) {
			kind := reflect.TypeOf(contract.value)
			var actual []string
			for i := range kind.NumField() {
				field := kind.Field(i)
				name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
				if !field.IsExported() || name == "-" {
					continue
				}
				if name == "" {
					name = field.Name
				}
				actual = append(actual, name)
			}
			slices.Sort(actual)
			slices.Sort(contract.fields)
			if !slices.Equal(actual, contract.fields) {
				t.Fatalf("public JSON fields changed; review web exact allowlists and shared examples together before updating this contract:\ngot:  %v\nwant: %v", actual, contract.fields)
			}
		})
	}
}

// The protected endpoint supplies fixed, independent values. The actual public
// handlers validate and serialize them; the shared files are only expectations,
// never a source of Go inputs. Comparing the complete JSON catches new fields as
// well as changed values, including fields the web's closed parsers do not know.
func requireURLResponseExample(t *testing.T, got, want []byte) {
	t.Helper()
	var actual, expected any
	if err := json.Unmarshal(got, &actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		pretty, _ := json.MarshalIndent(actual, "", "  ")
		t.Fatalf("public handler response differs from shared example:\n%s\nexpected:\n%s", pretty, want)
	}
}

func readURLResponseExample(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "docs", "contracts", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func exactPathContractJob() diagnosisprep.Job {
	return diagnosisprep.Job{
		ID: "job-1", Revision: 2, SourceDigest: strings.Repeat("c", 64),
		Input: diagnosisprep.Input{TargetURL: "https://owned.example/", ProfileID: "profile-1"},
		State: diagnosisprep.Ready, Phase: diagnosisprep.ScopeCheck, AuthorityState: diagnosisprep.AuthorityNotIssued,
		Scope: &diagnosisprep.Scope{
			TargetRef: "target-1", ScopeRef: "scope-1", Origin: "https://owned.example",
			PathPrefixes: []string{"=/", "/users/v1", "=/a/"}, Methods: []string{"GET", "HEAD"},
			QueryPolicy: "any", BodyPolicy: "bounded", ApprovalMode: "preauthorized_scope", AttestationRef: "attestation-1",
			ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
			Budget:    diagnosisprep.Budget{Requests: 100, ModelRequests: 2, InputTokens: 1000, OutputTokens: 1000, DurationSeconds: 60},
		},
	}
}

func TestURLExactPathStatusMatchesSharedExample(t *testing.T) {
	endpoint := &urlTestEndpoint{reply: func(req diagnosisprep.Request) (diagnosisprep.Response, error) {
		if req.Command != diagnosisprep.Status || req.JobID != "job-1" {
			t.Fatal("unexpected protected request", req.Command, req.JobID)
		}
		job := exactPathContractJob()
		return diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: req.RequestID, Job: &job}, nil
	}}
	response := urlTestRequest(urlTestHandler(t, endpoint), http.MethodGet, URLBasePath+"/drafts/job-1/status", "", "")
	if response.Code != http.StatusOK || len(endpoint.calls) != 1 {
		t.Fatal("status request failed", response.Code, response.Body.String())
	}
	requireURLResponseExample(t, response.Body.Bytes(), readURLResponseExample(t, "exact-path-approval.status.example.json"))
}

type runTimingContractCase struct {
	name           string
	job            diagnosisprep.Job
	actionRequired bool
}

func runTimingContractCases() []runTimingContractCase {
	started := time.Date(2026, 11, 17, 1, 2, 3, 123456789, time.UTC)
	finished := started.Add(5 * time.Minute)
	ready := exactPathContractJob()
	ready.Input.DisplayName = "합성 점검"
	running := ready
	running.Revision, running.State, running.Phase = 4, diagnosisprep.Running, diagnosisprep.ExecutionPhase
	running.TaskID, running.PreparationStarted, running.AuthorityState = "42", true, diagnosisprep.AuthorityConsumed
	running.Started, running.StartedAt = true, &started
	completed := running
	completed.Revision, completed.State, completed.Phase = 5, diagnosisprep.Completed, diagnosisprep.Report
	completed.DrainComplete, completed.FinishedAt, completed.ResultReady = true, &finished, true
	failed := ready
	failed.Revision, failed.State, failed.Phase = 3, diagnosisprep.Failed, diagnosisprep.Closure
	failed.ReasonCode, failed.PreparationStarted, failed.DrainComplete, failed.FinishedAt = "preparation_failed", true, true, &finished
	legacy := completed
	legacy.StartedAt, legacy.FinishedAt = nil, nil
	return []runTimingContractCase{
		{"ready_without_times", ready, false},
		{"running_with_start", running, true},
		{"completed_with_times", completed, false},
		{"failed_before_start_with_finish", failed, true},
		{"legacy_completed_without_times", legacy, false},
	}
}

func TestURLRunTimingResponsesMatchSharedExamples(t *testing.T) {
	var examples struct {
		Version string `json:"version"`
		Cases   []struct {
			Name   string          `json:"name"`
			Status json.RawMessage `json:"status"`
			List   json.RawMessage `json:"list"`
		} `json:"cases"`
	}
	raw := readURLResponseExample(t, "diagnosis-run-timing.responses.example.json")
	if err := json.Unmarshal(raw, &examples); err != nil {
		t.Fatal(err)
	}
	cases := runTimingContractCases()
	if examples.Version != "diagnosis-run-timing.examples.v1" || len(examples.Cases) != len(cases) {
		t.Fatal("shared timing example cases changed")
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			example := examples.Cases[i]
			if example.Name != tc.name {
				t.Fatal("shared timing example name changed", example.Name)
			}
			endpoint := &urlTestEndpoint{reply: func(req diagnosisprep.Request) (diagnosisprep.Response, error) {
				response := diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: req.RequestID}
				switch req.Command {
				case diagnosisprep.Status:
					if req.JobID != tc.job.ID {
						t.Fatal("unexpected protected job", req.JobID)
					}
					job := tc.job
					response.Job = &job
				case diagnosisprep.ListRuns:
					job := tc.job
					response.RunPage = &diagnosisprep.RunPage{
						Runs: []diagnosisprep.RunSummary{{
							ID: job.ID, Revision: job.Revision, DisplayName: job.Input.DisplayName,
							State: job.State, Phase: job.Phase, ReasonCode: job.ReasonCode, TaskID: job.TaskID,
							Started: job.Started, DrainComplete: job.DrainComplete, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt,
							ResultReady: job.ResultReady, ReportAvailable: job.ResultReady && job.TaskID != "",
							ApprovalStatus: "not_revalidated", ActionRequired: tc.actionRequired,
						}},
						Snapshot: strings.Repeat("b", 64), Limit: 25,
					}
				default:
					t.Fatal("unexpected protected command", req.Command)
				}
				return response, nil
			}}
			handler := urlTestHandler(t, endpoint)
			for _, view := range []struct {
				name, path string
				want       []byte
			}{
				{"status", URLBasePath + "/drafts/" + tc.job.ID + "/status", example.Status},
				{"list", URLBasePath + "/runs", example.List},
			} {
				t.Run(view.name, func(t *testing.T) {
					response := urlTestRequest(handler, http.MethodGet, view.path, "", "")
					if response.Code != http.StatusOK {
						t.Fatal("public request failed", response.Code, response.Body.String())
					}
					requireURLResponseExample(t, response.Body.Bytes(), view.want)
				})
			}
			if len(endpoint.calls) != 2 {
				t.Fatal("public reads unexpectedly called the service again", len(endpoint.calls))
			}
		})
	}
}
