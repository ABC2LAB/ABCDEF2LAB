package main

import (
	"github.com/Autumn-27/artex/e2export"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/server"
	"net/netip"
	"time"
)

// Validate the cross-package runtime/encoder bounds at installation, before any model
// completion or task authority. The synthetic rule is data only; this performs no DNS or I/O.
func validProductPolicyTemplate(p server.E2HTTPFilePolicy) bool {
	if p.Limits.RequestTimeoutMS > 60000 {
		return false
	}
	cfg := e2proxy.Config{ApprovalMode: p.ApprovalMode, HeaderMode: p.HeaderMode, Rules: []e2proxy.Rule{{Ref: "template", Origin: "https://template.invalid", ConnectIP: netip.MustParseAddr("192.0.2.1"), PathPrefix: "/", PathScopeRef: "template", Methods: []string{"GET"}}}, Limits: e2proxy.Limits{MaxAttempts: p.Limits.MaxAttempts, MaxCalls: p.Limits.MaxCalls, MaxConcurrent: p.Limits.MaxConcurrent, MaxBodyBytes: p.Limits.MaxBodyBytes, RequestTimeout: time.Duration(p.Limits.RequestTimeoutMS) * time.Millisecond, CallTimeout: time.Duration(p.Limits.CallTimeoutMS) * time.Millisecond, MaxDenials: p.Limits.MaxDenials, MaxTransient: p.Limits.MaxTransient, MaxUnknown: p.Limits.MaxUnknown}}
	_, err := e2export.PolicyFingerprint(cfg, p.MaxRequests, e2export.PolicyBehavior{MaxWorkerTurns: p.Runtime.MaxWorkerTurns, MaxToolCalls: p.Runtime.MaxToolCalls, WorkerTimeout: time.Duration(p.Runtime.WorkerTimeoutMS) * time.Millisecond, MaxPlannerRounds: p.Runtime.MaxPlannerRounds, MaxIntentRuns: p.Runtime.MaxIntentRuns, DrainTimeout: time.Duration(p.Runtime.DrainTimeoutMS) * time.Millisecond, OutputLimit: p.Runtime.OutputLimit, ProxyHeaderTimeout: time.Duration(p.Proxy.HeaderTimeoutMS) * time.Millisecond, ProxyRequestTimeout: time.Duration(p.Proxy.RequestTimeoutMS) * time.Millisecond, ProxyMaxHeaderBytes: p.Proxy.MaxHeaderBytes, ProxyMaxConnections: p.Proxy.MaxConnections})
	return err == nil
}
