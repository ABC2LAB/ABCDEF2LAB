package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"maps"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// The LLM gateway is the engine's only way out during an evaluation run. Unlike
// the meter it has to change requests (api: pin the serving provider and use
// the gateway's own key), so it runs as its own container with its own rules:
// the engine holds no API key and has no other route, and neither the meter nor
// the observer ever sees the key.

const (
	llmLogName      = "llm-requests.jsonl"
	llmVerifiedName = "llm-verified.json"

	// upstream_error 행이 멈춘 모양. 합이 upstream_errors와 같다.
	endClientClosedAfterDone  = "client_closed_after_done"
	endClientClosedBeforeDone = "client_closed_before_done"
	endUpstreamCutAfterDone   = "upstream_cut_after_done"
	endUpstreamCutBeforeDone  = "upstream_cut_before_done"

	// cancelled_by_seal 행이 멈춘 모양: 봉인 전에 위쪽 응답의 종료 이벤트 줄을 읽었는지다(#735 리뷰). 종료 줄을 읽은 뒤
	// 봉인된 호출은 엔진이 그 출력을 썼을 수 있어, 끊긴 호출 예외는 종료 줄 전 봉인만 받는다.
	sealEndBeforeDone = "sealed_before_done"
	sealEndAfterDone  = "sealed_after_done"

	// 엔진 본문의 샘플링 칸·provider 칸 거부 사유. 행 결정은 "rejected_"+사유다.
	rejectSampling = "sampling"
	rejectProvider = "provider"
	// The compose evaluation layer passes the key only to the gateway container.
	llmKeyEnv = "EVAL_LLM_API_KEY"

	backendAPI   = "api"
	backendLocal = "local"

	// One request carries the conversation so far; this bounds a request, not a run.
	maxLLMRequestBytes = 16 << 20
	// The generation id is the first JSON key or sits in the first SSE event,
	// so only the start of each response is kept to find it.
	maxIDScanBytes = 64 << 10
	// sseDoneRe가 찾는 가장 긴 줄의 바이트 수(앞 줄바꿈 포함).
	maxDoneLineBytes   = len("\ndata: [DONE]\r\n")
	maxGenerationBytes = 64 << 10
	// Generation stats can lag the response; each lookup still has to end.
	// generation 조회 한 번의 시간 상한. 막힌 조회 하나가 라운드를 오래 잡지 않게 짧게 두고, 실패한 것은 다음 라운드에서 다시
	// 묻는다(2026-11-18 보정 쌍 B1: 30초 상한에서 network_error·408 몇 건이 전체 시한을 다 써 12건을 묻지 못했다).
	generationLookupTimeout = 10 * time.Second
	upstreamTLSTimeout      = 10 * time.Second
)

var (
	errBadBody = errors.New("eval-meter: request body rejected")
	// 엔진이 샘플링 칸·provider 칸을 보낸 요청은 다른 본문 오류와 따로 센다. 개선 후(O) 제품 transport는 같은 칸을
	// 거부하지 않고 manifest 값으로 덮어쓰므로, B·O가 같은 조건이라는 말은 "엔진이 그 칸을 보내지 않는다"는 전제에서만
	// 성립한다. 그 전제가 깨졌는지 회차마다 드러나게 하려는 것이다(탐색 분리 배치 결정안 4-4, 2026-11-16 사용자 결정).
	errSamplingKey = errors.New("eval-meter: engine sent a sampling key")
	errProviderKey = errors.New("eval-meter: engine sent a provider key")
	generationIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	sseDoneRe      = regexp.MustCompile(`\ndata: ?\[DONE\]\r?\n`)
	providerSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}(/[a-z0-9][a-z0-9._-]{0,63})?$`)
)

type gatewayConfig struct {
	backend        string
	upstream       *url.URL
	allowPath      string
	provider       string      // api: full provider slug pinned on every request
	sampling       samplingPin // sampling parameters pinned on every request
	generationPath string      // api: generation stats endpoint used to verify the pin
	key            string      // api only; never written anywhere
	verifyAttempts int
	verifyInterval time.Duration
}

// llmRow deliberately has no path, query, header or body field; the generation
// id is an opaque provider handle used only to verify the pinned provider.
type llmRow struct {
	Seq           uint64 `json:"seq"`
	Decision      string `json:"decision"`
	Method        string `json:"method"`
	Status        int    `json:"status"`
	StartedAt     string `json:"started_at"`
	CompletedAt   string `json:"completed_at"`
	RequestBytes  int64  `json:"request_bytes"`
	ResponseBytes int64  `json:"response_bytes"`
	GenerationID  string `json:"generation_id"`
	UnfinishedEnd string `json:"unfinished_end,omitempty"` // upstream_error 행(end*)과 cancelled_by_seal 행(sealEnd*)에만 있다
}

type providerAttempt struct {
	ProviderName string `json:"provider_name"`
	EndpointID   string `json:"endpoint_id"`
	Status       int    `json:"status"`
}

type generationResult struct {
	ID           string            `json:"id"`
	Status       string            `json:"status"`
	ProviderName string            `json:"provider_name"`
	Attempts     []providerAttempt `json:"attempts"`
	Lookup       lookupTrace       `json:"lookup"`
}

// lookupTrace는 그 generation 조회의 시도 수와 마지막 응답 종류다(값·본문 없음). 미확인이 통계 지연인지(http_404 등)
// 즉시 거부인지(http_401·http_403)를 회차 뒤에 가르려고 남긴다(2026-11-18 VAmPI 유료 파일럿에서 원인을 가를 수 없었다).
// 시한 안에 한 번도 묻지 못한 것은 시도 0, last=deadline이다. 물어봤는데 시한에 걸린 것은 시도 수가 설정보다 적다.
type lookupTrace struct {
	Tries int    `json:"tries"`
	Last  string `json:"last"`
}

// verifiedMarker reports what was served and by whom; whether that matches the
// pinned provider is the runner's call, not the gateway's.
type verifiedMarker struct {
	Role           string            `json:"role"`
	Backend        string            `json:"backend"`
	Provider       string            `json:"provider"`
	Sampling       any               `json:"sampling"` // applied values; each backend has its own set
	SealedAt       string            `json:"sealed_at"`
	DrainedAt      string            `json:"drained_at"`
	VerifiedAt     string            `json:"verified_at"`
	Forwarded      uint64            `json:"forwarded_requests"`
	UpstreamErrors uint64            `json:"upstream_errors"`
	UpstreamEnds   map[string]uint64 `json:"upstream_error_ends"` // 합이 UpstreamErrors와 같다
	Rejected       uint64            `json:"rejected_requests"`
	// 거부 가운데 엔진 본문의 샘플링 칸·provider 칸 때문인 수(Rejected에 들어 있음). 게이트웨이를 거치지 않는
	// generation-verify(제품 기록)에는 이 거부가 없으므로 0이 아니라 null이다(해당 없음).
	RejectedSampling *uint64 `json:"rejected_sampling"`
	RejectedProvider *uint64 `json:"rejected_provider"`
	// 위쪽이 429로 답한 호출 수(전달·끊김 행 모두, 게이트웨이 자신의 거부는 빼고). 결정안 §3.2.
	RateLimited       uint64             `json:"rate_limited"`
	RejectedAfterSeal uint64             `json:"rejected_after_seal"`
	CancelledBySeal   uint64             `json:"cancelled_by_seal"`
	MissingIDs        uint64             `json:"responses_without_generation_id"`
	Generations       []generationResult `json:"generations"`
	LogComplete       bool               `json:"log_complete"`
}

type scannerKey struct{}

type llmGateway struct {
	cfg   gatewayConfig
	proxy *httputil.ReverseProxy
	// lookupTransport는 generation 조회 전용 연결을 조회 때마다 새로 만든다. 모델 호출 프록시의 연결 풀을 같이 쓰지 않는다:
	// 2026-11-18 VAmPI 유료 파일럿 B 1차에서 봉인 직후 조회가 몇 분 지난 호출까지 전부 바로 미확인으로 끝났고(O 쪽 새
	// 프로세스 조회는 확인됨), 남은 후보 원인이 이 공유였다. O의 generation-verify(새 프로세스)와 같은 조건으로 맞춘다.
	lookupTransport func() http.RoundTripper
	dir             *os.File
	now             func() time.Time
	sleep           func(time.Duration)
	verifyErr       chan error

	mu                sync.Mutex
	log               *rowLog
	seq               uint64
	sealed            bool
	sealedAt          time.Time
	drainedAt         time.Time
	inflight          int
	cancels           map[uint64]context.CancelCauseFunc
	forwarded         uint64
	upstreamErrors    uint64
	upstreamEnds      map[string]uint64
	rejected          uint64
	rejectedSampling  uint64
	rejectedProvider  uint64
	rateLimited       uint64
	rejectedAfterSeal uint64
	cancelledBySeal   uint64
	missingIDs        uint64
	ids               []string
	seenIDs           map[string]bool
	logFailed         bool
}

// gatewayTransports는 모델 호출 프록시 transport와 generation 조회 전용 transport 생성기를 돌려준다. 조회는 부를 때마다
// 새 transport라 프록시의 연결 풀과 섞이지 않는다.
func gatewayTransports() (http.RoundTripper, func() http.RoundTripper) {
	return defaultUpstreamTransport(), func() http.RoundTripper { return defaultUpstreamTransport() }
}

// newRunningGateway는 실제 실행용 게이트웨이다(main이 부른다): 프록시와 조회 연결을 나누고 실제 시계를 쓴다.
func newRunningGateway(dir *os.File, cfg gatewayConfig) (*llmGateway, string) {
	proxy, lookup := gatewayTransports()
	return newLLMGateway(dir, cfg, proxy, lookup, time.Now, time.Sleep)
}

func defaultUpstreamTransport() http.RoundTripper {
	// No environment proxy: the gateway must reach the configured upstream itself.
	return &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSHandshakeTimeout: upstreamTLSTimeout}
}

func newLLMGateway(dir *os.File, cfg gatewayConfig, transport http.RoundTripper, lookupTransport func() http.RoundTripper,
	now func() time.Time, sleep func(time.Duration)) (*llmGateway, string) {
	file, code := createPrivateFile(dir, llmLogName)
	if code != "" {
		return nil, code
	}
	g := &llmGateway{cfg: cfg, dir: dir, now: now, sleep: sleep, log: &rowLog{file: file}, lookupTransport: lookupTransport,
		cancels: map[uint64]context.CancelCauseFunc{}, seenIDs: map[string]bool{}, verifyErr: make(chan error, 1),
		upstreamEnds: map[string]uint64{endClientClosedAfterDone: 0, endClientClosedBeforeDone: 0, endUpstreamCutAfterDone: 0, endUpstreamCutBeforeDone: 0}}
	g.proxy = &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1, // stream every chunk as it arrives
		// A cut stream is logged with the transport error text, which can carry
		// addresses; the row's decision already records what happened.
		ErrorLog: log.New(io.Discard, "", 0),
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(cfg.upstream)
			r.Out.Host = ""
			// Only what the upstream needs leaves; engine headers (its placeholder
			// key, session ids, cookies) stay behind.
			header := http.Header{}
			header.Set("Content-Type", "application/json")
			if accept := r.In.Header.Get("Accept"); accept != "" {
				header.Set("Accept", accept)
			}
			// The id scan reads the plain response.
			header.Set("Accept-Encoding", "identity")
			if cfg.backend == backendAPI {
				header.Set("Authorization", "Bearer "+cfg.key)
			}
			r.Out.Header = header
		},
		ModifyResponse: func(resp *http.Response) error {
			if scan, ok := resp.Request.Context().Value(scannerKey{}).(*idScanner); ok {
				scan.ReadCloser = resp.Body
				scan.stream = isEventStream(resp.Header.Get("Content-Type"))
				resp.Body = scan
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			if rec, ok := w.(*statusRecorder); ok {
				rec.upstreamFailed = true
			}
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	return g, ""
}

func isEventStream(contentType string) bool {
	media, _, err := mime.ParseMediaType(contentType)
	return err == nil && media == "text/event-stream"
}

func (g *llmGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := g.now()
	// 취소 원인을 남긴다: 봉인이 먼저 끊었는지(errSealCut), 호출자가 먼저 끊었는지(부모 문맥의 원인)를 마감 때 가른다(#735 리뷰).
	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)
	g.mu.Lock()
	g.seq++
	seq := g.seq
	if g.sealed || g.logFailed {
		g.rejectedAfterSeal++
		g.writeRowLocked(llmRow{Seq: seq, Decision: "rejected_after_seal", Method: r.Method,
			Status: http.StatusServiceUnavailable, StartedAt: stamp(started), CompletedAt: stamp(g.now())})
		g.mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	g.inflight++
	g.cancels[seq] = cancel
	g.mu.Unlock()

	row := llmRow{Seq: seq, Method: r.Method, StartedAt: stamp(started)}
	rec := &statusRecorder{ResponseWriter: w}
	scan := &idScanner{}
	rejected := ""
	// The proxy aborts a broken stream with panic(http.ErrAbortHandler); the
	// bookkeeping must still run or the drain would wait forever.
	defer func() { g.finish(seq, ctx.Err() != nil, cutBySeal(ctx), &row, rec, scan, rejected) }()

	body, status, reason := g.prepare(r)
	if reason != "" {
		rejected = reason
		w.WriteHeader(status)
		row.Decision, row.Status = "rejected_"+reason, status
		return
	}
	row.RequestBytes = int64(len(body))
	out := r.Clone(context.WithValue(ctx, scannerKey{}, scan))
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	out.TransferEncoding = nil
	g.proxy.ServeHTTP(rec, out)
}

// unfinishedEnd는 upstream_error 행에서 게이트웨이가 관찰한 두 가지를 적는다. 호출자 쪽 취소나 쓰기 실패를
// 봤는지와, 위쪽 응답에서 종료 이벤트 줄을 읽었는지다. 종료 줄은 호출자에게 쓰기 전에 표시하므로 엔진이
// 그 줄을 받았다는 뜻이 아니고, 어느 쪽이 먼저 닫았는지와 끊긴 책임도 말하지 않는다.
func unfinishedEnd(clientClosed, doneSeen bool) string {
	switch {
	case clientClosed && doneSeen:
		return endClientClosedAfterDone
	case clientClosed:
		return endClientClosedBeforeDone
	case doneSeen:
		return endUpstreamCutAfterDone
	default:
		return endUpstreamCutBeforeDone
	}
}

// errSealCut은 봉인이 진행 중 요청을 끊을 때 남기는 취소 원인이다.
var errSealCut = errors.New("sealed by the gateway")

// cutBySeal은 그 요청 문맥을 처음 끊은 것이 게이트웨이 봉인인지 본다. 호출자가 먼저 끊었다가 그 호출이 마감되기 전에 봉인이
// 와도 원인은 호출자 쪽(부모 문맥) 그대로라 봉인 취소로 세지 않는다(#735 리뷰).
func cutBySeal(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), errSealCut)
}

// sealEnd는 봉인으로 끊긴 호출이 종료 이벤트 줄을 읽기 전에 끊겼는지 남긴다.
func sealEnd(doneSeen bool) string {
	if doneSeen {
		return sealEndAfterDone
	}
	return sealEndBeforeDone
}

// outcome classifies a forwarded request by whether the upstream answer was
// read to the end, not by when seal arrived: an answer that completed just
// before seal is still a served generation that must be verified. sealed는
// 봉인이 그 요청의 최초 취소 원인이었는지다(cutBySeal).
func outcome(complete, sealed, cancelled bool) string {
	switch {
	case complete:
		return "forwarded"
	case sealed && cancelled:
		return "cancelled_by_seal"
	default:
		return "upstream_error"
	}
}

// finish는 요청 하나를 센다. rejected는 게이트웨이가 거부한 사유(prepare의 reason)이고, 전달한 요청이면 빈 글이다.
func (g *llmGateway) finish(seq uint64, cancelled, sealCut bool, row *llmRow, rec *statusRecorder, scan *idScanner, rejected string) {
	completed := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.cancels, seq)
	g.inflight--
	if rejected != "" {
		g.rejected++
		switch rejected {
		case rejectSampling:
			g.rejectedSampling++
		case rejectProvider:
			g.rejectedProvider++
		}
	} else {
		row.Status, row.ResponseBytes = rec.status, rec.bytes
		if row.Status == http.StatusTooManyRequests {
			g.rateLimited++
		}
		row.Decision = outcome(scan.complete, sealCut, cancelled)
		switch row.Decision {
		case "forwarded":
			g.forwarded++
		case "cancelled_by_seal":
			g.cancelledBySeal++
			row.UnfinishedEnd = sealEnd(scan.done)
		default:
			g.upstreamErrors++
			// 봉인 취소는 위 갈래로 빠지므로 여기서 cancelled는 호출자 쪽 취소다.
			row.UnfinishedEnd = unfinishedEnd(cancelled || rec.writeFailed, scan.done)
			g.upstreamEnds[row.UnfinishedEnd]++
		}
		// Any id the provider sent means it served (part of) a generation, so it
		// is verified even when the stream was cut short.
		if id := scan.id(); id != "" {
			row.GenerationID = id
			if !g.seenIDs[id] {
				g.seenIDs[id] = true
				g.ids = append(g.ids, id)
			}
		} else if row.Decision == "forwarded" && row.Status >= 200 && row.Status < 300 {
			g.missingIDs++
		}
	}
	row.CompletedAt = stamp(completed)
	g.writeRowLocked(*row)
	g.maybeDrainedLocked(completed)
}

// prepare admits only the registered chat-completions POST with a single JSON
// object body. api pins the provider and sampling; local pins sampling only.
// Everything else in the engine's body reaches the upstream byte for byte.
func (g *llmGateway) prepare(r *http.Request) ([]byte, int, string) {
	if r.Method != http.MethodPost {
		return nil, http.StatusForbidden, "method"
	}
	if r.URL.Path != g.cfg.allowPath || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || isUpgrade(r) {
		return nil, http.StatusForbidden, "path"
	}
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
		return nil, http.StatusUnsupportedMediaType, "content_type"
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxLLMRequestBytes+1))
	if err != nil {
		return nil, http.StatusBadRequest, "body"
	}
	if len(body) > maxLLMRequestBytes {
		return nil, http.StatusRequestEntityTooLarge, "too_large"
	}
	open, err := checkObject(body)
	switch {
	case errors.Is(err, errSamplingKey):
		return nil, http.StatusBadRequest, rejectSampling
	case errors.Is(err, errProviderKey):
		return nil, http.StatusBadRequest, rejectProvider
	case err != nil:
		return nil, http.StatusBadRequest, "body"
	}
	if g.cfg.backend == backendLocal {
		return insertMembers(body, open, g.cfg.sampling.fields()), 0, ""
	}
	return pinRequest(body, open, g.cfg.provider, g.cfg.sampling), 0, ""
}

// checkObject accepts one JSON object with unique top-level keys and no
// "provider" or sampling key (an engine-chosen route or sampling value would mix
// conditions with the pin), and returns the offset of its opening brace.
// 거부 사유는 먼저 만난 문제다. 샘플링 칸·provider 칸이면 errSamplingKey·errProviderKey, 그 밖은 errBadBody.
func checkObject(body []byte) (int, error) {
	open := bytes.IndexFunc(body, func(r rune) bool { return r != ' ' && r != '\t' && r != '\n' && r != '\r' })
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') || open < 0 || body[open] != '{' {
		return 0, errBadBody
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		switch {
		case err != nil || !ok || seen[key]:
			return 0, errBadBody
		case key == "provider":
			return 0, errProviderKey
		case samplingKeys[key]:
			return 0, errSamplingKey
		}
		seen[key] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return 0, errBadBody
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return 0, errBadBody
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return 0, errBadBody
	}
	return open, nil
}

// pinRequest inserts the sampling parameters and the routing object right after
// the opening brace so the rest of the engine's body reaches the provider byte
// for byte. require_parameters makes the router refuse the request when the
// pinned provider does not support a parameter in it; without it the router
// drops that parameter silently, and the applied values in the verified marker
// would no longer be what the provider used.
func pinRequest(body []byte, open int, slug string, sampling samplingPin) []byte {
	pin, _ := json.Marshal(struct {
		Order             []string `json:"order"`
		AllowFallbacks    bool     `json:"allow_fallbacks"`
		RequireParameters bool     `json:"require_parameters"`
	}{Order: []string{slug}, AllowFallbacks: false, RequireParameters: true})
	var field []byte
	if sampling != nil {
		field = append(sampling.fields(), ',')
	}
	field = append(append(field, `"provider":`...), pin...)
	return insertMembers(body, open, field)
}

// insertMembers adds object members right after the opening brace.
func insertMembers(body []byte, open int, field []byte) []byte {
	rest := bytes.TrimLeft(body[open+1:], " \t\r\n")
	if len(rest) == 0 || rest[0] != '}' {
		field = append(field, ',')
	}
	out := make([]byte, 0, len(body)+len(field))
	out = append(out, body[:open+1]...)
	out = append(out, field...)
	return append(out, body[open+1:]...)
}

// idScanner keeps the start of a response to find its generation id; the
// response itself streams through untouched.
type idScanner struct {
	io.ReadCloser
	stream   bool
	complete bool // the upstream answer was read to its end
	done     bool // SSE 종료 이벤트가 지나갔다
	head     bytes.Buffer
	tail     []byte // 직전 읽기의 끝. 종료 줄이 읽기 경계에 걸려도 찾는다
}

func (s *idScanner) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	if room := maxIDScanBytes - s.head.Len(); room > 0 && n > 0 {
		s.head.Write(p[:min(n, room)])
	}
	if s.stream && !s.done && n > 0 {
		s.noteDone(p[:n])
	}
	if errors.Is(err, io.EOF) {
		s.complete = true
	}
	return n, err
}

// noteDone은 종료 이벤트 줄만 찾는다. JSON으로 인코딩된 이벤트 데이터에는 줄바꿈이 그대로 들어갈 수 없어서
// 모델 출력이 이 줄을 흉내 낼 수 없다. 바이트는 메모리에만 있고 기록하지 않는다.
func (s *idScanner) noteDone(chunk []byte) {
	if s.tail == nil {
		s.tail = []byte{'\n'}
	}
	window := append(s.tail[:len(s.tail):len(s.tail)], chunk...)
	if sseDoneRe.Match(window) {
		s.done = true
	}
	s.tail = bytes.Clone(window[max(0, len(window)-maxDoneLineBytes):])
}

func (s *idScanner) id() string {
	var id string
	if s.stream {
		id = sseID(s.head.Bytes())
	} else {
		id = jsonID(s.head.Bytes())
	}
	if !generationIDRe.MatchString(id) {
		return ""
	}
	return id
}

func sseID(head []byte) string {
	complete := head[:bytes.LastIndexByte(head, '\n')+1]
	for _, line := range bytes.Split(complete, []byte("\n")) {
		payload, ok := bytes.CutPrefix(bytes.TrimRight(line, "\r"), []byte("data:"))
		payload = bytes.TrimSpace(payload)
		if !ok || len(payload) == 0 || payload[0] != '{' {
			continue
		}
		var event struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(payload, &event) == nil && event.ID != "" {
			return event.ID
		}
	}
	return ""
}

func jsonID(head []byte) string {
	dec := json.NewDecoder(bytes.NewReader(head))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return ""
	}
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok {
			return ""
		}
		if key == "id" {
			var id string
			if dec.Decode(&id) != nil {
				return ""
			}
			return id
		}
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return ""
		}
	}
	return ""
}

// seal stops new requests and cancels the ones in flight; the run is over, so
// a late answer must not keep the drain open.
func (g *llmGateway) seal() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sealed {
		return
	}
	g.sealed = true
	g.sealedAt = g.now()
	for _, cancel := range g.cancels {
		cancel(errSealCut) // 이미 호출자 쪽에서 끝난 문맥이면 원인이 바뀌지 않는다
	}
	g.maybeDrainedLocked(g.sealedAt)
}

func (g *llmGateway) maybeDrainedLocked(at time.Time) {
	if !g.sealed || g.inflight != 0 || !g.drainedAt.IsZero() {
		return
	}
	g.drainedAt = at
	ids := append([]string(nil), g.ids...)
	go func() { g.verifyErr <- g.verify(ids) }()
}

// verify looks up every generation id with the gateway's key (api) and writes
// one marker. local has no provider to verify; the marker still records counts.
func (g *llmGateway) verify(ids []string) error {
	results := make([]generationResult, 0, len(ids))
	if g.cfg.backend == backendAPI {
		verifier := g.verifier()
		results = verifier.lookupAll(ids)
		verifier.client.CloseIdleConnections()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.log.file.Sync(); err != nil {
		g.logFailed = true
	}
	rejectedSampling, rejectedProvider := g.rejectedSampling, g.rejectedProvider
	return writeMarker(g.dir, llmVerifiedName, verifiedMarker{
		Role: roleGateway, Backend: g.cfg.backend, Provider: g.cfg.provider, Sampling: g.cfg.sampling,
		SealedAt: stamp(g.sealedAt), DrainedAt: stamp(g.drainedAt), VerifiedAt: stamp(g.now()),
		Forwarded: g.forwarded, UpstreamErrors: g.upstreamErrors, UpstreamEnds: maps.Clone(g.upstreamEnds), Rejected: g.rejected,
		RejectedSampling: &rejectedSampling, RejectedProvider: &rejectedProvider, RateLimited: g.rateLimited,
		RejectedAfterSeal: g.rejectedAfterSeal, CancelledBySeal: g.cancelledBySeal, MissingIDs: g.missingIDs,
		Generations: results, LogComplete: !g.logFailed,
	})
}

// generationVerifier looks a generation id up in the provider's generation stats.
// 평가 게이트웨이(B)의 봉인 뒤 검증과 제품 호출 기록 사후 검증(O, -role generation-verify)이 이 코드 하나를 쓴다.
// 그래서 두 쪽의 제공자·fallback 판정 근거가 같은 조회·같은 재시도 규칙에서 나온다(탐색 분리 배치 결정안 D(b)).
type generationVerifier struct {
	upstream *url.URL
	path     string
	key      string
	attempts int
	interval time.Duration
	client   *http.Client
	sleep    func(time.Duration)
	now      func() time.Time
}

// verifier는 부를 때마다 조회 전용 새 연결(lookupTransport)로 클라이언트를 만든다. 봉인 뒤 한 번 부른다.
func (g *llmGateway) verifier() generationVerifier {
	client := &http.Client{Transport: g.lookupTransport(), Timeout: generationLookupTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return generationVerifier{upstream: g.cfg.upstream, path: g.cfg.generationPath, key: g.cfg.key,
		attempts: g.cfg.verifyAttempts, interval: g.cfg.verifyInterval, client: client, sleep: g.sleep, now: g.now}
}

// lookupDeadline은 조회 전체의 시한이다: 쉬는 시간 상한(시도 수 × 간격)에 조회 한 번의 시간 상한을 더한다. 시한이 지나면
// 더 묻지 않고 남은 것을 미확인(last=deadline)으로 표식에 적는다. 그래서 조회가 끝나는 시각은 시한 + 조회 한 번의 시간 상한을
// 넘지 않고, 러너는 그보다 긴 대기 시한(runner_config가 검사)으로 표식을 늘 받는다(2026-11-18 파일럿: 표식 없이 시한 초과).
func (v generationVerifier) lookupDeadline() time.Duration {
	return time.Duration(v.attempts)*v.interval + generationLookupTimeout
}

// lookupAll은 generation들을 라운드로 조회한다. 라운드마다 아직 답이 정해지지 않은 것만 한 번씩 묻고, 라운드 사이에만
// 쉰다. 건마다 순서대로 재시도하면 통계가 늦을 때 걸리는 시간이 건수에 비례해 러너 대기 시한을 넘는다(2026-11-18 파일럿).
// 이렇게 하면 쉬는 시간의 합이 (시도 수 - 1) × 간격을 넘지 않는다. 결과는 입력 순서다.
func (v generationVerifier) lookupAll(ids []string) []generationResult {
	results := make([]generationResult, len(ids))
	traces := make([]lookupTrace, len(ids))
	pending := make([]int, len(ids))
	for i := range ids {
		pending[i] = i
	}
	deadline := v.now().Add(v.lookupDeadline())
	expired := false
	for round := 1; round <= v.attempts && len(pending) > 0 && !expired; round++ {
		var next []int
		for n, i := range pending {
			if !v.now().Before(deadline) {
				expired = true
				next = append(next, pending[n:]...)
				break
			}
			endpoint := *v.upstream
			endpoint.Path = v.path
			endpoint.RawQuery = url.Values{"id": {ids[i]}}.Encode()
			result, retry, last := v.lookupOnce(endpoint.String(), ids[i])
			traces[i].Tries++
			traces[i].Last = last
			if retry {
				next = append(next, i)
				continue
			}
			results[i] = result
		}
		pending = next
		if len(pending) > 0 && round < v.attempts && !expired {
			v.sleep(v.interval)
		}
	}
	for _, i := range pending {
		results[i] = generationResult{ID: ids[i], Status: "unverified", Attempts: []providerAttempt{}}
		if traces[i].Tries == 0 {
			traces[i].Last = "deadline" // 시한 안에 한 번도 묻지 못했다(물어본 것은 마지막 응답 종류가 남는다)
		}
	}
	for i := range results {
		results[i].Lookup = traces[i]
	}
	return results
}

// lookupOnce returns retry=true for answers that may change with time (stats
// not ready yet, rate limits, upstream errors) and false for a final answer.
// last는 응답 종류(verified, http_<코드>, network_error, request_error, body_invalid, no_provider)다.
func (v generationVerifier) lookupOnce(endpoint, id string) (generationResult, bool, string) {
	unverified := generationResult{ID: id, Status: "unverified", Attempts: []providerAttempt{}}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return unverified, false, "request_error"
	}
	req.Header.Set("Authorization", "Bearer "+v.key)
	req.Header.Set("Accept", "application/json")
	resp, err := v.client.Do(req)
	if err != nil {
		return unverified, true, "network_error"
	}
	defer resp.Body.Close()
	code := "http_" + strconv.Itoa(resp.StatusCode)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return unverified, false, code
	}
	if resp.StatusCode != http.StatusOK {
		return unverified, true, code
	}
	var stats struct {
		Data *struct {
			ProviderName      *string `json:"provider_name"`
			ProviderResponses []struct {
				ProviderName string `json:"provider_name"`
				EndpointID   string `json:"endpoint_id"`
				Status       int    `json:"status"`
			} `json:"provider_responses"`
		} `json:"data"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxGenerationBytes+1))
	if err != nil || len(raw) > maxGenerationBytes || json.Unmarshal(raw, &stats) != nil || stats.Data == nil {
		return unverified, true, "body_invalid"
	}
	if stats.Data.ProviderName == nil || *stats.Data.ProviderName == "" {
		return unverified, true, "no_provider"
	}
	result := generationResult{ID: id, Status: "verified", ProviderName: *stats.Data.ProviderName,
		Attempts: make([]providerAttempt, 0, len(stats.Data.ProviderResponses))}
	for _, attempt := range stats.Data.ProviderResponses {
		result.Attempts = append(result.Attempts, providerAttempt(attempt))
	}
	return result, false, "verified"
}

func (g *llmGateway) writeRowLocked(row llmRow) {
	if err := g.log.write(row); err != nil {
		g.logFailed = true
		return
	}
	// A handful of LLM calls per minute: syncing each row keeps the count readable mid-run.
	if err := g.log.file.Sync(); err != nil {
		g.logFailed = true
	}
}

func (g *llmGateway) close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	syncErr := g.log.file.Sync()
	closeErr := g.log.file.Close()
	if g.logFailed {
		return errors.Join(syncErr, closeErr, errors.New("eval-meter: llm log incomplete"))
	}
	return errors.Join(syncErr, closeErr)
}
