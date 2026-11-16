package diagnosisflow

import (
	"context"
	"github.com/Autumn-27/artex/e2proxy"
	"strings"
	"testing"
	"time"
)

func TestExactPathPlanningAndSendAdmission(t *testing.T) {
	store := &memoryStore{records: map[string]Record{}}
	c, err := New(t.Context(), Binding{JobID: "exact-root", TaskID: "1", RunRef: "owned", SourceDigest: strings.Repeat("a", 64), Owner: "ARTEX", Mode: Auto, TargetURL: "https://owned.test/", PathPrefixes: []string{"=/", "/users/v1"}, ReadonlyPrefixes: []string{"=/", "/users/v1"}, Methods: []string{"GET", "HEAD"}, Limits: Limits{Format: Version, MaxPages: 2, MaxEndpoints: 32, MaxResponseBytes: 4096}, Current: func() error { return nil }}, store)
	if err != nil {
		t.Fatal(err)
	}
	if !c.allowedPath("GET", "/") || c.allowedPath("GET", "/createdb") || !c.allowedPath("GET", "/users/v1/123") || c.allowedPath("GET", "/users/v10") {
		t.Fatal("탐색 후보 범위 불일치")
	}
	for _, tc := range []struct {
		path  string
		allow bool
	}{{"/", true}, {"/createdb", false}, {"/?action=createdb", false}, {"/%2fcreatedb", false}, {"//", false}} {
		c.nativePath = strings.Split(tc.path, "?")[0]
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		err := c.Before(ctx, e2proxy.RequestView{Method: "GET", URL: "https://owned.test" + tc.path})
		cancel()
		if (err == nil) != tc.allow {
			t.Errorf("실제 송신 전 진단 범위 판정 불일치: %s %v", tc.path, err)
		}
	}
}
