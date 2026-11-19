package main

import (
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/server"
)

func TestProductE3PolicyRulesRequireFrozenDeclarationAndWorkerScope(t *testing.T) {
	registry, _, target, _ := declarationTestTarget(t)
	stored, err := registry.putDeclaration("ARTEX", target.ID, target.Revision, declarationTestInput())
	if err != nil {
		t.Fatal(err)
	}
	rec := productTargetRecord{Target: target, SelectedIP: "10.200.1.10", ReversibleWrites: true, WriteConsent: productWriteConsentAuto}
	policy := server.E2HTTPFilePolicy{Auth: &server.E2HTTPFileAuth{Accounts: []server.E2HTTPFileAuthAccount{{Label: "tester-a"}, {Label: "tester-b"}}},
		Rules: []server.E2HTTPFileRule{{Ref: "worker", Origin: target.Origin, ConnectIP: rec.SelectedIP, TLSSPKISHA256: target.TLSSPKISHA256, PathPrefix: "/app", Methods: []string{"GET", "POST"}}}}
	got, types := productE3PolicyRules(policy, rec, *stored.Declaration)
	if types != 1 || len(got.Rules) != 4 || !got.E3WriteConsent || !got.E3Resettable ||
		got.Rules[1].PathPrefix != "/app/me" || got.Rules[2].PathPrefix != "/app/notes" ||
		got.Rules[3].PathPrefix != "/app/notes/{id}" {
		t.Fatalf("목적 규칙 발급 실패: kinds=%d rules=%d", types, len(got.Rules))
	}
	for name, change := range map[string]func(*server.E2HTTPFilePolicy, *productTargetRecord){
		"write not approved for Worker": func(p *server.E2HTTPFilePolicy, _ *productTargetRecord) { p.Rules[0].Methods = []string{"GET"} },
		"rule ref collision":            func(p *server.E2HTTPFilePolicy, _ *productTargetRecord) { p.Rules[0].Ref = "e3-0-create" },
		"wrong path":                    func(p *server.E2HTTPFilePolicy, _ *productTargetRecord) { p.Rules[0].PathPrefix = "/outside" },
		"missing account":               func(p *server.E2HTTPFilePolicy, _ *productTargetRecord) { p.Auth.Accounts = p.Auth.Accounts[:1] },
		"no write consent":              func(_ *server.E2HTTPFilePolicy, r *productTargetRecord) { r.WriteConsent = "" },
	} {
		t.Run(name, func(t *testing.T) {
			p := policy
			p.Rules = append([]server.E2HTTPFileRule(nil), policy.Rules...)
			auth := *policy.Auth
			auth.Accounts = append([]server.E2HTTPFileAuthAccount(nil), policy.Auth.Accounts...)
			p.Auth = &auth
			r := rec
			change(&p, &r)
			result, count := productE3PolicyRules(p, r, *stored.Declaration)
			if count != 0 || len(result.Rules) != 1 || result.E3WriteConsent || result.E3Resettable {
				t.Fatalf("승인 범위 밖 목적 규칙 발급: count=%d rules=%d", count, len(result.Rules))
			}
		})
	}
}

func TestIssueE3RulesUseCurrentConnectionPin(t *testing.T) {
	registry, _, target, _ := declarationTestTarget(t)
	if _, err := registry.putDeclaration("ARTEX", target.ID, target.Revision, declarationTestInput()); err != nil {
		t.Fatal(err)
	}
	rec, err := registry.lookup("ARTEX", target.Origin+"/app", "")
	if err != nil {
		t.Fatal(err)
	}
	registry.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.20")}, nil
	}
	registry.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		return strings.Repeat("b", 64), time.Now().Add(time.Hour), nil
	}
	loaded := connectionTestProfile(registry, rec)
	loaded.policy.Rules[0].PathPrefix = "/app"
	loaded.policy.Rules[0].Methods = []string{"GET", "POST"}
	loaded.policy.MaxRequests = 100
	loaded.policy.Auth = &server.E2HTTPFileAuth{Accounts: []server.E2HTTPFileAuthAccount{{Label: "tester-a"}, {Label: "tester-b"}}}
	loaded.launch.E2HostSocket = "/run/e2-host/audit.sock"
	loaded.launch.ShutdownTimeoutSeconds = 1
	loaded.profile = urlPreparationProfile{ValidFrom: time.Now().Add(-time.Minute), ValidUntil: time.Now().Add(time.Hour), Budget: diagnosisprep.Budget{DurationSeconds: 400}}
	if err := loaded.prepareTargetConnection(t.Context(), nil, "job-e3-live"); err != nil {
		t.Fatal(err)
	}
	runs := t.TempDir()
	if err := os.Chmod(runs, 0700); err != nil {
		t.Fatal(err)
	}
	runtime := &urlPreparationRuntime{config: urlPreparationInstallation{RunsDir: runs}}
	if _, err := runtime.issue(loaded, "job-e3-live", server.FirstDiagnosisPreparedTask{TaskID: 42, ExplorationID: 43}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(runs, "job-e3-live", "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var issued server.E2HTTPFilePolicy
	if err := json.Unmarshal(raw, &issued); err != nil {
		t.Fatal(err)
	}
	if len(issued.Rules) != 4 || issued.Limits.E3ConfirmationRequests == nil || *issued.Limits.E3ConfirmationRequests != 5 ||
		!issued.E3WriteConsent || !issued.E3Resettable {
		t.Fatalf("확정 규칙 또는 몫 미발급: rules=%d", len(issued.Rules))
	}
	for _, rule := range issued.Rules {
		if rule.ConnectIP != "10.200.1.20" || rule.TLSSPKISHA256 != strings.Repeat("b", 64) {
			t.Fatal("회차 연결 pin과 다른 목적 규칙")
		}
	}
}
