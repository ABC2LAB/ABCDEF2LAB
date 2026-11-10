// 브라우저 수용시험(docs/evaluation/browser-acceptance.md)용 SPA와 범위 밖 수신기다.
// 실제 앱을 흉내 내지 않는다. 페이지가 시나리오별 우회를 시도하고, 수신기와 쓰기 경로에 닿은 것만 기록한다.
// 프로세스 수준 우회(B01~B03, B10, B11)는 이 페이지로 시험하지 않고 실행기 쪽 하네스가 맡는다.
package main

import (
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
	surfaceTargetWrite = "target_write"
	unknownScenario    = "unknown"
	readHeaderTimeout  = 10 * time.Second
	shutdownTimeout    = 5 * time.Second
	udpBufferBytes     = 2048
)

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
	failed chan struct{}
	once   sync.Once
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
	r.seq++
	v.Seq = r.seq
	data, err := json.Marshal(v)
	if err == nil {
		_, err = r.file.Write(append(data, '\n'))
	}
	if err != nil {
		r.once.Do(func() { close(r.failed) })
	}
	return err
}

type receiverInfo struct {
	origin, websocket, stun string
}

func parseReceiver(raw, udpListen string) (receiverInfo, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || !safeHost.MatchString(u.Host) ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return receiverInfo{}, errors.New("receiver origin must be http(s)://host[:port]")
	}
	_, port, err := net.SplitHostPort(udpListen)
	if err != nil {
		return receiverInfo{}, fmt.Errorf("receiver udp address: %w", err)
	}
	origin := u.Scheme + "://" + u.Host
	return receiverInfo{origin, "ws" + strings.TrimPrefix(origin, "http"), net.JoinHostPort(u.Hostname(), port)}, nil
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
		if r.PathValue("id") == altSvcScenario {
			w.Header().Set("Alt-Svc", `h3="`+receiver.stun+`"; ma=60`)
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

// QUIC·STUN 시도는 UDP로 온다. 내용은 보지 않고 도달 사실만 남긴다.
func serveUDP(ctx context.Context, conn net.PacketConn, rec *recorder) error {
	buffer := make([]byte, udpBufferBytes)
	for {
		if _, _, err := conn.ReadFrom(buffer); err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if err := rec.record(row{Surface: surfaceReceiverUDP, Scenario: unknownScenario}); err != nil {
			return err
		}
	}
}

func run() error {
	listen := flag.String("listen", "", "target address")
	receiverListen := flag.String("receiver-listen", "", "out-of-scope HTTP receiver address")
	receiverUDP := flag.String("receiver-udp", "", "out-of-scope UDP receiver address")
	receiverOrigin := flag.String("receiver-origin", "", "origin the pages use to reach the receiver")
	logPath := flag.String("log", "", "new JSONL file for reached requests")
	certFile := flag.String("tls-cert", "", "target TLS certificate")
	keyFile := flag.String("tls-key", "", "target TLS key")
	flag.Parse()
	if *listen == "" || *receiverListen == "" || *receiverUDP == "" || *receiverOrigin == "" || *logPath == "" || (*certFile == "") != (*keyFile == "") {
		return errors.New("-listen, -receiver-listen, -receiver-udp, -receiver-origin and -log are required; -tls-cert and -tls-key go together")
	}
	info, err := parseReceiver(*receiverOrigin, *receiverUDP)
	if err != nil {
		return err
	}
	rec, err := newRecorder(*logPath)
	if err != nil {
		return err
	}
	defer rec.file.Close()
	udp, err := net.ListenPacket("udp", *receiverUDP)
	if err != nil {
		return err
	}
	defer udp.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	target := &http.Server{Addr: *listen, Handler: targetHandler(rec, info), ReadHeaderTimeout: readHeaderTimeout}
	outside := &http.Server{Addr: *receiverListen, Handler: receiverHandler(rec), ReadHeaderTimeout: readHeaderTimeout}
	failures := make(chan error, 3)
	go func() {
		if *certFile != "" {
			failures <- target.ListenAndServeTLS(*certFile, *keyFile)
			return
		}
		failures <- target.ListenAndServe()
	}()
	go func() { failures <- outside.ListenAndServe() }()
	go func() { failures <- serveUDP(ctx, udp, rec) }()
	var result error
	select {
	case <-ctx.Done():
	case <-rec.failed:
		result = errors.New("reach log write failed")
	case result = <-failures:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return errors.Join(result, target.Shutdown(shutdown), outside.Shutdown(shutdown))
}

func main() {
	if err := run(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "browser-fixture:", err)
		os.Exit(1)
	}
}
