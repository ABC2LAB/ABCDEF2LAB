package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
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
		Types: []e3contract.TypeDeclaration{{Type: "post", Sharing: e3contract.SharingShared}, note}}
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
		TargetRevision: v.TargetRevision, Declaration: *v.Declaration}
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
