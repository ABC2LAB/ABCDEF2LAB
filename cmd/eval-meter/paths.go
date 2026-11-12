package main

import (
	"context"
	"net/http"

	"github.com/Autumn-27/artex/pathmask"
)

// 브라우저 수용시험의 보존 시험 전용 기록이다(docs/evaluation/browser-acceptance.md §6).
// 공식 회차에서는 켜지 않는다. 경로 조각의 원문은 어떤 파일에도 남기지 않는다.
// 가림 규칙은 E② 거부 행의 평가 전용 경로 키와 같아야 해서 공용 패키지(pathmask)에 있다.

const (
	enginePathLogName = "engine-paths.jsonl"
	pathMaskProfile   = pathmask.Profile
	pathKeyBytes      = pathmask.KeyBytes
	// 요청이 들어온 리슨. main은 -listen(원본 엔진 직접·E② 진단 입구), explore는 -explore-listen(탐색 경계 입구)이다.
	ingressMain    = "main"
	ingressExplore = "explore"
)

type ingressKey struct{}

// ingress는 같은 계측기를 다른 입구 이름으로 부르는 처리기다. 기록·순번·봉인은 공유한다.
func (m *engineMeter) ingress(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ingressKey{}, name)))
	})
}

func requestIngress(r *http.Request) string {
	if name, ok := r.Context().Value(ingressKey{}).(string); ok {
		return name
	}
	return ingressMain
}

// 시도 ID·전송 단계는 같은 seq의 engine-requests.jsonl 행에 있다. 여기에 복사하지 않는다.
type pathRow struct {
	Seq            uint64 `json:"seq"`
	Method         string `json:"method"`
	PathPattern    string `json:"path_pattern"`
	MaskedSegments int    `json:"masked_segments"`
	PathProfileRef string `json:"path_profile_ref"`
	// 집계할 때 회차 묶음이 같은 키를 썼는지, 집계자가 가진 키가 그 키인지 확인하는 값이다. 키 값은 남기지 않는다.
	PathKeyRef string `json:"path_key_ref"`
	// 요청에 값이 비지 않은 Authorization·Cookie 헤더가 있었는지만 남긴다. 헤더 값·줄 수·쿠키 이름은 남기지 않는다.
	// 인증을 시도했다는 관찰일 뿐 로그인이 됐다는 근거가 아니다(browser-acceptance.md §6).
	HasAuthorization bool `json:"has_authorization"`
	HasCookie        bool `json:"has_cookie"`
	// 요청이 들어온 계측기 입구(ingressMain·ingressExplore). 입구별 전달 수(browser-acceptance.md §6)의 근거다.
	Ingress string `json:"ingress"`
}

// hasHeaderValue는 그 이름의 헤더 줄 중 빈 값이 아닌 줄이 하나라도 있는지 본다. 빈 헤더는 자격을 싣지 않는다.
// net/http가 값 앞뒤 공백을 이미 잘라서 넘기므로 공백만 있던 줄도 여기서는 빈 값이다.
func hasHeaderValue(h http.Header, name string) bool {
	for _, value := range h.Values(name) {
		if value != "" {
			return true
		}
	}
	return false
}
