//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/Autumn-27/artex/e2sandbox"
)

// Fixed no-network command exercises the real helper before any preparation/model call.
// No operator authority, target URL, credentials, or model-selected command is accepted.
func runProductPreflight(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 || os.Geteuid() == 0 {
		return 2
	}
	dir, err := os.MkdirTemp("", "product-preflight-")
	if err != nil {
		return 1
	}
	defer os.RemoveAll(dir)
	workspace, ca, socket := filepath.Join(dir, "work"), filepath.Join(dir, "ca.pem"), filepath.Join(dir, "p.sock")
	if os.Mkdir(workspace, 0700) != nil || os.WriteFile(ca, []byte("public-preflight-placeholder"), 0600) != nil {
		return 1
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return 1
	}
	defer listener.Close()
	if os.Chmod(socket, 0600) != nil {
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := e2sandbox.Launch(ctx, e2sandbox.Config{Helper: "/app/e2-bash-sandbox", Workspace: workspace, CACert: ca, ProxySocket: socket, Command: "/usr/bin/true", Timeout: 5 * time.Second, DrainTimeout: time.Second, OutputLimit: 1024})
	if err != nil {
		fmt.Fprintln(stderr, "product_isolation_unavailable")
		return 1
	}
	defer p.Close()
	if p.Start(ctx) != nil {
		fmt.Fprintln(stderr, "product_isolation_unavailable")
		return 1
	}
	r, err := p.Wait(ctx)
	if err != nil || r.ExitCode != 0 || !r.BashExited || !r.Drained {
		fmt.Fprintln(stderr, "product_isolation_unavailable")
		return 1
	}
	fmt.Fprintln(stdout, "product_isolation_ready")
	return 0
}
