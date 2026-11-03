//go:build linux && budgetuidacceptance

package budgetlocal_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Autumn-27/artex/budgetlocal"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/transmit/budget"
)

const (
	uidSmokeMarker             = "boxed-kernel-uids-v1"
	uidSmokeServer      uint32 = 41001
	uidSmokeOperator    uint32 = 41002
	uidSmokeModel       uint32 = 41003
	uidSmokeOther       uint32 = 41004
	uidSmokeFalseServer uint32 = 41005
	uidSmokeTimeout            = 12 * time.Second
)

type uidSmokeCredentials struct {
	UID, GID         uint32
	Groups           int
	CapabilitiesZero bool
}
type uidSmokeReport struct {
	Stage                    string
	Credentials              uidSmokeCredentials
	Listen                   bool
	Calls                    int64
	ObservedOperatorUID      uint32
	ObservedServerUID        uint32
	Granted                  bool
	Invalid, Denied, Unknown bool
}
type uidSmokeCase struct {
	name                                                 string
	server, operator, model, client, expectedServer      uint32
	listenRejected, allowed, peerPinMismatch, sameClient bool
}

// This manual entry never launches Docker. The explicit tag is absent from
// default go test; the caller must first place the static binary in an owned,
// network-disabled, read-only Docker box described by the companion document.
func TestBudgetRealUIDAcceptance(t *testing.T) {
	uidSmokeRequireBox(t, true)
	cases := []uidSmokeCase{
		{name: "operator", server: uidSmokeServer, operator: uidSmokeOperator, model: uidSmokeModel, client: uidSmokeOperator, expectedServer: uidSmokeServer, allowed: true},
		{name: "model", server: uidSmokeServer, operator: uidSmokeOperator, model: uidSmokeModel, client: uidSmokeModel, expectedServer: uidSmokeServer},
		{name: "wrong-operator", server: uidSmokeServer, operator: uidSmokeOperator, model: uidSmokeModel, client: uidSmokeOther, expectedServer: uidSmokeServer},
		{name: "server-peer-pin", server: uidSmokeServer, operator: uidSmokeOperator, model: uidSmokeModel, client: uidSmokeOperator, expectedServer: uidSmokeFalseServer, peerPinMismatch: true},
		{name: "root-operator", server: uidSmokeServer, operator: 0, model: uidSmokeModel, client: 0, expectedServer: uidSmokeServer, allowed: true},
		{name: "root-server", server: 0, operator: uidSmokeOperator, model: uidSmokeModel, listenRejected: true},
		{name: "server-operator-collision", server: uidSmokeServer, operator: uidSmokeServer, model: uidSmokeModel, listenRejected: true},
		{name: "server-model-collision", server: uidSmokeServer, operator: uidSmokeOperator, model: uidSmokeServer, listenRejected: true},
		{name: "operator-model-collision", server: uidSmokeServer, operator: uidSmokeOperator, model: uidSmokeOperator, listenRejected: true},
		{name: "client-server-collision", server: uidSmokeServer, operator: uidSmokeOperator, model: uidSmokeModel, client: uidSmokeServer, expectedServer: uidSmokeServer, sameClient: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join("/work", tc.name)
			if os.Mkdir(dir, 0o755) != nil || os.Chmod(dir, 0o755) != nil || os.Chown(dir, int(tc.server), int(tc.server)) != nil {
				t.Fatal("owned box directory provisioning failed")
			}
			path := filepath.Join(dir, "approval.sock")
			ctx, cancel := context.WithTimeout(context.Background(), uidSmokeTimeout)
			defer cancel()
			command := uidSmokeCommand(ctx, "server", tc.server, path, tc)
			stop, err := command.StdinPipe()
			if err != nil {
				t.Fatal("server control pipe unavailable")
			}
			output, err := command.StdoutPipe()
			if err != nil {
				t.Fatal("server report pipe unavailable")
			}
			var stderr bytes.Buffer
			command.Stderr = &stderr
			if command.Start() != nil {
				t.Fatal("boxed server credentials unavailable")
			}
			session := &uidSmokeSession{command: command, stop: stop, decoder: json.NewDecoder(output)}
			t.Cleanup(func() { _, _ = session.finish() })
			var ready uidSmokeReport
			if session.decoder.Decode(&ready) != nil {
				t.Fatal("server readiness unavailable")
			}
			uidSmokeAssertCredentials(t, ready.Credentials, tc.server)
			if tc.listenRejected {
				if ready.Stage != "rejected" || ready.Listen || !ready.Invalid || ready.Calls != 0 {
					t.Fatal("public listener UID guard accepted an invalid arrangement")
				}
				if command.Wait() != nil {
					t.Fatal("rejected listener process failed")
				}
				session.waited = true
				t.Logf("server_uid=%d callbacks=0 grants=0", ready.Credentials.UID)
				return
			}
			if ready.Stage != "ready" || !ready.Listen {
				t.Fatal("public listener rejected separated kernel UIDs")
			}
			if tc.peerPinMismatch {
				// Isolate the connected peer check: filesystem ownership now agrees with
				// the wrong pin while the already listening process retains its real UID.
				// Ownership changes occur only in the owned tmpfs and are restored before
				// asking that server to unlink its own socket.
				if os.Chown(path, int(tc.expectedServer), int(tc.expectedServer)) != nil ||
					os.Chown(dir, int(tc.expectedServer), int(tc.expectedServer)) != nil {
					t.Fatal("boxed peer-pin fixture unavailable")
				}
				t.Cleanup(func() {
					_ = os.Chown(dir, int(tc.server), int(tc.server))
					_ = os.Chown(path, int(tc.server), int(tc.server))
				})
			}
			client := uidSmokeCommand(ctx, "client", tc.client, path, tc)
			data, err := client.Output()
			if err != nil {
				t.Fatal("boxed client credentials or report unavailable")
			}
			var result uidSmokeReport
			if json.Unmarshal(data, &result) != nil || result.Stage != "client" {
				t.Fatal("client report invalid")
			}
			uidSmokeAssertCredentials(t, result.Credentials, tc.client)
			if result.ObservedServerUID != tc.server {
				t.Fatal("independent kernel server peer observation mismatch")
			}
			if tc.peerPinMismatch {
				if !result.Unknown || result.Granted {
					t.Fatal("client accepted an actual peer different from its configured UID")
				}
				if os.Chown(dir, int(tc.server), int(tc.server)) != nil || os.Chown(path, int(tc.server), int(tc.server)) != nil {
					t.Fatal("boxed directory ownership restore failed")
				}
			} else if tc.sameClient {
				if !result.Invalid || result.Granted {
					t.Fatal("public client accepted its own UID as server")
				}
			} else if tc.allowed {
				if !result.Granted || result.Invalid || result.Denied || result.Unknown {
					t.Fatal("authorized kernel operator did not receive synthetic metadata")
				}
			} else if result.Granted || !result.Denied && !result.Unknown {
				// A nonoperator can see ECONNRESET instead of a stable rejected response.
				t.Fatal("nonoperator received metadata or an unexpected result")
			}
			final, err := session.finish()
			if err != nil || final.Stage != "closed" {
				t.Fatal("owned server did not close cleanly")
			}
			expectedCalls := int64(0)
			if tc.allowed {
				expectedCalls = 1
			}
			if final.Calls != expectedCalls {
				t.Fatal("unexpected synthetic issuance callback count")
			}
			if tc.allowed && final.ObservedOperatorUID != tc.client {
				t.Fatal("callback did not receive the real kernel operator UID")
			}
			grants := 0
			if result.Granted {
				grants = 1
			}
			t.Logf("server_uid=%d client_uid=%d peer_server_uid=%d callback_peer_uid=%d callbacks=%d grants=%d",
				ready.Credentials.UID, result.Credentials.UID, result.ObservedServerUID, final.ObservedOperatorUID, final.Calls, grants)
		})
	}
}

// Process entry is selected explicitly by the coordinator's exec arguments.
// Credential changes use syscall.Credential in the owned container child; there
// is no host useradd/chown/setuid action or private allowSameUID call.
func TestBudgetRealUIDAcceptanceProcess(t *testing.T) {
	uidSmokeRequireBox(t, false)
	role := os.Getenv("ARTEX_BUDGET_UID_ROLE")
	tc := uidSmokeCase{
		operator:       uidSmokeEnvUID(t, "ARTEX_BUDGET_UID_OPERATOR"),
		model:          uidSmokeEnvUID(t, "ARTEX_BUDGET_UID_MODEL"),
		expectedServer: uidSmokeEnvUID(t, "ARTEX_BUDGET_UID_EXPECTED_SERVER"),
	}
	path := os.Getenv("ARTEX_BUDGET_UID_SOCKET")
	if filepath.Dir(filepath.Dir(path)) != "/work" {
		t.Fatal("boxed socket path required")
	}
	var report uidSmokeReport
	switch role {
	case "server":
		report = uidSmokeServe(path, tc)
	case "client":
		report = uidSmokeSubmit(path, tc)
	default:
		t.Fatal("manual subprocess entry required")
	}
	if json.NewEncoder(os.Stdout).Encode(report) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func uidSmokeCommand(ctx context.Context, role string, uid uint32, path string, tc uidSmokeCase) *exec.Cmd {
	command := exec.CommandContext(ctx, "/smoke.test", "-test.run=^TestBudgetRealUIDAcceptanceProcess$")
	command.Env = []string{
		"ARTEX_BUDGET_UID_ACCEPTANCE=" + uidSmokeMarker,
		"ARTEX_BUDGET_UID_ROLE=" + role,
		"ARTEX_BUDGET_UID_SOCKET=" + path,
		"ARTEX_BUDGET_UID_OPERATOR=" + strconv.FormatUint(uint64(tc.operator), 10),
		"ARTEX_BUDGET_UID_MODEL=" + strconv.FormatUint(uint64(tc.model), 10),
		"ARTEX_BUDGET_UID_EXPECTED_SERVER=" + strconv.FormatUint(uint64(tc.expectedServer), 10),
		"TMPDIR=/work", "GOMAXPROCS=2",
	}
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
		Uid: uid, Gid: uid, Groups: []uint32{},
	}}
	return command
}
func uidSmokeEnvUID(t *testing.T, name string) uint32 {
	t.Helper()
	uid, err := strconv.ParseUint(os.Getenv(name), 10, 32)
	if err != nil {
		t.Fatal("subprocess UID input invalid")
	}
	return uint32(uid)
}

func uidSmokeServe(path string, tc uidSmokeCase) uidSmokeReport {
	var calls atomic.Int64
	var operator atomic.Uint32
	approve := func(_ context.Context, uid uint32, request db.BudgetApprovalRequest, _ time.Time) (db.BudgetApprovalRecord, error) {
		calls.Add(1)
		operator.Store(uid)
		if uid != tc.operator || request.IssuerRef != fmt.Sprintf("uid.%d", uid) {
			return db.BudgetApprovalRecord{}, budgetlocal.ErrDenied
		}
		return db.BudgetApprovalRecord{
			RequestRef: request.RequestRef, IssuerRef: request.IssuerRef, Epoch: "synthetic-epoch", State: "ready",
			Grant: budget.Grant{Key: budget.Key{TaskRef: request.TaskRef, BudgetRef: "synthetic-budget"},
				Binding: request.Binding, MaxRequests: request.MaxRequests,
				ValidFrom:  request.ValidFrom.Round(0).UTC().Truncate(time.Microsecond),
				ValidUntil: request.ValidUntil.Round(0).UTC().Truncate(time.Microsecond)},
		}, nil
	}
	server, err := budgetlocal.Listen(context.Background(), path, tc.operator, tc.model, approve)
	report := uidSmokeReport{Stage: "rejected", Credentials: uidSmokeCredentialsNow(), Invalid: errors.Is(err, budgetlocal.ErrInvalid)}
	if err != nil {
		return report
	}
	report.Stage, report.Listen = "ready", true
	if json.NewEncoder(os.Stdout).Encode(report) != nil {
		os.Exit(2)
	}
	// The coordinator owns the write end. Close/EOF is the normal shutdown signal.
	var one [1]byte
	_, _ = os.Stdin.Read(one[:])
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if server.Close(ctx) != nil {
		return uidSmokeReport{Stage: "close-failed"}
	}
	report.Stage, report.Calls, report.ObservedOperatorUID = "closed", calls.Load(), operator.Load()
	return report
}
func uidSmokeSubmit(path string, tc uidSmokeCase) uidSmokeReport {
	report := uidSmokeReport{Stage: "client", Credentials: uidSmokeCredentialsNow()}
	// This independent SO_PEERCRED observation records the actual connected server
	// UID. It does not replace Submit's own public pinned-peer checks.
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return uidSmokeReport{Stage: "probe-failed"}
	}
	socket, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return uidSmokeReport{Stage: "probe-failed"}
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		_ = conn.Close()
		return uidSmokeReport{Stage: "probe-failed"}
	}
	var peerErr error
	err = raw.Control(func(fd uintptr) {
		peer, readErr := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		peerErr = readErr
		if readErr == nil {
			report.ObservedServerUID = peer.Uid
		}
	})
	_ = conn.Close()
	if err != nil || peerErr != nil {
		return uidSmokeReport{Stage: "probe-failed"}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	request := budgetlocal.Request{RequestRef: "synthetic-request", TaskRef: "synthetic-task", PolicyRef: "synthetic-policy",
		ResetRef: "synthetic-reset", MaxRequests: 1, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := budgetlocal.Submit(ctx, path, tc.expectedServer, request)
	report.Granted = err == nil && response.State == "ready" && response.BudgetRef == "synthetic-budget"
	report.Invalid, report.Denied, report.Unknown = errors.Is(err, budgetlocal.ErrInvalid), errors.Is(err, budgetlocal.ErrDenied), errors.Is(err, budgetlocal.ErrUnknown)
	return report
}

type uidSmokeSession struct {
	command *exec.Cmd
	stop    io.WriteCloser
	decoder *json.Decoder
	once    sync.Once
	waited  bool
	report  uidSmokeReport
	err     error
}

func (s *uidSmokeSession) finish() (uidSmokeReport, error) {
	s.once.Do(func() {
		_ = s.stop.Close()
		if s.waited {
			return
		}
		decodeErr := s.decoder.Decode(&s.report)
		waitErr := s.command.Wait()
		if decodeErr != nil {
			s.err = decodeErr
		} else {
			s.err = waitErr
		}
		s.waited = true
	})
	return s.report, s.err
}

func uidSmokeRequireBox(t *testing.T, coordinator bool) {
	t.Helper()
	if os.Getenv("ARTEX_BUDGET_UID_ACCEPTANCE") != uidSmokeMarker {
		t.Fatal("explicit boxed UID smoke marker required")
	}
	marker, err := os.Lstat("/.dockerenv")
	if err != nil || !marker.Mode().IsRegular() {
		t.Fatal("Docker box marker required")
	}
	var fs syscall.Statfs_t
	const tmpfsMagic = 0x01021994
	if syscall.Statfs("/work", &fs) != nil || fs.Type != tmpfsMagic {
		t.Fatal("owned tmpfs work directory required")
	}
	if syscall.Statfs("/", &fs) != nil || fs.Flags&1 == 0 {
		t.Fatal("read-only boxed root required")
	}
	info, err := os.Lstat("/work")
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o755 {
		t.Fatal("owned tmpfs permissions invalid")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		t.Fatal("owned tmpfs root ownership required")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil || !strings.Contains(string(status), "NoNewPrivs:\t1") {
		t.Fatal("no-new-privileges box required")
	}
	if coordinator {
		if os.Geteuid() != 0 || os.Getpid() != 1 {
			t.Fatal("owned container root coordinator required")
		}
		caps := uidSmokeStatusField(string(status), "CapEff:")
		// CHOWN(bit0), SETGID(bit6), SETUID(bit7) only, all within this box.
		n, err := strconv.ParseUint(caps, 16, 64)
		if err != nil || n != 0xc1 {
			t.Fatal("minimal boxed credential capabilities required")
		}
	}
}
func uidSmokeCredentialsNow() uidSmokeCredentials {
	groups, err := os.Getgroups()
	if err != nil {
		return uidSmokeCredentials{Groups: -1}
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return uidSmokeCredentials{Groups: -1}
	}
	caps, err := strconv.ParseUint(uidSmokeStatusField(string(status), "CapEff:"), 16, 64)
	if err != nil {
		return uidSmokeCredentials{Groups: -1}
	}
	return uidSmokeCredentials{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()), Groups: len(groups), CapabilitiesZero: caps == 0}
}
func uidSmokeStatusField(status, name string) string {
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, name) {
			return strings.TrimSpace(strings.TrimPrefix(line, name))
		}
	}
	return ""
}
func uidSmokeAssertCredentials(t *testing.T, got uidSmokeCredentials, uid uint32) {
	t.Helper()
	if got.UID != uid || got.GID != uid || got.Groups != 0 || uid != 0 && !got.CapabilitiesZero {
		t.Fatal("actual child kernel credentials did not match isolated role")
	}
}
