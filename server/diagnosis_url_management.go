package server

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/Autumn-27/artex/diagnosisapi"
	"github.com/Autumn-27/artex/diagnosisprep"
)

// NewURLDiagnosisManagementHandler는 URL v2와 설치된 비공개 준비 서비스만 조립한다.
// 인증 API와 보고서 DTO bridge는 v1과 같으며 모델·Planner·Worker를 만들지 않는다.
func NewURLDiagnosisManagementHandler(ctx context.Context, pool *sql.DB, keyDir, dataDir string, endpoint diagnosisprep.Endpoint, runtimeAPIOrigin ...string) (http.Handler, error) {
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
	if len(runtimeAPIOrigin) == 1 && runtimeAPIOrigin[0] != "" {
		bridge, err := newDiagnosisManagementBridge(ctx, runtimeAPIOrigin[0], authenticate, management.BoundTask)
		if err != nil {
			clear(s.jwtKey)
			return nil, err
		}
		mux.Handle("/api/tasks/", bridge)
		mux.Handle("/api/exploration/activity", bridge)
		mux.Handle("/api/exploration/activity/", bridge)
		mux.Handle("/api/report/", bridge)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ctx.Err() != nil {
			writeJSON(w, 503, map[string]string{"version": diagnosisapi.URLVersion, "error": "preparation_service_unavailable"})
			return
		}
		if r.URL.Path != "/api/auth/status" && r.URL.Path != "/api/auth/login" && endpoint == nil {
			writeJSON(w, 503, map[string]string{"version": diagnosisapi.URLVersion, "error": "preparation_service_unavailable"})
			return
		}
		requestContext, cancel := context.WithCancel(r.Context())
		defer cancel()
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		mux.ServeHTTP(w, r.WithContext(requestContext))
	}), nil
}
