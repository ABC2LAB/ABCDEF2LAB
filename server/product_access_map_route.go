package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// 제품 접근통제 지도 경로 설치(docs/contracts/product-access-map.md §2.1): 제품 판정 목록과 같은 자리, 제품 관리 중계의 작업
// 결속 읽기에만 둔다. 인증 → 준비 서비스 AuthorizeTask → 저장 판정 읽기 → IPC(policy_source) → 진위 확인 → 투영 순서다.
// 평가 지도(GET /api/tasks/{id}/access-map)와 저장 보고서 모드 허용 목록은 바꾸지 않는다.

const productAccessMapSuffix = "/product-access-map"

// ProductAccessMapPath는 mux의 경로 정리 전에 제품 지도 경로를 분기한다. 경로 조각 하나라도 이 이름이면 이 처리기로 보내고,
// 정확한 모양 검사는 productAccessMapRequest가 한다(다른 처리기로 새지 않게).
func ProductAccessMapPath(r *http.Request) bool {
	for part := range strings.SplitSeq(r.URL.Path, "/") {
		if part == strings.TrimPrefix(productAccessMapSuffix, "/") {
			return true
		}
	}
	return false
}

// productAccessMapRequest는 GET /api/tasks/{task_id}/product-access-map 정확한 모양만 받는다. query·본문·인코딩된 경로는
// 400이고, GET이 아니면 405다.
func productAccessMapRequest(r *http.Request) (int64, int) {
	if r.Method != http.MethodGet {
		return 0, http.StatusMethodNotAllowed
	}
	if r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		return 0, http.StatusBadRequest
	}
	tail, ok := strings.CutPrefix(r.URL.Path, "/api/tasks/")
	id, pathOK := strings.CutSuffix(tail, productAccessMapSuffix)
	task, taskOK := verdictDetailID(id)
	if !ok || !pathOK || !taskOK {
		return 0, http.StatusBadRequest
	}
	return task, http.StatusOK
}

// serveProductAccessMap은 제품 관리 중계의 제품 지도 조회다. AuthorizeTask 실패는 모두 같은 403이다(작업이 있는지를 응답으로
// 가르지 않는다).
func (b *diagnosisManagementBridge) serveProductAccessMap(w http.ResponseWriter, r *http.Request) {
	task, status := productAccessMapRequest(r)
	if status == http.StatusMethodNotAllowed {
		w.Header().Set("Allow", http.MethodGet)
		verdictDetailError(w, status, productAccessMapInvalidRequest)
		return
	}
	if status != http.StatusOK {
		verdictDetailError(w, status, productAccessMapInvalidRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), verdictDetailReadTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	owner, err := b.authenticate(r)
	if errors.Is(err, errManagementAuthenticationUnavailable) {
		verdictDetailError(w, verdictDetailStatus("management_auth", http.StatusServiceUnavailable), productAccessMapUnavailable)
		return
	}
	if err != nil || owner == "" {
		verdictDetailError(w, http.StatusUnauthorized, verdictDetailAuthRequired)
		return
	}
	if b.candidateJob == nil || b.candidateEndpoint == nil || b.readPool == nil {
		verdictDetailError(w, verdictDetailStatus("management_config", http.StatusServiceUnavailable), productAccessMapUnavailable)
		return
	}
	taskRef := strconv.FormatInt(task, 10)
	job, err := b.candidateJob(ctx, owner, taskRef)
	if err != nil {
		verdictDetailError(w, verdictDetailStatus("job_read", http.StatusServiceUnavailable), productAccessMapUnavailable)
		return
	}
	if job == nil {
		verdictDetailError(w, http.StatusForbidden, productAccessMapForbidden)
		return
	}
	if job.TaskID != taskRef || job.ID == "" {
		verdictDetailError(w, http.StatusConflict, productAccessMapBindingUnavailable)
		return
	}
	reader := productPolicySourceReader{endpoint: b.candidateEndpoint, principal: owner, jobID: job.ID}
	env, status, code := readProductAccessMap(ctx, b.readPool, task, job.ID, reader)
	if status != http.StatusOK {
		verdictDetailError(w, status, code)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, env)
}
