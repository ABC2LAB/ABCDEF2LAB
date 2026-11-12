package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisapi"
	"github.com/Autumn-27/artex/diagnosisprep"
	"golang.org/x/crypto/bcrypt"
)

type urlManagementEndpoint struct{ calls atomic.Int64 }

func (e *urlManagementEndpoint) Call(_ context.Context, req diagnosisprep.Request) (diagnosisprep.Response, error) {
	e.calls.Add(1)
	response := diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: req.RequestID}
	if req.Command == diagnosisprep.AuthorizeTask {
		allowed := false
		response.Authorized = &allowed
	} else {
		response.Profiles = []diagnosisprep.Profile{}
	}
	return response, nil
}

// 실제 설치 인증을 사용하되 실행 서비스는 기록용 합성 endpoint다.
// 이 시험을 엔진·브라우저 종단 수용으로 계산하지 않는다.
func TestURLDiagnosisManagementAuthenticationOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	installer := &db.DB{DB: f.Installer}
	password := "synthetic-owned-url-authentication"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil || installer.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("합성 인증 설정 실패")
	}
	keyDir, dataDir := firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t)
	if os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), []byte("synthetic-owned-url-jwt-material"), 0600) != nil {
		t.Fatal("합성 인증키 준비 실패")
	}
	endpoint := &urlManagementEndpoint{}
	handler, err := NewURLDiagnosisManagementHandler(t.Context(), f.Runtime, keyDir, dataDir, endpoint)
	if err != nil {
		t.Fatal("URL 관리 조립 실패", err)
	}
	api := httptest.NewServer(handler)
	defer api.Close()
	request := func(method, path, body string, auth []string) (int, []byte) {
		t.Helper()
		req, e := http.NewRequest(method, api.URL+path, strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/json")
		for _, value := range auth {
			req.Header.Add("Authorization", value)
		}
		resp, e := api.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		var value json.RawMessage
		if json.NewDecoder(resp.Body).Decode(&value) != nil {
			t.Fatal("JSON 응답 없음")
		}
		return resp.StatusCode, value
	}
	login, _ := json.Marshal(map[string]string{"username": "ABC2LAB", "password": password})
	status, raw := request("POST", "/api/auth/login", string(login), nil)
	var session struct{ Token string }
	if status != 200 || json.Unmarshal(raw, &session) != nil || session.Token == "" {
		t.Fatal("실제 로그인 실패", status)
	}
	status, _ = request("GET", diagnosisapi.URLBasePath+"/catalog", "", []string{"Bearer " + session.Token})
	if status != 200 || endpoint.calls.Load() != 1 {
		t.Fatal("인증된 catalog 실패", status)
	}
	for _, headers := range [][]string{nil, {"Basic synthetic"}, {"Bearer invalid"}, {"Bearer " + session.Token, "Bearer invalid"}} {
		status, _ = request("GET", diagnosisapi.URLBasePath+"/catalog", "", headers)
		if status != 401 || endpoint.calls.Load() != 1 {
			t.Fatal("미인증 요청이 준비 서비스에 도달", status)
		}
	}
	newHash, e := bcrypt.GenerateFromPassword([]byte("synthetic-replaced-password"), bcrypt.MinCost)
	if e != nil || installer.SetSetting(authPassKey, string(newHash)) != nil {
		t.Fatal("합성 인증 변경 실패")
	}
	status, _ = request("GET", diagnosisapi.URLBasePath+"/catalog", "", []string{"Bearer " + session.Token})
	if status != 401 || endpoint.calls.Load() != 1 {
		t.Fatal("변경된 인증을 재사용", status)
	}
	t.Log("실제 PostgreSQL 로그인·JWT 검증·중복 헤더·비밀번호 변경 후 회수 확인; 모델/대상 호출 없음")
}
