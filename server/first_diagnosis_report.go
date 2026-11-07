package server

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/Autumn-27/artex/db"
)

// NewFirstDiagnosisReportHandler는 이미 저장된 한 작업만 제공한다.
// pool은 호출자가 소유하며 HTTP drain 뒤 닫는다. 실행·복구·모델·migration·seed는 시작하지 않는다.
func NewFirstDiagnosisReportHandler(ctx context.Context, pool *sql.DB, keyDir, dataDir string, taskID int64) (http.Handler, error) {
	if ctx == nil || ctx.Err() != nil || pool == nil || taskID <= 0 || CheckFirstDiagnosisReportDatabase(ctx, pool) != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	var explorationID int64
	if pool.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL`, taskID).Scan(&explorationID) != nil || explorationID <= 0 {
		return nil, ErrControlledReadHostConfiguration
	}
	key, err := readControlledReadAssemblyJWTKey(keyDir, dataDir)
	if err != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	// 전체 Server 생성자는 기존 실행을 복구할 수 있으므로 보고서와 인증이 쓰는 DB만 제공한다.
	s := &Server{ctx: ctx, m: &Manager{pg: &db.DB{DB: pool}}, jwtKey: key}
	if s.bootstrapFirstDiagnosisAuth() != nil || ctx.Err() != nil {
		clear(key)
		return nil, ErrControlledReadHostConfiguration
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("POST /api/auth/login", s.authLogin)
	mux.HandleFunc("GET /api/report/diagnostic", s.diagnosticReport)
	mux.HandleFunc("GET /api/report/diagnostic/remediation", s.listDiagnosticRemediation)
	mux.HandleFunc("POST /api/report/diagnostic/remediation", s.createDiagnosticRemediation)
	mux.HandleFunc("GET /api/report/diagnostic/remediation/prompt", s.copyDiagnosticRemediation)
	scoped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ctx.Err() != nil {
			writeErr(w, http.StatusServiceUnavailable, "저장 보고서 서비스가 종료되었습니다")
			return
		}
		switch r.URL.Path {
		case "/api/health", "/api/auth/status", "/api/auth/login":
		default:
			requested, ok := firstDiagnosisReportRequestTask(r)
			if !ok || requested != taskID {
				writeErr(w, http.StatusForbidden, "선택한 작업의 저장 보고서만 사용할 수 있습니다")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
	return cors(s.requireAuth(scoped)), nil
}
