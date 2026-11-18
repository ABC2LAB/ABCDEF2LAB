//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3fake"
	"github.com/Autumn-27/artex/server"
)

// issueWithDeclaration은 승인된 대상(GET·HEAD, /read)에 선언을 둔 채 회차 하나를 발급한다. declare가 거짓이면 선언 없이 발급한다.
func issueWithDeclaration(t *testing.T, declare bool) (string, string, diagnosisprep.ProductTarget, e3contract.Declaration) {
	t.Helper()
	r, _, target := accountTestTarget(t, true)
	var stored e3contract.Declaration
	if declare {
		in := diagnosisprep.DeclarationInput{Identity: e3contract.IdentitySpec{Path: "/read/me", SubjectIDPath: "id"},
			Types: []e3contract.TypeDeclaration{{Type: "post", Sharing: e3contract.SharingShared}}}
		v, err := r.putDeclaration("ARTEX", target.ID, target.Revision, in)
		if err != nil {
			t.Fatal(err)
		}
		stored = *v.Declaration
	}
	rec, err := r.lookup("ARTEX", target.Origin+"/read", "")
	if err != nil {
		t.Fatal(err)
	}
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.20")}, nil
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		return strings.Repeat("b", 64), time.Now().Add(time.Hour), nil
	}
	l := connectionTestProfile(r, rec)
	now := time.Now()
	l.profile = urlPreparationProfile{ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour), Budget: diagnosisprep.Budget{DurationSeconds: 30}}
	l.launch.ShutdownTimeoutSeconds = 1
	if err = l.prepareTargetConnection(t.Context(), nil, "job-e3"); err != nil {
		t.Fatal(err)
	}
	runs := t.TempDir()
	if os.Chmod(runs, 0700) != nil {
		t.Fatal("fixture permissions")
	}
	b := &urlPreparationRuntime{config: urlPreparationInstallation{RunsDir: runs}}
	if _, err := b.issue(l, "job-e3", server.FirstDiagnosisPreparedTask{TaskID: 42, ExplorationID: 43}); err != nil {
		t.Fatal("발급 실패", err)
	}
	return runs, "job-e3", target, stored
}

func TestIssueFreezesActiveDeclaration(t *testing.T) {
	runs, job, target, stored := issueWithDeclaration(t, true)
	raw, err := os.ReadFile(filepath.Join(runs, job, productFrozenDeclarationFile))
	if err != nil {
		t.Fatal("동결 사본 없음", err)
	}
	policy, ok, err := readFrozenPolicy(runs, job)
	if err != nil || !ok {
		t.Fatalf("동결 정책 출처: %v %v", ok, err)
	}
	sum := sha256.Sum256(raw)
	digest, _ := stored.Digest()
	if hex.EncodeToString(sum[:]) != policy.Policy.DigestSHA256 || policy.Policy.DigestSHA256 != digest ||
		policy.Policy.Kind != e3contract.PolicyProductDeclaration || policy.Policy.Ref != "declaration-"+job || policy.TargetRef != target.ID ||
		policy.Policy.TargetApprovalRevision != targetApprovalRevision(target.Revision) {
		t.Fatalf("동결 정책 출처가 사본과 결속되지 않음: %+v", policy)
	}
	if policy.ValidFrom.Before(target.ApprovedAt) || policy.ValidUntil.After(target.ExpiresAt) || !policy.ValidFrom.Before(policy.ValidUntil) {
		t.Fatalf("유효창이 승인 기간 밖: %+v", policy)
	}
	for _, name := range []string{productFrozenDeclarationFile, productFrozenPolicyFile} {
		info, err := os.Stat(filepath.Join(runs, job, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%s 권한: %v", name, err)
		}
	}
	reader := productDeclarationReader{runsDir: runs}
	got, err := reader.ReadFrozen(t.Context(), policy.Policy)
	if err != nil || e3contract.VerifyFrozen(policy.Policy, got) != nil {
		t.Fatalf("동결 사본 읽기·대조: %v", err)
	}
	// 사본을 손대면 대조에서 진위 실패다.
	tampered := bytes.Replace(raw, []byte("/read/me"), []byte("/read/xx"), 1)
	if err := os.WriteFile(filepath.Join(runs, job, productFrozenDeclarationFile), tampered, 0600); err != nil {
		t.Fatal(err)
	}
	got, err = reader.ReadFrozen(t.Context(), policy.Policy)
	if err != nil || !errors.Is(e3contract.VerifyFrozen(policy.Policy, got), e3contract.ErrPolicyDigestMismatch) {
		t.Fatalf("손댄 사본: %v", err)
	}
}

func TestIssueWithoutDeclarationFreezesNothing(t *testing.T) {
	runs, job, _, _ := issueWithDeclaration(t, false)
	for _, name := range []string{productFrozenDeclarationFile, productFrozenPolicyFile} {
		if _, err := os.Stat(filepath.Join(runs, job, name)); !os.IsNotExist(err) {
			t.Fatalf("선언 없이 %s를 씀", name)
		}
	}
	if _, ok, err := readFrozenPolicy(runs, job); ok || err != nil {
		t.Fatalf("없는 동결: %v %v", ok, err)
	}
}

func TestProductDeclarationReaderRejects(t *testing.T) {
	runs := t.TempDir()
	if os.Chmod(runs, 0700) != nil {
		t.Fatal("fixture permissions")
	}
	reader := productDeclarationReader{runsDir: runs}
	source := e3contract.PolicySource{Kind: e3contract.PolicyProductDeclaration, Ref: "declaration-job-1",
		DigestSHA256: strings.Repeat("a", 64), TargetApprovalRevision: "2"}
	for name, s := range map[string]e3contract.PolicySource{
		"missing":   source,
		"kg":        {Kind: e3contract.PolicyKGSnapshot, Ref: source.Ref, DigestSHA256: source.DigestSHA256, TargetApprovalRevision: "2"},
		"traversal": {Kind: e3contract.PolicyProductDeclaration, Ref: "declaration-..", DigestSHA256: source.DigestSHA256, TargetApprovalRevision: "2"},
		"prefix":    {Kind: e3contract.PolicyProductDeclaration, Ref: "kg-job-1", DigestSHA256: source.DigestSHA256, TargetApprovalRevision: "2"},
	} {
		if _, err := reader.ReadFrozen(t.Context(), s); !errors.Is(err, e3contract.ErrPolicyUnavailable) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, _, err := readFrozenPolicy(runs, "../x"); err == nil {
		t.Fatal("경로 밖 회차를 받음")
	}
}

// 동결 사본을 읽는 연결이 확정 단계 순서(e3contract.Run)에 그대로 붙는다. 가짜 E²로 한 회를 돌려 CONFIRMED가 나오고,
// 사본을 손대면 진위 실패다.
func TestFrozenDeclarationDrivesConfirmationRun(t *testing.T) {
	r, _, target, _ := declarationTestTarget(t)
	v, err := r.putDeclaration("ARTEX", target.ID, target.Revision, declarationTestInput())
	if err != nil {
		t.Fatal(err)
	}
	runs := t.TempDir()
	if os.Chmod(runs, 0700) != nil || os.Mkdir(filepath.Join(runs, "job-e3"), 0700) != nil {
		t.Fatal("fixture")
	}
	now := time.Now().UTC()
	frozen, frozenPolicy, err := freezeDeclaration(*v.Declaration, "job-e3", now.Add(-time.Minute), now.Add(time.Hour), target.ApprovedAt, target.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{productFrozenDeclarationFile: frozen, productFrozenPolicyFile: frozenPolicy} {
		if os.WriteFile(filepath.Join(runs, "job-e3", name), raw, 0600) != nil {
			t.Fatal("write")
		}
	}
	policy, ok, err := readFrozenPolicy(runs, "job-e3")
	if err != nil || !ok {
		t.Fatal(err)
	}
	fake := e3fake.New()
	fake.Clock = func() time.Time { return time.Now().UTC() }
	fake.Behaviors[e3fake.Key{Purpose: e3contract.PurposeIdentity, Role: e3contract.RoleOwner}] = e3fake.Behavior{SubjectID: "user-a"}
	fake.Behaviors[e3fake.Key{Purpose: e3contract.PurposeIdentity, Role: e3contract.RoleCross}] = e3fake.Behavior{SubjectID: "user-b"}
	binding := e3contract.RunBinding{JobRef: "job-e3", RunRef: "run-job-e3", TaskRef: "42", E2PolicyRef: "policy-job-e3",
		GenerationRef: "gen-1", InstanceRef: "inst-1", Policy: policy.Policy}
	in := e3contract.Input{Binding: binding, TargetRef: target.ID, WriteConsent: true,
		Owner: e3contract.Account{Label: "tester-a", Authenticated: true}, Cross: e3contract.Account{Label: "tester-b", Authenticated: true},
		PolicyValidFrom: policy.ValidFrom, PolicyValidUntil: policy.ValidUntil,
		// 확정 몫(종류 수·확정 요청 시간 상한)은 실행 시작 때 서버가 정한다. 이 시험은 선언 종류가 모두 든다고 둔다.
		AdmittedTypes: e3contract.MaxDeclarationTypes, RequestTimeout: 10 * time.Second}
	deps := e3contract.Deps{Transport: fake, Journal: fake, Declarations: productDeclarationReader{runsDir: runs},
		Reset: productResetGate{registry: r}, Logger: slog.New(slog.DiscardHandler)}
	res := e3contract.Run(t.Context(), deps, in)
	if res.Status != e3contract.StatusCompleted || len(res.Verdicts) != 1 || res.Verdicts[0].Value != "confirmed" {
		t.Fatalf("동결 사본으로 확정 단계: %+v", res)
	}
	if err := os.WriteFile(filepath.Join(runs, "job-e3", productFrozenDeclarationFile), bytes.Replace(frozen, []byte("/app/me"), []byte("/app/xx"), 1), 0600); err != nil {
		t.Fatal(err)
	}
	res = e3contract.Run(t.Context(), e3contract.Deps{Transport: e3fake.New(), Journal: fake, Declarations: productDeclarationReader{runsDir: runs},
		Reset: productResetGate{registry: r}}, in)
	if res.Status != e3contract.StatusIntegrityFailed || res.Reason != string(e3contract.IntegrityPolicyDigestMismatch) {
		t.Fatalf("손댄 사본: %+v", res)
	}
}

func TestActiveDeclarationOnlyWhenValid(t *testing.T) {
	r, _, target, _ := declarationTestTarget(t)
	if _, ok := r.activeDeclaration(r.records[target.ID]); ok {
		t.Fatal("선언 없이 동결 대상")
	}
	if _, err := r.putDeclaration("ARTEX", target.ID, target.Revision, declarationTestInput()); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.activeDeclaration(r.records[target.ID]); !ok {
		t.Fatal("유효 선언을 동결하지 않음")
	}
	now := r.now
	r.now = func() time.Time { return target.ExpiresAt }
	if _, ok := r.activeDeclaration(r.records[target.ID]); ok {
		t.Fatal("만료된 대상의 선언을 동결")
	}
	r.now = now
	if _, err := r.revoke("ARTEX", target.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.activeDeclaration(r.records[target.ID]); ok {
		t.Fatal("철회된 대상의 선언을 동결")
	}
}

func TestFreezeDeclarationWindow(t *testing.T) {
	d := declarationTestInput()
	decl := e3contract.Normalize(e3contract.Declaration{Version: e3contract.DeclarationVersion, TargetRef: "target-x", TargetApprovalRevision: "2",
		DeclaredBy: "ARTEX", DeclaredAt: time.Now().UTC().Truncate(time.Microsecond), Identity: d.Identity, Types: d.Types})
	base := time.Date(2026, 11, 18, 6, 0, 0, 0, time.UTC)
	read := func(raw []byte) productFrozenPolicy {
		var p productFrozenPolicy
		if decodeFrozen(raw, &p) != nil {
			t.Fatal("정책 파일 모양")
		}
		return p
	}
	// 실행이 승인보다 먼저 시작하고 만료 뒤까지 가면, 유효창은 승인 기간으로 좁아진다.
	_, raw, err := freezeDeclaration(decl, "job-1", base, base.Add(4*time.Hour), base.Add(time.Hour), base.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if p := read(raw); !p.ValidFrom.Equal(base.Add(time.Hour)) || !p.ValidUntil.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("유효창: %v ~ %v", p.ValidFrom, p.ValidUntil)
	}
	// 승인 기간 안의 실행이면 실행 기간 그대로다.
	_, raw, _ = freezeDeclaration(decl, "job-1", base.Add(time.Hour), base.Add(90*time.Minute), base, base.Add(2*time.Hour))
	if p := read(raw); !p.ValidFrom.Equal(base.Add(time.Hour)) || !p.ValidUntil.Equal(base.Add(90*time.Minute)) {
		t.Fatalf("실행 기간: %v ~ %v", p.ValidFrom, p.ValidUntil)
	}
	if _, _, err := freezeDeclaration(decl, "job-1", base.Add(3*time.Hour), base.Add(4*time.Hour), base, base.Add(2*time.Hour)); err == nil {
		t.Fatal("겹치지 않는 기간을 동결")
	}
}

func TestFrozenReadersStayInsideTheRun(t *testing.T) {
	runs, job, _, _ := issueWithDeclaration(t, true)
	policy, _, err := readFrozenPolicy(runs, job)
	if err != nil {
		t.Fatal(err)
	}
	reader := productDeclarationReader{runsDir: runs}
	kg := policy.Policy
	kg.Kind = e3contract.PolicyKGSnapshot
	if _, err := reader.ReadFrozen(t.Context(), kg); !errors.Is(err, e3contract.ErrPolicyUnavailable) {
		t.Fatalf("kg_snapshot 출처로 제품 사본을 읽음: %v", err)
	}
	// 회차 디렉터리 밖(runs의 부모)에 사본이 있어도 참조로 넘어가지 않는다.
	raw, _ := os.ReadFile(filepath.Join(runs, job, productFrozenDeclarationFile))
	parent := filepath.Dir(runs)
	if os.WriteFile(filepath.Join(parent, productFrozenDeclarationFile), raw, 0600) != nil {
		t.Fatal("fixture")
	}
	escape := policy.Policy
	escape.Ref = productFrozenPolicyRefPrefix + ".."
	if _, err := reader.ReadFrozen(t.Context(), escape); !errors.Is(err, e3contract.ErrPolicyUnavailable) {
		t.Fatalf("회차 밖 사본을 읽음: %v", err)
	}
	// 다른 회차의 정책 파일을 옮겨 두면 받지 않는다.
	other := filepath.Join(runs, "job-other")
	if os.Mkdir(other, 0700) != nil {
		t.Fatal("fixture")
	}
	policyRaw, _ := os.ReadFile(filepath.Join(runs, job, productFrozenPolicyFile))
	if os.WriteFile(filepath.Join(other, productFrozenPolicyFile), policyRaw, 0600) != nil {
		t.Fatal("fixture")
	}
	if _, _, err := readFrozenPolicy(runs, "job-other"); err == nil {
		t.Fatal("다른 회차의 정책 파일을 받음")
	}
}
