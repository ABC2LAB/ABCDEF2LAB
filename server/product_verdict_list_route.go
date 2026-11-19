package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// 제품 판정 목록 조회 경로 설치(#801 §2): 판정 상세와 같은 자리(OD10), 제품 관리 중계의 작업 결속 읽기에만 둔다. 인증 →
// 준비 서비스 AuthorizeTask(Principal·Job·Task 소유) → 저장 판정 읽기 → 준비 서비스 IPC(policy_source) → 행마다 진위 확인
// 순서다. 저장 보고서 모드 허용 목록은 늘리지 않는다.

const productVerdictListSuffix = "/product-verdicts"

// ProductVerdictListPath는 mux의 경로 정리 전에 제품 판정 목록 경로를 분기한다. 경로 조각 하나라도 이 이름이면 이 처리기로
// 보내고, 정확한 모양 검사는 productVerdictListRequest가 한다(다른 처리기로 새지 않게).
func ProductVerdictListPath(r *http.Request) bool {
	for part := range strings.SplitSeq(r.URL.Path, "/") {
		if part == strings.TrimPrefix(productVerdictListSuffix, "/") {
			return true
		}
	}
	return false
}

// productVerdictListRequest는 GET /api/tasks/{task_id}/product-verdicts 정확한 모양만 받는다. query·본문·인코딩된 경로는
// 400이고, GET이 아니면 405다.
func productVerdictListRequest(r *http.Request) (int64, int) {
	if r.Method != http.MethodGet {
		return 0, http.StatusMethodNotAllowed
	}
	if r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		return 0, http.StatusBadRequest
	}
	tail, ok := strings.CutPrefix(r.URL.Path, "/api/tasks/")
	id, pathOK := strings.CutSuffix(tail, productVerdictListSuffix)
	task, taskOK := verdictDetailID(id)
	if !ok || !pathOK || !taskOK {
		return 0, http.StatusBadRequest
	}
	return task, http.StatusOK
}

// serveProductVerdictList는 제품 관리 중계의 제품 판정 목록 조회다. AuthorizeTask 실패는 모두 같은 403이다(작업이 있는지를
// 응답으로 가르지 않는다).
func (b *diagnosisManagementBridge) serveProductVerdictList(w http.ResponseWriter, r *http.Request) {
	task, status := productVerdictListRequest(r)
	if status == http.StatusMethodNotAllowed {
		w.Header().Set("Allow", http.MethodGet)
		verdictDetailError(w, status, productVerdictsInvalidRequest)
		return
	}
	if status != http.StatusOK {
		verdictDetailError(w, status, productVerdictsInvalidRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), verdictDetailReadTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	owner, err := b.authenticate(r)
	if errors.Is(err, errManagementAuthenticationUnavailable) {
		verdictDetailError(w, verdictDetailStatus("management_auth", http.StatusServiceUnavailable), productVerdictsUnavailable)
		return
	}
	if err != nil || owner == "" {
		verdictDetailError(w, http.StatusUnauthorized, verdictDetailAuthRequired)
		return
	}
	if b.candidateJob == nil || b.candidateEndpoint == nil || b.readPool == nil {
		verdictDetailError(w, verdictDetailStatus("management_config", http.StatusServiceUnavailable), productVerdictsUnavailable)
		return
	}
	taskRef := strconv.FormatInt(task, 10)
	job, err := b.candidateJob(ctx, owner, taskRef)
	if err != nil {
		verdictDetailError(w, verdictDetailStatus("job_read", http.StatusServiceUnavailable), productVerdictsUnavailable)
		return
	}
	if job == nil {
		verdictDetailError(w, http.StatusForbidden, productVerdictsForbidden)
		return
	}
	if job.TaskID != taskRef || job.ID == "" {
		verdictDetailError(w, http.StatusConflict, productVerdictsBindingUnavailable)
		return
	}
	reader := productPolicySourceReader{endpoint: b.candidateEndpoint, principal: owner, jobID: job.ID}
	env, status, code := readProductVerdictList(ctx, b.readPool, task, job.ID, reader)
	if status != http.StatusOK {
		verdictDetailError(w, status, code)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, env)
}
