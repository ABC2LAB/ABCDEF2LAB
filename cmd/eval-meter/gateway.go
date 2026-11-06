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
	generationLookupTimeout = 30 * time.Second
	upstreamTLSTimeout      = 10 * time.Second
)

var (
	errBadBody     = errors.New("eval-meter: request body rejected")
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
	UnfinishedEnd string `json:"unfinished_end,omitempty"` // upstream_error 행에만 있다
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
}

// verifiedMarker reports what was served and by whom; whether that matches the
// pinned provider is the runner's call, not the gateway's.
type verifiedMarker struct {
	Role              string             `json:"role"`
	Backend           string             `json:"backend"`
	Provider          string             `json:"provider"`
	Sampling          any                `json:"sampling"` // applied values; each backend has its own set
	SealedAt          string             `json:"sealed_at"`
	DrainedAt         string             `json:"drained_at"`
	VerifiedAt        string             `json:"verified_at"`
	Forwarded         uint64             `json:"forwarded_requests"`
	UpstreamErrors    uint64             `json:"upstream_errors"`
	UpstreamEnds      map[string]uint64  `json:"upstream_error_ends"` // 합이 UpstreamErrors와 같다
	Rejected          uint64             `json:"rejected_requests"`
	RejectedAfterSeal uint64             `json:"rejected_after_seal"`
	CancelledBySeal   uint64             `json:"cancelled_by_seal"`
	MissingIDs        uint64             `json:"responses_without_generation_id"`
	Generations       []generationResult `json:"generations"`
	LogComplete       bool               `json:"log_complete"`
}

type scannerKey struct{}

type llmGateway struct {
	cfg       gatewayConfig
	proxy     *httputil.ReverseProxy
	client    *http.Client
	dir       *os.File
	now       func() time.Time
	sleep     func(time.Duration)
	verifyErr chan error

	mu                sync.Mutex
	log               *rowLog
	seq               uint64
	sealed            bool
	sealedAt          time.Time
	drainedAt         time.Time
	inflight          int
	cancels           map[uint64]context.CancelFunc
	forwarded         uint64
	upstreamErrors    uint64
	upstreamEnds      map[string]uint64
	rejected          uint64
	rejectedAfterSeal uint64
	cancelledBySeal   uint64
	missingIDs        uint64
	ids               []string
	seenIDs           map[string]bool
	logFailed         bool
}

func defaultUpstreamTransport() http.RoundTripper {
	// No environment proxy: the gateway must reach the configured upstream itself.
	return &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSHandshakeTimeout: upstreamTLSTimeout}
}

func newLLMGateway(dir *os.File, cfg gatewayConfig, transport http.RoundTripper, now func() time.Time,
	sleep func(time.Duration)) (*llmGateway, string) {
	file, code := createPrivateFile(dir, llmLogName)
	if code != "" {
		return nil, code
	}
	g := &llmGateway{cfg: cfg, dir: dir, now: now, sleep: sleep, log: &rowLog{file: file},
		cancels: map[uint64]context.CancelFunc{}, seenIDs: map[string]bool{}, verifyErr: make(chan error, 1),
		upstreamEnds: map[string]uint64{endClientClosedAfterDone: 0, endClientClosedBeforeDone: 0, endUpstreamCutAfterDone: 0, endUpstreamCutBeforeDone: 0}}
	g.client = &http.Client{Transport: transport, Timeout: generationLookupTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
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
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
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
	rejected := false
	// The proxy aborts a broken stream with panic(http.ErrAbortHandler); the
	// bookkeeping must still run or the drain would wait forever.
	defer func() { g.finish(seq, ctx.Err() != nil, &row, rec, scan, rejected) }()

	body, status, reason := g.prepare(r)
	if reason != "" {
		rejected = true
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

// unfinishedEnd는 upstream_error 행이 어떻게 멈췄는지 적는다. 엔진이 [DONE]까지 읽고 닫은 스트림은
// 실패한 응답이 아니라서 제공자 장애로 읽히면 안 된다. 누가 먼저 닫았는지와 종료 이벤트가 지나갔는지만
// 말하고, 끊긴 책임이 누구에게 있는지는 말하지 않는다.
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

// outcome classifies a forwarded request by whether the upstream answer was
// read to the end, not by when seal arrived: an answer that completed just
// before seal is still a served generation that must be verified.
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

func (g *llmGateway) finish(seq uint64, cancelled bool, row *llmRow, rec *statusRecorder, scan *idScanner, rejected bool) {
	completed := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.cancels, seq)
	g.inflight--
	if rejected {
		g.rejected++
	} else {
		row.Status, row.ResponseBytes = rec.status, rec.bytes
		row.Decision = outcome(scan.complete, g.sealed, cancelled)
		switch row.Decision {
		case "forwarded":
			g.forwarded++
		case "cancelled_by_seal":
			g.cancelledBySeal++
		default:
			g.upstreamErrors++
			// 봉인 취소는 위 갈래로 빠지므로 여기서 cancelled는 엔진이 먼저 떠났다는 뜻이다.
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
	if err != nil {
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
		if err != nil || !ok || seen[key] || key == "provider" || samplingKeys[key] {
			return 0, errBadBody
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
		cancel()
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
		for _, id := range ids {
			results = append(results, g.lookup(id))
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.log.file.Sync(); err != nil {
		g.logFailed = true
	}
	return writeMarker(g.dir, llmVerifiedName, verifiedMarker{
		Role: roleGateway, Backend: g.cfg.backend, Provider: g.cfg.provider, Sampling: g.cfg.sampling,
		SealedAt: stamp(g.sealedAt), DrainedAt: stamp(g.drainedAt), VerifiedAt: stamp(g.now()),
		Forwarded: g.forwarded, UpstreamErrors: g.upstreamErrors, UpstreamEnds: maps.Clone(g.upstreamEnds), Rejected: g.rejected,
		RejectedAfterSeal: g.rejectedAfterSeal, CancelledBySeal: g.cancelledBySeal, MissingIDs: g.missingIDs,
		Generations: results, LogComplete: !g.logFailed,
	})
}

func (g *llmGateway) lookup(id string) generationResult {
	endpoint := *g.cfg.upstream
	endpoint.Path = g.cfg.generationPath
	endpoint.RawQuery = url.Values{"id": {id}}.Encode()
	for attempt := 1; attempt <= g.cfg.verifyAttempts; attempt++ {
		result, retry := g.lookupOnce(endpoint.String(), id)
		if !retry {
			return result
		}
		if attempt < g.cfg.verifyAttempts {
			g.sleep(g.cfg.verifyInterval)
		}
	}
	return generationResult{ID: id, Status: "unverified", Attempts: []providerAttempt{}}
}

// lookupOnce returns retry=true for answers that may change with time (stats
// not ready yet, rate limits, upstream errors) and false for a final answer.
func (g *llmGateway) lookupOnce(endpoint, id string) (generationResult, bool) {
	unverified := generationResult{ID: id, Status: "unverified", Attempts: []providerAttempt{}}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return unverified, false
	}
	req.Header.Set("Authorization", "Bearer "+g.cfg.key)
	req.Header.Set("Accept", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return unverified, true
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return unverified, false
	}
	if resp.StatusCode != http.StatusOK {
		return unverified, true
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
	if err != nil || len(raw) > maxGenerationBytes || json.Unmarshal(raw, &stats) != nil ||
		stats.Data == nil || stats.Data.ProviderName == nil || *stats.Data.ProviderName == "" {
		return unverified, true
	}
	result := generationResult{ID: id, Status: "verified", ProviderName: *stats.Data.ProviderName,
		Attempts: make([]providerAttempt, 0, len(stats.Data.ProviderResponses))}
	for _, attempt := range stats.Data.ProviderResponses {
		result.Attempts = append(result.Attempts, providerAttempt(attempt))
	}
	return result, false
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
