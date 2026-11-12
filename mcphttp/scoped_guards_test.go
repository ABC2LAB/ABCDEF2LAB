package mcphttp

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestScopedMCPRejectsLargeControlResponsesWithKnownAndStreamingSize(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(strconv.FormatBool(streaming), func(t *testing.T) {
			raw := []byte(`{"jsonrpc":"2.0","id":1,"result":{"padding":"` + strings.Repeat("x", int(maxScopedMCPResponseBytes)) + `"}}`)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					w.WriteHeader(204)
					return
				}
				var request rpcRequest
				_ = json.NewDecoder(r.Body).Decode(&request)
				if request.Method == "notifications/initialized" {
					w.WriteHeader(202)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if streaming {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				} else {
					w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
				}
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if client, err := NewScoped(ctx, "owned", server.URL+"/mcp"); err == nil || client != nil {
				t.Fatal("oversized valid control response accepted")
			}
		})
	}
}
func TestScopedMCPDoesNotUseEnvironmentProxyForOwnedNonLoopbackReceiver(t *testing.T) {
	if os.Getenv("MCP_SCOPED_PROXY_CHILD") == "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		client, err := NewScoped(ctx, "owned", os.Getenv("MCP_SCOPED_OWNED_URL"))
		if err != nil {
			t.Fatal("owned direct control handshake failed")
		}
		if err := client.CloseContext(ctx); err != nil {
			t.Fatal("owned direct control close failed")
		}
		return
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal("owned interface lookup failed")
	}
	var address net.IP
	for _, entry := range addresses {
		network, ok := entry.(*net.IPNet)
		if ok && !network.IP.IsLoopback() && network.IP.To4() != nil && network.IP.IsGlobalUnicast() {
			address = network.IP
			break
		}
	}
	if address == nil {
		t.Skip("owned non-loopback interface unavailable")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(address.String(), "0"))
	if err != nil {
		t.Fatal("owned receiver bind failed")
	}
	var directHits, proxyHits atomic.Int32
	receiver := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directHits.Add(1)
		if r.Method == http.MethodDelete {
			w.WriteHeader(204)
			return
		}
		var request rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Method == "notifications/initialized" {
			w.WriteHeader(202)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{}})
	}))
	receiver.Listener = listener
	receiver.Start()
	defer receiver.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyHits.Add(1); w.WriteHeader(502) }))
	defer proxy.Close()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestScopedMCPDoesNotUseEnvironmentProxyForOwnedNonLoopbackReceiver$")
	// 자식 프로세스는 ProxyFromEnvironment의 전역 캐시가 비어 있어 실제 proxy 변경을 검출한다.
	env := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		switch strings.ToUpper(key) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "MCP_SCOPED_PROXY_CHILD", "MCP_SCOPED_OWNED_URL":
			continue
		}
		env = append(env, value)
	}
	cmd.Env = append(env, "MCP_SCOPED_PROXY_CHILD=1", "MCP_SCOPED_OWNED_URL="+receiver.URL+"/mcp", "HTTP_PROXY="+proxy.URL, "HTTPS_PROXY="+proxy.URL, "ALL_PROXY="+proxy.URL, "NO_PROXY=")
	if err := cmd.Run(); err != nil || directHits.Load() < 2 || proxyHits.Load() != 0 {
		t.Fatal("environment proxy intercepted owned non-loopback control connection")
	}
}
