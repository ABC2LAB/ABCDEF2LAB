package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReportGraphRunnerTaskAuthUnchanged(t *testing.T) {
	// DB 없는 Server의 기존 JWT 검증은 동작하지만 보고서의 매 요청 DB 인증은
	// 503으로 닫힌다. 따라서 잘못 보고서 분기로 진입하면 유효 토큰 시험도 실패한다.
	s := &Server{jwtKey: []byte("synthetic-runner-auth-key-32-bytes")}
	valid, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	wrongSignature, err := signJWT([]byte("synthetic-other-auth-key-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}

	// runner_engine.start_task와 TaskStatusReader.read가 실제로 사용하는 메서드·경로다.
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		for _, tc := range []struct {
			name, token, wantError string
			wantStatus             int
		}{
			{"missing", "", authErrUnauthorized, http.StatusUnauthorized},
			{"valid", valid, "", http.StatusNoContent},
			{"wrong-signature", wrongSignature, authErrTokenInvalid, http.StatusUnauthorized},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				r := httptest.NewRequest(method, "/api/tasks", nil)
				if _, ok := reportGraphRequestTask(r); ok {
					t.Fatal("러너의 작업 생성·목록 조회가 보고서 경로로 분류됨")
				}
				if tc.token != "" {
					r.Header.Set("Authorization", "Bearer "+tc.token)
				}
				calls := 0
				h := s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.WriteHeader(http.StatusNoContent)
				}))
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != tc.wantStatus {
					t.Fatalf("status=%d, want %d", w.Code, tc.wantStatus)
				}
				wantCalls := 0
				if tc.wantStatus == http.StatusNoContent {
					wantCalls = 1
				}
				if calls != wantCalls {
					t.Fatalf("handler calls=%d, want %d", calls, wantCalls)
				}
				if w.Header().Get("Cache-Control") != "" {
					t.Fatal("러너 응답에 보고서 인증의 no-store 헤더가 추가됨")
				}
				if tc.wantError != "" {
					var body struct {
						Error string `json:"error"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					if body.Error != tc.wantError {
						t.Fatalf("error=%q, want %q", body.Error, tc.wantError)
					}
				}
			})
		}
	}
}

func TestReportGraphReadAuthBranchRequiresDatabase(t *testing.T) {
	s := &Server{jwtKey: []byte("synthetic-runner-auth-key-32-bytes")}
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaf := range []string{"coverage-graph", "access-map"} {
		t.Run(leaf, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/tasks/42/"+leaf, nil)
			if task, ok := reportGraphRequestTask(r); !ok || task != 42 {
				t.Fatal("정규 보고서 GET이 보고서 경로로 분류되지 않음")
			}
			r.Header.Set("Authorization", "Bearer "+token)
			calls := 0
			h := s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(http.StatusNoContent)
			}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusServiceUnavailable || calls != 0 {
				t.Fatalf("보고서 인증 DB 부재: status=%d, handler calls=%d", w.Code, calls)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("보고서 인증 응답에 no-store 헤더가 없음")
			}
		})
	}
}
