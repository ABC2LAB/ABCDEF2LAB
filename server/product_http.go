package server

import (
	"context"
	"database/sql"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/llmpolicy"
)

// ProductRevision은 제품 빌드가 주입하며 실제 실행 소스를 공개한다.
var ProductRevision = "development"

type ProductModelBinding struct {
	ProfileRef string
	ProfileID  int64
	Model      string
	Check      func() error
}

// 제품의 인증된 모델 키 설정은 진단 입력·실행 권위와 별도 경로다.
// writer는 llm_profiles.api_key만 갱신하며 모델·URL·예산을 변경할 권한이 없다.
func NewProductDiagnosisHandler(ctx context.Context, pool *sql.DB, keyDir, dataDir string, endpoint diagnosisprep.Endpoint, origin string, writer *sql.DB, models []ProductModelBinding) (http.Handler, error) {
	base, err := newURLDiagnosisManagementHandler(ctx, pool, keyDir, dataDir, endpoint, true, origin)
	if err != nil {
		return nil, err
	}
	_, authenticate, err := newDiagnosisManagementAuthentication(ctx, pool, keyDir, dataDir)
	if err != nil {
		return nil, err
	}
	if writer != nil {
		tx, err := writer.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return nil, ErrControlledReadHostConfiguration
		}
		defer tx.Rollback()
		if _, err = checkFirstDiagnosisRoleCatalog(ctx, tx); err != nil {
			return nil, err
		}
		var valid bool
		err = tx.QueryRowContext(ctx, `SELECT NOT has_column_privilege(current_user,'llm_profiles','api_key','SELECT') AND NOT has_table_privilege(current_user,'llm_profiles','INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER') AND bool_and(CASE WHEN attname='api_key' THEN has_column_privilege(current_user,attrelid,attnum,'UPDATE') ELSE NOT has_column_privilege(current_user,attrelid,attnum,'UPDATE') END) FROM pg_attribute WHERE attrelid='llm_profiles'::regclass AND attnum>0 AND NOT attisdropped`).Scan(&valid)
		if err != nil || !valid {
			return nil, ErrControlledReadHostConfiguration
		}
		if tx.Commit() != nil {
			return nil, ErrControlledReadHostConfiguration
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if ctx.Err() != nil {
			writeJSON(w, 503, map[string]string{"error": "service_closed"})
			return
		}
		if r.Method == "GET" && r.URL.Path == "/api/product/health" {
			response, err := endpoint.Call(r.Context(), diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "product-health", Principal: "ARTEX", Command: diagnosisprep.Catalog})
			ready := ctx.Err() == nil && err == nil && response.Error == "" && len(response.Profiles) > 0
			status := 200
			if !ready {
				status = 503
			}
			writeJSON(w, status, map[string]any{"service": "abc2lab-url-product", "revision": ProductRevision, "ready": ready, "diagnosis_api": "diagnosis-api.v2"})
			return
		}
		if r.URL.Path == "/api/product/model-credentials" {
			if r.Method == "GET" {
				owner, e := authenticate(r)
				if e != nil || owner != "ARTEX" {
					writeJSON(w, 401, map[string]string{"error": "authentication_required"})
					return
				}
				query := r.URL.Query()
				id := query.Get("profile_id")
				if len(query) != 1 || len(query["profile_id"]) != 1 {
					writeJSON(w, 400, map[string]string{"error": "invalid_model_profile"})
					return
				}
				for _, m := range models {
					if m.ProfileRef == id && m.Check != nil && m.Check() == nil {
						callProductControl(w, r, owner, endpoint, &diagnosisprep.ProductRequest{Operation: "credentials", ProfileID: id})
						return
					}
				}
				writeJSON(w, 400, map[string]string{"error": "model_not_installed"})
				return
			}
			if r.Method != "POST" {
				writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
				return
			}
			owner, err := authenticate(r)
			if err != nil || owner != "ARTEX" {
				writeJSON(w, 401, map[string]string{"error": "authentication_required"})
				return
			}
			if writer == nil {
				writeJSON(w, 501, map[string]string{"error": "model_registration_unavailable"})
				return
			}
			media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			var body struct {
				ProfileRef string `json:"profile_id"`
				Model      string `json:"model"`
				APIKey     string `json:"api_key"`
			}
			raw, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
			defer clear(raw)
			if err != nil || media != "application/json" || readErr != nil || strictJSON(raw, &body) != nil || body.Model != llmpolicy.OpenRouterModel || len(body.APIKey) < 8 || len(body.APIKey) > 512 || strings.ContainsAny(body.APIKey, "\r\n\x00\t ") {
				writeJSON(w, 400, map[string]string{"error": "invalid_model_credentials"})
				return
			}
			for _, m := range models {
				if m.ProfileRef != body.ProfileRef || m.Model != body.Model || m.ProfileID <= 0 || m.Check == nil {
					continue
				}
				if m.Check() != nil {
					writeJSON(w, 409, map[string]string{"error": "installation_changed"})
					return
				}
				result, err := writer.ExecContext(r.Context(), `UPDATE llm_profiles SET api_key=$1 WHERE id=$2 AND model=$3 AND format='openai' AND base_url=$4`, body.APIKey, m.ProfileID, m.Model, llmpolicy.OpenRouterBaseURL)
				body.APIKey = ""
				if err != nil {
					writeJSON(w, 503, map[string]string{"error": "model_registration_failed"})
					return
				}
				n, err := result.RowsAffected()
				if err != nil || n != 1 {
					writeJSON(w, 409, map[string]string{"error": "model_binding_changed"})
					return
				}
				writeJSON(w, 200, map[string]any{"configured": true, "model": m.Model})
				return
			}
			body.APIKey = ""
			writeJSON(w, 400, map[string]string{"error": "model_not_installed"})
			return
		}
		if r.URL.Path == productExternalConsentPath {
			owner, e := authenticate(r)
			if e != nil || owner != "ARTEX" {
				writeJSON(w, 401, map[string]string{"error": "authentication_required"})
				return
			}
			productExternalConsentHTTP(w, r, owner, endpoint)
			return
		}
		if strings.HasPrefix(r.URL.Path, candidateVerificationPrefix) && strings.Contains(r.URL.Path, "/verifications") {
			if strings.HasSuffix(r.URL.Path, "/answers") {
				candidateVerificationAnswerHTTP(w, r, pool, authenticate, endpoint)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/plan") {
				candidateVerificationLLMPlanHTTP(w, r, pool, authenticate, endpoint)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/decision") {
				candidateVerificationDecisionHTTP(w, r, pool, authenticate, endpoint)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/cancel") {
				candidateVerificationCancelHTTP(w, r, pool, authenticate, endpoint)
				return
			}
			candidateVerificationHTTP(w, r, pool, authenticate, endpoint)
			return
		}
		if r.URL.Path == productReleasePath {
			owner, e := authenticate(r)
			if e != nil {
				writeJSON(w, 401, map[string]string{"error": "authentication_required"})
				return
			}
			productReleaseHTTP(w, r, owner, endpoint)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/product/workflows/") {
			owner, e := authenticate(r)
			if e != nil || owner != "ARTEX" {
				writeJSON(w, 401, map[string]string{"error": "authentication_required"})
				return
			}
			productWorkflowHTTP(w, r, owner, endpoint)
			return
		}
		if r.URL.Path == "/api/product/targets" || strings.HasPrefix(r.URL.Path, "/api/product/targets/") {
			owner, e := authenticate(r)
			if e != nil {
				writeJSON(w, 401, map[string]string{"error": "authentication_required"})
				return
			}
			productTargetsHTTP(w, r, owner, endpoint)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			base.ServeHTTP(w, r)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
	}), nil
}

// productExternalConsentPath는 설치 단위 외부 전송 동의 경로다(사용자 결정 2026-11-26: 외부 전송 동의는 설치 때 한 번).
//
//	GET  /api/product/external-transfer-consent                              동의 상태(필요 여부 · 문장 · 기록)
//	POST /api/product/external-transfer-consent {"statement_confirmed":true} 관리자 동의를 한 번 남김(이미 있으면 그대로)
//
// 관리자(ARTEX)만 부른다. 문장은 서버 상수이고 기록에는 그 해시만 남는다.
const productExternalConsentPath = "/api/product/external-transfer-consent"

// productExternalConsentBodyBytes는 동의 요청 본문 상한이다(칸 하나).
const productExternalConsentBodyBytes = 128

func productExternalConsentHTTP(w http.ResponseWriter, r *http.Request, owner string, endpoint diagnosisprep.Endpoint) {
	switch r.Method {
	case "GET":
		if len(r.URL.RawQuery) != 0 {
			writeJSON(w, 400, map[string]string{"error": "invalid_consent_request"})
			return
		}
		callProductControl(w, r, owner, endpoint, &diagnosisprep.ProductRequest{Operation: "external_consent"})
	case "POST":
		var body struct {
			StatementConfirmed bool `json:"statement_confirmed"`
		}
		media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		raw, re := io.ReadAll(http.MaxBytesReader(w, r.Body, productExternalConsentBodyBytes))
		if len(r.URL.RawQuery) != 0 || e != nil || media != "application/json" || re != nil || strictJSON(raw, &body) != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid_consent_request"})
			return
		}
		if !body.StatementConfirmed {
			writeJSON(w, 409, map[string]string{"error": "statement_required"})
			return
		}
		callProductControl(w, r, owner, endpoint, &diagnosisprep.ProductRequest{Operation: "external_consent_record", ExternalConsent: &diagnosisprep.ExternalConsentRequest{StatementConfirmed: true}})
	default:
		writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
	}
}
