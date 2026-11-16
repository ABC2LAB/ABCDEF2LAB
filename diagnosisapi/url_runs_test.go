package diagnosisapi

import (
	"encoding/json"
	"github.com/Autumn-27/artex/diagnosisprep"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func runPageEndpoint() *urlTestEndpoint {
	return &urlTestEndpoint{reply: func(req diagnosisprep.Request) (diagnosisprep.Response, error) {
		return diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: req.RequestID, RunPage: &diagnosisprep.RunPage{Runs: []diagnosisprep.RunSummary{}, Snapshot: strings.Repeat("a", 64), Limit: 25}}, nil
	}}
}
func TestURLRunListAuthenticatesWithoutIdempotencyKey(t *testing.T) {
	endpoint := runPageEndpoint()
	response := urlTestRequest(urlTestHandler(t, endpoint), "GET", URLBasePath+"/runs", "", "")
	if response.Code != 200 || len(endpoint.calls) != 1 || endpoint.calls[0].Command != diagnosisprep.ListRuns || endpoint.calls[0].Principal != "owner" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("읽기 전용 목록 중계", response.Code)
	}
	var body struct {
		Version, OwnerContext string
		Page                  *diagnosisprep.RunPage
	}
	if json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Page == nil {
		t.Fatal("목록 계약")
	}
	// 기존 경로의 query 거부를 목록 예외로 넓히지 않는다.
	if got := urlTestRequest(urlTestHandler(t, endpoint), "GET", URLBasePath+"/catalog?limit=25", "", ""); got.Code != 400 {
		t.Fatal("기존 query 계약 확대")
	}
}
func TestURLRunListRejectsUnknownInputsBeforeIPC(t *testing.T) {
	for _, path := range []string{"/runs?limit=0", "/runs?limit=51", "/runs?limit=01", "/runs?limit=-1", "/runs?limit=1&limit=2", "/runs?cursor=bad", "/runs?principal=other", "/runs?limit=1;start=1", "/runs?"} {
		endpoint := runPageEndpoint()
		response := urlTestRequest(urlTestHandler(t, endpoint), "GET", URLBasePath+path, "", "")
		if response.Code != 400 || len(endpoint.calls) != 0 {
			t.Errorf("허용되지 않은 입력 중계 %s: %d", path, response.Code)
		}
	}
	for _, tc := range []struct{ method, body, key string }{{"POST", "", ""}, {"GET", "{}", ""}, {"GET", "", "key"}} {
		endpoint := runPageEndpoint()
		response := urlTestRequest(urlTestHandler(t, endpoint), tc.method, URLBasePath+"/runs", tc.body, tc.key)
		if response.Code < 400 || len(endpoint.calls) != 0 {
			t.Fatal("목록에서 쓰기 입력 수용")
		}
	}
	endpoint := runPageEndpoint()
	req := httptest.NewRequest(http.MethodGet, URLBasePath+"/runs", nil)
	out := httptest.NewRecorder()
	urlTestHandler(t, endpoint).ServeHTTP(out, req)
	if out.Code != 401 || len(endpoint.calls) != 0 {
		t.Fatal("미인증 조회")
	}
}
func TestURLRunListMapsSnapshotChangeAndRejectsWrongResponse(t *testing.T) {
	for _, code := range []string{"snapshot_changed", "unavailable"} {
		endpoint := &urlTestEndpoint{reply: func(r diagnosisprep.Request) (diagnosisprep.Response, error) {
			return diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: r.RequestID, Error: code}, nil
		}}
		response := urlTestRequest(urlTestHandler(t, endpoint), "GET", URLBasePath+"/runs", "", "")
		want := 503
		if code == "snapshot_changed" {
			want = 409
		}
		if response.Code != want {
			t.Fatal("목록 충돌/오류 분류")
		}
	}
	endpoint := &urlTestEndpoint{}
	response := urlTestRequest(urlTestHandler(t, endpoint), "GET", URLBasePath+"/runs", "", "")
	if response.Code != 503 {
		t.Fatal("목록 없는 서비스 응답을 성공 처리")
	}
}
