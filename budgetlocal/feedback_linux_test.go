//go:build linux

package budgetlocal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestProtocolRejectsExactDuplicatesAndValidOversizeJSON(t *testing.T) {
	r := testRequest()
	r.MaxRequests = 1
	valid, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := strings.Replace(string(valid), `"max_requests":1`, `"max_requests":1,"max_requests":100`, 1)
	url := r
	url.RequestRef = "http://x"
	invalidRef, err := json.Marshal(url)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{[]byte(duplicate), invalidRef, append(append([]byte(nil), valid...), []byte(strings.Repeat(" ", maxMessageBytes+1-len(valid)))...)} {
		if !json.Valid(body) {
			t.Fatal("negative fixture must remain valid JSON")
		}
		if got, err := ParseRequest(body); !errors.Is(err, ErrInvalid) || got != (Request{}) {
			t.Fatalf("invalid protocol accepted: %+v err=%v", got, err)
		}
	}
	if _, err := ParseClientConfig([]byte(`{"socket_path":"/run/artex/approval.sock","server_uid":1000,"server_uid":2000}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("exact duplicate config UID accepted: %v", err)
	}
}

func rawApproval(t *testing.T, path string, request Request) wireResponse {
	t.Helper()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	var reply wireResponse
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	return reply
}

func TestCallbackOutcomeIsNotMisreportedAsRejection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record bool
		err    error
		code   string
	}{
		{"approved", true, nil, ""},
		{"definite_denial", false, ErrDenied, wireRejected},
		{"store_unknown", false, ErrUnavailable, wireUnknown},
		{"raw_failure", false, errors.New("private_db_failure"), wireUnknown},
		{"record_and_denial", true, ErrDenied, wireUnknown},
		{"record_and_failure", true, ErrUnavailable, wireUnknown},
		{"empty_success", false, nil, wireUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := socketPath(t)
			callback := func(ctx context.Context, uid uint32, r db.BudgetApprovalRequest, now time.Time) (db.BudgetApprovalRecord, error) {
				if !tc.record {
					return db.BudgetApprovalRecord{}, tc.err
				}
				record, err := (&testIssuer{}).approve(ctx, uid, r, now)
				if err != nil {
					return record, err
				}
				return record, tc.err
			}
			s, err := listen(context.Background(), path, uint32(os.Geteuid()), uint32(os.Geteuid()), callback, true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close(context.Background()) })
			wire := rawApproval(t, path, testRequest())
			if wire.Error != tc.code || tc.code != "" && wire.Response != (Response{}) {
				t.Fatalf("wire outcome=%+v want=%s", wire, tc.code)
			}
			response, err := submit(context.Background(), path, uint32(os.Geteuid()), testRequest(), true)
			if tc.code == "" {
				if err != nil || response.BudgetRef != "budget.1" {
					t.Fatalf("approved positive control: %+v %v", response, err)
				}
				return
			}
			want := ErrUnknown
			if tc.code == wireRejected {
				want = ErrDenied
			}
			if !errors.Is(err, want) || response != (Response{}) {
				t.Fatalf("client outcome=%+v err=%v want=%v", response, err, want)
			}
			if strings.Contains(err.Error(), "private_db_failure") {
				t.Fatal("raw callback failure exposed")
			}
		})
	}
}

func TestServerGrantValidationReturnsUnknownWithoutMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*db.BudgetApprovalRecord)
	}{
		{"request_ref", func(r *db.BudgetApprovalRecord) { r.RequestRef = "other.request" }},
		{"task_ref", func(r *db.BudgetApprovalRecord) { r.Grant.Key.TaskRef = "other.task" }},
		{"policy_ref", func(r *db.BudgetApprovalRecord) { r.Grant.Binding.PolicyRef = "other.policy" }},
		{"reset_ref", func(r *db.BudgetApprovalRecord) { r.Grant.Binding.ResetRef = "other.reset" }},
		{"max_requests", func(r *db.BudgetApprovalRecord) { r.Grant.MaxRequests++ }},
		{"valid_until", func(r *db.BudgetApprovalRecord) { r.Grant.ValidUntil = r.Grant.ValidUntil.Add(time.Second) }},
		{"valid_from", func(r *db.BudgetApprovalRecord) { r.Grant.ValidFrom = r.Grant.ValidFrom.Add(time.Second) }},
		{"state", func(r *db.BudgetApprovalRecord) { r.State = "prepared" }},
		{"budget_ref", func(r *db.BudgetApprovalRecord) { r.Grant.Key.BudgetRef = "" }},
		{"epoch", func(r *db.BudgetApprovalRecord) { r.Epoch = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := socketPath(t)
			issuer := &testIssuer{mutate: tc.change}
			s, err := listen(context.Background(), path, uint32(os.Geteuid()), uint32(os.Geteuid()), issuer.approve, true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close(context.Background()) })
			wire := rawApproval(t, path, testRequest())
			if wire.Error != wireUnknown || wire.Response != (Response{}) {
				t.Fatalf("server emitted invalid metadata: %+v", wire)
			}
		})
	}
}

func TestPeerDenialPrecedesReadingAnyRequest(t *testing.T) {
	path := socketPath(t)
	issuer := &testIssuer{}
	s, err := Listen(context.Background(), path, uint32(os.Geteuid()+1), uint32(os.Geteuid()+2), issuer.approve)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var reply wireResponse
	// No request bytes or EOF are sent: peer refusal must happen first.
	if err := json.NewDecoder(conn).Decode(&reply); err != nil || reply.Error != wireRejected {
		t.Fatalf("peer denial: %+v err=%v", reply, err)
	}
	if calls, _ := issuer.snapshot(); calls != 0 {
		t.Fatalf("peer reached callback: %d", calls)
	}
}

func TestServerReadDeadlineRefusesIncompleteRequest(t *testing.T) {
	path := socketPath(t)
	var calls atomic.Int32
	s, err := listenWithTimeout(context.Background(), path, uint32(os.Geteuid()), uint32(os.Geteuid()), func(context.Context, uint32, db.BudgetApprovalRequest, time.Time) (db.BudgetApprovalRecord, error) {
		calls.Add(1)
		return db.BudgetApprovalRecord{}, ErrDenied
	}, true, 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("server did not close incomplete request at its own deadline: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("incomplete request reached callback: %d", calls.Load())
	}
}

func silentAuthority(t *testing.T, handle func(context.Context, *net.UnixConn)) (string, <-chan struct{}, func()) {
	t.Helper()
	path := socketPath(t)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		_ = l.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	accepted := make(chan struct{})
	done := make(chan struct{})
	var mu sync.Mutex
	var active *net.UnixConn
	go func() {
		defer close(done)
		c, err := l.AcceptUnix()
		if err != nil {
			return
		}
		defer c.Close()
		mu.Lock()
		active = c
		mu.Unlock()
		close(accepted)
		if ctx.Err() == nil {
			handle(ctx, c)
		}
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			_ = l.Close()
			mu.Lock()
			if active != nil {
				_ = active.Close()
			}
			mu.Unlock()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("fake authority did not drain")
			}
		})
	}
	t.Cleanup(stop)
	return path, accepted, stop
}

func TestClientDeadlineAndCancellationBoundSilentPeer(t *testing.T) {
	for _, kind := range []string{"deadline", "cancellation"} {
		t.Run(kind, func(t *testing.T) {
			path, accepted, stop := silentAuthority(t, func(ctx context.Context, _ *net.UnixConn) { <-ctx.Done() })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := 40 * time.Millisecond
			if kind == "cancellation" {
				timeout = 5 * time.Second
			}
			result := make(chan error, 1)
			go func() {
				_, err := submitWithTimeout(ctx, path, uint32(os.Geteuid()), testRequest(), true, timeout)
				result <- err
			}()
			select {
			case <-accepted:
			case <-time.After(2 * time.Second):
				stop()
				t.Fatal("client did not connect")
			}
			if kind == "cancellation" {
				cancel()
			}
			select {
			case err := <-result:
				if !errors.Is(err, ErrUnknown) {
					t.Fatalf("silent peer result: %v", err)
				}
			case <-time.After(2 * time.Second):
				stop()
				<-result
				t.Fatal("client remained blocked after its deadline/cancellation")
			}
		})
	}
}

func TestCloseCancelsAndDrainsCallback(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := socketPath(t)
	entered := make(chan struct{})
	canceled := make(chan struct{})
	s, err := listenWithTimeout(parent, path, uint32(os.Geteuid()), uint32(os.Geteuid()), func(ctx context.Context, _ uint32, _ db.BudgetApprovalRequest, _ time.Time) (db.BudgetApprovalRecord, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		return db.BudgetApprovalRecord{}, ctx.Err()
	}, true, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = s.Close(context.Background()) })
	result := make(chan error, 1)
	go func() {
		_, err := submit(context.Background(), path, uint32(os.Geteuid()), testRequest(), true)
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("callback did not start")
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := s.Close(ctx); err != nil {
		cancel()
		t.Errorf("Close failed to drain callback: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("Close did not cancel callback")
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrUnknown) {
			t.Fatalf("canceled callback reported: %v", err)
		}
	case <-time.After(time.Second):
		cancel()
		t.Fatal("client did not drain")
	}
}

type socketDirectoryInfo struct {
	uid  uint32
	mode os.FileMode
}

func (socketDirectoryInfo) Name() string        { return "directory" }
func (socketDirectoryInfo) Size() int64         { return 0 }
func (s socketDirectoryInfo) Mode() os.FileMode { return os.ModeDir | s.mode }
func (socketDirectoryInfo) ModTime() time.Time  { return time.Time{} }
func (socketDirectoryInfo) IsDir() bool         { return true }
func (s socketDirectoryInfo) Sys() any          { return &syscall.Stat_t{Uid: s.uid} }

func TestSocketAncestorTrustIndependentOfDAC(t *testing.T) {
	for _, tc := range []struct {
		name string
		uid  uint32
		mode os.FileMode
		want bool
	}{
		{"trusted_root", 0, 0o755, true}, {"root_nonsticky_writable", 0, 0o777, false}, {"third_uid", 2000, 0o755, false}, {"root_sticky", 0, os.ModeSticky | 0o777, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lstat := func(path string) (os.FileInfo, error) {
				switch path {
				case "/safe/issuer":
					return socketDirectoryInfo{1000, 0o755}, nil
				case "/safe":
					return socketDirectoryInfo{tc.uid, tc.mode}, nil
				case "/":
					return socketDirectoryInfo{0, 0o755}, nil
				default:
					return nil, os.ErrNotExist
				}
			}
			if got := trustedSocketDirectoryWithLstat(filepath.Clean("/safe/issuer"), 1000, lstat); got != tc.want {
				t.Fatalf("ancestor trust=%t want=%t", got, tc.want)
			}
		})
	}
}
