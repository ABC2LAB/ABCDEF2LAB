package llmpolicy

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestLocalHTTPBudgetRejectsRedirectResubmission(t *testing.T) {
	for _, status := range []int{307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var first, second atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/first" {
					first.Add(1)
					w.Header().Set("Location", "/second")
					w.WriteHeader(status)
				} else {
					second.Add(1)
					w.WriteHeader(200)
				}
			}))
			defer srv.Close()
			origin := localHTTPOrigin(t, srv)
			client, err := NewClient(localHTTPPolicy(t, origin), ClientOptions{LookupIP: localHTTPLookup, DisableRedirects: true})
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			localHTTPDialGuard(t, client)
			response, err := client.Post(origin+"/first", "application/json", bytes.NewBufferString("{}"))
			if response != nil {
				response.Body.Close()
			}
			if !errors.Is(err, ErrRequestBlocked) || first.Load() != 1 || second.Load() != 0 {
				t.Fatal("redirect resent model request")
			}
		})
	}
}
