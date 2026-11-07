package e2proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPObservationBoundedIndependentProjection(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "Caddy/2.8")
		w.Header().Set("X-Powered-By", "Bearer private-value")
		w.Write([]byte("private-body"))
	}))
	defer target.Close()
	_, c := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 40))
	for i := 0; i < 33; i++ {
		resp, _, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed/private-id?token=private-query"))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	result, err := c.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Observations) != 32 || !result.ObservationsTruncated {
		t.Fatal("observation ceiling lost")
	}
	first := result.Observations[0]
	if first.Endpoint != "/allowed/{resource}" || first.StatusCode != 200 || !first.AuditComplete || len(first.Technologies) != 1 {
		t.Fatalf("wrong projection %+v", first)
	}
	raw, _ := json.Marshal(result)
	for _, secret := range []string{"private-id", "private-query", "private-cookie", "private-body", "private-value"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private value in projection")
		}
	}
	result.Observations[0].Technologies[0].Name = "changed"
	if c.Result().Observations[0].Technologies[0].Name != "Caddy" {
		t.Fatal("snapshot alias")
	}
}
