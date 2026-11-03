//go:build linux

package budgetlocal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Submit sends one explicit request to the local authority. The socket must
// belong to the trusted server installation; a response is metadata, not an
// execution capability or proof that any HTTP request was sent.
func Submit(ctx context.Context, path string, expectedServerUID uint32, request Request) (Response, error) {
	return submit(ctx, path, expectedServerUID, request, false)
}

func submit(ctx context.Context, path string, expectedServerUID uint32, request Request, allowSameUID bool) (Response, error) {
	return submitWithTimeout(ctx, path, expectedServerUID, request, allowSameUID, requestTimeout)
}

func submitWithTimeout(ctx context.Context, path string, expectedServerUID uint32, request Request, allowSameUID bool, timeout time.Duration) (Response, error) {
	if ctx == nil || timeout <= 0 || !filepath.IsAbs(path) || !validRequest(request) ||
		expectedServerUID == 0 || expectedServerUID == ^uint32(0) ||
		(!allowSameUID && expectedServerUID == uint32(os.Geteuid())) {
		return Response{}, ErrInvalid
	}
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	parent, err := os.Lstat(dir)
	if err != nil || !parent.IsDir() || parent.Mode().Perm() != 0o755 ||
		parent.Mode()&os.ModeSymlink != 0 {
		return Response{}, ErrUnavailable
	}
	owner, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != expectedServerUID {
		return Response{}, ErrUnavailable
	}
	if !trustedSocketDirectory(dir, owner.Uid) {
		return Response{}, ErrUnavailable
	}
	socket, err := os.Lstat(path)
	if err != nil || socket.Mode()&os.ModeSocket == 0 || socket.Mode().Perm() != 0o666 {
		return Response{}, ErrUnavailable
	}
	socketOwner, ok := socket.Sys().(*syscall.Stat_t)
	if !ok || socketOwner.Uid != owner.Uid {
		return Response{}, ErrUnavailable
	}
	data, err := json.Marshal(request)
	if err != nil || len(data) > maxMessageBytes {
		return Response{}, ErrInvalid
	}
	raw, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return Response{}, ErrUnknown
	}
	conn, ok := raw.(*net.UnixConn)
	if !ok {
		_ = raw.Close()
		return Response{}, ErrUnknown
	}
	defer conn.Close()
	uid, err := peerUID(conn)
	if err != nil || uid != expectedServerUID {
		return Response{}, ErrUnknown
	}
	deadline := time.Now().Add(timeout)
	if until, exists := ctx.Deadline(); exists && until.Before(deadline) {
		deadline = until
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return Response{}, ErrUnknown
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if _, err := conn.Write(data); err != nil {
		return Response{}, ErrUnknown
	}
	if err := conn.CloseWrite(); err != nil {
		return Response{}, ErrUnknown
	}
	reply, err := io.ReadAll(io.LimitReader(conn, maxMessageBytes+1))
	if err != nil || len(reply) > maxMessageBytes || !singleTopLevelKeys(reply) {
		return Response{}, ErrUnknown
	}
	decoder := json.NewDecoder(bytes.NewReader(reply))
	decoder.DisallowUnknownFields()
	var wire wireResponse
	if decoder.Decode(&wire) != nil {
		return Response{}, ErrUnknown
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Response{}, ErrUnknown
	}
	if wire.Error != "" {
		if wire.Error == wireRejected && wire.Response == (Response{}) {
			return Response{}, ErrDenied
		}
		return Response{}, ErrUnknown
	}
	if wire.RequestRef != request.RequestRef || wire.TaskRef != request.TaskRef ||
		!validRef(wire.BudgetRef) || !validRef(wire.Epoch) || wire.State != "ready" ||
		wire.PolicyRef != request.PolicyRef || wire.ResetRef != request.ResetRef ||
		wire.MaxRequests != request.MaxRequests ||
		!wire.ValidFrom.Equal(request.ValidFrom.Round(0).UTC().Truncate(time.Microsecond)) ||
		!wire.ValidUntil.Equal(request.ValidUntil.Round(0).UTC().Truncate(time.Microsecond)) {
		return Response{}, ErrUnknown
	}
	return wire.Response, nil
}
