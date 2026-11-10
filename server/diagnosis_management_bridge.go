package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/diagnosisapi"
)

const managementBridgeRequestLimit = 256 << 10
const managementBridgeResponseLimit = 16 << 20

var managementApprovalRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// 이 bridge는 기존 공개 DTO의 이행 경계다. 알고리즘·엔진 명령·임의 경로 proxy가 아니다.
type diagnosisManagementBridge struct {
	origin       *url.URL
	client       *http.Client
	authenticate diagnosisapi.Authenticate
	authorized   func(context.Context, string, string) bool
}

func newDiagnosisManagementBridge(ctx context.Context, origin string, authenticate diagnosisapi.Authenticate, authorized func(context.Context, string, string) bool) (http.Handler, error) {
	target, err := url.Parse(origin)
	if err != nil || target.User != nil || target.RawQuery != "" || target.Fragment != "" || target.Path != "" || target.RawPath != "" ||
		(target.Scheme != "http" && target.Scheme != "https") || target.Port() == "" || authenticate == nil || authorized == nil {
		return nil, ErrControlledReadHostConfiguration
	}
	ip := net.ParseIP(target.Hostname())
	port, portErr := strconv.Atoi(target.Port())
	if ip == nil || !ip.IsLoopback() || portErr != nil || port < 1 || port > 65535 {
		return nil, ErrControlledReadHostConfiguration
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 10 * time.Second, DisableCompression: true, DisableKeepAlives: true}
	context.AfterFunc(ctx, transport.CloseIdleConnections)
	return &diagnosisManagementBridge{origin: target, authenticate: authenticate, authorized: authorized, client: &http.Client{
		Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect blocked") },
	}}, nil
}
func managementBridgeTask(r *http.Request) (string, bool) {
	if r.URL.RawPath != "" || r.Method != http.MethodGet && r.Method != http.MethodPost {
		return "", false
	}
	if requestApprovalRoute(r) {
		if r.URL.RawQuery != "" {
			return "", false
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/")
		n, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || n <= 0 || strconv.FormatInt(n, 10) != parts[0] {
			return "", false
		}
		if len(parts) > 2 && !managementApprovalRef.MatchString(parts[2]) {
			return "", false
		}
		return parts[0], true
	}
	if id, ok := diagnosisRunRequestTask(r); ok {
		return strconv.FormatInt(id, 10), true
	}
	allowed := map[string]bool{"task": true, "audience": true}
	switch r.URL.Path {
	case "/api/report/diagnostic":
		allowed["download"] = true
		allowed["format"] = true
	case "/api/report/diagnostic/image":
		allowed["entry"] = true
		allowed["step"] = true
		allowed["digest"] = true
	case "/api/report/diagnostic/remediation":
	case "/api/report/diagnostic/remediation/prompt":
		allowed["entry"] = true
		allowed["record"] = true
		allowed["digest"] = true
	default:
		return "", false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", false
	}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 {
			return "", false
		}
	}
	if audience := query.Get("audience"); audience != "" && audience != "internal" && audience != "shared" {
		return "", false
	}
	if download := query.Get("download"); download != "" && download != "1" {
		return "", false
	}
	number, ok := firstDiagnosisReportRequestTask(r)
	return strconv.FormatInt(number, 10), ok
}
func (b *diagnosisManagementBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	owner, err := b.authenticate(r)
	if err != nil || owner == "" {
		writeErr(w, 401, authErrUnauthorized)
		return
	}
	task, ok := managementBridgeTask(r)
	if !ok || !b.authorized(r.Context(), owner, task) {
		writeErr(w, 403, "승인된 작업의 공개 진단 API만 사용할 수 있습니다")
		return
	}
	var raw []byte
	if r.Method == http.MethodPost {
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			writeErr(w, 400, "JSON 요청만 사용할 수 있습니다")
			return
		}
		raw, err = io.ReadAll(http.MaxBytesReader(w, r.Body, managementBridgeRequestLimit))
		if err != nil {
			writeErr(w, 413, "요청 크기를 확인할 수 없습니다")
			return
		}
	} else if r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
		writeErr(w, 400, "조회 요청 본문은 사용할 수 없습니다")
		return
	}
	// A slow body must not retain credentials or draft authority checked before it.
	currentOwner, currentErr := b.authenticate(r)
	if currentErr != nil || currentOwner == "" || currentOwner != owner {
		writeErr(w, 401, authErrUnauthorized)
		return
	}
	if !b.authorized(r.Context(), currentOwner, task) {
		writeErr(w, 403, "작업 승인이 변경되었습니다")
		return
	}
	target := *b.origin
	target.Path = r.URL.Path
	target.RawQuery = r.URL.RawQuery
	out, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(raw))
	if err != nil {
		writeErr(w, 502, "진단 API 요청을 만들 수 없습니다")
		return
	}
	out.Header.Set("Authorization", r.Header.Get("Authorization"))
	if r.Method == http.MethodPost {
		out.Header.Set("Content-Type", "application/json")
	}
	response, err := b.client.Do(out)
	if err != nil {
		writeErr(w, 503, "진단 API 응답을 확인할 수 없습니다. 변경 요청은 재전송하지 않았습니다")
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		writeErr(w, 502, "진단 API redirect를 허용하지 않습니다")
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, managementBridgeResponseLimit+1))
	if err != nil || len(body) > managementBridgeResponseLimit {
		writeErr(w, 502, "진단 API 응답 크기를 확인할 수 없습니다")
		return
	}
	for _, name := range []string{"Content-Type", "Content-Disposition", "Content-Security-Policy", "X-Content-Type-Options"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(body)
}
