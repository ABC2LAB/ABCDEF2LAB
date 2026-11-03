//go:build linux

package budgetlocal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/transmit/budget"
)

type testIssuer struct {
	mu      sync.Mutex
	calls   int
	request db.BudgetApprovalRequest
	err     error
	mutate  func(*db.BudgetApprovalRecord)
}

func (f *testIssuer) approve(_ context.Context, _ uint32, request db.BudgetApprovalRequest, _ time.Time) (db.BudgetApprovalRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.request = request
	if f.err != nil {
		return db.BudgetApprovalRecord{}, f.err
	}
	record := db.BudgetApprovalRecord{
		RequestRef: request.RequestRef, IssuerRef: request.IssuerRef,
		Epoch: "epoch.1", State: "ready",
		Grant: budget.Grant{Key: budget.Key{TaskRef: request.TaskRef, BudgetRef: "budget.1"},
			Binding: request.Binding, MaxRequests: request.MaxRequests,
			ValidFrom: request.ValidFrom, ValidUntil: request.ValidUntil},
	}
	if f.mutate != nil {
		f.mutate(&record)
	}
	return record, nil
}

func (f *testIssuer) snapshot() (int, db.BudgetApprovalRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.request
}

func testRequest() Request {
	now := time.Now().UTC().Truncate(time.Second)
	return Request{RequestRef: "request.1", TaskRef: "task.1", PolicyRef: "policy.1",
		ResetRef: "reset.1", MaxRequests: 3,
		ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour)}
}

func socketPath(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("socket deployment requires a non-root server UID")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "approve.sock")
}

func TestLocalIssuerRejectsSameUIDAndUnsafePaths(t *testing.T) {
	for _, uids := range [][3]uint32{{0, 1001, 1002}, {1000, 1000, 1002}, {1000, 1001, 1000}, {1000, 1001, 1001}, {1000, 1001, 0}} {
		if validAuthorityUIDs(uids[0], uids[1], uids[2]) {
			t.Fatalf("unsafe UID arrangement accepted: %v", uids)
		}
	}
	ctx := context.Background()
	issuer := &testIssuer{}
	path := socketPath(t)
	if s, err := Listen(ctx, path, uint32(os.Geteuid()), uint32(os.Geteuid()+2), issuer.approve); !errors.Is(err, ErrInvalid) || s != nil {
		t.Fatalf("same server/model UID accepted: %v", err)
	}
	if s, err := Listen(ctx, path, uint32(os.Geteuid()+1), uint32(os.Geteuid()+2), nil); !errors.Is(err, ErrInvalid) || s != nil {
		t.Fatalf("missing authorization callback accepted: %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o777); err != nil {
		t.Fatal(err)
	}
	if s, err := Listen(ctx, path, uint32(os.Geteuid()+1), uint32(os.Geteuid()+2), issuer.approve); !errors.Is(err, ErrUnavailable) || s != nil {
		t.Fatalf("writable socket directory accepted: %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("do not remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := Listen(ctx, path, uint32(os.Geteuid()+1), uint32(os.Geteuid()+2), issuer.approve); !errors.Is(err, ErrUnavailable) || s != nil {
		t.Fatalf("preexisting path replaced: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "do not remove" {
		t.Fatalf("preexisting file changed: %v", err)
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(parent, "socket-dir")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if s, err := Listen(ctx, filepath.Join(child, "approve.sock"),
		uint32(os.Geteuid()+1), uint32(os.Geteuid()+2), issuer.approve); !errors.Is(err, ErrUnavailable) || s != nil {
		t.Fatalf("writable ancestor accepted: %v", err)
	}
	link := filepath.Join(t.TempDir(), "linked-dir")
	if err := os.Symlink(child, link); err != nil {
		t.Fatal(err)
	}
	if s, err := Listen(ctx, filepath.Join(link, "approve.sock"),
		uint32(os.Geteuid()+1), uint32(os.Geteuid()+2), issuer.approve); !errors.Is(err, ErrUnavailable) || s != nil {
		t.Fatalf("symlink ancestor accepted: %v", err)
	}
}

func TestLocalIssuerPeerUIDDeniedBeforeValidation(t *testing.T) {
	path := socketPath(t)
	issuer := &testIssuer{}
	var called atomic.Int32
	server, err := Listen(context.Background(), path, uint32(os.Geteuid()+1), uint32(os.Geteuid()+2),
		func(ctx context.Context, uid uint32, request db.BudgetApprovalRequest, now time.Time) (db.BudgetApprovalRecord, error) {
			called.Add(1)
			return issuer.approve(ctx, uid, request, now)
		})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o666 {
		t.Fatalf("peer-checked local socket missing: %v", err)
	}
	if _, err := submit(context.Background(), path, uint32(os.Geteuid()), testRequest(), true); !errors.Is(err, ErrDenied) && !errors.Is(err, ErrUnknown) {
		t.Fatalf("wrong UID issued: %v", err)
	}
	calls, _ := issuer.snapshot()
	if called.Load() != 0 || calls != 0 {
		t.Fatalf("wrong UID reached authorization callback: %d/%d", called.Load(), calls)
	}
}

func TestLocalIssuerDerivesIssuerAndRejectsUnverifiedInput(t *testing.T) {
	path := socketPath(t)
	issuer := &testIssuer{}
	var allow atomic.Bool
	allow.Store(true)
	server, err := listen(context.Background(), path, uint32(os.Geteuid()), uint32(os.Geteuid()),
		func(ctx context.Context, uid uint32, request db.BudgetApprovalRequest, now time.Time) (db.BudgetApprovalRecord, error) {
			if uid != uint32(os.Geteuid()) || request.TaskRef != "task.1" || !allow.Load() {
				return db.BudgetApprovalRecord{}, ErrDenied
			}
			return issuer.approve(ctx, uid, request, now)
		}, true) // test-only path; exported Listen rejects same-UID deployment
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())
	request := testRequest()
	if _, err := Submit(context.Background(), path, uint32(os.Geteuid()), request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("same-UID server socket accepted by CLI: %v", err)
	}
	if _, err := Submit(context.Background(), path, uint32(os.Geteuid()+1), request); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("model-owned fake endpoint matched trusted server UID: %v", err)
	}
	response, err := submit(context.Background(), path, uint32(os.Geteuid()), request, true)
	if err != nil || response.BudgetRef != "budget.1" || response.State != "ready" ||
		response.RequestRef != request.RequestRef || response.Epoch != "epoch.1" {
		t.Fatalf("local issuance response: %+v, %v", response, err)
	}
	wantIssuer := fmt.Sprintf("uid.%d", os.Geteuid())
	_, issued := issuer.snapshot()
	if issued.IssuerRef != wantIssuer || issued.RequestRef != request.RequestRef ||
		issued.Binding.PolicyRef != request.PolicyRef {
		t.Fatalf("issuer claim came from request: %+v", issued)
	}
	allow.Store(false)
	if _, err := submit(context.Background(), path, uint32(os.Geteuid()), request, true); !errors.Is(err, ErrDenied) {
		t.Fatalf("authorization denial accepted: %v", err)
	}
}

func TestLocalIssuerStrictBoundedProtocol(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"approval_request_ref":"x","task_ref":"task.1","policy_ref":"policy.1","reset_ref":"reset.1","max_requests":1,"valid_from":"2026-01-01T00:00:00Z","valid_until":"2026-01-02T00:00:00Z","issuer_ref":"forged"}`),
		[]byte(`{"approval_request_ref":"http://x"}`),
		[]byte(`{"approval_request_ref":"request.1","task_ref":"task.1","policy_ref":"policy.1","reset_ref":"reset.1","MAX_REQUESTS":1,"max_requests":100,"valid_from":"2026-01-01T00:00:00Z","valid_until":"2027-01-01T00:00:00Z"}`),
		[]byte(`{"approval_request_ref":"request.1","task_ref":"task.1","policy_ref":"policy.1","reset_ref":"reset.1","max_requests":1,"max_requeſts":100,"valid_from":"2026-01-01T00:00:00Z","valid_until":"2027-01-01T00:00:00Z"}`),
		[]byte(`{}{}`),
		make([]byte, maxMessageBytes+1),
	} {
		if _, err := ParseRequest(raw); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid protocol accepted: %v", err)
		}
	}
	path := socketPath(t)
	issuer := &testIssuer{}
	server, err := listen(context.Background(), path, uint32(os.Geteuid()), uint32(os.Geteuid()), issuer.approve, true)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte(`{"issuer_ref":"forged"}`))
	_ = conn.(*net.UnixConn).CloseWrite()
	var result wireResponse
	if err := json.NewDecoder(conn).Decode(&result); err != nil || result.Error != "rejected" {
		t.Fatalf("forged wire input: %+v, %v", result, err)
	}
	_ = conn.Close()
	calls, _ := issuer.snapshot()
	if calls != 0 {
		t.Fatalf("malformed input reached issuer: %d", calls)
	}
}

func TestClientConfigRequiresPinnedServerIdentity(t *testing.T) {
	if got, err := ParseClientConfig([]byte(`{"socket_path":"/run/artex/approval.sock","server_uid":1000}`)); err != nil ||
		got.ServerUID != 1000 || got.SocketPath != "/run/artex/approval.sock" {
		t.Fatalf("valid pinned identity rejected: %+v, %v", got, err)
	}
	for _, raw := range [][]byte{
		[]byte(`{"socket_path":"relative.sock","server_uid":1000}`),
		[]byte(`{"socket_path":"/run/artex/approval.sock","server_uid":0}`),
		[]byte(`{"socket_path":"/run/artex/approval.sock","server_uid":1000,"SERVER_UID":2000}`),
		[]byte(`{"socket_path":"/run/artex/approval.sock","server_uid":1000,"ſerver_uid":2000}`),
		[]byte(`{"socket_path":"/run/artex/approval.sock","server_uid":1000,"issuer_ref":"forged"}`),
	} {
		if _, err := ParseClientConfig(raw); !errors.Is(err, ErrInvalid) {
			t.Fatalf("untrusted config accepted: %s, %v", raw, err)
		}
	}
}

func TestLocalIssuerRejectsMismatchedReadyRecord(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*db.BudgetApprovalRecord)
	}{
		{"issuer", func(r *db.BudgetApprovalRecord) { r.IssuerRef = "uid.other" }},
		{"request", func(r *db.BudgetApprovalRecord) { r.RequestRef = "other" }},
		{"task", func(r *db.BudgetApprovalRecord) { r.Grant.Key.TaskRef = "other" }},
		{"policy", func(r *db.BudgetApprovalRecord) { r.Grant.Binding.PolicyRef = "other" }},
		{"reset", func(r *db.BudgetApprovalRecord) { r.Grant.Binding.ResetRef = "other" }},
		{"limit", func(r *db.BudgetApprovalRecord) { r.Grant.MaxRequests++ }},
		{"period", func(r *db.BudgetApprovalRecord) { r.Grant.ValidUntil = r.Grant.ValidUntil.Add(time.Second) }},
		{"valid_from", func(r *db.BudgetApprovalRecord) { r.Grant.ValidFrom = r.Grant.ValidFrom.Add(time.Second) }},
		{"state", func(r *db.BudgetApprovalRecord) { r.State = "prepared" }},
		{"budget_ref", func(r *db.BudgetApprovalRecord) { r.Grant.Key.BudgetRef = "" }},
		{"epoch", func(r *db.BudgetApprovalRecord) { r.Epoch = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := socketPath(t)
			issuer := &testIssuer{mutate: tc.mutate}
			server, err := listen(context.Background(), path, uint32(os.Geteuid()), uint32(os.Geteuid()), issuer.approve, true)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close(context.Background())
			if response, err := submit(context.Background(), path, uint32(os.Geteuid()), testRequest(), true); !errors.Is(err, ErrUnknown) || response != (Response{}) {
				t.Fatalf("mismatched ready record exposed: %+v, %v", response, err)
			}
		})
	}
}

func TestLocalIssuerClosePreservesReplacementPath(t *testing.T) {
	path := socketPath(t)
	server, err := listen(context.Background(), path, uint32(os.Geteuid()), uint32(os.Geteuid()),
		(&testIssuer{}).approve, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("replacement path was not detected: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "replacement" {
		t.Fatalf("replacement path removed: %v", err)
	}
}
