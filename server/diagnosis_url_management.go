package server

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"github.com/Autumn-27/artex/diagnosisapi"
	"github.com/Autumn-27/artex/diagnosisprep"
)

// NewURLDiagnosisManagementHandler는 URL v2와 설치된 비공개 준비 서비스만 조립한다.
// 인증 API와 보고서 DTO bridge는 v1과 같으며 모델·Planner·Worker를 만들지 않는다.
func NewURLDiagnosisManagementHandler(ctx context.Context, pool *sql.DB, keyDir, dataDir string, endpoint diagnosisprep.Endpoint, runtimeAPIOrigin ...string) (http.Handler, error) {
	return newURLDiagnosisManagementHandler(ctx, pool, keyDir, dataDir, endpoint, false, runtimeAPIOrigin...)
}

func newURLDiagnosisManagementHandler(ctx context.Context, pool *sql.DB, keyDir, dataDir string, endpoint diagnosisprep.Endpoint, product bool, runtimeAPIOrigin ...string) (http.Handler, error) {
	if ctx == nil || ctx.Err() != nil || pool == nil || len(runtimeAPIOrigin) > 1 {
		return nil, ErrControlledReadHostConfiguration
	}
	s, authenticate, err := newDiagnosisManagementAuthentication(ctx, pool, keyDir, dataDir)
	if err != nil {
		return nil, err
	}
	management, err := diagnosisapi.NewURL(diagnosisapi.URLOptions{Authenticate: authenticate, Endpoint: endpoint})
	if err != nil {
		clear(s.jwtKey)
		return nil, ErrControlledReadHostConfiguration
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("POST /api/auth/login", s.authLogin)
	mux.Handle(diagnosisapi.URLBasePath+"/", management)
	var runtimeBridge *diagnosisManagementBridge
	if len(runtimeAPIOrigin) == 1 && runtimeAPIOrigin[0] != "" {
		bridge, err := newDiagnosisManagementBridge(ctx, runtimeAPIOrigin[0], authenticate, management.BoundTask, pool)
		if err != nil {
			clear(s.jwtKey)
			return nil, err
		}
		bridge.reportAuthorize = management.CheckBoundTask
		if product {
			bridge.candidateJob = management.ReadBoundTask
			bridge.candidateEndpoint = endpoint
			runtimeBridge = bridge
		}
		mux.Handle("/api/tasks/", bridge)
		mux.Handle("/api/exploration/activity", bridge)
		mux.Handle("/api/exploration/activity/", bridge)
		mux.Handle("/api/report/", bridge)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if ctx.Err() != nil {
			writeJSON(w, 503, map[string]string{"version": diagnosisapi.URLVersion, "error": "preparation_service_unavailable"})
			return
		}
		if r.URL.Path != "/api/auth/status" && r.URL.Path != "/api/auth/login" && endpoint == nil {
			writeJSON(w, 503, map[string]string{"version": diagnosisapi.URLVersion, "error": "preparation_service_unavailable"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/coverage-graph") || strings.HasSuffix(r.URL.Path, "/access-map") {
			if _, ok := reportGraphRequestTask(r); !ok {
				writeErr(w, 403, "허용된 보고서 GET 경로만 사용할 수 있습니다")
				return
			}
		}
		requestContext, cancel := context.WithCancel(r.Context())
		defer cancel()
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		if strings.HasSuffix(r.URL.Path, followupSuffix) {
			if runtimeBridge == nil {
				http.NotFound(w, r)
				return
			}
			runtimeBridge.serveRemediationFollowup(w, r.WithContext(requestContext))
			return
		}
		if ProductCandidatePath(r) && runtimeBridge != nil {
			runtimeBridge.serveProductCandidates(w, r.WithContext(requestContext))
			return
		}
		// 판정 상세 조회는 제품 관리 중계에만 있다(OD10). 제품이 아니면 실행 API로 넘기지 않고 없는 경로로 답한다.
		if FindingVerdictDetailPath(r) {
			if runtimeBridge == nil {
				http.NotFound(w, r)
				return
			}
			runtimeBridge.serveFindingVerdictDetail(w, r.WithContext(requestContext))
			return
		}
		// 제품 판정 목록도 같은 자리다(#801 §2). 제품이 아니면 실행 API로 넘기지 않는다.
		if ProductVerdictListPath(r) {
			if runtimeBridge == nil {
				http.NotFound(w, r)
				return
			}
			runtimeBridge.serveProductVerdictList(w, r.WithContext(requestContext))
			return
		}
		// 제품 접근통제 지도도 같은 자리다(product-access-map §2.1). 제품이 아니면 실행 API로 넘기지 않는다.
		if ProductAccessMapPath(r) {
			if runtimeBridge == nil {
				http.NotFound(w, r)
				return
			}
			runtimeBridge.serveProductAccessMap(w, r.WithContext(requestContext))
			return
		}
		mux.ServeHTTP(w, r.WithContext(requestContext))
	}), nil
}
