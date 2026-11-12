//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/diagnosisprep"
)

// 실제 Unix 소켓 전송(ListenUnix·UnixClient)으로 계정 자격을 넣고 목록을 읽는다. 전송 계층이 같은 요청으로 응답을 다시
// 검증하므로, 준비 쪽이 요청을 훼손하면 응답 프레임 없이 연결이 닫혀 여기서 실패한다.
func TestProductAccountControlOverUnixTransport(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	dir, err := os.MkdirTemp("", "jxa")
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
		if err != nil || resp.Error != "" || resp.Product == nil || resp.Product.Reason != "" {
			t.Fatalf("%s over IPC: %v %+v", p.Operation, err, resp)
		}
		return *resp.Product
	}
	call(&diagnosisprep.ProductRequest{Operation: "account_register", TargetID: target.ID, Account: &diagnosisprep.AccountRequest{Label: "tester-a"}})
	got := call(&diagnosisprep.ProductRequest{Operation: "account_credential_set", TargetID: target.ID, Account: &diagnosisprep.AccountRequest{Label: "tester-a"},
		Credential: &diagnosisprep.AccountCredential{Username: strings.Repeat("<", 256), Password: strings.Repeat("\x01", 512)}})
	if got.Account == nil || !got.Account.CredentialConfigured {
		t.Fatalf("credential over IPC: %+v", got)
	}
	list := call(&diagnosisprep.ProductRequest{Operation: "accounts", TargetID: target.ID})
	if len(list.Accounts) != 1 || !list.Accounts[0].CredentialConfigured || list.AccountStatement == "" {
		t.Fatalf("list over IPC: %+v", list)
	}
}
