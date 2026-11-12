// 탐색 경계·보존 수용시험(docs/evaluation/browser-acceptance.md)용 SPA와 범위 밖 수신기다.
// 기존 수신기는 승인되지 않은 내부망 역할이고, 메타데이터·외부 CDN 역할 수신기는 선택이다(roles.go).
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

// metadata·cdn은 선택 역할 수신기의 origin이다(roles.go). 비어 있으면 그 역할의 시나리오는 내지 않는다.
type receiverInfo struct {
	origin, websocket, stun, udpPort string
	metadata, metadataWS, cdn        string
}

// 대상이 HTTPS면 수신기도 HTTPS여야 한다. 아니면 혼합 콘텐츠로 막혀서 시도 자체가 생기지 않는다.
func parseOrigin(raw string, tlsEnabled bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !safeHost.MatchString(u.Host) ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("receiver origin must be http(s)://host[:port]")
	}
	if (u.Scheme == "https") != tlsEnabled {
		return "", errors.New("receiver origin scheme must match the TLS setting")
	}
	return u.Scheme + "://" + u.Host, nil
}

func parseReceiver(raw, udpListen string, tlsEnabled bool) (receiverInfo, error) {
	origin, err := parseOrigin(raw, tlsEnabled)
	if err != nil {
		return receiverInfo{}, err
	}
	_, port, err := net.SplitHostPort(udpListen)
	if err != nil {
		return receiverInfo{}, fmt.Errorf("receiver udp address: %w", err)
	}
	u, err := url.Parse(origin)
	if err != nil {
		return receiverInfo{}, err
	}
	return receiverInfo{origin: origin, websocket: wsOrigin(origin), stun: net.JoinHostPort(u.Hostname(), port), udpPort: port}, nil
}

// withSTUNIP는 WebRTC 페이지(B09)의 STUN 서버를 IP 리터럴로 바꾼다. Chromium의 WebRTC는 STUN 서버 이름을
// --host-resolver-rules로 풀지 않아서(실측: 이름이면 UDP 0, 같은 주소의 IP면 STUN binding 도달) 이름을 쓰면 양성
// 대조가 성립하지 않고, 통제 회차의 0도 경로 차단이 아니라 이름 풀이 실패가 된다. IP면 망 경로만으로 도달이 정해진다.
func (i receiverInfo) withSTUNIP(raw string) (receiverInfo, error) {
	ip := net.ParseIP(raw)
	if ip == nil || ip.IsUnspecified() || ip.String() != raw {
		return receiverInfo{}, errors.New("receiver STUN address must be an IP literal")
	}
	i.stun = net.JoinHostPort(raw, i.udpPort)
	return i, nil
}

func wsOrigin(origin string) string { return "ws" + strings.TrimPrefix(origin, "http") }

func (i receiverInfo) render(text string) string {
	return strings.NewReplacer("{{R}}", i.origin, "{{WS}}", i.websocket, "{{STUN}}", i.stun,
		"{{MD}}", i.metadata, "{{MDWS}}", i.metadataWS, "{{CDN}}", i.cdn).Replace(text)
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
		id := r.PathValue("id")
		page, ok := scenarios[id]
		if !ok {
			page, ok = cdnPages[id]
		}
		if !ok || (cdnScenarios[id] && receiver.cdn == "") {
			http.NotFound(w, r)
			return
		}
		if part, has := metadataParts[id]; has && receiver.metadata != "" {
			page += part
		}
		if r.PathValue("id") == scenarioQUIC {
			// 다른 호스트를 가리키는 대안은 브라우저가 쓰지 않을 수 있어 같은 호스트의 UDP 포트를 광고한다.
			w.Header().Set("Alt-Svc", fmt.Sprintf(`h3=":%s"; ma=%d`, receiver.udpPort, altSvcMaxAge))
		}
		write(w, htmlType, pageHead+receiver.render(page))
	})
	mux.HandleFunc("GET /crawl/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		page, ok := crawlScenarios[id]
		if !ok {
			page, ok = cdnCrawlPages[id]
		}
		if !ok {
			page, ok = extraCrawlPages[id]
		}
		if !ok || (cdnScenarios[id] && receiver.cdn == "") {
			http.NotFound(w, r)
			return
		}
		write(w, htmlType, pageHead+receiver.render(page))
	})
	mux.HandleFunc("GET /out/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		switch {
		case redirectHits[id]:
			http.Redirect(w, r, receiver.origin+"/hit/"+id, http.StatusFound)
		case metadataRedirectHits[id] && receiver.metadata != "":
			http.Redirect(w, r, receiver.metadata+"/hit/"+id, http.StatusFound)
		default:
			http.NotFound(w, r)
		}
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
	// 쓰기 시도는 페이지가 쓰는 메서드로 왔을 때만 그 이름으로 남긴다(S13은 PUT·PATCH·DELETE도 시도한다).
	// 다른 메서드(OPTIONS 탐침 등)는 이름으로 세지 않고 unknown으로 남긴다. 이름 밖 POST는 예전처럼 unknown·204다.
	mux.HandleFunc("/write/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			http.NotFound(w, r)
			return
		}
		name, status := r.PathValue("id"), http.StatusNoContent
		if expected, ok := writeHits[name]; !ok || expected != r.Method {
			name = unknownScenario
			if r.Method != http.MethodPost {
				status = http.StatusMethodNotAllowed
			}
		}
		if rec.record(row{Surface: surfaceTargetWrite, Scenario: name, Method: r.Method}) != nil {
			http.Error(w, "record failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(status)
	})
	return mux
}

// 범위 밖 수신기(승인되지 않은 내부망 역할)는 닿은 요청을 전부 실패로 볼 수 있게 경로와 무관하게 모두 기록한다.
func receiverHandler(rec *recorder) http.Handler {
	return roleReceiverHandler(rec, surfaceReceiver, receiverHits)
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
	certFile, keyFile string
	receiver          receiverInfo
	extra             []extraReceiver
}

// HTTP 서버가 이미 닫은 리스너면 ErrClosed가 정상이다.
func closeListener(l net.Listener) {
	if err := l.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		fmt.Fprintln(os.Stderr, "browser-fixture: listener close failed:", err)
	}
}

// serve는 받은 리스너를 어떤 경로로 끝나든 닫는다. TLS 인증서를 못 읽어 Serve에 넘기지 못한 경우도 같다.
func serve(ctx context.Context, cfg config, rec *recorder, targetListener, receiverListener net.Listener, udp net.PacketConn) error {
	defer closeListener(targetListener)
	defer closeListener(receiverListener)
	for _, x := range cfg.extra {
		defer closeListener(x.listener)
	}
	target := &http.Server{Handler: targetHandler(rec, cfg.receiver), ReadHeaderTimeout: readHeaderTimeout}
	outside := receiverServer(rec, receiverHandler(rec), surfaceReceiverTCP)
	extras := make([]*http.Server, 0, len(cfg.extra))
	for _, x := range cfg.extra {
		extras = append(extras, receiverServer(rec, x.handler, x.connSurface))
	}
	listen := func(server *http.Server, l net.Listener) error {
		if cfg.certFile != "" {
			return server.ServeTLS(l, cfg.certFile, cfg.keyFile)
		}
		return server.Serve(l)
	}
	failures := make(chan error, 2+len(extras))
	go func() { failures <- listen(target, targetListener) }()
	go func() { failures <- listen(outside, receiverListener) }()
	for i, server := range extras {
		go func() { failures <- listen(server, cfg.extra[i].listener) }()
	}
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
	for _, server := range extras {
		results = append(results, server.Shutdown(shutdown))
	}
	if !udpFinished {
		results = append(results, <-udpDone)
	}
	return errors.Join(append(results, rec.close())...)
}

// receiverServer는 수신기 하나의 HTTP 서버다. HTTP 요청 전에 끊긴 연결도 도달이라 connSurface로 남기고,
// 기록 실패는 recorder가 들고 있다가 실행 실패로 돌려준다.
func receiverServer(rec *recorder, handler http.Handler, connSurface string) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: readHeaderTimeout,
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew && rec.record(row{Surface: connSurface, Scenario: unknownScenario}) != nil {
				fmt.Fprintln(os.Stderr, "browser-fixture: connection record failed")
			}
		}}
}

// roleFlag는 선택 역할 수신기 하나의 주소와 origin이다. 둘 다 있거나 둘 다 없어야 한다.
type roleFlag struct{ listen, origin *string }

// roleSetting은 켠 선택 역할 하나다. connSurface가 역할을 가린다.
type roleSetting struct {
	flag        roleFlag
	connSurface string
}

func (f roleFlag) set() bool { return *f.listen != "" }

func (f roleFlag) paired() bool { return (*f.listen == "") == (*f.origin == "") }

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("browser-fixture", flag.ContinueOnError)
	listen := flags.String("listen", "", "target address")
	receiverListen := flags.String("receiver-listen", "", "out-of-scope HTTP receiver address")
	receiverUDP := flags.String("receiver-udp", "", "UDP address that accepts traffic for both the target and receiver host names, for example 0.0.0.0:PORT")
	receiverOrigin := flags.String("receiver-origin", "", "origin the pages use to reach the receiver")
	receiverSTUNIP := flags.String("receiver-stun-ip", "", "optional IP literal the WebRTC page uses as its STUN server instead of the receiver host name")
	logPath := flags.String("log", "", "new JSONL file for reached requests")
	certFile := flags.String("tls-cert", "", "TLS certificate for the target and the receiver")
	keyFile := flags.String("tls-key", "", "TLS key for the target and the receiver")
	metadata := roleFlag{flags.String("metadata-listen", "", "optional metadata-role receiver address"),
		flags.String("metadata-origin", "", "origin the pages use to reach the metadata-role receiver")}
	cdn := roleFlag{flags.String("cdn-listen", "", "optional external-CDN-role receiver address"),
		flags.String("cdn-origin", "", "origin the pages use to reach the external-CDN-role receiver")}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *listen == "" || *receiverListen == "" || *receiverUDP == "" || *receiverOrigin == "" || *logPath == "" || (*certFile == "") != (*keyFile == "") {
		return errors.New("-listen, -receiver-listen, -receiver-udp, -receiver-origin and -log are required; -tls-cert and -tls-key go together")
	}
	if !metadata.paired() || !cdn.paired() {
		return errors.New("-metadata-listen/-metadata-origin and -cdn-listen/-cdn-origin go together")
	}
	udp, err := net.ListenPacket("udp", *receiverUDP)
	if err != nil {
		return err
	}
	targetListener, err := net.Listen("tcp", *listen)
	if err != nil {
		return errors.Join(err, udp.Close())
	}
	receiverListener, err := net.Listen("tcp", *receiverListen)
	if err != nil {
		return errors.Join(err, targetListener.Close(), udp.Close())
	}
	announce(targetListener.Addr(), receiverListener.Addr())
	// 임시 포트(:0)를 써도 페이지가 실제 포트를 광고하도록 연 뒤의 주소를 쓴다.
	info, err := parseReceiver(*receiverOrigin, udp.LocalAddr().String(), *certFile != "")
	if err == nil && *receiverSTUNIP != "" {
		info, err = info.withSTUNIP(*receiverSTUNIP)
	}
	var opened []net.Listener
	var roles []roleSetting
	if metadata.set() {
		roles = append(roles, roleSetting{metadata, surfaceConnMetadata})
	}
	if cdn.set() {
		roles = append(roles, roleSetting{cdn, surfaceConnCDN})
	}
	var extraListeners []net.Listener
	for _, role := range roles {
		if err != nil {
			break
		}
		var origin string
		if origin, err = parseOrigin(*role.flag.origin, *certFile != ""); err != nil {
			break
		}
		var l net.Listener
		if l, err = net.Listen("tcp", *role.flag.listen); err != nil {
			break
		}
		opened = append(opened, l)
		extraListeners = append(extraListeners, l)
		if role.connSurface == surfaceConnMetadata {
			info.metadata, info.metadataWS = origin, wsOrigin(origin)
		} else {
			info.cdn = origin
		}
	}
	if err == nil {
		var rec *recorder
		if rec, err = newRecorder(*logPath); err == nil {
			var extra []extraReceiver
			for i, role := range roles {
				handler := roleReceiverHandler(rec, surfaceReceiverMetadata, metadataHits)
				if role.connSurface == surfaceConnCDN {
					handler = cdnHandler(rec)
				}
				extra = append(extra, extraReceiver{listener: extraListeners[i], handler: handler, connSurface: role.connSurface})
			}
			return serve(ctx, config{certFile: *certFile, keyFile: *keyFile, receiver: info, extra: extra}, rec, targetListener, receiverListener, udp)
		}
	}
	closers := []error{err, targetListener.Close(), receiverListener.Close(), udp.Close()}
	for _, l := range opened {
		closers = append(closers, l.Close())
	}
	return errors.Join(closers...)
}

// 시험이 임시 포트로 연 실제 주소를 알 수 있게 한다.
var announce = func(target, receiver net.Addr) {}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx, os.Args[1:])
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser-fixture:", err)
		os.Exit(1)
	}
}
