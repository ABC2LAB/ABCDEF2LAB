package e2proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// Go의 실제 응답 헤더/트레일러 이름 정규화를 거친 반사도 전체 보류한다(#620).
func TestPasswordResponseCanonicalFieldNamesWithhold(t *testing.T) {
	for _, password := range []string{"myPass123", "lowercasepw1", "secret-word-9"} {
		for _, trailer := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/trailer=%t", password, trailer), func(t *testing.T) {
				name := "X-" + password
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/plain")
					if trailer {
						w.Header().Set("Trailer", name)
					} else {
						w.Header().Set(name, "public")
					}
					io.WriteString(w, "public response")
					if trailer {
						w.Header().Set(name, "public")
					}
				}))
				defer target.Close()
				e, call := coreOpen(t, passwordConfig(t, target.URL, &coreRecorder{}))
				if err := e.AddProtectedPasswords([]byte(password)); err != nil {
					t.Fatal(err)
				}
				response, receipt, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed/page"))
				if !errors.Is(err, ErrWithheld) || response != nil || receipt.Reason != Withheld || !receipt.ResponseReceived || e.Snapshot().Counts.Unknown != 0 {
					t.Fatalf("정규화한 이름 반사를 전달: err=%v receipt=%+v", err, receipt)
				}
			})
		}
	}
}

func TestPasswordResponseFoldBoundsAndNonCaseDistinctions(t *testing.T) {
	for _, pair := range [][2]string{{"seeded-İ-value", "seeded-i-value"}, {"straße-password", "STRASSE-password"}, {"café-password", "cafe\u0301-password"}} {
		e := &Execution{cfg: Config{PasswordResponse: PasswordResponseMaskSpans}, passwords: [][]byte{[]byte(pair[0])}}
		response := &http.Response{Header: http.Header{"Content-Type": {"text/plain"}}, Status: "200 OK", Proto: "HTTP/1.1"}
		out, masked, safe := e.prepareResponse(response, []byte(pair[1]), 0, 1024, false, false)
		if strings.EqualFold(pair[0], pair[1]) || !safe || masked || string(out) != pair[1] {
			t.Fatal("단순 대소문자 변형 밖의 비민감 문자를 변경")
		}
	}
	response := &http.Response{Header: http.Header{"Content-Type": {"text/plain"}}, Status: "200 OK", Proto: "HTTP/1.1"}
	e := &Execution{cfg: Config{PasswordResponse: PasswordResponseMaskSpans}, passwords: [][]byte{[]byte("seeded-password")}}
	if _, _, safe := e.prepareResponse(response, []byte(strings.Repeat("K", maxPasswordTransforms+1)), 0, 1<<20, false, false); safe {
		t.Fatal("길이 변경 위치표 상한을 넘겨도 전달")
	}
	prefix := strings.Repeat("Q", maxPasswordTransforms+1)
	out, masked, safe := e.prepareResponse(response, []byte(prefix+"SEEDED-PASSWORD"), 0, 1<<20, false, false)
	if !safe || !masked || string(out) != prefix+"[REDACTED]" {
		t.Fatal("길이가 같은 치환을 위치표 상한으로 오인하거나 비민감 원문 변경")
	}
}

func FuzzPasswordResponseCaseFoldedJSON(f *testing.F) {
	for _, password := range []string{"myPass123", "key-K-value", "long-ſ-password", "greek-ς-value", `literal\u0061-Pass`, "percent%6a-Pass"} {
		f.Add(password)
	}
	f.Fuzz(func(t *testing.T, password string) {
		if !utf8.ValidString(password) || len(password) < minProtectedValueBytes || len(password) > 256 {
			return
		}
		for _, reflected := range []string{strings.ToLower(password), strings.ToUpper(password)} {
			if !strings.EqualFold(password, reflected) {
				continue
			}
			value, _ := json.Marshal(reflected)
			body := []byte(`[7,` + string(value) + `]`)
			e := &Execution{cfg: Config{PasswordResponse: PasswordResponseMaskSpans}, passwords: [][]byte{[]byte(password)}}
			response := &http.Response{Header: http.Header{"Content-Type": {"application/json"}}, Status: "200 OK", Proto: "HTTP/1.1"}
			out, _, safe := e.prepareResponse(response, body, 0, 4096, false, false)
			if !safe {
				if out != nil {
					t.Fatal("보류와 함께 본문을 반환")
				}
				continue
			}
			if string(out) != `[7,"[REDACTED]"]` {
				t.Fatal("대소문자 변형 노출 또는 비민감 JSON 손상")
			}
		}
	})
}

// 기존 검사 자체를 차등 oracle로 사용한다. 본문은 일치 구간을 가리고 메타데이터는 전체 보류한다.
// 기존 검사가 못 찾던 대소문자 변형도 새 모드에서는 같은 기준으로 보호한다.
func TestPasswordResponseNeverWeakensLegacyDetection(t *testing.T) {
	for _, place := range []string{"body", "header-value", "header-name", "trailer-value", "trailer-name", "status", "protocol"} {
		t.Run(place, func(t *testing.T) {
			legacyHits := 0
			for _, password := range []string{"myPass123", "lowercasepw1", "secret-word-9", "seeded-İ-value"} {
				for _, reflected := range []string{password, strings.ToUpper(password), strings.ToLower(password)} {
					response := &http.Response{Header: http.Header{"Content-Type": {"text/plain"}}, Trailer: http.Header{}, Status: "200 OK", Proto: "HTTP/1.1"}
					body := []byte("public")
					switch place {
					case "body":
						body = []byte("before " + reflected + " after")
					case "header-value":
						response.Header.Set("X-Echo", reflected)
					case "header-name":
						response.Header.Set("X-"+reflected, "public")
					case "trailer-value":
						response.Trailer.Set("X-Echo", reflected)
					case "trailer-name":
						response.Trailer.Set("X-"+reflected, "public")
					case "status":
						response.Status = "200 " + reflected
					case "protocol":
						response.Proto = reflected
					}
					e := &Execution{cfg: Config{PasswordResponse: PasswordResponseMaskSpans}, passwords: [][]byte{[]byte(password)}}
					legacySafe := e.protectedHTTPValuesSafeLocked(response, body, e.passwords)
					if !legacySafe {
						legacyHits++
					}
					out, masked, safe := e.prepareResponse(response, body, 0, 1024, false, false)
					if !legacySafe && safe && !masked {
						t.Fatalf("기존 검사가 찾은 반사를 전달: %s", place)
					}
					if legacySafe && !strings.EqualFold(password, reflected) {
						if !safe || masked || string(out) != string(body) {
							t.Fatal("단순 fold 밖의 비민감 표현을 변경")
						}
						continue
					}
					if place == "body" {
						if !safe || !masked || string(out) != "before [REDACTED] after" {
							t.Fatal("본문 대소문자 변형의 구간 가림 실패")
						}
					} else if safe || out != nil {
						t.Fatal("메타데이터 대소문자 변형을 보류하지 않음")
					}
				}
			}
			if legacyHits == 0 {
				t.Fatal("차등 시험이 기존 검사의 실제 검출 경로에 닿지 않음")
			}
		})
	}
}

// Unicode 단순 대소문자 접기에서 UTF-8 바이트 수가 달라져도 원문 위치를 정확히 가린다.
func TestPasswordResponseFoldedEncodingSourceOffsets(t *testing.T) {
	for _, pair := range [][2]string{{"key-k-value", "KEY-K-VALUE"}, {"long-s-password", "LONG-ſ-PAſſWORD"}, {"greek-σ-value", "GREEK-ς-VALUE"}, {"KELVIN-K-PASS", "kelvin-k-pass"}} {
		password, reflected := pair[0], pair[1]
		if !strings.EqualFold(password, reflected) {
			t.Fatal("잘못된 합성 fold fixture")
		}
		var entity strings.Builder
		for _, r := range reflected {
			fmt.Fprintf(&entity, "&#%d;", r)
		}
		for _, tc := range []struct{ media, value string }{{"text/plain", reflected}, {"application/json", passwordUnicode(reflected)}, {"text/html", entity.String()}, {"application/x-www-form-urlencoded", passwordPercent(reflected)}} {
			body, want := "front-ſ| "+tc.value+" end", "front-ſ| [REDACTED] end"
			if tc.media == "application/json" {
				body, want = `{"v":"`+body+`","count":7}`, `{"v":"`+want+`","count":7}`
			} else if tc.media == "application/x-www-form-urlencoded" {
				want = "front-ſ| %5BREDACTED%5D end"
			}
			e := &Execution{cfg: Config{PasswordResponse: PasswordResponseMaskSpans}, passwords: [][]byte{[]byte(password)}}
			response := &http.Response{Header: http.Header{"Content-Type": {tc.media}}, Status: "200 OK", Proto: "HTTP/1.1"}
			out, masked, safe := e.prepareResponse(response, []byte(body), 0, 1024, false, false)
			if !safe || !masked || string(out) != want {
				t.Fatalf("fold 후 원문 위치/형식 손상: media=%s safe=%v masked=%v", tc.media, safe, masked)
			}
			if e.authSessionSafe(http.Header{"Authorization": {"Bearer " + tc.value}}) {
				t.Fatal("대소문자 변형 비밀번호를 세션으로 전달")
			}
		}
	}
}
