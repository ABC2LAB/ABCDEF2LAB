package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
)

func syntheticAuthJWT(label string) string {
	encode := base64.RawURLEncoding.EncodeToString
	plain := encode([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + encode([]byte(fmt.Sprintf(`{"sub":%q,"role":"synthetic-test"}`, label)))
	h := hmac.New(sha256.New, []byte("synthetic-auth-signing-key"))
	h.Write([]byte(plain))
	return plain + "." + encode(h.Sum(nil))
}
func TestAuthJWTFragmentsFollowPrepAndRefresh(t *testing.T) {
	for _, cookie := range []bool{true, false} {
		t.Run(fmt.Sprint(cookie), func(t *testing.T) {
			first := syntheticAuthJWT("tester-a")
			next := syntheticAuthJWT("tester-a-refresh")
			target := &authLoginTarget{token: first}
			var guard sync.Mutex
			handler := target.handler(t)
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				guard.Lock()
				defer guard.Unlock()
				handler.ServeHTTP(w, r)
			}))
			defer receiver.Close()
			dir := authTestDir(t)
			pin := authCredentialPin(t, dir, "test.json", "tester@owned.test", "seeded-pass-88ff")
			auth := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
			if !cookie {
				auth.Login.Cookies, auth.Login.JSONField, auth.Login.Header, auth.Login.Prefix = nil, "data.token", "Authorization", "Bearer "
			}
			e := authTestExecution(t, receiver.URL, &authEventCounter{}, e2proxy.HeaderTestAccountPassthrough)
			result, err := e2HTTPAuthPrep(t.Context(), e, dir, receiver.URL, auth)
			if err != nil || result.AccountAuth["tester-a"] != E2HTTPAuthAuthenticated {
				t.Fatal("실제 인증 준비 실패", err)
			}
			raw, _ := json.Marshal(result.Sessions)
			if !strings.Contains(string(raw), first) {
				t.Fatal("허용한 모델용 세션이 조각 가림에 의해 손상됨")
			}
			// handler의 이전 호출이 끝난 뒤 새 요청과만 읽으므로 대상 토큰 교체도 경합하지 않는다.
			guard.Lock()
			target.token = next
			guard.Unlock()
			header, status := e2HTTPRefreshSession(t.Context(), e, dir, receiver.URL, auth, &e2HTTPRefreshCounter{}, newE2HTTPProgramCalls(), "tester-a", false)
			raw, _ = json.Marshal(header)
			if status != E2HTTPAuthAuthenticated || !strings.Contains(string(raw), next) || target.posts.Load() != 2 {
				t.Fatal("다시 로그인 세션 교체 실패")
			}
			for _, token := range []string{first, next} {
				for _, part := range strings.Split(token, ".")[1:] {
					detail, _ := json.Marshal(map[string]string{"command": "printf '" + part + " " + part + "'", "summary": "Observed request"})
					if strings.Contains(string(e.RedactOutput(detail)), part) {
						t.Fatal("인증 준비·갱신 JWT 조각이 기록에 남음")
					}
				}
			}
		})
	}
}

func TestAuthJWTCapacityDoesNotReleaseModelSession(t *testing.T) {
	target := &authLoginTarget{token: syntheticAuthJWT("capacity-tester")}
	receiver := httptest.NewServer(target.handler(t))
	defer receiver.Close()
	dir := authTestDir(t)
	pin := authCredentialPin(t, dir, "test.json", "tester@owned.test", "seeded-pass-88ff")
	auth := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
	execution := authTestExecution(t, receiver.URL, &authEventCounter{}, e2proxy.HeaderTestAccountPassthrough)
	// 조각까지 합쳐 한도를 넘을 때 인증은 했어도 모델에 세션을 반환하지 않는다.
	for i := 0; i < 62; i++ {
		if err := execution.AddRedactValues([]byte(fmt.Sprintf("synthetic-opaque-session-%03d", i))); err != nil {
			t.Fatal(err)
		}
	}
	result, err := e2HTTPAuthPrep(t.Context(), execution, dir, receiver.URL, auth)
	if err != nil || result.AccountAuth["tester-a"] != E2HTTPAuthSessionNotFound || len(result.Sessions) != 0 || target.posts.Load() != 1 {
		t.Fatal("가림 상한 초과 세션을 모델에 반환하거나 인증을 재시도함", err)
	}
}
