package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// newAPIFront forwards the operator's and the runner's engine API calls as
// they are. It exists only because the engine sits on internal networks and so
// cannot publish its own port; it adds nothing, logs nothing and holds no
// secret. Inbound forwarding headers are dropped by the proxy, so a client
// cannot pose as another address to the engine.
func newAPIFront(engine *url.URL) http.Handler {
	return &httputil.ReverseProxy{
		FlushInterval: -1, // the UI's event streams must not be buffered
		ErrorLog:      log.New(io.Discard, "", 0),
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(engine)
			r.Out.Host = r.In.Host
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.WriteHeader(http.StatusBadGateway)
		},
	}
}
