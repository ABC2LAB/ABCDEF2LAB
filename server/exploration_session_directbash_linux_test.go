//go:build linux

package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/exploresandbox"
)

// 세션은 typed 직접 Bash(exploresandbox)를 받아 Worker에 Bash 도구로 준다. 실행 자체는 exploresandbox·agent 시험이 본다.
func TestExplorationSessionAcceptsTypedDirectBash(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "calls")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(5), Subject: pkix.Name{CommonName: "session test root"}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	shell, err := exploresandbox.New(exploresandbox.Config{Helper: helper, RootDir: root, DirectProxy: &url.URL{Scheme: "http", Host: "explore-direct:3129"},
		CACert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), Timeout: time.Minute, DrainTimeout: 10 * time.Second, OutputLimit: 4096})
	if err != nil {
		t.Fatal(err)
	}
	o, b, browser, f, p := explorationSessionFixture(t, exploration.BrowserToolNames())
	o.DirectBash = shell
	observed := 0
	o.Observe = func(context.Context, exploration.Binding) error { observed++; return nil }
	// 첫 턴에 Bash를 부른다. helper가 가짜라 실행은 실패하지만, 경계를 지나는 도구처럼 관찰 저장을 거쳐야 한다.
	p.firstTool, p.firstInput = "Bash", `{"command":"true"}`
	s, err := OpenExplorationSession(t.Context(), o)
	if err != nil {
		t.Fatal("typed direct Bash rejected:", err)
	}
	result, err := s.Run(t.Context(), o.Worker, agent.ExplorationRunOptions{TargetURL: "https://owned.test/", Reporter: &explorationFixtureReporter{}, MaxTurns: 5, MaxDuration: time.Second}, nil)
	if err != nil || !slices.Contains(result.Tools, "Bash") || len(result.Tools) != 23 || p.calls.Load() == 0 {
		t.Fatalf("direct Bash not exposed: %v %v", err, result.Tools)
	}
	if observed != 1 {
		t.Fatalf("direct Bash call was not followed by host observation: %d", observed)
	}
	assertExplorationClosed(t, s, b, browser, f, exploration.Completed)
}
