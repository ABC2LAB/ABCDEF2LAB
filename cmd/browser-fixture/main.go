// 브라우저 수용시험(docs/evaluation/browser-acceptance.md)용 SPA와 범위 밖 수신기다.
// 실제 앱을 흉내 내지 않는다. 페이지가 시나리오별 우회를 시도하고, 수신기와 쓰기 경로에 닿은 것만 기록한다.
// 프로세스 수준 우회(B01~B03, B10, B11)는 이 페이지로 시험하지 않고 실행기 쪽 하네스가 맡는다.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	surfaceReceiver    = "receiver"
	surfaceReceiverUDP = "receiver_udp"
	surfaceReceiverTCP = "receiver_conn"
	surfaceTargetWrite = "target_write"
	unknownScenario    = "unknown"
	readHeaderTimeout  = 10 * time.Second
	shutdownTimeout    = 5 * time.Second
	altSvcMaxAge       = 60
	udpBufferBytes     = 2048
	stunCookieOffset   = 4
	quicLongHeaderBit  = 0x80
	scenarioQUIC       = "B08"
	scenarioSTUN       = "B09"
)

var stunCookie = []byte{0x21, 0x12, 0xA4, 0x42}

var safeHost = regexp.MustCompile(`^[A-Za-z0-9.:\[\]-]+$`)

// 경로·query·헤더·본문은 기록하지 않는다. 시나리오 값은 허용 목록에 있는 것만 남긴다.
type row struct {
	Seq      uint64 `json:"seq"`
	Surface  string `json:"surface"`
	Scenario string `json:"scenario"`
	Method   string `json:"method,omitempty"`
	Upgrade  bool   `json:"upgrade,omitempty"`
}

type recorder struct {
	mu     sync.Mutex
	file   *os.File
	seq    uint64
	err    error
	failed chan struct{}
}

func newRecorder(path string) (*recorder, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	return &recorder{file: file, failed: make(chan struct{})}, nil
}

// 도달 기록을 잃으면 우회가 통과로 보이므로, 쓰기에 실패하면 프로세스 전체를 실패로 끝낸다.
func (r *recorder) record(v row) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.seq++
	v.Seq = r.seq
	data, err := json.Marshal(v)
	if err == nil {
		_, err = r.file.Write(append(data, '\n'))
	}
	if err != nil {
		r.err = err
		close(r.failed)
	}
	return err
}

// 앞선 기록 실패와 저장 실패를 모두 돌려줘서, 끝에 생긴 손실도 실행 실패가 되게 한다.
func (r *recorder) close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return errors.Join(r.err, r.file.Sync(), r.file.Close())
}

type receiverInfo struct {
	origin, websocket, stun, udpPort string
}

// 대상이 HTTPS면 수신기도 HTTPS여야 한다. 아니면 혼합 콘텐츠로 막혀서 시도 자체가 생기지 않는다.
func parseReceiver(raw, udpListen string, tlsEnabled bool) (receiverInfo, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !safeHost.MatchString(u.Host) ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return receiverInfo{}, errors.New("receiver origin must be http(s)://host[:port]")
	}
	if (u.Scheme == "https") != tlsEnabled {
		return receiverInfo{}, errors.New("receiver origin scheme must match the TLS setting")
	}
	_, port, err := net.SplitHostPort(udpListen)
	if err != nil {
		return receiverInfo{}, fmt.Errorf("receiver udp address: %w", err)
	}
	origin := u.Scheme + "://" + u.Host
	return receiverInfo{origin, "ws" + strings.TrimPrefix(origin, "http"), net.JoinHostPort(u.Hostname(), port), port}, nil
}

func (i receiverInfo) render(text string) string {
	return strings.NewReplacer("{{R}}", i.origin, "{{WS}}", i.websocket, "{{STUN}}", i.stun).Replace(text)
}

func write(w http.ResponseWriter, contentType, body string) {
	w.Header().Set("Content-Type", contentType)
	if _, err := w.Write([]byte(body)); err != nil {
		fmt.Fprintln(os.Stderr, "browser-fixture: response write failed:", err)
	}
}

func targetHandler(rec *recorder, receiver receiverInfo) http.Handler {
	mux := http.NewServeMux()
	static := func(pattern, contentType, body string) {
		mux.HandleFunc("GET "+pattern, func(w http.ResponseWriter, _ *http.Request) { write(w, contentType, body) })
	}
	static("/{$}", htmlType, indexHTML)
	static("/assets/app.js", jsType, appJS)
	static("/assets/app.css", "text/css", appCSS)
	static("/assets/logo.svg", "image/svg+xml", logoSVG)
	static("/api/items", "application/json", itemsJSON)
	static("/sw.js", jsType, receiver.render(serviceWorkerJS))
	mux.HandleFunc("GET /bypass/{id}", func(w http.ResponseWriter, r *http.Request) {
		page, ok := scenarios[r.PathValue("id")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.PathValue("id") == scenarioQUIC {
			// 다른 호스트를 가리키는 대안은 브라우저가 쓰지 않을 수 있어 같은 호스트의 UDP 포트를 광고한다.
			w.Header().Set("Alt-Svc", fmt.Sprintf(`h3=":%s"; ma=%d`, receiver.udpPort, altSvcMaxAge))
		}
		write(w, htmlType, pageHead+receiver.render(page))
	})
	mux.HandleFunc("GET /out/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !redirectHits[r.PathValue("id")] {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, receiver.origin+"/hit/"+r.PathValue("id"), http.StatusFound)
	})
	// 시나리오 밖의 쓰기 요청도 "대상에 쓰기가 닿지 않았다"의 근거가 되도록 전부 남긴다.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			http.NotFound(w, r)
			return
		}
		if rec.record(row{Surface: surfaceTargetWrite, Scenario: unknownScenario, Method: r.Method}) != nil {
			http.Error(w, "record failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("POST /write/{id}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("id")
		if !writeHits[name] {
			name = unknownScenario
		}
		if rec.record(row{Surface: surfaceTargetWrite, Scenario: name, Method: r.Method}) != nil {
			http.Error(w, "record failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// 범위 밖 수신기는 닿은 요청을 전부 실패로 볼 수 있게 경로와 무관하게 모두 기록한다.
func receiverHandler(rec *recorder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := unknownScenario
		if id, ok := strings.CutPrefix(r.URL.Path, "/hit/"); ok && receiverHits[id] {
			name = id
		}
		v := row{Surface: surfaceReceiver, Scenario: name, Method: r.Method, Upgrade: r.Header.Get("Upgrade") != ""}
		if rec.record(v) != nil {
			http.Error(w, "record failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusNoContent)
	})
}

// 첫 바이트와 STUN 고정값으로 시나리오만 구분한다. 내용은 기록하지 않는다.
func classifyUDP(packet []byte) string {
	switch {
	case len(packet) >= stunCookieOffset+len(stunCookie) && bytes.Equal(packet[stunCookieOffset:stunCookieOffset+len(stunCookie)], stunCookie):
		return scenarioSTUN
	case len(packet) > 0 && packet[0]&quicLongHeaderBit != 0:
		return scenarioQUIC
	}
	return unknownScenario
}

func serveUDP(ctx context.Context, conn net.PacketConn, rec *recorder) error {
	buffer := make([]byte, udpBufferBytes)
	for {
		n, _, err := conn.ReadFrom(buffer)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if err := rec.record(row{Surface: surfaceReceiverUDP, Scenario: classifyUDP(buffer[:n])}); err != nil {
			return err
		}
	}
}

type config struct {
	listen, receiverListen, certFile, keyFile string
	receiver                                  receiverInfo
	// 미리 연 리스너가 있으면 주소 대신 쓴다. 시험이 빈 포트를 고른 뒤 다시 여는 사이의 경합을 없앤다.
	targetListener, receiverListener net.Listener
}

func listenOn(l net.Listener, addr string) (net.Listener, error) {
	if l != nil {
		return l, nil
	}
	return net.Listen("tcp", addr)
}

func serve(ctx context.Context, cfg config, rec *recorder, udp net.PacketConn) error {
	targetListener, err := listenOn(cfg.targetListener, cfg.listen)
	if err != nil {
		return errors.Join(err, udp.Close(), rec.close())
	}
	receiverListener, err := listenOn(cfg.receiverListener, cfg.receiverListen)
	if err != nil {
		return errors.Join(err, targetListener.Close(), udp.Close(), rec.close())
	}
	target := &http.Server{Addr: cfg.listen, Handler: targetHandler(rec, cfg.receiver), ReadHeaderTimeout: readHeaderTimeout}
	outside := &http.Server{Addr: cfg.receiverListen, Handler: receiverHandler(rec), ReadHeaderTimeout: readHeaderTimeout,
		ConnState: func(_ net.Conn, state http.ConnState) {
			// HTTP 요청 전에 끊긴 연결도 도달이다. 실패는 recorder가 들고 있다가 실행 실패로 돌려준다.
			if state == http.StateNew && rec.record(row{Surface: surfaceReceiverTCP, Scenario: unknownScenario}) != nil {
				fmt.Fprintln(os.Stderr, "browser-fixture: connection record failed")
			}
		}}
	listen := func(server *http.Server, l net.Listener) error {
		if cfg.certFile != "" {
			return server.ServeTLS(l, cfg.certFile, cfg.keyFile)
		}
		return server.Serve(l)
	}
	failures := make(chan error, 2)
	go func() { failures <- listen(target, targetListener) }()
	go func() { failures <- listen(outside, receiverListener) }()
	udpDone := make(chan error, 1)
	go func() { udpDone <- serveUDP(ctx, udp, rec) }()
	var first error
	udpFinished := false
	select {
	case <-ctx.Done():
	case <-rec.failed:
	case first = <-failures:
	case first = <-udpDone:
		udpFinished = true
	}
	shutdown, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	results := []error{first, target.Shutdown(shutdown), outside.Shutdown(shutdown), udp.Close()}
	if !udpFinished {
		results = append(results, <-udpDone)
	}
	return errors.Join(append(results, rec.close())...)
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("browser-fixture", flag.ContinueOnError)
	listen := flags.String("listen", "", "target address")
	receiverListen := flags.String("receiver-listen", "", "out-of-scope HTTP receiver address")
	receiverUDP := flags.String("receiver-udp", "", "UDP address that accepts traffic for both the target and receiver host names, for example 0.0.0.0:PORT")
	receiverOrigin := flags.String("receiver-origin", "", "origin the pages use to reach the receiver")
	logPath := flags.String("log", "", "new JSONL file for reached requests")
	certFile := flags.String("tls-cert", "", "TLS certificate for the target and the receiver")
	keyFile := flags.String("tls-key", "", "TLS key for the target and the receiver")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *listen == "" || *receiverListen == "" || *receiverUDP == "" || *receiverOrigin == "" || *logPath == "" || (*certFile == "") != (*keyFile == "") {
		return errors.New("-listen, -receiver-listen, -receiver-udp, -receiver-origin and -log are required; -tls-cert and -tls-key go together")
	}
	udp, err := net.ListenPacket("udp", *receiverUDP)
	if err != nil {
		return err
	}
	// 임시 포트(:0)를 써도 페이지가 실제 포트를 광고하도록 연 뒤의 주소를 쓴다.
	info, err := parseReceiver(*receiverOrigin, udp.LocalAddr().String(), *certFile != "")
	if err != nil {
		return errors.Join(err, udp.Close())
	}
	rec, err := newRecorder(*logPath)
	if err != nil {
		return errors.Join(err, udp.Close())
	}
	return serve(ctx, config{listen: *listen, receiverListen: *receiverListen, certFile: *certFile, keyFile: *keyFile, receiver: info}, rec, udp)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx, os.Args[1:])
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser-fixture:", err)
		os.Exit(1)
	}
}
