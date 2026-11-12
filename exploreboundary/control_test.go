//go:build linux

package exploreboundary

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
)

const controlTimeout = 2 * time.Second

func controlFixture(t *testing.T, allow func(net.Conn) bool) (*Manager, ControlClient, *fakeSource) {
	t.Helper()
	// 유닉스 소켓 경로 길이 상한(108바이트) 때문에 짧은 임시 경로를 쓴다.
	dir, err := os.MkdirTemp("", "ebc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "control.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_, source, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	source.mu.Lock()
	source.target.ExpiresAt = time.Now().Add(time.Hour)
	source.mu.Unlock()
	m := &Manager{Policy: Policy{Resolver: fakeResolver{}, PathPattern: func(p string) string { return p }, MaxDeniedRecords: fixtureDenied}, CloseWait: controlTimeout}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.Serve(ctx, l, allow)
	return m, ControlClient{Path: path, Timeout: controlTimeout}, source
}

func sameUID(conn net.Conn) bool {
	uid, err := PeerUID(conn)
	return err == nil && uid == uint32(os.Getuid())
}

func TestControlSocketOpenStatusCloseAndPrivateReads(t *testing.T) {
	m, client, source := controlFixture(t, sameUID)
	binding := fixtureBinding(exploration.ReadOnly, exploration.FullLocal)
	boundary := ControlBoundary{Client: client, SourceDigest: fixtureDigest, Target: source.Current}
	lease, err := boundary.Open(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := boundary.Open(t.Context(), binding); err == nil {
		t.Fatal("second session opened while one is active")
	}
	s := m.Current()
	if s == nil {
		t.Fatal("proxy cannot see the opened session")
	}
	d, ticket := s.Admit(t.Context(), approved("GET", "/app/items"))
	if !d.Forward {
		t.Fatal(d)
	}
	ticket.Done(Result{Status: 200, ResponseReceived: true, ResponseContentType: "text/html", ResponseSHA256: [32]byte{1, 2, 3}})
	flow := diagnosisflow.ExplorationBinding{SessionRef: binding.SessionRef, RunRef: binding.RunRef, JobID: binding.JobID, TaskID: binding.TaskID, TargetRef: binding.TargetRef, TargetRevision: binding.TargetRevision, SourceDigest: fixtureDigest}
	reader := ControlReader{Client: client, Binding: binding, Flow: flow}
	if got, err := reader.Session(t.Context()); err != nil || got != flow {
		t.Fatal("private session binding", got, err)
	}
	seqs, err := reader.Sequences(t.Context(), flow)
	if err != nil || len(seqs) != 1 {
		t.Fatal("sequences", seqs, err)
	}
	o, err := reader.ReadObservation(t.Context(), flow, seqs[0])
	if err != nil || o.Path != "/app/items" || o.ResponseContentType != "text/html" || o.ResponseSHA256 != ([32]byte{1, 2, 3}) {
		t.Fatalf("observation round trip lost private fields: %+v %v", o, err)
	}
	other := flow
	other.SourceDigest = strings.Repeat("ef", 32)
	if _, err := reader.ReadObservation(t.Context(), other, seqs[0]); err == nil {
		t.Fatal("reader accepted another binding")
	}
	st, err := lease.Status(t.Context())
	if err != nil || !st.Active || st.TargetRequests != 1 {
		t.Fatal("status", st, err)
	}
	st, err = lease.Close(t.Context(), exploration.Completed)
	if err != nil || st.Active || st.ActiveConnections != 0 || st.Validate(binding) != nil || st.TargetRequests != 1 {
		t.Fatal("close was not confirmed", st, err)
	}
	if m.Current() != nil {
		t.Fatal("closed session still served to proxies")
	}
	if _, err := reader.Session(t.Context()); err == nil {
		t.Fatal("reader session stayed open after close")
	}
	// 닫힌 뒤에는 같은 결속으로 다시 열 수 있다(새 회차는 새 session_ref를 쓴다).
	binding.SessionRef = "session-2"
	if _, err := boundary.Open(t.Context(), binding); err != nil {
		t.Fatal("new session after close", err)
	}
}

func TestControlSocketRefusesBadRequestsAndPeers(t *testing.T) {
	_, client, source := controlFixture(t, sameUID)
	raw := func(line string) ControlResponse {
		conn, err := net.Dial("unix", client.Path)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(controlTimeout))
		conn.Write([]byte(line + "\n"))
		out, err := readLine(bufio.NewReader(conn))
		if err != nil {
			t.Fatal(err)
		}
		var resp ControlResponse
		if strictUnmarshal(out, &resp) != nil {
			t.Fatal("response not strict JSON")
		}
		return resp
	}
	for _, line := range []string{`{"op":"nope"}`, `{"op":"open","extra":1}`, `{"op":"status","session_ref":"unknown"}`, `not json`, `{"op":"close","session_ref":"session-1","reason":"bogus"}`} {
		if resp := raw(line); resp.OK || resp.Error != ControlErrInvalid {
			t.Fatalf("%s accepted: %+v", line, resp)
		}
	}
	// 다른 칸이 모두 맞아도 모르는 칸이 있으면 막는다.
	if _, err := (ControlBoundary{Client: client, SourceDigest: fixtureDigest, Target: source.Current}).Open(t.Context(), fixtureBinding(exploration.ReadOnly, exploration.FullLocal)); err != nil {
		t.Fatal(err)
	}
	if resp := raw(`{"op":"status","session_ref":"session-1"}`); !resp.OK {
		t.Fatal("valid status refused", resp)
	}
	if resp := raw(`{"op":"status","session_ref":"session-1","extra":1}`); resp.OK || resp.Error != ControlErrInvalid {
		t.Fatal("unknown control field accepted", resp)
	}
	if resp := raw(`{"op":"close","session_ref":"session-1","reason":"cancelled"}`); !resp.OK {
		t.Fatal("close refused", resp)
	}
	// open에 승인 스냅샷이 없으면 막는다. 소켓 요청의 대상 값은 서버 조립의 Target 함수에서만 온다.
	if resp := raw(`{"op":"open","binding":{"session_ref":"s"}}`); resp.OK || resp.Error != ControlErrInvalid {
		t.Fatal("open without a target snapshot accepted", resp)
	}
	source.set(ErrTargetEnded)
	if _, err := (ControlBoundary{Client: client, SourceDigest: fixtureDigest, Target: source.Current}).Open(t.Context(), fixtureBinding(exploration.ReadOnly, exploration.FullLocal)); !errors.Is(err, ErrTargetEnded) {
		t.Fatal("revoked target was sent to the boundary", err)
	}
	source.set(nil)
	// 허용되지 않은 peer는 응답 없이 끊긴다.
	_, denied, deniedSource := controlFixture(t, func(net.Conn) bool { return false })
	if _, err := (ControlBoundary{Client: denied, SourceDigest: fixtureDigest, Target: deniedSource.Current}).Open(t.Context(), fixtureBinding(exploration.ReadOnly, exploration.FullLocal)); err == nil {
		t.Fatal("unauthorized peer opened a session")
	}
}

func TestPeerUIDReportsCaller(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if _, err := PeerUID(a); err == nil {
		t.Fatal("non-unix connection produced a peer uid")
	}
}

// 서버가 대상 철회를 close(target_ended)로 알리면 그 세션은 다시 열 수 없고 프록시에도 보이지 않는다.
func TestControlTargetEndedFromServerStopsSession(t *testing.T) {
	m, client, source := controlFixture(t, sameUID)
	binding := fixtureBinding(exploration.ReadOnly, exploration.FullLocal)
	lease, err := (ControlBoundary{Client: client, SourceDigest: fixtureDigest, Target: source.Current}).Open(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	s := m.Current()
	st, err := lease.Close(t.Context(), exploration.TargetEnded)
	if err != nil || st.Active || st.ClosedReason != exploration.TargetEnded {
		t.Fatal("server revocation was not applied", st, err)
	}
	if d, _ := s.Admit(t.Context(), approved("GET", "/app")); d.Forward || m.Current() != nil {
		t.Fatal("revoked session kept forwarding")
	}
}
