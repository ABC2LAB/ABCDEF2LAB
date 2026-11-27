package server

import (
	"bytes"
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

// 인증 준비·다시 로그인으로 받은 시험 계정 세션(JWT)은 모델에 그대로 가고, 모델 출력·저장 기록에서 전체 값도 조각도
// 가리지 않는다(절대규칙 2, 2026-11-26 DLP 폐기).
func TestAuthJWTSessionsStayVisibleAfterPrepAndRefresh(t *testing.T) {
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
				for _, part := range append([]string{token}, strings.Split(token, ".")[1:]...) {
					detail, _ := json.Marshal(map[string]string{"command": "printf '" + part + " " + part + "'", "summary": "Observed request"})
					if !bytes.Equal(e.RedactOutput(detail), detail) || !bytes.Equal(e.RedactRecord(detail), detail) {
						t.Fatal("인증 준비·갱신 세션을 가림")
					}
				}
			}
		})
	}
}

// 시험 계정 실행에는 세션 가림 칸이 없다: 세션을 아무리 많이 받아도(다시 로그인 포함) 가림 자리 때문에 모델용 세션을
// 보류하지 않는다(#840의 칸 상한을 걷음, 2026-11-26 DLP 폐기).
func TestAuthSessionsAreNotLimitedByRedactionCapacity(t *testing.T) {
	target := &authLoginTarget{token: syntheticAuthJWT("capacity-tester")}
	receiver := httptest.NewServer(target.handler(t))
	defer receiver.Close()
	dir := authTestDir(t)
	pin := authCredentialPin(t, dir, "test.json", "tester@owned.test", "seeded-pass-88ff")
	auth := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
	execution := authTestExecution(t, receiver.URL, &authEventCounter{}, e2proxy.HeaderTestAccountPassthrough)
	for i := 0; i < 5000; i++ {
		if err := execution.AddRedactValues([]byte(fmt.Sprintf("synthetic-opaque-session-%04d", i))); err != nil {
			t.Fatal(err)
		}
	}
	result, err := e2HTTPAuthPrep(t.Context(), execution, dir, receiver.URL, auth)
	if err != nil || result.AccountAuth["tester-a"] != E2HTTPAuthAuthenticated || len(result.Sessions) != 1 || target.posts.Load() != 1 {
		t.Fatal("세션 수 때문에 모델용 세션을 주지 않음", err)
	}
}
