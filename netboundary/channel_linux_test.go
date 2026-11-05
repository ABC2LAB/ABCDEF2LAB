//go:build linux

package netboundary

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Autumn-27/artex/auditpeer"
	"github.com/Autumn-27/artex/transmit"
)

type testPeerAuthorizer struct {
	calls       atomic.Int32
	releases    atomic.Int32
	deny        bool
	permitOnErr bool
	check       func(transmit.Scope, auditpeer.Credentials) bool
}

func (p *testPeerAuthorizer) AcquirePeer(_ context.Context, scope transmit.Scope, creds auditpeer.Credentials) (Permit, error) {
	p.calls.Add(1)
	if p.permitOnErr {
		return &syntheticPermit{release: func() { p.releases.Add(1) }}, ErrPeer
	}
	if p.deny || p.check == nil || !p.check(scope, creds) {
		return nil, ErrPeer
	}
	return &syntheticPermit{release: func() {}}, nil
}

type serveResult struct {
	out Outcome
	err error
}

type waitingPeerAuthorizer struct{ entered chan struct{} }

type delayedPeerAuthorizer struct {
	entered chan struct{}
	release chan struct{}
}

func (p *waitingPeerAuthorizer) AcquirePeer(ctx context.Context, _ transmit.Scope, _ auditpeer.Credentials) (Permit, error) {
	close(p.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *delayedPeerAuthorizer) AcquirePeer(_ context.Context, _ transmit.Scope, _ auditpeer.Credentials) (Permit, error) {
	close(p.entered)
	<-p.release
	return nil, ErrPeer
}

func socketRequest(t *testing.T, b *Boundary, peer PeerAuthorizer, payload string) ([]byte, serveResult) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registered.sock")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	result := make(chan serveResult, 1)
	go func() {
		conn, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			result <- serveResult{err: acceptErr}
			return
		}
		out, serveErr := b.ServePacketConn(conn, peer)
		result <- serveResult{out: out, err: serveErr}
	}()
	client, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err = client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	ready := make([]byte, 64)
	readyN, err := client.Read(ready)
	if err != nil || !strings.HasPrefix(string(ready[:readyN]), "E2NB/1 ") || readyN != 39 {
		t.Fatalf("channel not ready: %q, %v", ready[:readyN], err)
	}
	nonce := string(ready[7:readyN])
	if !strings.Contains(payload, `"nonce":`) {
		payload = strings.TrimSuffix(payload, "}") + `,"nonce":"` + nonce + `"}`
	}
	if _, err = client.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 1024)
	n, _ := client.Read(response)
	select {
	case served := <-result:
		return response[:n], served
	case <-time.After(6 * time.Second):
		t.Fatal("ServePacketConn did not return")
		return nil, serveResult{}
	}
}

func sameProcessPeer(scope transmit.Scope, creds auditpeer.Credentials) bool {
	return scope.RunRef == "run:1" && creds.PID == int32(os.Getpid()) &&
		creds.UID == uint32(os.Getuid()) && creds.GID == uint32(os.Getgid())
}

func TestPacketChannelRegisteredReadAndFixedAck(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/read?fixed=1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		_, _ = w.Write([]byte(`{"private":"response-value"}`))
	}))
	defer target.Close()
	recorder := &syntheticRecorder{}
	b, _ := setup(t, target.URL, nil, 2, recorder, newAuthority(), true)
	peer := &testPeerAuthorizer{check: sameProcessPeer}
	ack, served := socketRequest(t, b, peer, `{"version":1,"account_ref":"account:1","resource_ref":"item:1"}`)
	if served.err != nil || served.out.State != ResponseObserved || received.Load() != 1 || peer.calls.Load() != 1 ||
		recorder.count("intent") != 1 || recorder.count("result") != 1 {
		t.Fatalf("serve=%+v received=%d peer=%d", served, received.Load(), peer.calls.Load())
	}
	var decoded ChannelAck
	if err := json.Unmarshal(ack, &decoded); err != nil || decoded.Version != 1 || decoded.State != ResponseObserved ||
		decoded.Reason != "" || !decoded.Admitted || !decoded.ResponseReceived {
		t.Fatalf("bad ACK: %q, %+v, %v", ack, decoded, err)
	}
	if string(ack) == "" || containsAny(string(ack), "response-value", "private", target.URL, "session:1") {
		t.Fatalf("ACK exposed response or protected binding: %q", ack)
	}
}

func TestPacketChannelRejectsUntrustedAndMalformedPackets(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	recorder := &syntheticRecorder{}
	b, _ := setup(t, target.URL, nil, 2, recorder, newAuthority(), true)
	for _, payload := range []string{
		`{"version":1,"account_ref":"account:1","resource_ref":"item:1","url":"http://other"}`,
		`{"version":1,"account_ref":"account:1","account_ref":"account:1","resource_ref":"item:1"}`,
		`{"version":1,"account_ref":"account:1","resource_ref":"item:1","method":"POST"}`,
		`{"version":2,"account_ref":"account:1","resource_ref":"item:1"}`,
		`{"version":1,"account_ref":"account:1","resource_ref":"item:1","nonce":"wrong"}`,
	} {
		peer := &testPeerAuthorizer{check: sameProcessPeer}
		ack, served := socketRequest(t, b, peer, payload)
		if !errors.Is(served.err, ErrChannel) || len(ack) != 0 || peer.calls.Load() != 0 {
			t.Fatalf("malformed packet accepted: %q %+v peer=%d", payload, served, peer.calls.Load())
		}
	}
	peer := &testPeerAuthorizer{deny: true, check: sameProcessPeer}
	ack, served := socketRequest(t, b, peer, `{"version":1,"account_ref":"account:1","resource_ref":"item:1"}`)
	if !errors.Is(served.err, ErrPeer) || len(ack) != 0 || peer.calls.Load() != 1 || received.Load() != 0 || recorder.count("intent") != 0 {
		t.Fatalf("denied peer sent: %+v received=%d", served, received.Load())
	}
}

func TestPacketChannelPeerPermitErrorReleasesPermit(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	b, _ := setup(t, target.URL, nil, 2, &syntheticRecorder{}, newAuthority(), true)
	peer := &testPeerAuthorizer{permitOnErr: true, check: sameProcessPeer}
	ack, served := socketRequest(t, b, peer, `{"version":1,"account_ref":"account:1","resource_ref":"item:1"}`)
	if !errors.Is(served.err, ErrPeer) || len(ack) != 0 || peer.calls.Load() != 1 || peer.releases.Load() != 1 || received.Load() != 0 {
		t.Fatalf("peer permit was not released on error: %+v received=%d calls=%d releases=%d", served, received.Load(), peer.calls.Load(), peer.releases.Load())
	}
}

func TestPacketChannelStopClosesIdleConnection(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) }))
	defer target.Close()
	b, _ := setup(t, target.URL, nil, 1, &syntheticRecorder{}, newAuthority(), true)
	path := filepath.Join(t.TempDir(), "registered.sock")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() {
		_, serveErr := b.ServePacketConn(server, &testPeerAuthorizer{check: sameProcessPeer})
		served <- serveErr
	}()
	ready := make([]byte, 64)
	if err := client.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	readyN, err := client.Read(ready)
	if err != nil || !strings.HasPrefix(string(ready[:readyN]), "E2NB/1 ") || readyN != 39 {
		t.Fatalf("channel not ready: %q, %v", ready[:readyN], err)
	}
	drained := make(chan struct{})
	go func() { b.StopAndWait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(3 * time.Second):
		t.Fatal("StopAndWait left an idle channel open")
	}
	select {
	case serveErr := <-served:
		if !errors.Is(serveErr, ErrChannel) {
			t.Fatalf("serve error=%v", serveErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ServePacketConn did not drain")
	}
}

func TestPacketChannelStopCancelsPeerAdmission(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) }))
	defer target.Close()
	b, _ := setup(t, target.URL, nil, 1, &syntheticRecorder{}, newAuthority(), true)
	path := filepath.Join(t.TempDir(), "registered.sock")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	peer := &waitingPeerAuthorizer{entered: make(chan struct{})}
	served := make(chan error, 1)
	go func() { _, serveErr := b.ServePacketConn(server, peer); served <- serveErr }()
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	ready := make([]byte, 64)
	n, err := client.Read(ready)
	if err != nil || !strings.HasPrefix(string(ready[:n]), "E2NB/1 ") || n != 39 {
		t.Fatalf("ready=%q err=%v", ready[:n], err)
	}
	if _, err = client.Write([]byte(`{"version":1,"account_ref":"account:1","resource_ref":"item:1","nonce":"` + string(ready[7:n]) + `"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-peer.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("peer admission was not entered")
	}
	drained := make(chan struct{})
	go func() { b.StopAndWait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(3 * time.Second):
		t.Fatal("StopAndWait did not cancel peer admission")
	}
	select {
	case serveErr := <-served:
		if !errors.Is(serveErr, ErrPeer) {
			t.Fatalf("serve error=%v", serveErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ServePacketConn did not drain")
	}
}

func TestPacketChannelStopAndWaitDrainsDelayedPeerAdmission(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) }))
	defer target.Close()
	b, _ := setup(t, target.URL, nil, 1, &syntheticRecorder{}, newAuthority(), true)
	path := filepath.Join(t.TempDir(), "registered.sock")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	peer := &delayedPeerAuthorizer{entered: make(chan struct{}), release: make(chan struct{})}
	served := make(chan error, 1)
	go func() { _, serveErr := b.ServePacketConn(server, peer); served <- serveErr }()
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	ready := make([]byte, 64)
	n, err := client.Read(ready)
	if err != nil || n != 39 {
		t.Fatalf("ready=%q err=%v", ready[:n], err)
	}
	if _, err = client.Write([]byte(`{"version":1,"account_ref":"account:1","resource_ref":"item:1","nonce":"` + string(ready[7:n]) + `"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-peer.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("peer admission was not entered")
	}
	drained := make(chan struct{})
	go func() { b.StopAndWait(); close(drained) }()
	select {
	case <-drained:
		t.Fatal("StopAndWait returned before delayed peer admission completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(peer.release)
	select {
	case <-drained:
	case <-time.After(3 * time.Second):
		t.Fatal("StopAndWait did not drain delayed peer admission")
	}
	if err := <-served; !errors.Is(err, ErrPeer) {
		t.Fatalf("serve error=%v", err)
	}
}

func TestPacketChannelEarlyPacketCannotSend(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	recorder := &syntheticRecorder{}
	b, _ := setup(t, target.URL, nil, 1, recorder, newAuthority(), true)
	path := filepath.Join(t.TempDir(), "registered.sock")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var raw syscall.RawConn
	if raw, err = client.SyscallConn(); err != nil {
		t.Fatal(err)
	}
	if err = raw.Control(func(fd uintptr) {
		err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_PASSCRED, 1)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Write([]byte(`{"version":1,"account_ref":"account:1","resource_ref":"item:1","nonce":"00000000000000000000000000000000"}`)); err != nil {
		t.Fatal(err)
	}
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	peer := &testPeerAuthorizer{check: sameProcessPeer}
	_, serveErr := b.ServePacketConn(server, peer)
	if !errors.Is(serveErr, ErrChannel) || peer.calls.Load() != 0 || received.Load() != 0 || recorder.count("intent") != 0 {
		t.Fatalf("early packet sent: err=%v peer=%d received=%d", serveErr, peer.calls.Load(), received.Load())
	}
}

func containsAny(s string, values ...string) bool {
	for _, value := range values {
		if value != "" && strings.Contains(s, value) {
			return true
		}
	}
	return false
}
