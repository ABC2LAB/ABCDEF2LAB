package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e3contract"
)

// declarationTestTarget은 되돌릴 수 있는 쓰기(POST)를 승인한 대상 하나를 등록·승인한다.
func declarationTestTarget(t *testing.T) (*productTargetRegistry, string, diagnosisprep.ProductTarget, map[string][]string) {
	t.Helper()
	r, root := targetTestRegistry(t)
	// 되돌릴 수 있는 쓰기는 설치 정책의 워크플로가 허용할 때만 등록된다.
	r.policy.Workflow = &diagnosisflow.Limits{Format: diagnosisflow.Version, MaxPages: 2, MaxEndpoints: 32, MaxResponseBytes: 4096, AllowReversibleWrites: true}
	txt := map[string][]string{}
	r.lookupTXT = func(_ context.Context, name string) ([]string, error) { return txt[name], nil }
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.10")}, nil
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		return strings.Repeat("a", 64), time.Now().Add(24 * time.Hour), nil
	}
	spec := targetTestSpec("a.example.test")
	spec.URL, spec.PathPrefixes = "https://a.example.test/app", []string{"/app"}
	spec.Methods, spec.ReversibleWrites = []string{"GET", "HEAD", "POST"}, true
	target, err := r.register("ARTEX", spec)
	if err != nil {
		t.Fatal(err)
	}
	txt[target.ChallengeName] = []string{target.ChallengeValue}
	if target, err = r.approve(t.Context(), "ARTEX", target.ID); err != nil {
		t.Fatal(err)
	}
	return r, root, target, txt
}

func declarationTestInput() diagnosisprep.DeclarationInput {
	note := e3contract.TypeDeclaration{Type: "note", Sharing: e3contract.SharingOwnerOnly, OwnerOnlyFields: []string{"zeta", "body"},
		Create: e3contract.CreateSpec{Path: "/app/notes", Method: "POST", Kind: e3contract.BodyJSON, BodyTemplate: `{"title":"t"}`,
			MarkerField: "body", IDPath: "id"},
		Read: e3contract.ReadSpec{PathTemplate: "/app/notes/{id}", DataPath: "."}}
	// 화면 입력 순서가 이름순이 아니어도 서버가 정렬한다.
	return diagnosisprep.DeclarationInput{Identity: e3contract.IdentitySpec{Path: "/app/me", SubjectIDPath: "id"},
		Types:             []e3contract.TypeDeclaration{{Type: "post", Sharing: e3contract.SharingShared}, note},
		OwnerAccountLabel: "tester-a", CrossAccountLabel: "tester-b", AccountsSamePrivilege: true}
}

func declarationField(t *testing.T, err error) string {
	t.Helper()
	var fe *declarationInputError
	if !errors.As(err, &fe) || err.Error() != "invalid_declaration" {
		t.Fatalf("형식 오류가 아님: %v", err)
	}
	return fe.field
}

func TestProductDeclarationLifecycle(t *testing.T) {
	r, root, target, _ := declarationTestTarget(t)
	if v, err := r.declaration("ARTEX", target.ID); err != nil || v.State != diagnosisprep.DeclarationNone || v.Declaration != nil {
		t.Fatalf("빈 선언: %+v %v", v, err)
	}
	if _, err := r.declaration("other", target.ID); err == nil || err.Error() != "target_not_found" {
		t.Fatalf("다른 주체 조회: %v", err)
	}
	before := r.now().UTC()
	v, err := r.putDeclaration("ARTEX", target.ID, target.Revision, declarationTestInput())
	if err != nil {
		t.Fatal(err)
	}
	d := v.Declaration
	if v.State != diagnosisprep.DeclarationActive || v.TargetRevision != target.Revision || d == nil || d.DeclaredBy != "ARTEX" ||
		d.TargetRef != target.ID || d.TargetApprovalRevision != targetApprovalRevision(target.Revision) || d.DeclaredAt.Before(before.Truncate(time.Microsecond)) ||
		d.Types[0].Type != "note" || d.Types[0].OwnerOnlyFields[0] != "body" {
		t.Fatalf("저장 결과: %+v", v)
	}
	digest, _ := d.Digest()
	// 저널이 감사 기록이다: 다시 열어도 같은 선언·digest다.
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	again, err := r.declaration("ARTEX", target.ID)
	if err != nil || again.State != diagnosisprep.DeclarationActive {
		t.Fatalf("재생: %+v %v", again, err)
	}
	if got, _ := again.Declaration.Digest(); got != digest {
		t.Fatal("재생한 선언이 다름")
	}
	// 대상을 철회하면(판이 바뀜) 저장된 선언은 남아도 무효다.
	if _, err := r.revoke("ARTEX", target.ID); err != nil {
		t.Fatal(err)
	}
	if v, _ := r.declaration("ARTEX", target.ID); v.State != diagnosisprep.DeclarationInvalid || v.InvalidReason != "target_reapproved" || v.Declaration == nil {
		t.Fatalf("철회 뒤: %+v", v)
	}
	if _, err := r.putDeclaration("ARTEX", target.ID, target.Revision, declarationTestInput()); err == nil || err.Error() != "target_not_approved" {
		t.Fatalf("철회된 대상에 저장: %v", err)
	}
}

func TestProductDeclarationRevisionBinding(t *testing.T) {
	r, _, target, _ := declarationTestTarget(t)
	for _, expected := range []uint64{target.Revision - 1, target.Revision + 1} {
		if _, err := r.putDeclaration("ARTEX", target.ID, expected, declarationTestInput()); err == nil || err.Error() != "declaration_target_revision_mismatch" {
			t.Fatalf("판 %d: %v", expected, err)
		}
	}
	if v, _ := r.declaration("ARTEX", target.ID); v.State != diagnosisprep.DeclarationNone {
		t.Fatal("판이 다른데 저장됨")
	}
	// 승인 만료도 승인 상태가 아니다.
	r.now = func() time.Time { return target.ExpiresAt.Add(time.Second) }
	if _, err := r.putDeclaration("ARTEX", target.ID, target.Revision, declarationTestInput()); err == nil || err.Error() != "target_not_approved" {
		t.Fatalf("만료된 대상: %v", err)
	}
}

func TestProductDeclarationExpiredView(t *testing.T) {
	r, _, target, _ := declarationTestTarget(t)
	if _, err := r.putDeclaration("ARTEX", target.ID, target.Revision, declarationTestInput()); err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return target.ExpiresAt }
	if v, _ := r.declaration("ARTEX", target.ID); v.State != diagnosisprep.DeclarationInvalid || v.InvalidReason != "target_expired" {
		t.Fatalf("만료 뒤: %+v", v)
	}
}

func TestProductDeclarationRejectsWithFieldOnly(t *testing.T) {
	cases := map[string]struct {
		edit  func(*diagnosisprep.DeclarationInput)
		field string
	}{
		"credential_template": {func(in *diagnosisprep.DeclarationInput) {
			in.Types[1].Create.BodyTemplate = `{"title":"t","password":"hunter2-secret-value"}`
		}, "types[0].create.body_template"},
		"credential_escaped": {func(in *diagnosisprep.DeclarationInput) {
			in.Types[1].Create.BodyTemplate = `{"title":"t","pass\u0077ord":"hunter2-secret-value"}`
		}, "types[0].create.body_template"},
		"credential_normal": {func(in *diagnosisprep.DeclarationInput) {
			in.Types[1].Read.StatusPath, in.Types[1].Read.NormalValue = "s", "Bearer hunter2-secret-value"
		}, "types[0].read.normal_value"},
		"out_of_scope_create": {func(in *diagnosisprep.DeclarationInput) { in.Types[1].Create.Path = "/admin/notes" }, "types[0].create.path"},
		"out_of_scope_read":   {func(in *diagnosisprep.DeclarationInput) { in.Types[1].Read.PathTemplate = "/other/{id}" }, "types[0].read.path_template"},
		"identity_scope":      {func(in *diagnosisprep.DeclarationInput) { in.Identity.Path = "/me" }, "identity.path"},
		"method_not_approved": {func(in *diagnosisprep.DeclarationInput) { in.Types[1].Create.Method = "PUT" }, "types[0].create.method"},
		"shape":               {func(in *diagnosisprep.DeclarationInput) { in.Types[1].Read.PathTemplate = "/app/notes" }, "types[0].read.path_template"},
		"duplicate":           {func(in *diagnosisprep.DeclarationInput) { in.Types[0] = in.Types[1] }, "types[1].type"},
		"no_types":            {func(in *diagnosisprep.DeclarationInput) { in.Types = nil }, "types"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r, root, target, _ := declarationTestTarget(t)
			in := declarationTestInput()
			c.edit(&in)
			_, err := r.putDeclaration("ARTEX", target.ID, target.Revision, in)
			if got := declarationField(t, err); got != c.field {
				t.Fatalf("칸 %q, 기대 %q", got, c.field)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatal("오류에 입력 값이 들어감")
			}
			if v, _ := r.declaration("ARTEX", target.ID); v.State != diagnosisprep.DeclarationNone {
				t.Fatal("거부한 선언이 저장됨")
			}
			// 거부한 값은 저널에도 남지 않는다.
			assertNoFileContains(t, filepath.Join(root, "declarations"), "hunter2")
		})
	}
}

func assertNoFileContains(t *testing.T, dir, needle string) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(raw), needle) {
			t.Fatalf("%s에 거부한 값이 남음", path)
		}
		return nil
	})
}

// 저널 줄을 손대면(digest·결속 불일치) 다시 열 때 받지 않는다.
func TestProductDeclarationReplayRejectsTamperedLine(t *testing.T) {
	r, _, target, _ := declarationTestTarget(t)
	v, err := r.putDeclaration("ARTEX", target.ID, target.Revision, declarationTestInput())
	if err != nil {
		t.Fatal(err)
	}
	good := productDeclarationEvent{TargetID: target.ID, Action: declarationDeclare, Actor: "ARTEX", At: v.DeclaredAt,
		TargetRevision: v.TargetRevision, Declaration: v.Declaration}
	good.DigestSHA256, _ = v.Declaration.Digest()
	if err := r.replayDeclaration(good); err != nil {
		t.Fatalf("정상 줄: %v", err)
	}
	cases := map[string]func(*productDeclarationEvent){
		"digest":      func(e *productDeclarationEvent) { e.DigestSHA256 = strings.Repeat("0", 64) },
		"content":     func(e *productDeclarationEvent) { e.Declaration.Types[0].Read.PathTemplate = "/app/x/{id}" },
		"actor":       func(e *productDeclarationEvent) { e.Actor = "other" },
		"declared_by": func(e *productDeclarationEvent) { e.Declaration.DeclaredBy = "other" },
		"at":          func(e *productDeclarationEvent) { e.At = e.At.Add(time.Second) },
		"revision":    func(e *productDeclarationEvent) { e.TargetRevision++ },
		"bound_rev":   func(e *productDeclarationEvent) { e.Declaration.TargetApprovalRevision = "99" },
		"target_ref":  func(e *productDeclarationEvent) { e.Declaration.TargetRef = "other-target" },
		"unknown":     func(e *productDeclarationEvent) { e.TargetID = "missing-target" },
		"action":      func(e *productDeclarationEvent) { e.Action = "withdraw" },
	}
	// 결속 칸만 바꾼 줄은 digest를 다시 계산해도 받지 않는다(digest 대조에 가려지지 않게 따로 본다).
	rebound := map[string]bool{"declared_by": true, "bound_rev": true, "target_ref": true, "at": true}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(good)
			var e productDeclarationEvent
			_ = json.Unmarshal(raw, &e)
			edit(&e)
			if rebound[name] {
				if digest, err := e.Declaration.Digest(); err == nil {
					e.DigestSHA256 = digest
				}
			}
			if err := r.replayDeclaration(e); err == nil {
				t.Fatal("손댄 줄을 받음")
			}
		})
	}
}

func confirmationObjects() []productConfirmationObject {
	return []productConfirmationObject{{Type: "note", IDDigest: strings.Repeat("d", 64)}}
}

// 확정 단계 쓰기 뒤에는 운영자의 리셋 확인 전까지 다음 회차를 시작하지 않는다. 확인한 뒤에도 지난 객체의 표식 잔존 점검이
// absent여야 시작한다(점검이 없으면 모른다). 새 쓰기는 앞선 확인을 덮는다. 다시 열어도 같은 상태다.
func TestProductResetConfirmationLifecycle(t *testing.T) {
	r, root, target, _ := declarationTestTarget(t)
	gate := productResetGate{registry: r}
	binding := e3contract.RunBinding{}
	start := func(g productResetGate) e3contract.StartRefusal {
		return e3contract.CheckRunStart(t.Context(), g, binding, target.ID)
	}
	if v, err := r.resetConfirmation("ARTEX", target.ID); err != nil || v.ResetRequired || v.LastWritesJob != "" || v.Statement != productResetStatement {
		t.Fatalf("쓰기 전: %+v %v", v, err)
	}
	if got := start(gate); got != "" {
		t.Fatalf("쓰기 전 시작 거절: %s", got)
	}
	if err := r.recordConfirmationWrites(target.ID, "job-1", confirmationObjects()); err != nil {
		t.Fatal(err)
	}
	if v, _ := r.resetConfirmation("ARTEX", target.ID); !v.ResetRequired || v.LastWritesJob != "job-1" || v.LastWritesObjects != 1 {
		t.Fatalf("쓰기 뒤: %+v", v)
	}
	if got := start(gate); got != e3contract.RefuseResetConfirmationMissing {
		t.Fatalf("확인 없이: %s", got)
	}
	if _, err := r.confirmReset("ARTEX", target.ID, false, writesRef(t, r, target.ID)); err == nil || err.Error() != "statement_required" {
		t.Fatalf("진술 없이: %v", err)
	}
	if _, err := r.confirmReset("other", target.ID, true, writesRef(t, r, target.ID)); err == nil || err.Error() != "target_not_found" {
		t.Fatalf("다른 주체: %v", err)
	}
	v, err := r.confirmReset("ARTEX", target.ID, true, writesRef(t, r, target.ID))
	if err != nil || v.ResetRequired || v.ConfirmedBy != "ARTEX" || v.ConfirmedAt.IsZero() {
		t.Fatalf("확인: %+v %v", v, err)
	}
	// 같은 쓰기를 두 번 확인해도 줄을 더 남기지 않는다.
	if again, err := r.confirmReset("ARTEX", target.ID, true, writesRef(t, r, target.ID)); err != nil || !again.ConfirmedAt.Equal(v.ConfirmedAt) {
		t.Fatalf("두 번째 확인: %+v %v", again, err)
	}
	if got := start(gate); got != e3contract.RefuseResetStateUnavailable {
		t.Fatalf("잔존 점검 없이: %s", got)
	}
	for residue, want := range map[e3contract.Residue]e3contract.StartRefusal{
		e3contract.ResidueAbsent: "", e3contract.ResiduePresent: e3contract.RefusePreviousMarkersPresent, e3contract.ResidueUnknown: e3contract.RefuseResetStateUnavailable,
	} {
		var seen []productConfirmationObject
		g := productResetGate{registry: r, residue: func(_ context.Context, _ e3contract.RunBinding, objects []productConfirmationObject) (e3contract.Residue, error) {
			seen = objects
			return residue, nil
		}}
		if got := start(g); got != want || len(seen) != 1 || seen[0].IDDigest != strings.Repeat("d", 64) {
			t.Fatalf("잔존 %s: %s %+v", residue, got, seen)
		}
	}
	failing := productResetGate{registry: r, residue: func(context.Context, e3contract.RunBinding, []productConfirmationObject) (e3contract.Residue, error) {
		return e3contract.ResidueAbsent, errors.New("점검 실패")
	}}
	if got := start(failing); got != e3contract.RefuseResetStateUnavailable {
		t.Fatalf("점검 실패를 absent로 읽음: %s", got)
	}
	if err := r.recordConfirmationWrites(target.ID, "job-2", confirmationObjects()); err != nil {
		t.Fatal(err)
	}
	if got := start(gate); got != e3contract.RefuseResetConfirmationMissing {
		t.Fatalf("새 쓰기가 확인을 덮지 않음: %s", got)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if v, _ := reopened.resetConfirmation("ARTEX", target.ID); !v.ResetRequired || v.LastWritesJob != "job-2" {
		t.Fatalf("재생: %+v", v)
	}
	if got := e3contract.CheckRunStart(t.Context(), productResetGate{registry: reopened}, binding, target.ID); got != e3contract.RefuseResetConfirmationMissing {
		t.Fatalf("재생 뒤 시작: %s", got)
	}
	if got := e3contract.CheckRunStart(t.Context(), productResetGate{registry: reopened}, binding, "missing-target"); got != e3contract.RefuseResetStateUnavailable {
		t.Fatalf("없는 대상: %s", got)
	}
}

func TestProductResetRejectsBadLines(t *testing.T) {
	r, root, target, _ := declarationTestTarget(t)
	rec := r.records[target.ID]
	at := r.now().UTC()
	writes := productDeclarationEvent{TargetID: target.ID, Action: declarationConfirmationWrites, Actor: productConfirmationActor, At: at,
		TargetRevision: target.Revision, JobRef: "job-1", Objects: confirmationObjects()}
	if _, err := applyReset(rec, nil, nil, writes); err != nil {
		t.Fatalf("정상 쓰기 줄: %v", err)
	}
	pending, _ := applyReset(rec, nil, nil, writes)
	confirm := productDeclarationEvent{TargetID: target.ID, Action: declarationResetConfirmed, Actor: "ARTEX", At: at,
		TargetRevision: target.Revision, JobRef: "job-1", WritesRef: pending.writesRef, StatementSHA256: productResetStatementSHA256()}
	if _, err := applyReset(rec, pending, nil, confirm); err != nil {
		t.Fatalf("정상 확인 줄: %v", err)
	}
	for name, c := range map[string]struct {
		st *productResetState
		e  func() productDeclarationEvent
	}{
		"writes_by_person":  {nil, func() productDeclarationEvent { e := writes; e.Actor = "ARTEX"; return e }},
		"writes_no_objects": {nil, func() productDeclarationEvent { e := writes; e.Objects = nil; return e }},
		"writes_bad_digest": {nil, func() productDeclarationEvent {
			e := writes
			e.Objects = []productConfirmationObject{{Type: "note", IDDigest: "raw-id-42"}}
			return e
		}},
		"writes_with_decl": {nil, func() productDeclarationEvent { e := writes; e.Declaration = &e3contract.Declaration{}; return e }},
		// 미리 남기는 줄에는 거둔 쓰기를 넣지 않는다.
		"writes_not_sent": {nil, func() productDeclarationEvent {
			e := writes
			e.Objects = []productConfirmationObject{{Type: "note", IDUnknown: true, NotSent: true}}
			return e
		}},
		"confirm_without":      {nil, func() productDeclarationEvent { return confirm }},
		"confirm_by_program":   {pending, func() productDeclarationEvent { e := confirm; e.Actor = productConfirmationActor; return e }},
		"confirm_wrong_stmt":   {pending, func() productDeclarationEvent { e := confirm; e.StatementSHA256 = strings.Repeat("0", 64); return e }},
		"future_revision":      {nil, func() productDeclarationEvent { e := writes; e.TargetRevision = target.Revision + 1; return e }},
		"unknown_action":       {pending, func() productDeclarationEvent { e := confirm; e.Action = "reset_skipped"; return e }},
		"confirm_with_objects": {pending, func() productDeclarationEvent { e := confirm; e.Objects = confirmationObjects(); return e }},
		"confirm_other_job":    {pending, func() productDeclarationEvent { e := confirm; e.JobRef = "job-2"; return e }},
		"confirm_other_ref":    {pending, func() productDeclarationEvent { e := confirm; e.WritesRef = strings.Repeat("b", 64); return e }},
		"confirm_no_ref":       {pending, func() productDeclarationEvent { e := confirm; e.WritesRef = ""; return e }},
		"writes_with_ref":      {nil, func() productDeclarationEvent { e := writes; e.WritesRef = strings.Repeat("c", 64); return e }},
		"confirm_no_job":       {pending, func() productDeclarationEvent { e := confirm; e.JobRef = ""; return e }},
	} {
		if _, err := applyReset(rec, c.st, nil, c.e()); err == nil {
			t.Fatalf("%s 받음", name)
		}
	}
	later := writes
	later.At = at.Add(time.Second)
	if _, err := applyReset(rec, pending, nil, later); err != nil {
		t.Fatalf("다른 회차 목록에서 정상 쓰기 줄: %v", err)
	}
	// 지금 쓰기 기록의 회차는 줄을 더 남길 수 있다(생성 요청마다 먼저 기록, 사용자 결정 2026-11-19). 객체가 쌓이고 참조가
	// 바뀌며 앞선 확인은 지워진다.
	confirmed, _ := applyReset(rec, pending, nil, confirm)
	appended, err := applyReset(rec, confirmed, map[string]bool{"job-1": true}, later)
	if err != nil || len(appended.writesObjects) != 2*len(confirmationObjects()) || appended.writesRef == pending.writesRef || !appended.confirmedAt.IsZero() {
		t.Fatalf("지금 회차의 줄 더하기: %+v %v", appended, err)
	}
	// 지나간 회차(지금 기록이 아닌 회차)는 더 받지 않는다.
	old := later
	old.JobRef = "job-0"
	if _, err := applyReset(rec, pending, map[string]bool{"job-0": true, "job-1": true}, old); err == nil {
		t.Fatal("지나간 회차의 쓰기 줄을 받음")
	}
	twice, _ := applyReset(rec, pending, nil, confirm)
	if _, err := applyReset(rec, twice, nil, confirm); err == nil {
		t.Fatal("두 번째 확인 줄을 받음")
	}
	// 거부한 줄은 저널에 쓰지 않는다: 다시 열어도 재생이 깨지지 않고 상태가 그대로다.
	if err := r.recordConfirmationWrites(target.ID, "job-1", nil); err == nil {
		t.Fatal("객체 없는 쓰기 기록을 남김")
	}
	if v, _ := r.resetConfirmation("ARTEX", target.ID); v.ResetRequired {
		t.Fatal("거부한 줄이 상태를 바꿈")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatal("거부한 줄이 저널에 남아 다시 열지 못함", err)
	}
	defer reopened.Close()
	if v, _ := reopened.resetConfirmation("ARTEX", target.ID); v.ResetRequired {
		t.Fatal("재생 뒤 상태가 바뀜")
	}
}

// 선언 디렉터리를 만든 뒤 부모 엔트리를 동기 저장하고, 실패하면 레지스트리를 열지 않는다.
func TestProductDeclarationDirectorySyncsParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("protected preparation uses an unprivileged owner")
	}
	original := productSyncDirectory
	t.Cleanup(func() { productSyncDirectory = original })
	root := t.TempDir()
	if os.Chmod(root, 0700) != nil {
		t.Fatal("private root")
	}
	policy := productTargetPolicy{Format: "abc2lab.product-target-policy/v1", AllowedPorts: []int{443}, PrivateCIDRs: []string{"10.200.1.0/24"}, MaxTargets: 32, MaxRequests: 4}
	var synced []string
	productSyncDirectory = func(path string) error {
		// 동기화할 때 선언 디렉터리가 이미 있어야 그 엔트리가 함께 저장된다.
		if _, err := os.Stat(filepath.Join(path, "declarations")); err != nil {
			t.Errorf("선언 디렉터리 전에 부모를 동기화함: %v", err)
		}
		synced = append(synced, path)
		return original(path)
	}
	r, err := newProductTargetRegistry(root, policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	if len(synced) != 1 || synced[0] != root {
		t.Fatalf("부모 동기화: %v", synced)
	}
	productSyncDirectory = func(string) error { return errors.New("동기화 실패") }
	failing := t.TempDir()
	if os.Chmod(failing, 0700) != nil {
		t.Fatal("private root")
	}
	if r, err := newProductTargetRegistry(failing, policy, func() error { return nil }); err == nil {
		_ = r.Close()
		t.Fatal("부모 동기화 실패에도 레지스트리를 엶")
	}
}

// 운영자가 회차 A의 기록을 보고 동의하는 사이에 기록이 바뀌면(A가 쓰기를 더 남김) 그 동의는 반영되지 않는다. 다른 회차 B의
// 쓰기는 A가 미해결인 동안 아예 들어오지 못한다(#738 이동찬 리뷰).
func TestProductResetConfirmationBoundToSeenRecord(t *testing.T) {
	r, root, target, _ := declarationTestTarget(t)
	if err := r.recordConfirmationWrites(target.ID, "job-a", confirmationObjects()); err != nil {
		t.Fatal(err)
	}
	seen, _ := r.resetConfirmation("ARTEX", target.ID)
	if seen.LastWritesJob != "job-a" {
		t.Fatalf("본 기록: %+v", seen)
	}
	if err := r.recordConfirmationWrites(target.ID, "job-b", confirmationObjects()); err == nil {
		t.Fatal("A 미해결 중 B 쓰기를 받음")
	}
	if err := r.recordConfirmationWrites(target.ID, "job-a", []productConfirmationObject{{Type: "secret", IDUnknown: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.confirmReset("ARTEX", target.ID, true, seen.LastWritesRef); err == nil || err.Error() != "reset_record_changed" {
		t.Fatalf("바뀐 기록에 동의를 반영함: %v", err)
	}
	if v, _ := r.resetConfirmation("ARTEX", target.ID); !v.ResetRequired || v.LastWritesJob != "job-a" || v.LastWritesObjects != 2 || v.ConfirmedBy != "" {
		t.Fatalf("바뀐 기록이 확인됨: %+v", v)
	}
	if got := e3contract.CheckRunStart(t.Context(), productResetGate{registry: r}, e3contract.RunBinding{}, target.ID); got != e3contract.RefuseResetConfirmationMissing {
		t.Fatalf("B 확인 없이 시작: %s", got)
	}
	// 기록이 없을 때의 동의도 반영하지 않는다.
	r2, _, target2, _ := declarationTestTarget(t)
	if _, err := r2.confirmReset("ARTEX", target2.ID, true, strings.Repeat("a", 64)); err == nil || err.Error() != "reset_record_changed" {
		t.Fatalf("기록 없이 동의: %v", err)
	}
	// 바뀐 기록을 본 동의는 반영되고, 저널에는 그 기록(A의 쓰기 둘)이 남는다.
	v, err := r.confirmReset("ARTEX", target.ID, true, writesRef(t, r, target.ID))
	if err != nil || v.ResetRequired || v.ConfirmedBy != "ARTEX" {
		t.Fatalf("바뀐 기록 확인: %+v %v", v, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if v, _ := reopened.resetConfirmation("ARTEX", target.ID); v.ResetRequired || v.LastWritesJob != "job-a" || v.LastWritesObjects != 2 {
		t.Fatalf("재생: %+v", v)
	}
}

func writesRef(t *testing.T, r *productTargetRegistry, id string) string {
	t.Helper()
	v, err := r.resetConfirmation("ARTEX", id)
	if err != nil {
		t.Fatal(err)
	}
	return v.LastWritesRef
}

// 첫 기록을 본 동의는 그 뒤 바뀐 기록을 확인 완료로 바꾸지 않는다(#708 이동찬 리뷰). 지금 회차가 쓰기를 더 남기면
// (기록하고 나서 보내기) 참조가 바뀌어 먼저 본 동의는 반영되지 않는다.
func TestProductResetConsentBoundToWritesRecord(t *testing.T) {
	r, _, target, _ := declarationTestTarget(t)
	if err := r.recordConfirmationWrites(target.ID, "job-1", confirmationObjects()); err != nil {
		t.Fatal(err)
	}
	first := writesRef(t, r, target.ID)
	other := []productConfirmationObject{{Type: "secret", IDDigest: strings.Repeat("e", 64)}}
	if err := r.recordConfirmationWrites(target.ID, "job-1", other); err != nil {
		t.Fatal(err)
	}
	if writesRef(t, r, target.ID) == first {
		t.Fatal("쓰기를 더했는데 참조가 그대로임")
	}
	if _, err := r.confirmReset("ARTEX", target.ID, true, first); err == nil || err.Error() != "reset_record_changed" {
		t.Fatalf("바뀌기 전 기록에 동의 반영: %v", err)
	}
	first = writesRef(t, r, target.ID)
	// 다른 회차의 새 기록은 미해결 기록을 덮지 못한다(#738 이동찬 리뷰). 기록과 참조가 그대로라 지금 본 기록에는 동의할 수 있다.
	if err := r.recordConfirmationWrites(target.ID, "job-2", other); err == nil {
		t.Fatal("미해결 기록을 다른 회차가 덮음")
	}
	if v, _ := r.resetConfirmation("ARTEX", target.ID); !v.ResetRequired || v.LastWritesRef != first || v.LastWritesJob != "job-1" {
		t.Fatalf("미해결 기록이 바뀜: %+v", v)
	}
	// 같은 회차라도 시각·객체가 다르면 참조가 다르다.
	at := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	a, b := confirmationWritesRef("job-1", at, confirmationObjects()), confirmationWritesRef("job-1", at.Add(time.Second), confirmationObjects())
	c := confirmationWritesRef("job-1", at, other)
	if a == b || a == c || len(a) != 64 {
		t.Fatal("쓰기 기록 참조가 시각·객체를 구분하지 않음")
	}
}

// 쓰기 줄 하나를 적용하는 비용은 앞선 회차 수와 관계없다(재생이 줄 수의 제곱이 되지 않게, #708 이동찬 리뷰).
func TestProductResetApplyCostIndependentOfHistory(t *testing.T) {
	r, _, target, _ := declarationTestTarget(t)
	rec := r.records[target.ID]
	e := productDeclarationEvent{TargetID: target.ID, Action: declarationConfirmationWrites, Actor: productConfirmationActor, At: r.now().UTC(),
		TargetRevision: target.Revision, JobRef: "job-new", Objects: confirmationObjects()}
	allocs := func(n int) float64 {
		jobs := map[string]bool{}
		for i := range n {
			jobs[fmt.Sprintf("job-%d", i)] = true
		}
		return testing.AllocsPerRun(20, func() {
			if _, err := applyReset(rec, nil, jobs, e); err != nil {
				t.Fatal(err)
			}
		})
	}
	if small, large := allocs(1), allocs(4096); large > small {
		t.Fatalf("앞선 회차 수에 따라 할당이 늚: %v → %v", small, large)
	}
	// 재생도 회차 목록을 줄마다 다시 만들지 않는다: 줄을 많이 받아도 목록은 하나이고 회차마다 한 번만 더해진다.
	for i := range 64 {
		if err := r.recordConfirmationWrites(target.ID, fmt.Sprintf("job-%d", i), confirmationObjects()); err != nil {
			t.Fatal(err)
		}
		// 다음 회차가 쓰기를 남기려면 앞 회차 기록의 리셋 확인이 먼저다(#738 이동찬 리뷰).
		if _, err := r.confirmReset("ARTEX", target.ID, true, r.resets[target.ID].writesRef); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.resetJobs[target.ID]) != 64 {
		t.Fatalf("회차 목록 %d", len(r.resetJobs[target.ID]))
	}
	if err := r.recordConfirmationWrites(target.ID, "job-3", confirmationObjects()); err == nil || len(r.resetJobs[target.ID]) != 64 {
		t.Fatal("같은 회차 두 번째 쓰기 기록을 받음")
	}
}

// 생성 요청이 시간 초과·결과 불명이면 쓰기 기록에 ID 모르는 객체로 남는다. 리셋 확인 전까지 다음 회차를 시작하지 않고,
// 확인 뒤에는 잔존 점검이 ID 아는 객체만 읽는다(ID 모르는 객체는 읽을 수 없다, 사용자 결정 2026-11-18).
func TestProductResetUnknownIDWrites(t *testing.T) {
	r, _, target, _ := declarationTestTarget(t)
	start := func(g productResetGate) e3contract.StartRefusal {
		return e3contract.CheckRunStart(t.Context(), g, e3contract.RunBinding{}, target.ID)
	}
	for name, objects := range map[string][]productConfirmationObject{
		"unknown with digest":  {{Type: "note", IDDigest: strings.Repeat("d", 64), IDUnknown: true}},
		"known without digest": {{Type: "note"}},
	} {
		if err := r.recordConfirmationWrites(target.ID, "job-bad-"+strings.ReplaceAll(name, " ", "-"), objects); err == nil {
			t.Fatal("받으면 안 되는 쓰기 기록:", name)
		}
	}
	unknown := productConfirmationObject{Type: "note", IDUnknown: true}
	if err := r.recordConfirmationWrites(target.ID, "job-1", []productConfirmationObject{unknown}); err != nil {
		t.Fatal(err)
	}
	if got := start(productResetGate{registry: r}); got != e3contract.RefuseResetConfirmationMissing {
		t.Fatalf("ID 모르는 쓰기 뒤 확인 없이: %s", got)
	}
	if _, err := r.confirmReset("ARTEX", target.ID, true, writesRef(t, r, target.ID)); err != nil {
		t.Fatal(err)
	}
	// ID 모르는 객체만 있으면 읽을 것이 없다: 잔존 점검을 부르지 않고 리셋 확인으로 시작한다.
	called := false
	g := productResetGate{registry: r, residue: func(context.Context, e3contract.RunBinding, []productConfirmationObject) (e3contract.Residue, error) {
		called = true
		return e3contract.ResidueUnknown, nil
	}}
	if got := start(g); got != "" || called {
		t.Fatalf("리셋 확인 뒤 시작 안 됨: %s called=%t", got, called)
	}
	// 섞여 있으면 ID 아는 객체만 잔존 점검에 넘긴다.
	known := productConfirmationObject{Type: "secret", IDDigest: strings.Repeat("e", 64)}
	if err := r.recordConfirmationWrites(target.ID, "job-2", []productConfirmationObject{unknown, known}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.confirmReset("ARTEX", target.ID, true, writesRef(t, r, target.ID)); err != nil {
		t.Fatal(err)
	}
	var seen []productConfirmationObject
	g.residue = func(_ context.Context, _ e3contract.RunBinding, objects []productConfirmationObject) (e3contract.Residue, error) {
		seen = objects
		return e3contract.ResidueAbsent, nil
	}
	if got := start(g); got != "" || len(seen) != 1 || seen[0] != known {
		t.Fatalf("잔존 점검 대상: %s %+v", got, seen)
	}
	// ID를 아는지 모르는지는 쓰기 기록 참조에 들어간다(같은 종류라도 다른 기록).
	at := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	if confirmationWritesRef("job-1", at, []productConfirmationObject{unknown}) ==
		confirmationWritesRef("job-1", at, []productConfirmationObject{{Type: "note", IDDigest: ""}}) {
		t.Fatal("ID 모름 표시가 쓰기 기록 참조에 없음")
	}
}

// 갱신 줄(confirmation_write_settled): 지금 회차의 미리 남긴 ID 모름 쓰기 하나를 created(ID digest) 또는 not_sent(거둠)로
// 바꾼다. 미리 남긴 쓰기가 없거나 지나간 회차면 받지 않는다. 다시 열어도 같은 상태다.
func TestProductResetSettleLines(t *testing.T) {
	r, root, target, _ := declarationTestTarget(t)
	digest := strings.Repeat("c", 64)
	if err := r.settleConfirmationWrite(target.ID, "job-1", productConfirmationObject{Type: "note", IDDigest: digest}); err == nil {
		t.Fatal("미리 남긴 쓰기 없이 갱신함")
	}
	reserve := func(job string, types ...string) {
		t.Helper()
		for _, typ := range types {
			if err := r.recordConfirmationWrites(target.ID, job, []productConfirmationObject{{Type: typ, IDUnknown: true}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	reserve("job-1", "note", "card")
	before := r.resets[target.ID].writesRef
	for name, o := range map[string]productConfirmationObject{
		"unknown_settle":  {Type: "note", IDUnknown: true},
		"other_type":      {Type: "secret", IDDigest: digest},
		"bad_digest":      {Type: "note", IDDigest: "raw-42"},
		"not_sent_digest": {Type: "note", IDDigest: digest, NotSent: true},
	} {
		if err := r.settleConfirmationWrite(target.ID, "job-1", o); err == nil {
			t.Fatalf("%s 갱신을 받음", name)
		}
	}
	if err := r.settleConfirmationWrite(target.ID, "job-1", productConfirmationObject{Type: "note", IDDigest: digest}); err != nil {
		t.Fatal(err)
	}
	if err := r.settleConfirmationWrite(target.ID, "job-1", productConfirmationObject{Type: "card", IDUnknown: true, NotSent: true}); err != nil {
		t.Fatal(err)
	}
	// 같은 종류를 두 번 갱신하지 않는다(미리 남긴 ID 모름이 더 없음).
	if err := r.settleConfirmationWrite(target.ID, "job-1", productConfirmationObject{Type: "note", IDDigest: digest}); err == nil {
		t.Fatal("갱신한 쓰기를 다시 갱신함")
	}
	want := []productConfirmationObject{{Type: "note", IDDigest: digest}, {Type: "card", IDUnknown: true, NotSent: true}}
	st := r.resets[target.ID]
	if !slices.Equal(st.writesObjects, want) || st.writesRef == before {
		t.Fatalf("갱신 뒤: %+v", st)
	}
	if v, _ := r.resetConfirmation("ARTEX", target.ID); !v.ResetRequired || v.LastWritesObjects != 1 {
		t.Fatalf("남은 쓰기 수(거둔 것 제외): %+v", v)
	}
	// 다음 회차가 시작되면 지난 회차는 더 갱신하지 않는다.
	ref := r.resets[target.ID].writesRef
	if _, err := r.confirmReset("ARTEX", target.ID, true, ref); err != nil {
		t.Fatal(err)
	}
	reserve("job-2", "note")
	// 지금 회차(job-2)가 미리 남긴 종류(note)와 같아도 지나간 회차의 갱신 줄로는 바꾸지 않는다.
	if err := r.settleConfirmationWrite(target.ID, "job-1", productConfirmationObject{Type: "note", IDDigest: digest}); err == nil {
		t.Fatal("지나간 회차를 갱신함")
	}
	if err := r.recordConfirmationWrites(target.ID, "job-1", []productConfirmationObject{{Type: "note", IDUnknown: true}}); err == nil {
		t.Fatal("지나간 회차에 쓰기를 더함")
	}
	want2 := r.resets[target.ID]
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatal("갱신 줄이 든 저널을 다시 열지 못함", err)
	}
	defer reopened.Close()
	if got := reopened.resets[target.ID]; got == nil || got.writesJob != want2.writesJob || got.writesRef != want2.writesRef ||
		!slices.Equal(got.writesObjects, want2.writesObjects) {
		t.Fatalf("재생 뒤: %+v, 기대 %+v", got, want2)
	}
}

// 선언 저장 API는 계정 역할 칸(계약 §4)을 받아 선언·동결본에 넣는다. 한쪽만 주거나 같은 계정을 두 역할에 두면 그 칸 이름으로
// 거부한다. 역할 없이 저장한 선언은 정본 바이트가 칸을 넣기 전과 같다(확정 단계는 꺼진다).
func TestProductDeclarationAccountRoles(t *testing.T) {
	r, _, target, _ := declarationTestTarget(t)
	v, err := r.putDeclaration("ARTEX", target.ID, target.Revision, declarationTestInput())
	if err != nil {
		t.Fatal(err)
	}
	if d := v.Declaration; d == nil || d.OwnerAccountLabel != "tester-a" || d.CrossAccountLabel != "tester-b" || !d.AccountsSamePrivilege {
		t.Fatalf("저장한 역할: %+v", v.Declaration)
	}
	got, err := r.declaration("ARTEX", target.ID)
	if err != nil || got.Declaration == nil || got.Declaration.OwnerAccountLabel != "tester-a" || got.Declaration.CrossAccountLabel != "tester-b" {
		t.Fatalf("조회한 역할: %+v %v", got, err)
	}
	for name, c := range map[string]struct {
		edit  func(*diagnosisprep.DeclarationInput)
		field string
	}{
		"owner_only":   {func(in *diagnosisprep.DeclarationInput) { in.CrossAccountLabel = "" }, "cross_account_label"},
		"same_account": {func(in *diagnosisprep.DeclarationInput) { in.CrossAccountLabel = in.OwnerAccountLabel }, "cross_account_label"},
		"bad_label":    {func(in *diagnosisprep.DeclarationInput) { in.OwnerAccountLabel = "tester a" }, "owner_account_label"},
		"privilege_without_roles": {func(in *diagnosisprep.DeclarationInput) {
			in.OwnerAccountLabel, in.CrossAccountLabel = "", ""
		}, "accounts_same_privilege"},
	} {
		in := declarationTestInput()
		c.edit(&in)
		if _, err := r.putDeclaration("ARTEX", target.ID, target.Revision, in); declarationField(t, err) != c.field {
			t.Fatalf("%s: %v", name, err)
		}
	}
	none := declarationTestInput()
	none.OwnerAccountLabel, none.CrossAccountLabel, none.AccountsSamePrivilege = "", "", false
	v, err = r.putDeclaration("ARTEX", target.ID, target.Revision, none)
	if err != nil || v.Declaration == nil || v.Declaration.OwnerAccountLabel != "" {
		t.Fatalf("역할 없는 선언 저장: %+v %v", v, err)
	}
	raw, err := v.Declaration.CanonicalBytes()
	if err != nil || bytes.Contains(raw, []byte("account")) {
		t.Fatalf("역할 없는 선언 정본에 역할 칸: %s %v", raw, err)
	}
}

// 같은 대상에 회차가 둘 붙어 있어도 앞 회차의 미해결 쓰기(리셋 확인 전, 거두지 않은 쓰기)는 뒤 회차의 예약·정산이 지울 수
// 없다(#738 이동찬 리뷰). 외부 송신 없이 저널 경계만 본다.
func TestProductResetKeepsOtherJobUnresolvedWrites(t *testing.T) {
	r, root, target, _ := declarationTestTarget(t)
	reserve := func(job, typ string) error {
		return r.recordConfirmationWrites(target.ID, job, []productConfirmationObject{{Type: typ, IDUnknown: true}})
	}
	if err := reserve("job-a", "note"); err != nil {
		t.Fatal(err)
	}
	// 리뷰 재현: 뒤 회차가 예약하고 not_sent로 정산하면 앞 회차의 기록이 사라졌다. 이제 뒤 회차의 예약부터 받지 않는다.
	if err := reserve("job-b", "note"); err == nil {
		t.Fatal("앞 회차 미해결 쓰기를 뒤 회차 예약이 덮음")
	}
	if err := r.settleConfirmationWrite(target.ID, "job-b", productConfirmationObject{Type: "note", IDUnknown: true, NotSent: true}); err == nil {
		t.Fatal("뒤 회차 정산이 앞 회차 기록을 바꿈")
	}
	v, _ := r.resetConfirmation("ARTEX", target.ID)
	if !v.ResetRequired || v.LastWritesJob != "job-a" || v.LastWritesObjects != 1 {
		t.Fatalf("앞 회차 미해결 쓰기가 사라짐: %+v", v)
	}
	// applyReset 수준에서도 같은 줄은 받지 않는다(재생이 거부한 줄을 만나지 않게).
	rec := r.records[target.ID]
	other := productDeclarationEvent{TargetID: target.ID, Action: declarationConfirmationWrites, Actor: productConfirmationActor,
		At: r.now().UTC(), TargetRevision: target.Revision, JobRef: "job-b", Objects: []productConfirmationObject{{Type: "note", IDUnknown: true}}}
	if _, err := applyReset(rec, r.resets[target.ID], r.resetJobs[target.ID], other); err == nil {
		t.Fatal("applyReset이 다른 회차 예약으로 미해결 쓰기를 덮음")
	}
	// 앞 회차가 자기 예약을 거두면(보내지 않음) 남은 쓰기가 없으니 뒤 회차가 예약할 수 있다.
	if err := r.settleConfirmationWrite(target.ID, "job-a", productConfirmationObject{Type: "note", IDUnknown: true, NotSent: true}); err != nil {
		t.Fatal(err)
	}
	if err := reserve("job-b", "note"); err != nil {
		t.Fatalf("남은 쓰기가 없는데 뒤 회차 예약을 거부: %v", err)
	}
	// 뒤 회차가 미해결이면 셋째 회차도 막히고, 리셋을 확인하면 열린다.
	if err := reserve("job-c", "note"); err == nil {
		t.Fatal("미해결 쓰기를 셋째 회차가 덮음")
	}
	v, _ = r.resetConfirmation("ARTEX", target.ID)
	if _, err := r.confirmReset("ARTEX", target.ID, true, v.LastWritesRef); err != nil {
		t.Fatal(err)
	}
	if err := reserve("job-c", "note"); err != nil {
		t.Fatalf("리셋 확인 뒤 새 회차 예약을 거부: %v", err)
	}
	want := r.resets[target.ID]
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.resets[target.ID]; got == nil || got.writesJob != "job-c" || got.writesRef != want.writesRef {
		t.Fatalf("재생 뒤: %+v", got)
	}
}

// 쓰기 기록의 회차가 아직 실행 중이면 리셋 확인을 받지 않는다. 확인 뒤에도 그 회차가 생성 요청을 더 보낼 수 있어서다.
func TestProductResetConfirmRefusedWhileRunActive(t *testing.T) {
	r, _, target, _ := declarationTestTarget(t)
	p := &productRuntime{targets: r}
	rec := r.records[target.ID]
	release, err := p.attach(rec, "job-a", func() {})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.recordConfirmationWrites(target.ID, "job-a", []productConfirmationObject{{Type: "note", IDUnknown: true}}); err != nil {
		t.Fatal(err)
	}
	v, _ := r.resetConfirmation("ARTEX", target.ID)
	if _, err := p.confirmReset("ARTEX", target.ID, true, v.LastWritesRef); err == nil || err.Error() != "reset_run_active" {
		t.Fatalf("실행 중 회차의 리셋 확인: %v", err)
	}
	if v, _ := r.resetConfirmation("ARTEX", target.ID); !v.ResetRequired {
		t.Fatal("거부한 확인이 상태를 바꿈")
	}
	release()
	if _, err := p.confirmReset("ARTEX", target.ID, true, v.LastWritesRef); err != nil {
		t.Fatalf("실행이 끝난 뒤 리셋 확인: %v", err)
	}
}
