package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/report"
)

const managementBridgeRequestLimit = 256 << 10
const managementBridgeResponseLimit = report.MaxDiagnosticExportBytes

var managementApprovalRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// 이 bridge는 기존 공개 DTO의 이행 경계다. 알고리즘·엔진 명령·임의 경로 proxy가 아니다.
type diagnosisManagementBridge struct {
	origin            *url.URL
	client            *http.Client
	authenticate      diagnosisapi.Authenticate
	authorized        func(context.Context, string, string) bool
	readPool          *sql.DB
	reportAuthorize   func(context.Context, string, string) (bool, error)
	candidateJob      func(context.Context, string, string) (*diagnosisprep.Job, error)
	candidateEndpoint diagnosisprep.Endpoint
}

func newDiagnosisManagementBridge(ctx context.Context, origin string, authenticate diagnosisapi.Authenticate, authorized func(context.Context, string, string) bool, readPool ...*sql.DB) (*diagnosisManagementBridge, error) {
	target, err := url.Parse(origin)
	if len(readPool) > 1 || err != nil || target.User != nil || target.RawQuery != "" || target.Fragment != "" || target.Path != "" || target.RawPath != "" ||
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
	var pool *sql.DB
	if len(readPool) == 1 {
		pool = readPool[0]
	}
	return &diagnosisManagementBridge{origin: target, authenticate: authenticate, authorized: authorized, readPool: pool, client: &http.Client{
		Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect blocked") },
	}}, nil
}
func managementBridgeTask(r *http.Request) (string, bool) {
	if r.URL.RawPath != "" || r.Method != http.MethodGet && r.Method != http.MethodPost {
		return "", false
	}
	if task, ok := managementOperationsTask(r); ok {
		return task, true
	}
	if task, ok := reportGraphRequestTask(r); ok {
		return strconv.FormatInt(task, 10), true
	}
	if id, ok := savedApprovalRequestTask(r); ok {
		return strconv.FormatInt(id, 10), true
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
	if task, ok := managementLiveViewTask(r); ok {
		return task, true
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

// managementLiveViewTask는 작업대 실데이터 조회(통제 정책·전송 통제 흐름, 계약 workbench-live-data §3·§4)다.
// 읽기만 받고, 전송 통제 흐름만 커서 after 하나를 받는다. 값 검사는 실행 쪽 serveLiveData가 한다.
// E2ModelWritesHeader lets current clients request the optional write summary.
const E2ModelWritesHeader = "X-Jindo-E2-Model-Writes"

func managementLiveViewTask(r *http.Request) (string, bool) {
	if r.Method != http.MethodGet {
		return "", false
	}
	tail, ok := strings.CutPrefix(r.URL.Path, "/api/tasks/")
	if !ok {
		return "", false
	}
	parts := strings.Split(tail, "/")
	// 받는 경로: 통제 정책·전송 통제 흐름(after), 요청 전체 기록(cursor·limit), 요청 상세(호출·시도 식별자, 조회 조건 없음).
	keys := map[string]bool{}
	switch {
	case len(parts) == 2 && parts[1] == "control-policy":
	case len(parts) == 2 && parts[1] == "e2-flow":
		keys["after"] = true
	case len(parts) == 3 && parts[1] == "e2-flow" && parts[2] == "requests":
		keys["cursor"], keys["limit"] = true, true
	case len(parts) == 5 && parts[1] == "e2-flow" && parts[2] == "requests" && managementLiveRef.MatchString(parts[3]) && managementLiveAttempt.MatchString(parts[4]):
	default:
		return "", false
	}
	n, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != parts[0] {
		return "", false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", false
	}
	for key, values := range query {
		if !keys[key] || len(values) != 1 {
			return "", false
		}
	}
	return parts[0], true
}

// 요청 상세 경로의 호출 식별자(E② call_ref)와 시도 식별자(16바이트 소문자 16진수) 형식이다. 값 검사는 실행 쪽도 한다.
var (
	managementLiveRef     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	managementLiveAttempt = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

func (b *diagnosisManagementBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	graphTask, graphRead := reportGraphRequestTask(r)
	if graphRead {
		readContext, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		r = r.WithContext(readContext)
	}
	owner, err := b.authenticate(r)
	if err != nil || owner == "" {
		writeManagementBridgeAuthError(w, r, err)
		return
	}
	task, ok := managementBridgeTask(r)
	if !ok || !graphRead && !b.authorized(r.Context(), owner, task) {
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

		if firstDiagnosisControlRoute(r) {
			var control struct {
				Action string `json:"action"`
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&control) != nil || decoder.Decode(new(any)) != io.EOF || control.Action != "pause" && control.Action != "resume" {
				writeErr(w, 400, "일시정지·재개만 요청할 수 있습니다")
				return
			}
			raw, _ = json.Marshal(control)
		}
	} else if r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
		writeErr(w, 400, "조회 요청 본문은 사용할 수 없습니다")
		return
	}
	// A slow body must not retain credentials or draft authority checked before it.
	currentOwner, currentErr := b.authenticate(r)
	if currentErr != nil || currentOwner == "" || currentOwner != owner {
		writeManagementBridgeAuthError(w, r, currentErr)
		return
	}
	if !graphRead && !b.authorized(r.Context(), currentOwner, task) {
		writeErr(w, 403, "작업 승인이 변경되었습니다")
		return
	}
	var readBinding reportReadBinding
	if graphRead {
		var status int
		readBinding, status = b.authorizedReportBinding(r.Context(), owner, graphTask, strings.HasSuffix(r.URL.Path, "/coverage-graph"))
		if status != 0 {
			writeErr(w, status, "저장 보고서의 조회 권한·문맥을 확인할 수 없습니다")
			return
		}
	}
	target := *b.origin
	target.Path = r.URL.Path
	target.RawQuery = r.URL.RawQuery
	out, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(raw))
	if err != nil {
		writeErr(w, 502, "진단 API 요청을 만들 수 없습니다")
		return
	}
	if last := r.Header.Get("Last-Event-ID"); last != "" && r.URL.Path == "/api/exploration/activity/stream" {
		if len(r.Header.Values("Last-Event-ID")) != 1 || !canonicalNatural(last) {
			writeErr(w, 400, "잘못된 활동 커서입니다")
			return
		}
		out.Header.Set("Last-Event-ID", last)
	}
	out.Header.Set("Authorization", r.Header.Get("Authorization"))
	if strings.HasSuffix(r.URL.Path, "/e2-flow") && r.Header.Get(E2ModelWritesHeader) == "1" {
		out.Header.Set(E2ModelWritesHeader, "1")
	}
	if graphRead {
		out.Header.Set(reportReadScopeHeader, reportReadDigest(readBinding))
	}
	if r.Method == http.MethodPost {
		out.Header.Set("Content-Type", "application/json")
	}
	client := *b.client
	if r.URL.Path == "/api/exploration/activity/stream" {
		streamContext, cancel := context.WithTimeout(out.Context(), 30*time.Second)
		defer cancel()
		out = out.WithContext(streamContext)
		client.Timeout = 0
	}
	response, err := client.Do(out)
	if err != nil {
		writeErr(w, 503, "진단 API 응답을 확인할 수 없습니다. 변경 요청은 재전송하지 않았습니다")
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		writeErr(w, 502, "진단 API redirect를 허용하지 않습니다")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/exploration/activity") {
		b.servePublicActivity(w, r, response, owner, task)
		return
	}
	if graphRead {
		switch response.StatusCode {
		case 200:
			proof := response.Header.Values(reportReadScopeHeader)
			if len(proof) != 1 || proof[0] != reportReadDigest(readBinding) {
				writeErr(w, 503, "실행 서버의 저장 조회 결속을 확인할 수 없습니다")
				return
			}
		case 400, 401, 403, 404, 409, 503:
		case http.StatusRequestEntityTooLarge:
			// KG 조회 v1의 상한 초과(kg_graph_too_large, 재조회 불가)만 그대로 넘긴다(kg-query-v1 §11-3).
			// 무인자 coverage-graph·access-map의 413은 지금처럼 503이다.
			if !reportGraphKGView(r) {
				writeErr(w, 503, "저장 조회 서버의 응답을 확인할 수 없습니다")
				return
			}
		default:
			writeErr(w, 503, "저장 조회 서버의 응답을 확인할 수 없습니다")
			return
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, managementBridgeResponseLimit+1))
	if err != nil || len(body) > managementBridgeResponseLimit {
		writeErr(w, 502, "진단 API 응답 크기를 확인할 수 없습니다")
		return
	}
	if graphRead {
		// 응답을 내보내기 전에 JWT 회수, Job 소유권 및 직접 원천/현재 세대 변경을 다시 확인한다.
		currentOwner, err := b.authenticate(r)
		if err != nil || currentOwner != owner {
			writeManagementBridgeAuthError(w, r, err)
			return
		}
		next, status := b.authorizedReportBinding(r.Context(), owner, graphTask, strings.HasSuffix(r.URL.Path, "/coverage-graph"))
		if status != 0 {
			writeErr(w, status, "저장 보고서의 조회 권한·문맥이 변경되었습니다")
			return
		}
		if reportReadDigest(next) != reportReadDigest(readBinding) {
			writeErr(w, 409, "저장 보고서의 조회 문맥이 변경되었습니다")
			return
		}
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

func writeManagementBridgeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	status := managementBridgeAuthStatus(r, err)
	message := authErrUnauthorized
	if status == 503 {
		message = authErrDataSourceUnavailable
	}
	writeErr(w, status, message)
}

func managementBridgeAuthStatus(r *http.Request, err error) int {
	if _, ok := reportGraphRequestTask(r); ok && errors.Is(err, errManagementAuthenticationUnavailable) {
		return 503
	}
	return 401
}

func (b *diagnosisManagementBridge) authorizedReportBinding(ctx context.Context, owner string, task int64, graph bool) (reportReadBinding, int) {
	var none reportReadBinding
	if b.readPool == nil {
		return none, 503
	}
	ctx, cancel := context.WithTimeout(ctx, accessMapTimeout)
	defer cancel()
	check := func(id int64) int {
		if b.reportAuthorize != nil {
			allowed, err := b.reportAuthorize(ctx, owner, strconv.FormatInt(id, 10))
			if err != nil {
				return 503
			}
			if !allowed {
				return 403
			}
			return 0
		}
		if !b.authorized(ctx, owner, strconv.FormatInt(id, 10)) {
			return 403
		}
		return 0
	}
	if status := check(task); status != 0 {
		return none, status
	}
	bound, err := reportBindingSnapshot(ctx, b.readPool, task, graph)
	if errors.Is(err, sql.ErrNoRows) {
		return none, 404
	}
	if errors.Is(err, errReportReadScope) {
		return none, 409
	}
	if err != nil {
		return none, 503
	}
	for _, source := range bound.Tasks[1:] {
		if status := check(source.ID); status != 0 {
			return none, status
		}
	}
	return bound, 0
}
