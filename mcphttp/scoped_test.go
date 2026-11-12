package mcphttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestScopedMCPRejectsRedirectWithoutContactingSecondEndpoint(t *testing.T) {
	var forwarded atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1); w.WriteHeader(200) }))
	defer second.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if client, err := NewScoped(ctx, "owned", first.URL+"/mcp"); err == nil || client != nil || forwarded.Load() != 0 {
		t.Fatal("scoped MCP followed a control redirect")
	}
}
func TestScopedMCPDoesNotUseEnvironmentProxyOrCredentialsAndCloseIsBounded(t *testing.T) {
	var proxyCalls, deleted atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls.Add(1); w.WriteHeader(502) }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	deleting := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("credentials inherited")
		}
		if r.Method == http.MethodDelete {
			deleted.Add(1)
			close(deleting)
			<-release
			return
		}
		var req rpcRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(202)
			return
		}
		w.Header().Set("Mcp-Session-Id", "owned-session")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": protocolVersion}})
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	client, err := NewScoped(ctx, "owned", server.URL+"/mcp")
	if err != nil {
		t.Fatal("scoped initialize failed")
	}
	closeCtx, closeCancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer closeCancel()
	done := make(chan error, 1)
	go func() { done <- client.CloseContext(closeCtx) }()
	select {
	case <-deleting:
	case <-time.After(time.Second):
		t.Fatal("MCP session DELETE not attempted")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled close unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("close ignored cleanup timeout")
	}
	if err := client.CloseContext(t.Context()); err != nil || deleted.Load() != 1 || proxyCalls.Load() != 0 {
		t.Fatal("closed MCP session reused or environment proxy used")
	}
}
func TestScopedMCPRejectsNonControlAddressShapesBeforeIO(t *testing.T) {
	for _, address := range []string{"https://127.0.0.1:9/mcp", "http://127.0.0.1/mcp", "http://u:p@127.0.0.1:9/mcp", "http://127.0.0.1:9/mcp?key=seeded", "http://127.0.0.1:9/other"} {
		if client, err := NewScoped(t.Context(), "owned", address); err == nil || client != nil {
			t.Fatal("invalid control endpoint accepted")
		}
	}
}
