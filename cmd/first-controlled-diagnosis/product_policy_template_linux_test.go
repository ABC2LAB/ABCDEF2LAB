//go:build linux

package main

import (
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/server"
	"testing"
)

func TestProductWorkflowPolicyUsesActualEncoderBounds(t *testing.T) {
	p := server.E2HTTPFilePolicy{MaxRequests: 64, ApprovalMode: e2proxy.ApprovalWorkbenchV1, HeaderMode: e2proxy.HeaderPreserve, Limits: server.E2HTTPFileLimits{MaxAttempts: 200, MaxCalls: 64, MaxConcurrent: 1, MaxBodyBytes: 2 << 20, RequestTimeoutMS: 5000, CallTimeoutMS: 60000, MaxDenials: 16, MaxTransient: 1, MaxUnknown: 1}, Proxy: server.E2HTTPFileProxy{HeaderTimeoutMS: 1000, RequestTimeoutMS: 60000, MaxHeaderBytes: 32768, MaxConnections: 4}, Runtime: server.E2HTTPFileRuntime{MaxWorkerTurns: 16, MaxToolCalls: 64, WorkerTimeoutMS: 180000, MaxPlannerRounds: 12, MaxIntentRuns: 1, DrainTimeoutMS: 10000, OutputLimit: (2 << 20) + 8192}}
	if !validProductPolicyTemplate(p) {
		t.Fatal("bounded workflow policy rejected by actual E2 encoder")
	}
	p.Proxy.RequestTimeoutMS = 120000
	if validProductPolicyTemplate(p) {
		t.Fatal("proxy timeout beyond file-factory/fingerprint limit accepted")
	}
}
