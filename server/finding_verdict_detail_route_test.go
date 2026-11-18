package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/oracle"
)

// 판정 상세 조회 경로 설치 시험(docs/contracts/finding-verdict-detail-decision.md OD06·OD10).

func TestFindingVerdictDetailRequestShape(t *testing.T) {
	for _, tc := range []struct {
		method, target, body string
		valid                bool
	}{
		{"GET", "/api/tasks/1/finding-verdict-detail?verdict_id=2", "", true},
		{"GET", "/api/tasks/9223372036854775807/finding-verdict-detail?finding_id=9223372036854775807", "", true},
		{"GET", "/api/tasks/1/finding-verdict-detail", "", false},
		{"GET", "/api/tasks/1/finding-verdict-detail?", "", false},
		{"GET", "/api/tasks/1/finding-verdict-detail?verdict_id=2&finding_id=3", "", false},
		{"GET", "/api/tasks/1/finding-verdict-detail?verdict_id=2&verdict_id=2", "", false},
		{"GET", "/api/tasks/1/finding-verdict-detail?candidate_id=2", "", false},
		{"GET", "/api/tasks/1/finding-verdict-detail?verdict_id=02", "", false},
		{"GET", "/api/tasks/1/finding-verdict-detail?verdict_id=2;x=1", "", false},
		// query 해석 오류가 난 조각을 버리면 남은 selector가 유효해 보인다. 오류 자체로 거부한다.
		{"GET", "/api/tasks/1/finding-verdict-detail?verdict_id=2&x;y=1", "", false},
		{"GET", "/api/tasks/1/finding-verdict-detail?verdict_id=2&%zz=1", "", false},
		{"GET", "/api/tasks/01/finding-verdict-detail?verdict_id=2", "", false},
		{"GET", "/api/tasks/0/finding-verdict-detail?verdict_id=2", "", false},
		{"GET", "/api/tasks/%31/finding-verdict-detail?verdict_id=2", "", false},
		{"GET", "/api/tasks/1/finding%2Dverdict-detail?verdict_id=2", "", false},
		{"GET", "/api/tasks/1/finding-verdict-detail/?verdict_id=2", "", false},
		{"GET", "/api/tasks/2/../1/finding-verdict-detail?verdict_id=2", "", false},
		{"GET", "/api/tasks//1/finding-verdict-detail?verdict_id=2", "", false},
		{"GET", "/api/tasks/1/finding-verdict-detail?verdict_id=2", "{}", false},
		{"HEAD", "/api/tasks/1/finding-verdict-detail?verdict_id=2", "", false},
		{"POST", "/api/tasks/1/finding-verdict-detail?verdict_id=2", "", false},
	} {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
			if _, _, valid := findingVerdictDetailRequest(r); valid != tc.valid {
				t.Fatal("판정 상세 경로 판정 불일치", valid)
			}
			if !FindingVerdictDetailPath(r) && !strings.Contains(tc.target, "%2D") {
				t.Fatal("판정 상세 경로를 다른 처리기로 보냄")
			}
		})
	}
	if FindingVerdictDetailPath(httptest.NewRequest("GET", "/api/tasks/1/diagnostic-candidates", nil)) {
		t.Fatal("다른 경로를 판정 상세로 봄")
	}
}

// verdictDetailTestEndpoint는 준비 서비스 IPC의 policy_source만 답한다.
type verdictDetailTestEndpoint struct {
	source *diagnosisprep.ProductPolicySource
	reason string
	err    error
	calls  []diagnosisprep.Request
}

func (e *verdictDetailTestEndpoint) Call(_ context.Context, req diagnosisprep.Request) (diagnosisprep.Response, error) {
	e.calls = append(e.calls, req)
	if e.err != nil {
		return diagnosisprep.Response{}, e.err
	}
	return diagnosisprep.Response{Version: req.Version, RequestID: req.RequestID,
		Product: &diagnosisprep.ProductResponse{PolicySource: e.source, Reason: e.reason}}, nil
}

func TestProductPolicySourceReader(t *testing.T) {
	declaration := sha256.Sum256([]byte("synthetic declaration"))
	match := hex.EncodeToString(declaration[:])
	other := strings.Repeat("c", 64)
	req := verdictPolicySourceRequest{SourceKind: "product", JobRef: "job-1", RunRef: "run-1", Ref: "declaration-job-1",
		DigestSHA256: declaration, TargetApprovalRevision: "target-revision-1"}
	for name, tc := range map[string]struct {
		edit     func(*verdictPolicySourceRequest)
		endpoint *verdictDetailTestEndpoint
		want     verdictPolicySourceResult
		calls    int
	}{
		"matched":         {endpoint: &verdictDetailTestEndpoint{source: &diagnosisprep.ProductPolicySource{Present: true, SHA256: match}}, want: verdictPolicySourceMatched, calls: 1},
		"digest_mismatch": {endpoint: &verdictDetailTestEndpoint{source: &diagnosisprep.ProductPolicySource{Present: true, SHA256: other}}, want: verdictPolicySourceMismatch, calls: 1},
		"absent":          {endpoint: &verdictDetailTestEndpoint{source: &diagnosisprep.ProductPolicySource{}}, want: verdictPolicySourceUnavailable, calls: 1},
		"prep_not_found":  {endpoint: &verdictDetailTestEndpoint{reason: "not_found"}, want: verdictPolicySourceUnavailable, calls: 1},
		"prep_unreadable": {endpoint: &verdictDetailTestEndpoint{reason: "policy_source_unavailable"}, want: verdictPolicySourceUnavailable, calls: 1},
		"ipc_error":       {endpoint: &verdictDetailTestEndpoint{err: errors.New("closed")}, want: verdictPolicySourceError, calls: 1},
		// 응답 모양이 계약 밖이면(present인데 digest 없음) 장애다.
		"bad_response": {endpoint: &verdictDetailTestEndpoint{source: &diagnosisprep.ProductPolicySource{Present: true}}, want: verdictPolicySourceError, calls: 1},
		// 판정이 가리키는 회차·참조가 조회를 허가한 작업의 회차와 다르면 준비 서비스에 묻지 않고 불일치다.
		"other_job":  {edit: func(r *verdictPolicySourceRequest) { r.JobRef = "job-2" }, endpoint: &verdictDetailTestEndpoint{}, want: verdictPolicySourceMismatch},
		"other_ref":  {edit: func(r *verdictPolicySourceRequest) { r.Ref = "declaration-job-2" }, endpoint: &verdictDetailTestEndpoint{}, want: verdictPolicySourceMismatch},
		"eval_kind":  {edit: func(r *verdictPolicySourceRequest) { r.SourceKind = "eval" }, endpoint: &verdictDetailTestEndpoint{}, want: verdictPolicySourceMismatch},
		"empty_ref":  {edit: func(r *verdictPolicySourceRequest) { r.Ref = "" }, endpoint: &verdictDetailTestEndpoint{}, want: verdictPolicySourceMismatch},
		"empty_job":  {edit: func(r *verdictPolicySourceRequest) { r.JobRef, r.Ref = "", "declaration-" }, endpoint: &verdictDetailTestEndpoint{}, want: verdictPolicySourceMismatch},
		"no_command": {endpoint: nil, want: verdictPolicySourceError},
	} {
		t.Run(name, func(t *testing.T) {
			r := req
			if tc.edit != nil {
				tc.edit(&r)
			}
			reader := productPolicySourceReader{principal: "ARTEX", jobID: "job-1"}
			if tc.endpoint != nil {
				reader.endpoint = tc.endpoint
			}
			if got := reader.CheckPolicySource(t.Context(), r); got != tc.want {
				t.Fatalf("결과 %d, 기대 %d", got, tc.want)
			}
			if tc.endpoint == nil {
				return
			}
			if len(tc.endpoint.calls) != tc.calls {
				t.Fatalf("준비 서비스 호출 %d건", len(tc.endpoint.calls))
			}
			if tc.calls == 1 {
				c := tc.endpoint.calls[0]
				if c.Command != diagnosisprep.ProductControl || c.Principal != "ARTEX" || c.Product == nil || c.Product.Operation != "policy_source" ||
					c.Product.JobID != "job-1" || diagnosisprep.ValidateRequest(c) != nil {
					t.Fatalf("policy_source 요청: %+v", c)
				}
			}
		})
	}
}

// 저장된 제품 판정을 제품 관리 중계의 판정 상세 경로로 읽는다. 인증 → 작업 소유(AuthorizeTask) → 저장 판정 읽기·검증 순서이고,
// 정책 동결본은 준비 서비스 IPC로 대조한다.
func TestFindingVerdictDetailRouteProduct(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	refs := storeVerdictDetailProduct(t, pg, oracle.Confirmed)
	taskRef := strconv.FormatInt(refs.task, 10)
	declaration := sha256.Sum256([]byte("synthetic declaration"))
	path := "/api/tasks/" + taskRef + "/finding-verdict-detail?verdict_id=" + strconv.FormatInt(refs.verdict, 10)
	const owner = "ARTEX"
	type call struct{ owner, task string }
	for name, tc := range map[string]struct {
		target     string
		authErr    error
		owner      string
		job        *diagnosisprep.Job
		jobErr     error
		noEndpoint bool
		source     *diagnosisprep.ProductPolicySource
		ipcErr     error
		status     int
		code       string
		check      func(t *testing.T, body map[string]any)
	}{
		"verified": {owner: owner, job: &diagnosisprep.Job{ID: "job-synthetic", TaskID: taskRef},
			source: &diagnosisprep.ProductPolicySource{Present: true, SHA256: hex.EncodeToString(declaration[:])}, status: 200,
			check: func(t *testing.T, body map[string]any) {
				checks, _ := body["record_checks"].(map[string]any)
				if body["source_kind"] != "product" || checks["status"] != "verified" || body["record"] == nil {
					t.Fatalf("검증된 제품 판정: %v", body)
				}
			}},
		"policy_digest_mismatch": {owner: owner, job: &diagnosisprep.Job{ID: "job-synthetic", TaskID: taskRef},
			source: &diagnosisprep.ProductPolicySource{Present: true, SHA256: strings.Repeat("d", 64)}, status: 200,
			check: func(t *testing.T, body map[string]any) {
				checks, _ := body["record_checks"].(map[string]any)
				reasons, _ := checks["reason_codes"].([]any)
				if checks["status"] != "unavailable" || len(reasons) != 1 || reasons[0] != verdictDetailRecordIntegrityFailed || body["record"] != nil {
					t.Fatalf("동결본 불일치: %v", body)
				}
			}},
		"policy_absent": {owner: owner, job: &diagnosisprep.Job{ID: "job-synthetic", TaskID: taskRef},
			source: &diagnosisprep.ProductPolicySource{}, status: 200,
			check: func(t *testing.T, body map[string]any) {
				checks, _ := body["record_checks"].(map[string]any)
				reasons, _ := checks["reason_codes"].([]any)
				if checks["status"] != "unavailable" || len(reasons) != 1 || reasons[0] != verdictDetailPolicySourceMissing || body["record"] == nil {
					t.Fatalf("동결본 없음: %v", body)
				}
			}},
		// 판정의 회차가 조회를 허가한 작업의 회차가 아니면 동결본을 묻지 않고 진위 실패다.
		"other_job_binding": {owner: owner, job: &diagnosisprep.Job{ID: "job-other", TaskID: taskRef}, status: 200,
			check: func(t *testing.T, body map[string]any) {
				checks, _ := body["record_checks"].(map[string]any)
				reasons, _ := checks["reason_codes"].([]any)
				if checks["status"] != "unavailable" || len(reasons) != 1 || reasons[0] != verdictDetailRecordIntegrityFailed || body["record"] != nil {
					t.Fatalf("다른 회차 결속: %v", body)
				}
			}},
		"policy_ipc_error": {owner: owner, job: &diagnosisprep.Job{ID: "job-synthetic", TaskID: taskRef}, ipcErr: errors.New("closed"),
			status: 503, code: verdictDetailUnavailable},
		"bad_request": {target: "/api/tasks/" + taskRef + "/finding-verdict-detail?verdict_id=1&finding_id=1", owner: owner,
			status: 400, code: verdictDetailInvalidRequest},
		"auth_unavailable": {authErr: errManagementAuthenticationUnavailable, status: 503, code: verdictDetailUnavailable},
		"unauthenticated":  {authErr: errors.New("인증이 필요합니다"), status: 401, code: verdictDetailAuthRequired},
		"job_read_error":   {owner: owner, jobErr: errors.New("prep down"), status: 503, code: verdictDetailUnavailable},
		"not_owned":        {owner: owner, status: 403, code: verdictDetailForbidden},
		"job_task_differs": {owner: owner, job: &diagnosisprep.Job{ID: "job-synthetic", TaskID: "999"}, status: 409, code: verdictDetailBindingUnavailable},
		"job_without_id":   {owner: owner, job: &diagnosisprep.Job{TaskID: taskRef}, status: 409, code: verdictDetailBindingUnavailable},
		"not_configured":   {owner: owner, noEndpoint: true, status: 503, code: verdictDetailUnavailable},
		// 다른 작업 번호로 고른 판정은 없음과 같다(존재 여부를 드러내지 않음).
		"other_task_verdict": {target: "/api/tasks/" + strconv.FormatInt(refs.task+1_000_000, 10) + "/finding-verdict-detail?verdict_id=" + strconv.FormatInt(refs.verdict, 10),
			owner: owner, job: &diagnosisprep.Job{ID: "job-synthetic", TaskID: strconv.FormatInt(refs.task+1_000_000, 10)}, status: 404, code: verdictDetailNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			endpoint := &verdictDetailTestEndpoint{source: tc.source, err: tc.ipcErr}
			var asked []call
			bridge := &diagnosisManagementBridge{
				authenticate: func(*http.Request) (string, error) { return tc.owner, tc.authErr },
				readPool:     pg.DB,
				candidateJob: func(_ context.Context, o, task string) (*diagnosisprep.Job, error) {
					asked = append(asked, call{o, task})
					return tc.job, tc.jobErr
				},
				candidateEndpoint: endpoint,
			}
			if tc.noEndpoint {
				bridge.candidateEndpoint = nil
			}
			target := path
			if tc.target != "" {
				target = tc.target
			}
			w := httptest.NewRecorder()
			bridge.serveFindingVerdictDetail(w, httptest.NewRequest(http.MethodGet, target, nil))
			if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("상태 %d: %s", w.Code, w.Body)
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if tc.code != "" {
				if len(body) != 1 || body["error"] != tc.code {
					t.Fatalf("오류 응답: %s", w.Body)
				}
			} else if tc.check != nil {
				tc.check(t, body)
			}
			// 작업 소유는 인증된 주체와 경로의 작업 번호로만 묻는다.
			if tc.status != 400 && tc.authErr == nil && !tc.noEndpoint {
				wantTask := taskRef
				if tc.job != nil {
					wantTask = tc.job.TaskID
					if tc.job.TaskID == "999" {
						wantTask = taskRef
					}
				}
				if len(asked) != 1 || asked[0].owner != owner || asked[0].task != wantTask {
					t.Fatalf("작업 소유 확인: %+v", asked)
				}
			}
			if tc.status == 400 && len(asked) != 0 {
				t.Fatal("형식이 틀린 요청에서 작업 소유를 물음")
			}
		})
	}
}
