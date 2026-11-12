// 이 바이너리는 소유 컨테이너 시험 전용이며 제품 탐색 경계를 대체하지 않는다.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/internal/explorefixture"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if run() != nil {
		fmt.Fprintln(os.Stderr, "소유 탐색 계약 fixture 시작 또는 마감 실패")
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 2 {
		return explorefixture.ErrFixture
	}
	raw, e := os.ReadFile(os.Args[1])
	if e != nil {
		return e
	}
	var cfg explorefixture.Config
	if json.Unmarshal(raw, &cfg) != nil {
		return explorefixture.ErrFixture
	}
	f, e := explorefixture.New(cfg)
	if e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	defer f.Close("cancelled")
	type endpoint struct {
		network, address string
		handler          http.Handler
	}
	endpoints := []endpoint{{"tcp", cfg.BrowserListen, f.Proxy("browser")}, {"tcp", cfg.DirectListen, f.Proxy("direct")}, {"unix", cfg.ControlSocket, f.Control()}}
	servers := []*http.Server{}
	fail := make(chan error, len(endpoints))
	for _, p := range endpoints {
		l, e := net.Listen(p.network, p.address)
		if e != nil {
			return e
		}
		s := &http.Server{Handler: p.handler, ReadHeaderTimeout: 5 * time.Second}
		servers = append(servers, s)
		go func() { fail <- s.Serve(l) }()
	}
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()
	select {
	case <-ctx.Done():
		return nil
	case e := <-fail:
		return e
	}
}
