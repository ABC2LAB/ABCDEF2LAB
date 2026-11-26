package exploreboundary

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/loginattempt"
)

// 제어 소켓 연산(계약 §9-4). 줄 하나에 요청 JSON 하나, 응답 JSON 하나다. 소켓은 엔진 서버만 닿는다.
const (
	OpOpen        = "open"
	OpStatus      = "status"
	OpClose       = "close"
	OpSequences   = "sequences"
	OpObservation = "observation"
	OpRecords     = "records"
	// OpLoginAttempts는 모델 직접 로그인 회차의 로그인 시도 행(쪽)이다(docs/contracts/model-login-attempts.md §3).
	OpLoginAttempts = "login_attempts"
)

// 제어 오류 어휘다. 원문 오류·경로·대상 값은 소켓으로 내보내지 않는다.
const (
	ControlErrInvalid     = "invalid_request"
	ControlErrBusy        = "session_active"
	ControlErrUnavailable = "unavailable"
	ControlErrTargetEnded = "target_ended"
)

// maxControlLine은 제어 요청·응답 한 줄의 상한이다. 기록 응답은 행 크기를 묶고(record_bounds.go) 쪽으로 나눠 이 안에 담는다.
const maxControlLine = 4 << 20

type ControlRequest struct {
	Op      string               `json:"op"`
	Binding *exploration.Binding `json:"binding,omitempty"`
	// Target은 서버가 보호된 대상 등록 저장소에서 읽은 승인 스냅샷이다. 서버 전용 소켓으로만 받는다.
	// 대상 등록 저널은 쓰는 프로세스가 배타 잠금을 잡고 있어 경계 컨테이너가 직접 열지 않는다.
	// 실행기는 이 소켓에 닿지 못하므로 "실행기는 참조만 넘기고 값을 넘기지 않는다"(§2)는 그대로다.
	Target       *WireTarget                       `json:"target,omitempty"`
	SourceDigest string                            `json:"source_digest,omitempty"`
	SessionRef   string                            `json:"session_ref,omitempty"`
	Reason       exploration.Reason                `json:"reason,omitempty"`
	Flow         *diagnosisflow.ExplorationBinding `json:"flow,omitempty"`
	Seq          uint64                            `json:"seq,omitempty"`
	// Offset은 records의 쪽 시작 위치(기록 행 순서의 0부터)다. 응답의 Next를 다음 요청에 넣는다.
	Offset uint64 `json:"offset,omitempty"`
	// Cause는 close의 세부 원인이다. 서버가 보낼 수 있는 값은 restart_cleanup뿐이다(control_lost는 경계만 쓴다).
	Cause string `json:"cause,omitempty"`
	// Login은 open 때만 싣는 모델 직접 로그인 판정 설정이다. 등록 계정의 아이디·비밀번호가 들어 있어 서버 전용 소켓으로만
	// 받고, 세션 메모리에만 둔다(기록·원자료·로그에 넣지 않음).
	Login *WireLogin `json:"login,omitempty"`
}

// WireLogin은 LoginConfig의 소켓 표현이다.
type WireLogin struct {
	Accounts               []WireLoginAccount `json:"accounts"`
	MaxConsecutiveFailures int                `json:"max_consecutive_failures"`
	MaxContrastProbes      int                `json:"max_contrast_probes"`
	TokenMinBytes          int                `json:"token_min_bytes"`
}

func (WireLogin) String() string { return "exploreboundary.WireLogin[protected]" }

type WireLoginAccount struct {
	Label    string `json:"label"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (WireLoginAccount) String() string { return "exploreboundary.WireLoginAccount[protected]" }

func (w WireLogin) config() LoginConfig {
	cfg := LoginConfig{MaxConsecutiveFailures: w.MaxConsecutiveFailures, MaxContrastProbes: w.MaxContrastProbes, TokenMinBytes: w.TokenMinBytes}
	for _, a := range w.Accounts {
		cfg.Accounts = append(cfg.Accounts, loginattempt.Account{Label: a.Label, Username: a.Username, Password: a.Password})
	}
	return cfg
}

// WireObservation은 private 관찰의 소켓 표현이다. 공개 JSON에서 빠지는 응답 형식·해시도 이 채널로만 넘긴다.
type WireObservation struct {
	diagnosisflow.ExplorationObservation
	ResponseContentType string `json:"response_content_type,omitempty"`
	ResponseSHA256      string `json:"response_sha256,omitempty"`
}

type ControlResponse struct {
	OK          bool               `json:"ok"`
	Error       string             `json:"error,omitempty"`
	State       *exploration.State `json:"state,omitempty"`
	Seqs        []uint64           `json:"seqs,omitempty"`
	Observation *WireObservation   `json:"observation,omitempty"`
	Records     []Record           `json:"records,omitempty"`
	// LoginAttempts는 login_attempts의 쪽이다.
	LoginAttempts []loginattempt.Row `json:"login_attempts,omitempty"`
	Omitted       uint64             `json:"omitted,omitempty"`
	// Next는 남은 기록 행이 있을 때 다음 쪽의 Offset이다. 0이면 끝이다.
	Next uint64 `json:"next,omitempty"`
}

// WireTarget은 Target의 소켓 표현이다.
type WireTarget struct {
	Ref      string `json:"ref"`
	Revision uint64 `json:"revision"`
	// Scheme은 비우면 https다(이전 형식). http 대상만 "http"를 싣는다(C22).
	Scheme           string    `json:"scheme,omitempty"`
	Host             string    `json:"host"`
	Port             uint16    `json:"port"`
	PathPrefixes     []string  `json:"path_prefixes"`
	Methods          []string  `json:"methods"`
	ConnectIP        string    `json:"connect_ip"`
	SPKISHA256       string    `json:"spki_sha256"`
	ExpiresAt        time.Time `json:"expires_at"`
	RiskyWords       []string  `json:"risky_words,omitempty"`
	ExcludedPrefixes []string  `json:"excluded_prefixes,omitempty"`
}

func WireTargetOf(t Target) WireTarget {
	scheme := t.Scheme
	if scheme == "https" {
		scheme = ""
	}
	return WireTarget{Ref: t.Ref, Revision: t.Revision, Scheme: scheme, Host: t.Host, Port: t.Port, PathPrefixes: append([]string(nil), t.PathPrefixes...), Methods: append([]string(nil), t.Methods...),
		ConnectIP: t.ConnectIP.String(), SPKISHA256: t.SPKISHA256, ExpiresAt: t.ExpiresAt, RiskyWords: append([]string(nil), t.RiskyWords...), ExcludedPrefixes: append([]string(nil), t.ExcludedPrefixes...)}
}

func (w WireTarget) target() (Target, error) {
	ip, err := netip.ParseAddr(w.ConnectIP)
	if err != nil {
		return Target{}, ErrConfig
	}
	if w.Scheme != "" && w.Scheme != "http" {
		return Target{}, ErrConfig
	}
	return Target{Ref: w.Ref, Revision: w.Revision, Scheme: w.Scheme, Host: w.Host, Port: w.Port, PathPrefixes: append([]string(nil), w.PathPrefixes...), Methods: append([]string(nil), w.Methods...),
		ConnectIP: ip, SPKISHA256: w.SPKISHA256, ExpiresAt: w.ExpiresAt, RiskyWords: append([]string(nil), w.RiskyWords...), ExcludedPrefixes: append([]string(nil), w.ExcludedPrefixes...)}, nil
}

// snapshotSource는 서버가 넘긴 스냅샷을 그대로 돌려준다. 철회는 서버가 close(target_ended)로 세션을 닫고, 만료는
// 세션이 ExpiresAt으로 스스로 감지한다.
type snapshotSource struct{ target Target }

func (s snapshotSource) Current(_ context.Context, ref string, revision uint64) (Target, error) {
	if ref != s.target.Ref || revision != s.target.Revision {
		return Target{}, ErrConfig
	}
	return s.target, nil
}

// Manager는 경계 프로세스의 세션 소유자다. 한 번에 세션 하나만 연다. 닫힐 때 프록시 터널을 끊는다.
type Manager struct {
	Policy  Policy
	Proxies []*Proxy
	// CloseWait는 마감 때 진행 중 요청을 기다리는 상한이다.
	CloseWait time.Duration
	// Heartbeat는 실행기 상태 조회가 끊겼다고 보는 시간이다. 열린 세션에 open·status가 이만큼 오지 않으면 경계가
	// 세션을 runtime_unavailable(closed_by control_lost)로 닫는다. 0이면 보지 않는다(docs/contracts/exploration-boundary.md
	// 세션 수명). 실행기는 모델 호출과 따로 도는 감시 고루틴에서 1초마다 status를 보내므로, 모델 응답이 오래 걸려도 끊기지 않는다.
	Heartbeat time.Duration
	mu        sync.Mutex
	current   *Session
	// lastSeen은 지금 세션에 실행기의 open·status가 마지막으로 온 때다. current(지금 있는 세션 조회)는 세지 않는다.
	lastSeen time.Time
}

// heartbeatChecks는 Heartbeat 한 번 동안 끊김을 확인하는 횟수다(감지가 Heartbeat보다 최대 1/4 늦다).
const heartbeatChecks = 4

func (m *Manager) seen(s *Session) {
	m.mu.Lock()
	if m.current == s {
		m.lastSeen = s.policy.Now()
	}
	m.mu.Unlock()
}

// controlLost는 s가 지금 세션이고 실행기 조회가 Heartbeat보다 오래 없었는지다.
func (m *Manager) controlLost(s *Session) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Heartbeat > 0 && m.current == s && s.policy.Now().Sub(m.lastSeen) > m.Heartbeat
}

// Current는 프록시가 쓰는 지금 세션이다. 닫힌 세션은 돌려주지 않는다.
func (m *Manager) Current() *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || !m.current.Status().Active {
		return nil
	}
	return m.current
}

func (m *Manager) session(ref string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.binding.SessionRef != ref {
		return nil
	}
	return m.current
}

func fail(code string) ControlResponse { return ControlResponse{Error: code} }

// Handle은 제어 요청 하나를 처리한다. 신뢰는 소켓 배치·peer 검사로 정하고 요청 내용으로 권한을 늘리지 않는다.
func (m *Manager) Handle(ctx context.Context, in ControlRequest) ControlResponse {
	switch in.Op {
	case OpOpen:
		if in.Binding == nil || in.Target == nil {
			return fail(ControlErrInvalid)
		}
		target, err := in.Target.target()
		if err != nil {
			return fail(ControlErrInvalid)
		}
		source := snapshotSource{target: target}
		m.mu.Lock()
		if m.current != nil && m.current.Status().Active {
			m.mu.Unlock()
			return fail(ControlErrBusy)
		}
		m.mu.Unlock()
		s, err := Open(ctx, *in.Binding, in.SourceDigest, source, m.Policy)
		switch {
		case errors.Is(err, ErrTargetEnded):
			return fail(ControlErrTargetEnded)
		case errors.Is(err, ErrConfig):
			return fail(ControlErrInvalid)
		case err != nil:
			return fail(ControlErrUnavailable)
		}
		// 로그인 판정기는 세션을 프록시에 드러내기 전에 붙인다(첫 요청부터 판정).
		if in.Login != nil && s.AttachLogin(in.Login.config()) != nil {
			return fail(ControlErrInvalid)
		}
		m.mu.Lock()
		if m.current != nil && m.current.Status().Active {
			m.mu.Unlock()
			return fail(ControlErrBusy)
		}
		// 세션을 프록시에 드러내기 전에 결속을 원자료에 남긴다. 원자료 디렉터리 하나에는 세션 하나만 남기므로(§4.1)
		// 이미 세션을 받았거나 봉인한 원자료면 여기서 막는다.
		if m.Policy.Evidence != nil {
			if err := m.Policy.Evidence.opened(s.flow, s.policy.Now().UTC()); err != nil {
				m.mu.Unlock()
				return fail(ControlErrUnavailable)
			}
		}
		m.current = s
		m.lastSeen = s.policy.Now()
		m.mu.Unlock()
		go m.watch(s)
		st := s.Status()
		return ControlResponse{OK: true, State: &st}
	case OpCurrent:
		if in.SessionRef != "" || in.Binding != nil || in.Target != nil || in.Flow != nil {
			return fail(ControlErrInvalid)
		}
		s := m.latest()
		if s == nil {
			return ControlResponse{OK: true}
		}
		st := s.Status()
		return ControlResponse{OK: true, State: &st}
	case OpStatus:
		s := m.session(in.SessionRef)
		if s == nil {
			return fail(ControlErrInvalid)
		}
		m.seen(s)
		st := s.Status()
		return ControlResponse{OK: true, State: &st}
	case OpClose:
		s := m.session(in.SessionRef)
		if s == nil || !in.Reason.Valid() || in.Cause != "" && in.Cause != CloseCauseRestartCleanup {
			return fail(ControlErrInvalid)
		}
		wait, cancel := context.WithTimeout(ctx, m.CloseWait)
		defer cancel()
		st, err := s.closeCause(wait, in.Reason, in.Cause)
		if err != nil {
			return fail(ControlErrInvalid)
		}
		return ControlResponse{OK: true, State: &st}
	case OpSequences:
		if in.Flow == nil {
			return fail(ControlErrInvalid)
		}
		s := m.session(in.Flow.SessionRef)
		if s == nil {
			return fail(ControlErrInvalid)
		}
		seqs, err := s.Sequences(*in.Flow)
		if err != nil {
			return fail(ControlErrInvalid)
		}
		return ControlResponse{OK: true, Seqs: seqs}
	case OpObservation:
		if in.Flow == nil {
			return fail(ControlErrInvalid)
		}
		s := m.session(in.Flow.SessionRef)
		if s == nil {
			return fail(ControlErrInvalid)
		}
		o, err := s.Reader().ReadObservation(ctx, *in.Flow, in.Seq)
		if err != nil {
			return fail(ControlErrUnavailable)
		}
		wire := &WireObservation{ExplorationObservation: o, ResponseContentType: o.ResponseContentType}
		if o.ResponseSHA256 != ([32]byte{}) {
			wire.ResponseSHA256 = hex.EncodeToString(o.ResponseSHA256[:])
		}
		return ControlResponse{OK: true, Observation: wire}
	case OpRecords:
		s := m.session(in.SessionRef)
		if s == nil {
			return fail(ControlErrInvalid)
		}
		// 한 쪽은 제어 소켓 한 줄에 담기는 만큼이다. 행 수를 묶어 탐색 요청 수를 줄이지 않고 쪽으로 나눠 읽는다.
		records, omitted, next := s.RecordsPage(in.Offset, recordsPerPage)
		return ControlResponse{OK: true, Records: records, Omitted: omitted, Next: next}
	case OpLoginAttempts:
		s := m.session(in.SessionRef)
		if s == nil {
			return fail(ControlErrInvalid)
		}
		rows, omitted, next := s.LoginAttemptsPage(in.Offset, loginRowsPerPage)
		return ControlResponse{OK: true, LoginAttempts: rows, Omitted: omitted, Next: next}
	}
	return fail(ControlErrInvalid)
}

// Seal은 경계를 내릴 때 원자료를 봉인한다. 입구·터널을 먼저 닫은 뒤 부른다. 열린 세션이 있으면 cancelled로 닫고
// 진행 중 송신을 CloseWait까지 기다린다. 그다음 drain으로 아직 행을 남길 수 있는 입구 처리(송신 ticket 전 본문 읽기,
// 입구를 닫은 뒤 늦게 시작한 핸들러, 터널)가 끝나길 같은 상한까지 기다린다. 세션을 먼저 닫으므로 그동안 새 송신은
// 없고 그 요청들은 차단 행으로 남는다. 그 뒤 행 파일을 flush·fsync하고 표식을 마지막에 쓴다. 원자료를 쓰지 않는
// 설치(제품)는 아무것도 하지 않는다. 봉인 때 끝나지 않은 송신과 drain이 돌려준 남은 처리는 원자료에 행이 없을 수
// 있으므로, 그 합이 표식의 inflight_at_seal이다(0이 아니면 원자료가 불완전하다, #615 리뷰). drain이 nil이면 기다릴
// 입구 처리가 없는 것이다.
func (m *Manager) Seal(by string, drain func(wait time.Duration) int) error {
	ev := m.Policy.Evidence
	if ev == nil {
		return nil
	}
	now := m.Policy.Now
	if now == nil {
		now = time.Now
	}
	m.mu.Lock()
	s := m.current
	m.mu.Unlock()
	if s == nil {
		return ev.seal(0, false, by, now().UTC())
	}
	closedAtSeal := s.Status().Active
	wait, cancel := context.WithTimeout(context.Background(), m.CloseWait)
	_, err := s.Close(wait, exploration.Cancelled)
	cancel()
	if err != nil {
		return err
	}
	pending := 0
	if drain != nil {
		pending = drain(m.CloseWait)
	}
	// 세션 잠금 안에서 진행 중 수를 읽고 봉인해, 그 사이에 끝난 요청의 행이 수와 어긋나지 않게 한다.
	s.mu.Lock()
	defer s.mu.Unlock()
	return ev.seal(s.inflight+pending, closedAtSeal, by, s.policy.Now().UTC())
}

// watch는 세션이 닫히면 열린 터널을 끊어 새 송신과 남은 연결을 함께 정리한다(§6). Heartbeat가 있으면 실행기 상태 조회가
// 끊긴 세션을 닫는다. 실행기(준비 서비스)가 갑자기 죽으면 브라우저에 남은 페이지가 스스로 보내는 요청(타이머·주기
// 조회·자동 새로고침)이 세션 기한까지 대상에 닿을 수 있어서다(2026-11-24 사용자 결정).
func (m *Manager) watch(s *Session) {
	var tick <-chan time.Time
	if m.Heartbeat > 0 {
		ticker := time.NewTicker(m.Heartbeat / heartbeatChecks)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		select {
		case <-s.Closed():
			for _, p := range m.Proxies {
				p.CloseTunnels()
			}
			return
		case <-tick:
			if m.controlLost(s) {
				wait, cancel := context.WithTimeout(context.Background(), m.CloseWait)
				_, _ = s.closeCause(wait, exploration.RuntimeUnavailable, CloseCauseControlLost)
				cancel()
			}
		}
	}
}

// Serve는 제어 리스너에서 연결마다 요청을 처리한다. Allow가 거부한 peer는 아무 응답 없이 끊는다.
func (m *Manager) Serve(ctx context.Context, l net.Listener, allow func(net.Conn) bool) error {
	if l == nil || allow == nil {
		return ErrConfig
	}
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go m.serveConn(ctx, conn, allow)
	}
}

func (m *Manager) serveConn(ctx context.Context, conn net.Conn, allow func(net.Conn) bool) {
	defer conn.Close()
	if !allow(conn) {
		return
	}
	reader := bufio.NewReaderSize(conn, 1<<16)
	encoder := json.NewEncoder(conn)
	for {
		line, err := readLine(reader)
		if err != nil {
			return
		}
		var in ControlRequest
		resp := fail(ControlErrInvalid)
		if strictUnmarshal(line, &in) == nil {
			resp = m.Handle(ctx, in)
		}
		if encoder.Encode(resp) != nil {
			return
		}
	}
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		part, prefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
		if len(out) > maxControlLine {
			return nil, ErrConfig
		}
		if !prefix {
			return out, nil
		}
	}
}

func strictUnmarshal(raw []byte, v any) error {
	if err := strictKeys(raw, v); err != nil {
		return err
	}
	d := json.NewDecoder(bytesReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return ErrConfig
	}
	return nil
}
