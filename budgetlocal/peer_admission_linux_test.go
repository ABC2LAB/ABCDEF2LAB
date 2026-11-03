//go:build linux

package budgetlocal

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestDeniedPeerDoesNotNeedAnIssuanceSlot(t *testing.T) {
	path := socketPath(t)
	issuer := &testIssuer{}
	s, err := listen(context.Background(), path, uint32(os.Geteuid()+1), uint32(os.Geteuid()+2), issuer.approve, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	// Keep every issuance slot occupied throughout the denial checks.
	for i := 0; i < maxInFlight; i++ {
		s.sem <- struct{}{}
	}
	t.Cleanup(func() {
		for i := 0; i < maxInFlight; i++ {
			<-s.sem
		}
	})
	for i := 0; i < 2*maxInFlight; i++ {
		conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		var reply wireResponse
		err = json.NewDecoder(conn).Decode(&reply)
		conn.Close()
		if err != nil || reply.Error != wireRejected || reply.Response != (Response{}) {
			t.Fatalf("denial %d used issuance capacity: %+v err=%v", i, reply, err)
		}
	}
	if len(s.sem) != maxInFlight {
		t.Fatal("rejected peers changed issuance occupancy")
	}
	if calls, _ := issuer.snapshot(); calls != 0 {
		t.Fatalf("rejected peers reached callback: %d", calls)
	}
}

func TestPeerRejectionDoesNotWaitForSocketCapacity(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fds[0]), "reject-peer")
	peer := os.NewFile(uintptr(fds[1]), "silent-peer")
	defer peer.Close()
	conn, err := net.FileConn(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	unix := conn.(*net.UnixConn)
	defer unix.Close()
	raw, err := unix.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var fillErr error
	if err := raw.Control(func(fd uintptr) {
		fillErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, 1024)
		if fillErr != nil {
			return
		}
		for {
			_, fillErr = syscall.SendmsgN(int(fd), []byte{0}, nil, nil, syscall.MSG_DONTWAIT|syscall.MSG_NOSIGNAL)
			if fillErr != nil {
				return
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(fillErr, syscall.EAGAIN) {
		t.Fatalf("could not fill socket output buffer: %v", fillErr)
	}
	done := make(chan struct{})
	go func() {
		rejectPeer(unix)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		unix.Close()
		<-done
		t.Fatal("peer rejection waited for a reader")
	}
}

func TestOperatorIssuanceSlotsRemainBoundedAndRecover(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := socketPath(t)
	entered := make(chan struct{}, maxInFlight)
	release := make(chan struct{})
	var calls atomic.Int32
	issuer := &testIssuer{}
	s, err := listenWithTimeout(ctx, path, uint32(os.Geteuid()), uint32(os.Geteuid()),
		func(ctx context.Context, uid uint32, r db.BudgetApprovalRequest, now time.Time) (db.BudgetApprovalRecord, error) {
			calls.Add(1)
			entered <- struct{}{}
			select {
			case <-release:
				return issuer.approve(ctx, uid, r, now)
			case <-ctx.Done():
				return db.BudgetApprovalRecord{}, ctx.Err()
			}
		}, true, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = s.Close(context.Background()) })
	results := make(chan error, maxInFlight)
	for i := 0; i < maxInFlight; i++ {
		go func() {
			_, err := submit(ctx, path, uint32(os.Geteuid()), testRequest(), true)
			results <- err
		}()
	}
	for i := 0; i < maxInFlight; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("operator did not enter issuance callback")
		}
	}
	extraCtx, extraCancel := context.WithTimeout(ctx, time.Second)
	defer extraCancel()
	if _, err := submit(extraCtx, path, uint32(os.Geteuid()), testRequest(), true); !errors.Is(err, ErrUnknown) {
		t.Fatalf("full operator pool changed overload outcome: %v", err)
	}
	if got := calls.Load(); got != maxInFlight {
		t.Fatalf("issuance cap exceeded: %d", got)
	}
	close(release)
	for i := 0; i < maxInFlight; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("admitted operator failed: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("operator did not drain")
		}
	}
	s.wg.Wait()
	if _, err := submit(ctx, path, uint32(os.Geteuid()), testRequest(), true); err != nil {
		t.Fatalf("released slot was not reusable: %v", err)
	}
	if got := calls.Load(); got != maxInFlight+1 {
		t.Fatalf("callback count after recovery: %d", got)
	}
}
