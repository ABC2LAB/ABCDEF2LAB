//go:build linux

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/db"
	"golang.org/x/crypto/bcrypt"
)

// 판정 상세 경로는 제품 관리 중계에만 있다(OD10). 제품이 아닌 관리 API는 없는 경로로 답하고, 어느 쪽도 실행 API로
// 넘기지 않는다. 제품 중계는 인증부터 본다.
func TestFindingVerdictDetailInstalledOnlyInProductRelayOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	installer := &db.DB{DB: f.Installer}
	hash, err := bcrypt.GenerateFromPassword([]byte("owned-verdict-detail-fixture"), bcrypt.MinCost)
	if err != nil || installer.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("인증 fixture 실패")
	}
	keys, data := firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t)
	if os.WriteFile(filepath.Join(keys, jwtKeyFilename), []byte("synthetic-verdict-detail-key-material"), 0o600) != nil {
		t.Fatal("키 fixture 실패")
	}
	var forwarded atomic.Int64
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forwarded.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer runtime.Close()
	const path = "/api/tasks/1/finding-verdict-detail?verdict_id=1"
	for _, product := range []bool{false, true} {
		handler, err := newURLDiagnosisManagementHandler(t.Context(), f.Runtime, keys, data, &verdictDetailTestEndpoint{}, product, runtime.URL)
		if err != nil {
			t.Fatal(err)
		}
		for _, token := range []string{"", "Bearer synthetic-invalid-token"} {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			if token != "" {
				r.Header.Set("Authorization", token)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if !product {
				if w.Code != http.StatusNotFound {
					t.Fatalf("제품 아닌 관리 API가 판정 상세를 받음: %d %s", w.Code, w.Body)
				}
				continue
			}
			var body map[string]string
			if w.Code != http.StatusUnauthorized || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body) != 1 ||
				body["error"] != verdictDetailAuthRequired || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("제품 중계 인증 거부: %d %s", w.Code, w.Body)
			}
		}
		// 경로 조각 이름이 같으면 모양이 틀려도 실행 API로 넘기지 않는다.
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/tasks/1/finding-verdict-detail/extra", nil))
		if w.Code == http.StatusTeapot || product && (w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), verdictDetailInvalidRequest)) {
			t.Fatalf("모양이 틀린 판정 상세 경로: product=%v %d %s", product, w.Code, w.Body)
		}
	}
	if forwarded.Load() != 0 {
		t.Fatalf("판정 상세를 실행 API로 넘김: %d", forwarded.Load())
	}
}
