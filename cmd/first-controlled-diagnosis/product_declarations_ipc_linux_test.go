//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e3contract"
)

// 실제 Unix 소켓 전송으로 선언을 저장·조회한다. 전송 계층은 null·모르는 칸을 받지 않고 같은 요청으로 응답을 다시
// 검증하므로, 빈 목록·빈 선언·형식 오류 칸이 그 규칙을 지나는지 여기서 확인한다. 가장 큰 선언도 한 프레임에 들어가야 한다.
func TestProductDeclarationControlOverUnixTransport(t *testing.T) {
	r, _, target, _ := declarationTestTarget(t)
	dir, err := os.MkdirTemp("", "jxd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "p.sock")
	server, err := diagnosisprep.ListenUnix(context.Background(), socket, productPreparationEndpoint{product: &productRuntime{targets: r}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	client, err := diagnosisprep.NewUnixClient(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	call := func(p *diagnosisprep.ProductRequest) diagnosisprep.ProductResponse {
		t.Helper()
		resp, err := client.Call(t.Context(), diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "product-control", Principal: "ARTEX", Command: diagnosisprep.ProductControl, Product: p})
		if err != nil || resp.Error != "" || resp.Product == nil {
			t.Fatalf("%s over IPC: %v %+v", p.Operation, err, resp)
		}
		return *resp.Product
	}
	if got := call(&diagnosisprep.ProductRequest{Operation: "declaration", TargetID: target.ID}); got.Declaration == nil || got.Declaration.State != diagnosisprep.DeclarationNone {
		t.Fatalf("빈 선언: %+v", got)
	}
	in := declarationTestInput()
	in.Types[0].OwnerOnlyFields = []string{}
	got := call(&diagnosisprep.ProductRequest{Operation: "declaration_put", TargetID: target.ID, ExpectedRevision: target.Revision, Declaration: &in})
	if got.Reason != "" || got.Declaration == nil || got.Declaration.State != diagnosisprep.DeclarationActive {
		t.Fatalf("저장: %+v", got)
	}
	bad := declarationTestInput()
	// IPC는 null을 받지 않는다. 공개 API는 빈 필드 목록을 []로 바꿔 보낸다(server.productDeclarationRequest).
	bad.Types[0].OwnerOnlyFields = []string{}
	bad.Types[1].Create.BodyTemplate = `{"title":"t","token":"hunter2-secret-value"}`
	got = call(&diagnosisprep.ProductRequest{Operation: "declaration_put", TargetID: target.ID, ExpectedRevision: target.Revision, Declaration: &bad})
	if got.Reason != "invalid_declaration" || got.Field != "types[0].create.body_template" || got.Declaration != nil {
		t.Fatalf("형식 오류: %+v", got)
	}
	got = call(&diagnosisprep.ProductRequest{Operation: "declaration_put", TargetID: target.ID, ExpectedRevision: target.Revision + 1, Declaration: &in})
	if got.Reason != "declaration_target_revision_mismatch" {
		t.Fatalf("판 불일치: %+v", got)
	}
	// 인코딩 상한 가까운 선언도 요청·응답 프레임에 들어간다.
	big := declarationTestInput()
	base := big.Types[1]
	big.Types = nil
	for i := 0; i < e3contract.MaxDeclarationTypes; i++ {
		ty := base
		ty.Type = "t" + strings.Repeat("x", i+1)
		ty.Create.BodyTemplate = `{"title":"` + strings.Repeat("y", 1200) + `"}`
		big.Types = append(big.Types, ty)
	}
	got = call(&diagnosisprep.ProductRequest{Operation: "declaration_put", TargetID: target.ID, ExpectedRevision: target.Revision, Declaration: &big})
	if got.Reason != "" || got.Declaration == nil || len(got.Declaration.Declaration.Types) != e3contract.MaxDeclarationTypes {
		t.Fatalf("큰 선언: reason=%s field=%s", got.Reason, got.Field)
	}
}
