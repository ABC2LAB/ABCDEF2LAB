package exploreboundary

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
)

// ErrFetchRefused는 경계가 그 요청 하나만 막은 것이다(범위 밖·위험 경로 등). 세션은 계속 열려 있다.
var ErrFetchRefused = errors.New("explore_fetch_refused")

// DirectFetch는 서버가 소유한 정적 탐색 fetch다(diagnosisflow.ExplorationFetch). 직접 입구 프록시로 GET을 보내고,
// 경계가 붙인 순번 헤더로 받은 본문을 같은 순번의 private 관찰(응답 해시)과 맞추게 한다. 리다이렉트는 따라가지 않는다
// (Location의 새 주소는 다음 방문에서 다시 판정한다). 설정은 설치가 고정하고 모델 입력으로 바꾸지 않는다.
type DirectFetch struct {
	// Proxy는 경계의 직접 입구 주소다(http://<host>:<port>).
	Proxy *url.URL
	// Roots는 경계 CA다. 경계가 TLS를 가로채므로 대상 인증서가 아니라 경계 CA로 검증한다.
	Roots    *x509.CertPool
	Timeout  time.Duration
	MaxBytes int64
	// Status는 세션이 닫혀 막혔을 때 닫힌 사유를 읽는다(제어 소켓 lease의 Status).
	Status func(context.Context) (exploration.State, error)
}

func (f DirectFetch) valid() bool {
	return f.Proxy != nil && f.Proxy.Scheme == "http" && f.Proxy.Host != "" && f.Roots != nil && f.Timeout > 0 && f.MaxBytes > 0 && f.Status != nil
}

// closeReason은 세션이 닫혔으면 그 사유를, 아니면 빈 문자열을 돌려준다. 읽지 못하면 경계 장애로 본다.
func (f DirectFetch) closeReason(ctx context.Context) string {
	state, err := f.Status(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ""
		}
		return string(exploration.BoundaryUnavailable)
	}
	if state.Active {
		return ""
	}
	if !state.ClosedReason.Valid() {
		return string(exploration.BoundaryUnavailable)
	}
	return string(state.ClosedReason)
}

func (f DirectFetch) Fetch(ctx context.Context, target string) (diagnosisflow.ExplorationFetchResult, error) {
	if !f.valid() {
		return diagnosisflow.ExplorationFetchResult{}, ErrConfig
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return diagnosisflow.ExplorationFetchResult{}, ErrFetchRefused
	}
	transport := &http.Transport{Proxy: http.ProxyURL(f.Proxy), DisableKeepAlives: true, DisableCompression: true, ForceAttemptHTTP2: false,
		TLSHandshakeTimeout: f.Timeout, ResponseHeaderTimeout: f.Timeout,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.Roots, NextProtos: []string{"http/1.1"}}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: f.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return diagnosisflow.ExplorationFetchResult{}, ErrFetchRefused
	}
	resp, err := client.Do(req)
	if err != nil {
		// CONNECT 단계에서 막히면(세션 없음·터널 상한) 응답 헤더를 볼 수 없다. 세션이 닫혔으면 그 사유로 탐색을 마감한다.
		if reason := f.closeReason(ctx); reason != "" {
			return diagnosisflow.ExplorationFetchResult{Reason: reason}, nil
		}
		return diagnosisflow.ExplorationFetchResult{}, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, f.MaxBytes+1))
	if denial := resp.Header.Get(DenialHeader); denial != "" {
		clear(body)
		// cap_exhausted는 세션 마감(요청 수·시간)일 수도 있고 그 요청만 막은 것(동시 연결·터널 상한)일 수도 있다.
		// 세션 상태로 가른다. 닫혔으면 그 사유로 탐색을 마감하고, 열려 있으면 그 요청만 실패다.
		switch denial {
		case ReasonCapExhausted, ReasonTargetEnded, ReasonSessionClosed:
			if reason := f.closeReason(ctx); reason != "" {
				return diagnosisflow.ExplorationFetchResult{Reason: reason}, nil
			}
		}
		return diagnosisflow.ExplorationFetchResult{}, ErrFetchRefused
	}
	seq, err := strconv.ParseUint(resp.Header.Get(SeqHeader), 10, 64)
	if err != nil || seq == 0 {
		// 경계를 지난 승인 대상 응답에는 순번이 있다. 없으면 받은 본문을 기록과 맞출 수 없다.
		clear(body)
		return diagnosisflow.ExplorationFetchResult{}, ErrUnavailable
	}
	complete := readErr == nil && int64(len(body)) <= f.MaxBytes && resp.Header.Get(ResultHeader) == ""
	return diagnosisflow.ExplorationFetchResult{Seq: seq, Body: body, Complete: complete}, nil
}
