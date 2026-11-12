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
)

// 제어 소켓 연산(계약 §9-4). 줄 하나에 요청 JSON 하나, 응답 JSON 하나다. 소켓은 엔진 서버만 닿는다.
const (
	OpOpen        = "open"
	OpStatus      = "status"
	OpClose       = "close"
	OpSequences   = "sequences"
	OpObservation = "observation"
	OpRecords     = "records"
)

// 제어 오류 어휘다. 원문 오류·경로·대상 값은 소켓으로 내보내지 않는다.
const (
	ControlErrInvalid     = "invalid_request"
	ControlErrBusy        = "session_active"
	ControlErrUnavailable = "unavailable"
	ControlErrTargetEnded = "target_ended"
)

// maxControlLine은 제어 요청·응답 한 줄의 상한이다. 기록 응답도 이 안에 담기도록 MaxDeniedRecords를 설치가 맞춘다.
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
	Omitted     uint64             `json:"omitted,omitempty"`
}

// WireTarget은 Target의 소켓 표현이다.
type WireTarget struct {
	Ref              string    `json:"ref"`
	Revision         uint64    `json:"revision"`
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
	return WireTarget{Ref: t.Ref, Revision: t.Revision, Host: t.Host, Port: t.Port, PathPrefixes: append([]string(nil), t.PathPrefixes...), Methods: append([]string(nil), t.Methods...),
		ConnectIP: t.ConnectIP.String(), SPKISHA256: t.SPKISHA256, ExpiresAt: t.ExpiresAt, RiskyWords: append([]string(nil), t.RiskyWords...), ExcludedPrefixes: append([]string(nil), t.ExcludedPrefixes...)}
}

func (w WireTarget) target() (Target, error) {
	ip, err := netip.ParseAddr(w.ConnectIP)
	if err != nil {
		return Target{}, ErrConfig
	}
	return Target{Ref: w.Ref, Revision: w.Revision, Host: w.Host, Port: w.Port, PathPrefixes: append([]string(nil), w.PathPrefixes...), Methods: append([]string(nil), w.Methods...),
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
	mu        sync.Mutex
	current   *Session
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
		m.mu.Lock()
		if m.current != nil && m.current.Status().Active {
			m.mu.Unlock()
			return fail(ControlErrBusy)
		}
		m.current = s
		m.mu.Unlock()
		go m.watch(s)
		st := s.Status()
		return ControlResponse{OK: true, State: &st}
	case OpStatus:
		s := m.session(in.SessionRef)
		if s == nil {
			return fail(ControlErrInvalid)
		}
		st := s.Status()
		return ControlResponse{OK: true, State: &st}
	case OpClose:
		s := m.session(in.SessionRef)
		if s == nil || !in.Reason.Valid() {
			return fail(ControlErrInvalid)
		}
		wait, cancel := context.WithTimeout(ctx, m.CloseWait)
		defer cancel()
		st, err := s.Close(wait, in.Reason)
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
		records, omitted := s.Records()
		return ControlResponse{OK: true, Records: records, Omitted: omitted}
	}
	return fail(ControlErrInvalid)
}

// watch는 세션이 닫히면 열린 터널을 끊어 새 송신과 남은 연결을 함께 정리한다(§6).
func (m *Manager) watch(s *Session) {
	<-s.Closed()
	for _, p := range m.Proxies {
		p.CloseTunnels()
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
