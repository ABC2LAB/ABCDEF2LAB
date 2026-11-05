//go:build linux

package e2proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type proxyTestPeers struct {
	allow   bool
	checked atomic.Int64
}

func (p *proxyTestPeers) LivePeer(pid int32, uid, gid uint32) bool {
	p.checked.Add(1)
	return p.allow && pid == int32(os.Getpid()) && uid == uint32(os.Getuid()) && gid == uint32(os.Getgid())
}
func proxyTestUnixListener(t *testing.T) *net.UnixListener {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "e2pu-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error("own socket directory cleanup failed")
		}
	})
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "proxy.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(listener.Addr().String(), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener
}

func TestProxyUnixRequiresLivePeerVerifier(t *testing.T) {
	cfg, _ := proxyTestConfig(t)
	_, call, _ := proxyTestCall(t, "http://target.invalid", nil)
	p, err := NewProxy(call, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.ServeUnix(proxyTestUnixListener(t), nil); err == nil {
		t.Fatal("unverified ingress was enabled")
	}
}

func TestProxyUnixUsesActualKernelCredentials(t *testing.T) {
	listener := proxyTestUnixListener(t)
	connected := make(chan *net.UnixConn, 1)
	go func() {
		conn, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
		if err == nil {
			connected <- conn
		} else {
			connected <- nil
		}
	}()
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client := <-connected
	if client == nil {
		t.Fatal("unix connection failed")
	}
	defer client.Close()
	credential, err := proxyPeerCredential(server)
	if err != nil || credential.Pid != int32(os.Getpid()) || credential.Pid <= 0 ||
		credential.Uid != uint32(os.Getuid()) || credential.Gid != uint32(os.Getgid()) {
		t.Fatal("peer identity was not kernel supplied")
	}
}

func TestProxyUnixRefusesUnboundPeerBeforeTargetSend(t *testing.T) {
	for _, name := range []string{"allow_http", "allow_https", "refuse"} {
		t.Run(name, func(t *testing.T) {
			var received atomic.Int64
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); io.WriteString(w, "unix response") }))
			var targetRoots *x509.CertPool
			if name == "allow_https" {
				target.StartTLS()
				targetRoots = x509.NewCertPool()
				targetRoots.AddCert(target.Certificate())
			} else {
				target.Start()
			}
			defer target.Close()
			exec, call, _ := proxyTestCall(t, target.URL, targetRoots)
			cfg, roots := proxyTestConfig(t)
			p, err := NewProxy(call, cfg)
			if err != nil {
				t.Fatal(err)
			}
			listener := proxyTestUnixListener(t)
			peers := &proxyTestPeers{allow: name != "refuse"}
			done := make(chan error, 1)
			go func() { done <- p.ServeUnix(listener, peers) }()
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if err := p.Close(ctx); err != nil {
					t.Error("unix proxy did not drain")
				}
				if err := <-done; err != nil {
					t.Error("unix proxy accept failed")
				}
			})
			proxyURL, _ := url.Parse("http://per-call.invalid")
			protocols := new(http.Protocols)
			protocols.SetHTTP1(true)
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true,
				TLSClientConfig: proxyTestTLSConfig(roots), Protocols: protocols,
				DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", listener.Addr().String())
				}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			response, err := client.Get(target.URL + "/allowed")
			if name == "refuse" {
				if err == nil {
					response.Body.Close()
					t.Fatal("unbound peer reached HTTP handling")
				}
				if received.Load() != 0 || exec.Snapshot().Counts.Attempts != 0 {
					t.Fatal("unbound peer was assigned to a call")
				}
			} else {
				if err != nil {
					t.Fatal("verified unix request failed")
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != 200 || string(body) != "unix response" || received.Load() != 1 {
					t.Fatal("verified unix request failed positive delivery")
				}
			}
			if peers.checked.Load() == 0 {
				t.Fatal("live peer verification was skipped")
			}
		})
	}
}

func proxyTestTLSConfig(roots *x509.CertPool) *tls.Config {
	return &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
}
