package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/diagnosisapi"
	"github.com/golang-jwt/jwt/v5"
)

// NewDiagnosisManagementHandler는 이미 설치된 인증과 외부 실행 Gateway만 조립한다.
// 기존 단일 관리자 ARTEX를 사용하며 다중 사용자 권한 분리를 구현했다고 주장하지 않는다.
// pool과 Gateway transport의 수명은 운영자 bootstrap이 소유한다. 모델·엔진·키를 만들지 않는다.
func NewDiagnosisManagementHandler(ctx context.Context, pool *sql.DB, keyDir, dataDir string, registry diagnosisapi.Registry, gateway diagnosis.Gateway, runtimeAPIOrigin ...string) (http.Handler, error) {
	if ctx == nil || ctx.Err() != nil || pool == nil || len(runtimeAPIOrigin) > 1 {
		return nil, ErrControlledReadHostConfiguration
	}
	key, err := readControlledReadAssemblyJWTKey(keyDir, dataDir)
	if err != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	s := &Server{ctx: ctx, m: &Manager{pg: &db.DB{DB: pool}}, jwtKey: key}
	if s.bootstrapFirstDiagnosisAuth() != nil || ctx.Err() != nil {
		clear(key)
		return nil, ErrControlledReadHostConfiguration
	}
	authenticate := func(r *http.Request) (string, error) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || len(auth) <= 7 {
			return "", errors.New("인증이 필요합니다")
		}
		hash, err := s.reloadPassHash()
		if err != nil {
			return "", err
		}
		claims := &jwt.RegisteredClaims{}
		token, err := jwt.ParseWithClaims(strings.TrimPrefix(auth, "Bearer "), claims, func(*jwt.Token) (any, error) { return s.signingKey(hash), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
		if err != nil || !token.Valid || claims.Subject != "ARTEX" {
			return "", errors.New("인증을 확인할 수 없습니다")
		}
		return "ARTEX", nil
	}
	management, err := diagnosisapi.New(diagnosisapi.Options{Registry: registry, Gateway: gateway, Authenticate: authenticate})
	if err != nil {
		clear(key)
		return nil, ErrControlledReadHostConfiguration
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("POST /api/auth/login", s.authLogin)
	mux.Handle(diagnosisapi.BasePath+"/", management)
	if len(runtimeAPIOrigin) == 1 && runtimeAPIOrigin[0] != "" {
		bridge, bridgeErr := newDiagnosisManagementBridge(ctx, runtimeAPIOrigin[0], authenticate, management.BoundTask)
		if bridgeErr != nil {
			return nil, bridgeErr
		}
		mux.Handle("/api/tasks/", bridge)
		mux.Handle("/api/report/", bridge)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ctx.Err() != nil {
			writeErr(w, 503, "진단 관리 서비스가 종료되었습니다")
			return
		}
		if r.URL.Path != "/api/auth/status" && r.URL.Path != "/api/auth/login" &&
			(registry == nil || gateway == nil) {
			writeJSON(w, 503, map[string]string{"version": diagnosisapi.Version, "error": "execution_service_unconfigured"})
			return
		}
		requestContext, cancel := context.WithCancel(r.Context())
		defer cancel()
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		mux.ServeHTTP(w, r.WithContext(requestContext))
	}), nil
}
