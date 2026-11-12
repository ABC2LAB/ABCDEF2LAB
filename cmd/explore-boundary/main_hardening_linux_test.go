//go:build linux

package main

// 리뷰(#488) 재현용 바이너리 설정 시험.

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// R10: IPv4-mapped 표기로 0.0.0.0·같은 주소 검사를 피한다.
func TestListenAddrRejectsMappedWildcardAndSharedInterface(t *testing.T) {
	ap, err := listenAddr("[::ffff:0.0.0.0]:18080")
	t.Logf("listenAddr([::ffff:0.0.0.0]:18080) err=%v", err)
	l, lerr := net.Listen("tcp", netip.AddrPortFrom(netip.MustParseAddr("::ffff:0.0.0.0"), 0).String())
	if lerr != nil {
		t.Fatal(lerr)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	c, dialErr := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
	if c != nil {
		c.Close()
	}
	t.Logf("net.Listen(%s) bound %s; dial via 127.0.0.1 err=%v", ap, l.Addr(), dialErr)

	shared := validConfig(t)
	shared.DirectListen = "[::ffff:10.94.241.10]:8081" // 브라우저 입구와 같은 인터페이스 주소
	_, sharedErr := build(shared)

	wildcard := validConfig(t)
	wildcard.BrowserListen = "[::ffff:0.0.0.0]:8080"
	_, wildErr := build(wildcard)
	t.Logf("build shared-interface(mapped) err=%v, build wildcard(mapped) err=%v", sharedErr, wildErr)
	if err == nil && dialErr == nil || sharedErr == nil || wildErr == nil {
		t.Fatal("mapped IPv4 notation bypasses the wildcard/loopback/same-interface listen checks")
	}
}

// R11: engine_uid를 빼면 0(root)으로 받아들인다.
func TestMissingEngineUIDRejected(t *testing.T) {
	c := validConfig(t)
	c.EngineUID = 0 // JSON에 칸이 없을 때와 같다
	p, err := build(c)
	if err == nil && p.uid == 0 {
		t.Fatal("configuration without engine_uid accepted; control socket peer uid defaults to 0")
	}
}

// R12: 입구 http.Server에 IdleTimeout이 없어 평문 keep-alive 연결이 터널 상한 밖에서 끝없이 남는다.
func TestIngressKeepAliveTimesOut(t *testing.T) {
	c := validConfig(t)
	c.TimeoutMS = 200
	p, err := build(c)
	if err != nil {
		t.Fatal(err)
	}
	free := func() netip.AddrPort {
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		defer l.Close()
		return netip.MustParseAddrPort(l.Addr().String())
	}
	p.listens = [2]netip.AddrPort{free(), free()}
	p.socket = filepath.Join(filepath.Dir(c.CACertFile), "c.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.serve(ctx)
	var conn net.Conn
	for i := 0; i < 50 && conn == nil; i++ {
		conn, _ = net.Dial("tcp", p.listens[0].String())
		if conn == nil {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if conn == nil {
		t.Fatal("ingress did not start")
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	for i := 0; i < 2; i++ {
		io.WriteString(conn, "GET http://x.example/ HTTP/1.1\r\nHost: x.example\r\n\r\n")
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Logf("request %d after idle: %v", i, err)
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if i == 0 {
			time.Sleep(15 * 200 * time.Millisecond) // 15 x timeout_ms
		}
	}
	t.Fatal("plaintext keep-alive connection survived 15x timeout_ms idle (no IdleTimeout, not counted in max_tunnels)")
}

// R13: 제어 소켓을 경계 uid 소유 0600으로만 만들고 engine_uid로 chown하지 않는다. engine_uid가 경계 uid와 다르고
// root가 아니면 connect(2)가 쓰기 권한(unix(7))에서 먼저 막혀 SO_PEERCRED 검사까지 가지 못한다.
// 제어 소켓 접속에는 소켓 파일 쓰기 권한이 필요하다. 엔진 uid가 경계와 같으면 0600 그대로, 다르면 엔진 uid 소유로 넘기고,
// 넘길 수 없으면 권한을 넓히지 않고 시작을 멈춘다(#488 리뷰).
func TestControlSocketOwnerForDistinctEngineUID(t *testing.T) {
	serveWith := func(uid uint32) (*parts, chan error, context.CancelFunc) {
		c := validConfig(t)
		c.EngineUID = uid
		p, err := build(c)
		if err != nil {
			t.Fatal(err)
		}
		free := func() netip.AddrPort {
			l, _ := net.Listen("tcp", "127.0.0.1:0")
			defer l.Close()
			return netip.MustParseAddrPort(l.Addr().String())
		}
		p.listens = [2]netip.AddrPort{free(), free()}
		p.socket = filepath.Join(filepath.Dir(c.CACertFile), "c.sock")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- p.serve(ctx) }()
		return p, done, cancel
	}
	self := uint32(os.Getuid())
	p, done, cancel := serveWith(self)
	var st os.FileInfo
	var err error
	for i := 0; i < 50; i++ {
		if st, err = os.Stat(p.socket); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || st.Mode().Perm() != 0o600 || st.Sys().(*syscall.Stat_t).Uid != self {
		t.Fatalf("same-uid socket: err=%v mode=%v", err, st)
	}
	cancel()
	<-done
	if self == 0 {
		t.Skip("root can chown; the distinct-uid refusal path needs a non-root test process")
	}
	_, done, cancel = serveWith(self + 1)
	defer cancel()
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serve kept running with a socket the engine uid cannot open")
	}
	if err == nil || err.Error() != "control_socket_owner" {
		t.Fatalf("distinct engine uid without chown: %v", err)
	}
}
