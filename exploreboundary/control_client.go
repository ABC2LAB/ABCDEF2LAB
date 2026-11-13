package exploreboundary

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// ControlClient는 엔진 서버 쪽 제어 소켓 클라이언트다. 요청마다 연결을 새로 열고 응답 하나만 읽는다.
type ControlClient struct {
	Path    string
	Timeout time.Duration
}

func (c ControlClient) call(ctx context.Context, in ControlRequest) (ControlResponse, error) {
	if ctx == nil || c.Path == "" || c.Timeout <= 0 {
		return ControlResponse{}, ErrConfig
	}
	callCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(callCtx, "unix", c.Path)
	if err != nil {
		return ControlResponse{}, ErrUnavailable
	}
	defer conn.Close()
	if deadline, ok := callCtx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err = json.NewEncoder(conn).Encode(in); err != nil {
		return ControlResponse{}, ErrUnavailable
	}
	line, err := readLine(bufio.NewReaderSize(conn, 1<<16))
	if err != nil {
		return ControlResponse{}, ErrUnavailable
	}
	var out ControlResponse
	if strictUnmarshal(line, &out) != nil {
		return ControlResponse{}, ErrUnavailable
	}
	if !out.OK {
		if out.Error == ControlErrTargetEnded {
			return out, ErrTargetEnded
		}
		return out, ErrUnavailable
	}
	return out, nil
}

// ControlBoundary는 제어 소켓 너머의 경계를 exploration.Boundary로 쓴다. 원본 SourceDigest와 승인 대상 읽기는
// 서버 조립이 결속한다. Target은 서버가 보호된 대상 등록 저장소에서 같은 ref·revision을 읽는 함수다.
type ControlBoundary struct {
	Client       ControlClient
	SourceDigest string
	Target       func(ctx context.Context, ref string, revision uint64) (Target, error)
}

func (b ControlBoundary) Open(ctx context.Context, binding exploration.Binding) (exploration.Lease, error) {
	if b.Target == nil {
		return nil, ErrConfig
	}
	target, err := b.Target(ctx, binding.TargetRef, binding.TargetRevision)
	if err != nil {
		return nil, err
	}
	wire := WireTargetOf(target)
	out, err := b.Client.call(ctx, ControlRequest{Op: OpOpen, Binding: &binding, SourceDigest: b.SourceDigest, Target: &wire})
	if err != nil {
		// 서버의 명시적 거절은 열리지 않은 것이다. 전송·응답 오류는 open 도착 여부가 불명이다.
		switch out.Error {
		case ControlErrBusy, ControlErrInvalid, ControlErrUnavailable, ControlErrTargetEnded:
			return nil, err
		}
		return controlLease{client: b.Client, binding: binding}, err
	}
	if out.State == nil || out.State.Validate(binding) != nil || !out.State.Active {
		// 열렸는지 확신할 수 없으면 lease를 돌려줘 호출자가 마감을 시도하게 한다.
		return controlLease{client: b.Client, binding: binding}, exploration.ErrBinding
	}
	return controlLease{client: b.Client, binding: binding}, nil
}

type controlLease struct {
	client  ControlClient
	binding exploration.Binding
}

func (l controlLease) state(ctx context.Context, in ControlRequest) (exploration.State, error) {
	out, err := l.client.call(ctx, in)
	if err != nil {
		return exploration.State{}, err
	}
	if out.State == nil || out.State.Binding != l.binding {
		return exploration.State{}, exploration.ErrBinding
	}
	return *out.State, nil
}

func (l controlLease) Status(ctx context.Context) (exploration.State, error) {
	return l.state(ctx, ControlRequest{Op: OpStatus, SessionRef: l.binding.SessionRef})
}

func (l controlLease) Close(ctx context.Context, reason exploration.Reason) (exploration.State, error) {
	return l.state(ctx, ControlRequest{Op: OpClose, SessionRef: l.binding.SessionRef, Reason: reason})
}

// ControlReader는 제어 소켓의 private 관찰 채널이다. 같은 결속으로만 읽는다.
type ControlReader struct {
	Client  ControlClient
	Binding exploration.Binding
	Flow    diagnosisflow.ExplorationBinding
}

// Status는 이 결속 세션의 경계 상태다(정적 fetch가 막혔을 때 세션이 닫혔는지 가른다).
func (r ControlReader) Status(ctx context.Context) (exploration.State, error) {
	return controlLease{client: r.Client, binding: r.Binding}.Status(ctx)
}

// maxRecordPages는 기록을 읽을 때 쪽 수 상한이다. 세션 기록은 요청 수·차단 행 상한으로 묶여 있어 이보다 훨씬 적다.
const maxRecordPages = 1 << 16

// Records는 이 결속 세션의 경계 기록 전체와 상한으로 생략한 차단 행 수다. 쪽으로 나눠 읽는다(보호 저장소로 옮길 때·
// 수용시험의 차단 사유 대조). 세션을 닫은 뒤에도 다음 세션을 열기 전까지 읽을 수 있다.
func (r ControlReader) Records(ctx context.Context) ([]Record, uint64, error) {
	var out []Record
	var offset uint64
	for page := 0; page < maxRecordPages; page++ {
		resp, err := r.Client.call(ctx, ControlRequest{Op: OpRecords, SessionRef: r.Binding.SessionRef, Offset: offset})
		if err != nil {
			return nil, 0, err
		}
		out = append(out, resp.Records...)
		if resp.Next == 0 {
			return out, resp.Omitted, nil
		}
		// 쪽 위치는 앞으로만 간다. 뒤로 가거나 제자리면 경계 응답이 어긋난 것이다.
		if resp.Next <= offset {
			return nil, 0, ErrUnavailable
		}
		offset = resp.Next
	}
	return nil, 0, ErrUnavailable
}

func (r ControlReader) Session(ctx context.Context) (diagnosisflow.ExplorationBinding, error) {
	st, err := controlLease{client: r.Client, binding: r.Binding}.Status(ctx)
	if err != nil || !st.Active {
		return diagnosisflow.ExplorationBinding{}, ErrUnavailable
	}
	return r.Flow, nil
}

func (r ControlReader) ReadObservation(ctx context.Context, b diagnosisflow.ExplorationBinding, seq uint64) (diagnosisflow.ExplorationObservation, error) {
	if b != r.Flow {
		return diagnosisflow.ExplorationObservation{}, ErrConfig
	}
	out, err := r.Client.call(ctx, ControlRequest{Op: OpObservation, Flow: &b, Seq: seq})
	if err != nil || out.Observation == nil || out.Observation.Binding != b || out.Observation.Seq != seq {
		return diagnosisflow.ExplorationObservation{}, ErrUnavailable
	}
	o := out.Observation.ExplorationObservation
	o.ResponseContentType = out.Observation.ResponseContentType
	if out.Observation.ResponseSHA256 != "" {
		raw, err := hex.DecodeString(out.Observation.ResponseSHA256)
		if err != nil || len(raw) != len(o.ResponseSHA256) {
			return diagnosisflow.ExplorationObservation{}, ErrUnavailable
		}
		copy(o.ResponseSHA256[:], raw)
	}
	return o, nil
}

func (r ControlReader) Sequences(ctx context.Context, b diagnosisflow.ExplorationBinding) ([]uint64, error) {
	if b != r.Flow {
		return nil, ErrConfig
	}
	out, err := r.Client.call(ctx, ControlRequest{Op: OpSequences, Flow: &b})
	if err != nil {
		return nil, err
	}
	return out.Seqs, nil
}
