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
	if ctx == nil || !filepath.IsAbs(path) || !validRequest(request) ||
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
		return Response{}, ErrUnavailable
	}
	conn, ok := raw.(*net.UnixConn)
	if !ok {
		_ = raw.Close()
		return Response{}, ErrUnavailable
	}
	defer conn.Close()
	uid, err := peerUID(conn)
	if err != nil || uid != expectedServerUID {
		return Response{}, ErrUnavailable
	}
	deadline := time.Now().Add(requestTimeout)
	if until, exists := ctx.Deadline(); exists && until.Before(deadline) {
		deadline = until
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return Response{}, ErrUnavailable
	}
	if _, err := conn.Write(data); err != nil {
		return Response{}, ErrUnavailable
	}
	if err := conn.CloseWrite(); err != nil {
		return Response{}, ErrUnavailable
	}
	reply, err := io.ReadAll(io.LimitReader(conn, maxMessageBytes+1))
	if err != nil || len(reply) > maxMessageBytes || !singleTopLevelKeys(reply) {
		return Response{}, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(reply))
	decoder.DisallowUnknownFields()
	var wire wireResponse
	if decoder.Decode(&wire) != nil {
		return Response{}, ErrUnavailable
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Response{}, ErrUnavailable
	}
	if wire.Error != "" {
		return Response{}, ErrDenied
	}
	if wire.RequestRef != request.RequestRef || wire.TaskRef != request.TaskRef ||
		!validRef(wire.BudgetRef) || !validRef(wire.Epoch) || wire.State != "ready" ||
		wire.PolicyRef != request.PolicyRef || wire.ResetRef != request.ResetRef ||
		wire.MaxRequests != request.MaxRequests ||
		!wire.ValidFrom.Equal(request.ValidFrom.Round(0).UTC().Truncate(time.Microsecond)) ||
		!wire.ValidUntil.Equal(request.ValidUntil.Round(0).UTC().Truncate(time.Microsecond)) {
		return Response{}, ErrUnavailable
	}
	return wire.Response, nil
}
