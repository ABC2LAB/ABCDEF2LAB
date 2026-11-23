package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

// 멈춘 작업 해제(C04) API. 운영자(제품 관리자)만 쓴다. 해제가 받아들여지면 준비 서비스가 스스로 정상 종료하고 컨테이너
// 재시작 정책으로 다시 뜬다. 이 API·서버는 Docker를 다루지 않는다. 응답을 못 받아도 감사 기록은 준비 저널에 남으므로
// 화면은 재접속 뒤 GET으로 결과를 확인한다.
const productReleasePath = "/api/product/preparation/release"

// productReleaseCallTimeout은 준비 서비스 호출 상한이다. 해제는 탐색 경계 세션 닫기를 기다릴 수 있어 다른 제어보다 길다.
const productReleaseCallTimeout = 30 * time.Second

// maxProductReleaseBody는 해제 요청 본문 상한이다(사유 상한 + JSON 여유).
const maxProductReleaseBody = 4 << 10

type productReleaseBody struct {
	JobID            string `json:"job_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	Reason           string `json:"reason"`
}

func productReleaseRequestID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "product-release-" + hex.EncodeToString(b[:]), nil
}

func productReleaseHTTP(w http.ResponseWriter, r *http.Request, owner string, endpoint diagnosisprep.Endpoint) {
	if owner != "ARTEX" {
		// 해제는 설치 전체의 새 시작 잠금을 다룬다. 운영자(제품 관리자)가 아니면 준비 서비스에 보내지도 않는다.
		writeJSON(w, 403, map[string]string{"error": "operator_required"})
		return
	}
	id, err := productReleaseRequestID()
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "preparation_service_unavailable"})
		return
	}
	request := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: id, Principal: owner}
	switch r.Method {
	case http.MethodGet:
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.URL.RawQuery != "" {
			writeJSON(w, 400, map[string]string{"error": "invalid_request"})
			return
		}
		request.Command = diagnosisprep.ReleaseStatus
	case http.MethodPost:
		media, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		raw, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProductReleaseBody))
		var body productReleaseBody
		if mediaErr != nil || media != "application/json" || readErr != nil || strictJSON(raw, &body) != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid_request"})
			return
		}
		request.Command = diagnosisprep.Release
		request.JobID = body.JobID
		request.ExpectedRevision = body.ExpectedRevision
		request.Release = &diagnosisprep.ReleaseRequest{Reason: strings.TrimSpace(body.Reason)}
	default:
		writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
		return
	}
	if diagnosisprep.ValidateRequest(request) != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), productReleaseCallTimeout)
	defer cancel()
	response, err := endpoint.Call(ctx, request)
	if err != nil || diagnosisprep.ValidateResponse(request, response) != nil {
		writeJSON(w, 503, map[string]string{"error": "preparation_service_unavailable"})
		return
	}
	if response.Error != "" {
		writeJSON(w, productReleaseErrorStatus(response.Error), map[string]string{"error": response.Error})
		return
	}
	out := map[string]any{"release": response.Release}
	if response.Job != nil {
		out["draft"] = response.Job
	}
	writeJSON(w, 200, out)
}

func productReleaseErrorStatus(code string) int {
	switch {
	case strings.HasPrefix(code, "release_"), code == "revision_conflict":
		return 409
	case code == "not_found":
		return 404
	case code == "forbidden":
		return 403
	case code == "invalid_request":
		return 400
	default:
		return 503
	}
}
