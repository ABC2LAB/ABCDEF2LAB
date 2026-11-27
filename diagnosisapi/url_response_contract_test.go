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
			// 2026-11-27 시간 상한(diagnosis-run-timing.md §시간 상한). 웹 parseURLJob이 같은 변경에서 받는다.
			"time_limit",
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
	// 시간 상한(§시간 상한): 실행 상한 01:12:00, 모델 마감 = 상한 − 유예 90초 − 확정 몫 120초 − 정리 여유 20초.
	deadline := time.Date(2026, 11, 17, 1, 12, 0, 0, time.UTC)
	limit := &diagnosisprep.TimeLimit{RunDeadlineAt: deadline, ModelCloseAt: deadline.Add(-230 * time.Second),
		ModelDrainMS: 90_000, ConfirmationReserveMS: 120_000, ShutdownMarginMS: 20_000}
	limited := running
	limited.Revision, limited.TimeLimit = 6, limit
	// 기한은 시작 확인보다 먼저 정한 600초 예산 안에 있어야 한다. 다른 예시의 60초 범위는 보존한다.
	limitedScope := *running.Scope
	limitedScope.Budget.DurationSeconds = 600
	limited.Scope = &limitedScope
	stopped := limited
	stopped.Revision, stopped.State, stopped.Phase, stopped.ReasonCode = 9, diagnosisprep.Cancelled, diagnosisprep.Report, "stop_requested"
	stopped.DrainComplete, stopped.FinishedAt, stopped.ResultReady = true, &finished, true
	return []runTimingContractCase{
		{"ready_without_times", ready, false},
		{"running_with_start", running, true},
		{"completed_with_times", completed, false},
		{"failed_before_start_with_finish", failed, true},
		{"legacy_completed_without_times", legacy, false},
		{"running_with_time_limit", limited, true},
		{"cancelled_keeps_time_limit", stopped, false},
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
			if tc.job.TimeLimit != nil && tc.job.StartedAt != nil &&
				tc.job.TimeLimit.RunDeadlineAt.Sub(*tc.job.StartedAt) > time.Duration(tc.job.Scope.Budget.DurationSeconds)*time.Second {
				t.Fatal("시간 상한 예시가 실행 예산을 초과합니다")
			}
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
			// server_time은 응답 시계다. 취소 응답은 마감 확인 시각 이후로 둔다.
			minute := 5
			if tc.name == "cancelled_keeps_time_limit" {
				minute = 8
			}
			handler.options.Now = func() time.Time { return time.Date(2026, 11, 17, 10, minute, 3, 0, time.FixedZone("kst", 9*60*60)) }
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

type runConfirmationContractCase struct {
	name         string
	job          diagnosisprep.Job
	confirmation *diagnosisprep.ProductConfirmationState
}

// 회차 상태 응답의 확정 단계 결과(e3-product-confirmation-interface.md '회차 상태 응답'). 다섯 상태·기록 없음·못 읽음과,
// 제품 설치가 아닐 때(칸 없음)를 실제 handler가 내는 그대로 공유 예시와 비교한다.
func runConfirmationContractCases() []runConfirmationContractCase {
	cases := runTimingContractCases()
	running, completed := cases[1].job, cases[2].job
	state := func(status, reason string) *diagnosisprep.ProductConfirmationState {
		return &diagnosisprep.ProductConfirmationState{Present: true, Status: status, Reason: reason}
	}
	return []runConfirmationContractCase{
		{"running_not_recorded", running, &diagnosisprep.ProductConfirmationState{}},
		{"completed", completed, state("completed", "")},
		{"disabled", completed, state("disabled", "test_accounts_unavailable")},
		{"start_refused", completed, state("start_refused", "e2_execution_unavailable")},
		{"interrupted", completed, state("interrupted", "")},
		{"confirmation_integrity_failed", completed, state("confirmation_integrity_failed", "journal_integrity_failed")},
		{"unreadable", completed, &diagnosisprep.ProductConfirmationState{Present: true, Status: diagnosisprep.ConfirmationUnreadable}},
		{"not_product_install", completed, nil},
	}
}

func TestURLRunConfirmationMatchesSharedExamples(t *testing.T) {
	var examples struct {
		Version string `json:"version"`
		Cases   []struct {
			Name   string          `json:"name"`
			Status json.RawMessage `json:"status"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(readURLResponseExample(t, "run-confirmation.status.example.json"), &examples); err != nil {
		t.Fatal(err)
	}
	cases := runConfirmationContractCases()
	if examples.Version != "run-confirmation.examples.v1" || len(examples.Cases) != len(cases) {
		t.Fatal("shared confirmation example cases changed")
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if examples.Cases[i].Name != tc.name {
				t.Fatal("shared confirmation example name changed", examples.Cases[i].Name)
			}
			endpoint := &urlTestEndpoint{reply: func(req diagnosisprep.Request) (diagnosisprep.Response, error) {
				job := tc.job
				return diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: req.RequestID, Job: &job, Confirmation: tc.confirmation}, nil
			}}
			response := urlTestRequest(urlTestHandler(t, endpoint), http.MethodGet, URLBasePath+"/drafts/"+tc.job.ID+"/status", "", "")
			if response.Code != http.StatusOK {
				t.Fatal(response.Code, response.Body.String())
			}
			requireURLResponseExample(t, response.Body.Bytes(), examples.Cases[i].Status)
		})
	}
}

// 확정 단계 상태는 Status 응답에만 온다. 다른 명령에 붙거나 모양이 계약 밖이면 서비스 응답째 버린다(503).
func TestURLRunConfirmationRejectsOutOfContract(t *testing.T) {
	job := runTimingContractCases()[2].job
	for name, tc := range map[string]struct {
		method, path, body, key string
		confirmation            diagnosisprep.ProductConfirmationState
	}{
		"start carries confirmation": {http.MethodPost, URLBasePath + "/drafts/job-1/start", `{"version":"diagnosis-api.v2","expected_revision":1}`, "start-1",
			diagnosisprep.ProductConfirmationState{Present: true, Status: "completed"}},
		"unknown status":     {http.MethodGet, URLBasePath + "/drafts/job-1/status", "", "", diagnosisprep.ProductConfirmationState{Present: true, Status: "maybe"}},
		"absent with status": {http.MethodGet, URLBasePath + "/drafts/job-1/status", "", "", diagnosisprep.ProductConfirmationState{Status: "completed"}},
		"unreadable with reason": {http.MethodGet, URLBasePath + "/drafts/job-1/status", "", "",
			diagnosisprep.ProductConfirmationState{Present: true, Status: diagnosisprep.ConfirmationUnreadable, Reason: "x"}},
	} {
		t.Run(name, func(t *testing.T) {
			endpoint := &urlTestEndpoint{reply: func(req diagnosisprep.Request) (diagnosisprep.Response, error) {
				j := job
				c := tc.confirmation
				return diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: req.RequestID, Job: &j, Confirmation: &c}, nil
			}}
			response := urlTestRequest(urlTestHandler(t, endpoint), tc.method, tc.path, tc.body, tc.key)
			if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "confirmation") {
				t.Fatalf("%d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestURLRunConfirmationRejectsErrorPayload(t *testing.T) {
	endpoint := &urlTestEndpoint{reply: func(req diagnosisprep.Request) (diagnosisprep.Response, error) {
		return diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: req.RequestID, Error: "not_found",
			Confirmation: &diagnosisprep.ProductConfirmationState{Present: true, Status: "completed"}}, nil
	}}
	response := urlTestRequest(urlTestHandler(t, endpoint), http.MethodGet, URLBasePath+"/drafts/job-1/status", "", "")
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "confirmation") {
		t.Fatalf("IPC 오류 응답의 확정 상태를 받아들임: %d %s", response.Code, response.Body.String())
	}
}

// 시간 상한(A)과 확정 단계 상태(D)는 같은 Status 봉투의 독립적인 선택 칸이다.
func TestURLStatusCombinesTimeLimitAndConfirmation(t *testing.T) {
	job := runTimingContractCases()[5].job
	endpoint := &urlTestEndpoint{reply: func(req diagnosisprep.Request) (diagnosisprep.Response, error) {
		return diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: req.RequestID, Job: &job,
			Confirmation: &diagnosisprep.ProductConfirmationState{}}, nil
	}}
	handler := urlTestHandler(t, endpoint)
	handler.options.Now = func() time.Time { return time.Date(2026, 11, 17, 1, 5, 3, 0, time.UTC) }
	response := urlTestRequest(handler, http.MethodGet, URLBasePath+"/drafts/"+job.ID+"/status", "", "")
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var body struct {
		Draft struct {
			TimeLimit *diagnosisprep.TimeLimit `json:"time_limit"`
		} `json:"draft"`
		ServerTime time.Time `json:"server_time"`
		Confirmation urlConfirmation `json:"confirmation"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Draft.TimeLimit == nil || !sameTimeLimitContract(body.Draft.TimeLimit, job.TimeLimit) ||
		!body.ServerTime.Equal(handler.options.Now()) || body.Confirmation.Record != urlConfirmationAbsent {
		t.Fatalf("함께 온 상태 칸이 다름: %s", response.Body.String())
	}
}

func sameTimeLimitContract(a, b *diagnosisprep.TimeLimit) bool {
	return a.RunDeadlineAt.Equal(b.RunDeadlineAt) && a.ModelCloseAt.Equal(b.ModelCloseAt) &&
		a.ModelDrainMS == b.ModelDrainMS && a.ConfirmationReserveMS == b.ConfirmationReserveMS &&
		a.ShutdownMarginMS == b.ShutdownMarginMS
}
