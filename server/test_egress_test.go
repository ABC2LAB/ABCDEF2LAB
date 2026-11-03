package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

var errTestHTTPDestination = errors.New("server test HTTP is restricted to loopback")

var testHTTPObservers struct {
	sync.RWMutex
	byAddress map[string]func(context.Context)
}

func installTestHTTPBoundary() {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dial := transport.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	transport.DialContext = testLoopbackDialer(dial)
	http.DefaultTransport = transport
}

func testLoopbackDialer(dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errTestHTTPDestination
		}
		if strings.EqualFold(host, "localhost") {
			host = "127.0.0.1"
			if network == "tcp6" {
				host = "::1"
			}
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || ip.Zone() != "" || !ip.Unmap().IsLoopback() {
			return nil, errTestHTTPDestination
		}
		testHTTPObservers.RLock()
		observe := testHTTPObservers.byAddress[address]
		testHTTPObservers.RUnlock()
		if observe != nil {
			observe(ctx)
		}
		return dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
	}
}

// observeTestHTTPDial observes the actual provider request context for a unique
// loopback fixture address. It cannot enable a non-loopback destination.
func observeTestHTTPDial(t *testing.T, address string, observe func(context.Context)) {
	t.Helper()
	testHTTPObservers.Lock()
	if testHTTPObservers.byAddress == nil {
		testHTTPObservers.byAddress = make(map[string]func(context.Context))
	}
	if _, exists := testHTTPObservers.byAddress[address]; exists {
		testHTTPObservers.Unlock()
		t.Fatal("duplicate test HTTP observer")
	}
	testHTTPObservers.byAddress[address] = observe
	testHTTPObservers.Unlock()
	t.Cleanup(func() {
		testHTTPObservers.Lock()
		delete(testHTTPObservers.byAddress, address)
		testHTTPObservers.Unlock()
	})
}

func TestHTTPBoundaryRejectsExternalBeforeDial(t *testing.T) {
	calls := 0
	dial := testLoopbackDialer(func(context.Context, string, string) (net.Conn, error) {
		calls++
		return nil, errors.New("unexpected underlying dial")
	})
	for _, address := range []string{"api.anthropic.com:443", "192.0.2.1:80", "[2001:db8::1]:443", "10.0.0.1:80", "invalid"} {
		if _, err := dial(t.Context(), "tcp", address); !errors.Is(err, errTestHTTPDestination) {
			t.Fatalf("non-loopback destination did not fail at the test boundary: %v", err)
		}
	}
	if calls != 0 {
		t.Fatal("non-loopback destination reached the underlying network dialer")
	}
}

func TestHTTPBoundaryAllowsLiteralLoopbackAndPinsLocalhost(t *testing.T) {
	var addresses []string
	stop := errors.New("synthetic dial result")
	dial := testLoopbackDialer(func(_ context.Context, _ string, address string) (net.Conn, error) {
		addresses = append(addresses, address)
		return nil, stop
	})
	for _, address := range []string{"127.0.0.1:80", "[::1]:443", "localhost:8080"} {
		if _, err := dial(t.Context(), "tcp", address); !errors.Is(err, stop) {
			t.Fatalf("loopback did not reach the synthetic dialer: %v", err)
		}
	}
	if len(addresses) != 3 || addresses[2] != "127.0.0.1:8080" {
		t.Fatal("localhost was not pinned without DNS")
	}
}
