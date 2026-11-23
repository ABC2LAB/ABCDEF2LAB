package main

import (
	"context"
	"flag"
	"github.com/Autumn-27/artex/server"
	"io"
	"net"
	"net/http"
	"time"
)

func runProductFront(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("product-front", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	listen := flags.String("listen", ":8787", "product frontend listen")
	origin := flags.String("api-origin", "http://127.0.0.1:28081", "fixed loopback API")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return 2
	}
	handler, err := server.NewProductFrontend(*origin)
	if err != nil {
		return 1
	}
	httpServer := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32768}
	done := make(chan error, 1)
	go func() { done <- httpServer.ListenAndServe() }()
	lost := watchSharedNetwork(ctx, sharedNetworkCheckInterval, net.Interfaces, apiRejoinReady(&http.Client{Timeout: sharedNetworkCheckInterval}, *origin))
	networkLost := false
	select {
	case <-ctx.Done():
	case <-done:
		return 1
	case <-lost:
		networkLost = true
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	shutdownErr := httpServer.Shutdown(cleanup)
	if networkLost {
		return exitSharedNetworkLost
	}
	if shutdownErr != nil {
		return 1
	}
	return 0
}
