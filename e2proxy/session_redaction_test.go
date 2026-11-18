package e2proxy

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// 실제 자격 대신 시험마다 합성 키로 만든 JWS만 사용한다.
func syntheticSessionJWT(subject string) string {
	encode := base64.RawURLEncoding.EncodeToString
	header := encode([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := encode([]byte(fmt.Sprintf(`{"sub":%q,"scope":"synthetic-session-only"}`, subject)))
	unsigned := header + "." + payload
	mac := hmac.New(sha256.New, []byte("synthetic-signing-key-only"))
	mac.Write([]byte(unsigned))
	return unsigned + "." + encode(mac.Sum(nil))
}
func TestSessionJWTFragmentsAreRedactedOnlyForTestAccounts(t *testing.T) {
	token := syntheticSessionJWT("tester-a")
	parts := strings.Split(token, ".")
	for _, mode := range []HeaderMode{HeaderTestAccountPassthrough, HeaderPreserve, HeaderProtectedSessionProposal} {
		t.Run(string(mode), func(t *testing.T) {
			e := &Execution{cfg: Config{HeaderMode: mode}}
			if err := e.AddRedactValues([]byte(token)); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(e.RedactOutput([]byte(token)), []byte(token)) {
				t.Fatal("전체 세션 가림 실패")
			}
			for _, segment := range parts[1:] {
				payload, _ := json.Marshal(map[string]string{"command": "printf '" + segment + " " + segment + "'", "summary": "Observed request"})
				out := e.RedactOutput(payload)
				if !json.Valid(out) {
					t.Fatal("가림 뒤 JSON 파손")
				}
				hidden := !bytes.Contains(out, []byte(segment))
				if hidden != (mode == HeaderTestAccountPassthrough) {
					t.Fatal("시험 계정 JWT 조각 가림 범위 불일치")
				}
			}
			if mode == HeaderTestAccountPassthrough && string(e.RedactOutput([]byte(token))) != "[REDACTED]" {
				t.Fatal("전체 JWT를 조각으로 나눠 가림")
			}
		})
	}
}
func TestSessionJWTRefreshAndAtomicCapacity(t *testing.T) {
	e := &Execution{cfg: Config{HeaderMode: HeaderTestAccountPassthrough}}
	tokens := []string{syntheticSessionJWT("tester-a"), syntheticSessionJWT("tester-b"), syntheticSessionJWT("tester-a-refresh")}
	for _, token := range tokens {
		if e.AddRedactValues([]byte(token)) != nil {
			t.Fatal("세션 등록 실패")
		}
	}
	for _, token := range tokens {
		for _, v := range strings.Split(token, ".")[1:] {
			if bytes.Contains(e.RedactOutput([]byte(v)), []byte(v)) {
				t.Fatal("계정·갱신 세션 조각 누락")
			}
		}
	}
	before := len(e.redactOnly)
	if e.AddRedactValues([]byte(tokens[0])) != nil || len(e.redactOnly) != before {
		t.Fatal("같은 세션이 개수 상한을 소모함")
	}
	e.redactOnly = nil
	for i := 0; i < maxRedactValues-2; i++ {
		e.redactOnly = append(e.redactOnly, []byte(fmt.Sprintf("synthetic-opaque-session-%03d", i)))
	}
	before = len(e.redactOnly)
	if e.AddRedactValues([]byte(tokens[0])) == nil || len(e.redactOnly) != before {
		t.Fatal("JWT 조각 일부만 등록하고 안전하다고 처리함")
	}
}

func TestSessionJWTSyntaxDoesNotSplitOpaqueValues(t *testing.T) {
	encode := base64.RawURLEncoding.EncodeToString
	valid := syntheticSessionJWT("tester")
	p := strings.Split(valid, ".")
	for _, value := range []string{"opaque.session.value", encode([]byte(`{"alg":"none"}`)) + "." + p[1] + "." + p[2], p[0] + "." + encode([]byte(`["not-claims"]`)) + "." + p[2], p[0] + "." + p[1] + "." + p[2] + "=", p[0] + "." + p[1] + ".*invalid*", p[0] + "." + p[1] + ".", valid + ".extra", p[0] + "." + encode([]byte(`{"sub":`)) + "." + p[2]} {
		got := sessionRedactionValues([][]byte{[]byte(value)})
		if len(got) != 1 || string(got[0]) != value {
			t.Fatal("opaque·잘못된 JWT까지 조각으로 분할함")
		}
	}
	// 비밀이 없는 공통 머리나 가림 하한보다 짧은 빈 claims는 목록에 넣지 않는다.
	value := p[0] + "." + encode([]byte(`{}`)) + "." + p[2]
	got := sessionRedactionValues([][]byte{[]byte(value)})
	if len(got) != 2 || string(got[1]) != p[2] {
		t.Fatal("짧은 공통 조각을 과도하게 가림")
	}
}
func TestSessionJWTResponseRotationKeepsModelSession(t *testing.T) {
	token := syntheticSessionJWT("rotated-tester")
	e := &Execution{cfg: Config{HeaderMode: HeaderTestAccountPassthrough}}
	response := &http.Response{StatusCode: 200, Header: http.Header{"Set-Cookie": {"session=" + token + "; HttpOnly"}}}
	if !e.safeHTTPResponse(response, []byte("ordinary body")) {
		t.Fatal("JWT 세션 때문에 응답 전체를 보류함")
	}
	if response.Cookies()[0].Value != token {
		t.Fatal("모델용 시험 계정 세션 값을 변경함")
	}
	for _, part := range strings.Split(token, ".")[1:] {
		if bytes.Contains(e.RedactOutput([]byte(part)), []byte(part)) {
			t.Fatal("회전 발급 조각이 기록에 남음")
		}
	}
	// 새 세션의 세 조각을 모두 추가할 공간이 없으면 응답을 보류하고 목록을 일부만 바꾸지 않는다.
	full := &Execution{cfg: Config{HeaderMode: HeaderTestAccountPassthrough}}
	for i := 0; i < maxIssuedRedactValues-2; i++ {
		full.redactOnly = append(full.redactOnly, []byte(fmt.Sprintf("issued-opaque-session-%04d", i)))
	}
	before := len(full.redactOnly)
	if full.safeHTTPResponse(response, nil) || len(full.redactOnly) != before {
		t.Fatal("회전 세션 가림 상한을 우회함")
	}
}

func TestSessionJWTUnsecuredPayloadStillPrivate(t *testing.T) {
	token := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"synthetic-session"}`)) + "."
	e := &Execution{cfg: Config{HeaderMode: HeaderTestAccountPassthrough}}
	if e.AddRedactValues([]byte(token)) != nil {
		t.Fatal("합성 세션 등록 실패")
	}
	payload := strings.Split(token, ".")[1]
	if bytes.Contains(e.RedactOutput([]byte(payload)), []byte(payload)) {
		t.Fatal("서명 없는 대상 발급 세션도 claims를 기록하면 안 됨")
	}
}

func TestSessionJWTUnicodeJSONValues(t *testing.T) {
	token := syntheticSessionJWT("tester-a")
	e := &Execution{cfg: Config{HeaderMode: HeaderTestAccountPassthrough}}
	if e.AddRedactValues([]byte(token)) != nil {
		t.Fatal("합성 세션 등록 실패")
	}
	for _, part := range strings.Split(token, ".")[1:] {
		in := []byte(fmt.Sprintf(`{"intent_id":42,"summary":"ordinary","detail":"\u%04x%s"}`, part[0], part[1:]))
		out := e.RedactOutput(in)
		var got map[string]any
		if json.Unmarshal(out, &got) != nil || got["intent_id"] != float64(42) || got["summary"] != "ordinary" {
			t.Fatal("정상 JSON 구조 파손")
		}
		if strings.Contains(got["detail"].(string), part) {
			t.Error("JSON Unicode로 표현한 JWT 조각이 저장 값에 남음")
		}
	}
}

func TestSessionJWTValueByteLimitsAreAtomic(t *testing.T) {
	for _, size := range []int{minRedactValueBytes - 1, maxRedactValueBytes + 1} {
		e := &Execution{cfg: Config{HeaderMode: HeaderTestAccountPassthrough}}
		if e.AddRedactValues([]byte(syntheticSessionJWT("tester")), bytes.Repeat([]byte("x"), size)) == nil || len(e.redactOnly) != 0 {
			t.Fatal("길이 오류 뒤 일부 세션이 등록됨")
		}
	}
}

func TestRedactOutputPreservesPublicJSONAlongsideEscapedSession(t *testing.T) {
	execution := &Execution{cfg: Config{HeaderMode: HeaderTestAccountPassthrough}}
	secret := []byte("synthetic-session/🔒\"value")
	if err := execution.AddRedactValues(secret); err != nil {
		t.Fatal(err)
	}
	input := []byte(`{ "intent_id":9223372036854775807, "values":[true,null,{"detail":"before synthetic-session\u002f\ud83d\udd12\"value after","public":"catalog\u002fname"}] }`)
	original := bytes.Clone(input)
	want := []byte(`{ "intent_id":9223372036854775807, "values":[true,null,{"detail":"before [REDACTED] after","public":"catalog\u002fname"}] }`)
	output := execution.RedactOutput(input)
	if !json.Valid(output) || !bytes.Equal(output, want) {
		t.Fatal("알려진 세션을 가리는 동안 JSON의 공개 값 또는 구조가 바뀌었습니다")
	}
	if !bytes.Equal(input, original) {
		t.Fatal("가림이 호출자의 원본 입력을 수정했습니다")
	}
	if !bytes.Equal(execution.RedactOutput(output), want) {
		t.Fatal("이미 가린 출력을 다시 가리면 값이 바뀝니다")
	}
}
