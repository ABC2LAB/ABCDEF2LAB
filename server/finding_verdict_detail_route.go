package server

import (
	"context"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
)

// 판정 상세 조회 경로 설치(docs/contracts/finding-verdict-detail-decision.md OD10). 제품 관리 중계의 작업 결속 읽기에만
// 둔다(진행 중 후보 조회 #694와 같은 자리). 인증 → 준비 서비스 AuthorizeTask(Principal·Job·Task 소유) → 저장 판정 읽기·
// 검증 순서다. 저장 보고서 모드 허용 목록은 늘리지 않고, 조회는 실행 복구·재검증을 하지 않는다.

const (
	verdictDetailSegment = "finding-verdict-detail"
	verdictDetailSuffix  = "/" + verdictDetailSegment
	// 읽기 한 번의 상한. 후보 조회와 같은 값이다.
	verdictDetailReadTimeout = 10 * time.Second
	// OD08·초안 §8.1의 권한·결속 오류 코드.
	verdictDetailForbidden          = "verdict_detail_forbidden"
	verdictDetailBindingUnavailable = "verdict_detail_binding_unavailable"
	verdictDetailAuthRequired       = "authentication_required"
	// 제품 정책 출처 참조는 declaration-<job>이다(cmd productFrozenPolicyRefPrefix와 같은 값).
	verdictDetailProductPolicyRefPrefix = "declaration-"
)

// FindingVerdictDetailPath는 mux의 경로 정리 전에 판정 상세 경로를 분기한다. 경로 조각 하나라도 이 이름이면 이 처리기로
// 보내고, 정확한 모양 검사는 findingVerdictDetailRequest가 한다(다른 처리기로 새지 않게).
func FindingVerdictDetailPath(r *http.Request) bool {
	for part := range strings.SplitSeq(r.URL.Path, "/") {
		if part == verdictDetailSegment {
			return true
		}
	}
	return false
}

// findingVerdictDetailRequest는 GET /api/tasks/{task_id}/finding-verdict-detail?{selector} 정확한 모양만 받는다. 본문·
// 인코딩된 경로·query 해석 오류(';' 등)는 거부한다.
func findingVerdictDetailRequest(r *http.Request) (int64, verdictDetailSelector, bool) {
	if r.Method != http.MethodGet || r.URL.RawPath != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		return 0, verdictDetailSelector{}, false
	}
	tail, ok := strings.CutPrefix(r.URL.Path, "/api/tasks/")
	id, pathOK := strings.CutSuffix(tail, verdictDetailSuffix)
	task, taskOK := verdictDetailID(id)
	if !ok || !pathOK || !taskOK {
		return 0, verdictDetailSelector{}, false
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, verdictDetailSelector{}, false
	}
	sel, ok := parseVerdictDetailSelector(q)
	return task, sel, ok
}

func verdictDetailError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code})
}

// verdictDetailStatus는 호출 위치에서 정한 단계 상수와 상태만 남긴다. 요청 값·오류 원문은 남기지 않는다.
func verdictDetailStatus(stage string, status int) int {
	if status >= http.StatusInternalServerError {
		log.Printf(logT("[verdict-detail] 조회 실패 단계=%s 상태=%d"), stage, status)
	}
	return status
}

// serveFindingVerdictDetail은 제품 관리 중계의 판정 상세 조회다.
func (b *diagnosisManagementBridge) serveFindingVerdictDetail(w http.ResponseWriter, r *http.Request) {
	task, sel, ok := findingVerdictDetailRequest(r)
	if !ok {
		verdictDetailError(w, http.StatusBadRequest, verdictDetailInvalidRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), verdictDetailReadTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	owner, err := b.authenticate(r)
	if errors.Is(err, errManagementAuthenticationUnavailable) {
		verdictDetailError(w, verdictDetailStatus("management_auth", http.StatusServiceUnavailable), verdictDetailUnavailable)
		return
	}
	if err != nil || owner == "" {
		verdictDetailError(w, http.StatusUnauthorized, verdictDetailAuthRequired)
		return
	}
	if b.candidateJob == nil || b.candidateEndpoint == nil || b.readPool == nil {
		verdictDetailError(w, verdictDetailStatus("management_config", http.StatusServiceUnavailable), verdictDetailUnavailable)
		return
	}
	taskRef := strconv.FormatInt(task, 10)
	job, err := b.candidateJob(ctx, owner, taskRef)
	if err != nil {
		verdictDetailError(w, verdictDetailStatus("job_read", http.StatusServiceUnavailable), verdictDetailUnavailable)
		return
	}
	if job == nil {
		verdictDetailError(w, http.StatusForbidden, verdictDetailForbidden)
		return
	}
	// AuthorizeTask 응답은 작업 번호가 같은 Job만 싣는다(ValidateResponse). 회차 참조가 없으면 결속을 고를 수 없다.
	if job.TaskID != taskRef || job.ID == "" {
		verdictDetailError(w, http.StatusConflict, verdictDetailBindingUnavailable)
		return
	}
	reader := productPolicySourceReader{endpoint: b.candidateEndpoint, principal: owner, jobID: job.ID}
	env, status, code := readFindingVerdictDetail(ctx, b.readPool, task, sel, reader)
	if status != http.StatusOK {
		verdictDetailError(w, status, code)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, env)
}

// productPolicySourceReader는 제품 판정의 당시 정책 동결본을 준비 서비스 IPC(policy_source)로 대조한다(OD06). 관리 API
// 프로세스는 회차 디렉터리를 직접 읽지 않는다. 판정의 회차가 조회를 허가한 작업의 회차가 아니면(다른 회차의 동결본을
// 가리킴) 불일치다.
type productPolicySourceReader struct {
	endpoint  diagnosisprep.Endpoint
	principal string
	jobID     string
}

func (p productPolicySourceReader) CheckPolicySource(ctx context.Context, req verdictPolicySourceRequest) verdictPolicySourceResult {
	if req.SourceKind != db.E3SourceProduct || req.JobRef != p.jobID || req.Ref != verdictDetailProductPolicyRefPrefix+p.jobID {
		return verdictPolicySourceMismatch
	}
	if p.endpoint == nil || p.principal == "" {
		return verdictPolicySourceError
	}
	call := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "verdict-policy-source", Principal: p.principal,
		Command: diagnosisprep.ProductControl, Product: &diagnosisprep.ProductRequest{Operation: "policy_source", JobID: p.jobID}}
	response, err := p.endpoint.Call(ctx, call)
	if err != nil || diagnosisprep.ValidateResponse(call, response) != nil || response.Error != "" || response.Product == nil {
		return verdictPolicySourceError
	}
	if response.Product.Reason != "" || response.Product.PolicySource == nil {
		// 준비 서비스가 회차를 찾지 못했거나(not_found) 동결본을 읽지 못했다. 당시 원본을 확인할 수 없다는 뜻이다.
		return verdictPolicySourceUnavailable
	}
	source := response.Product.PolicySource
	if !source.Present {
		return verdictPolicySourceUnavailable
	}
	if source.SHA256 != hex.EncodeToString(req.DigestSHA256[:]) {
		return verdictPolicySourceMismatch
	}
	return verdictPolicySourceMatched
}
