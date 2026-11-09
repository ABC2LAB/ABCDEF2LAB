//go:build linux

package diagnosisprep

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type endpointFunc func(context.Context, Request) (Response, error)

func (f endpointFunc) Call(c context.Context, r Request) (Response, error) { return f(c, r) }
func TestUnixTransportRoundTripOwnershipAndRestart(t *testing.T) {
	s, _, root := newFixture(t)
	path := filepath.Join(root, "preparation.sock")
	server, e := ListenUnix(context.Background(), path, s)
	if e != nil {
		t.Fatal(e)
	}
	client, e := NewUnixClient(path)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	j := create(t, client, "create")
	if j.ID == "" {
		t.Fatal("missing job")
	}
	r := request(Status)
	r.JobID = j.ID
	r.Principal = "other"
	if got := call(t, client, r).Error; got != "not_found" {
		t.Fatal(got)
	}
	if e = server.Close(context.Background()); e != nil {
		t.Fatal(e)
	}
	restarted, e := ListenUnix(context.Background(), path, s)
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close(context.Background())
	if got := status(t, client, j.ID); got.ID != j.ID {
		t.Fatal("transport restart lost durable ownership")
	}
}
func TestUnixTransportStrictFrameBeforeDispatch(t *testing.T) {
	root := privateDir(t)
	path := filepath.Join(root, "preparation.sock")
	var calls atomic.Int64
	endpoint := endpointFunc(func(_ context.Context, r Request) (Response, error) {
		calls.Add(1)
		return Response{Version: Version, RequestID: r.RequestID}, nil
	})
	server, e := ListenUnix(context.Background(), path, endpoint)
	if e != nil {
		t.Fatal(e)
	}
	defer server.Close(context.Background())
	valid := `{"version":"diagnosis-preparation.v1","request_id":"request","command":"catalog","principal":"owner"}`
	cases := []string{
		strings.Replace(valid, `"version"`, `"Version"`, 1) + "\n",
		strings.Replace(valid, `"command":"catalog"`, `"command":"catalog","command":"start"`, 1) + "\n",
		strings.Replace(valid, `"principal":"owner"`, `"principal":"owner","runtime_address":"http://attacker"`, 1) + "\n",
		valid + "\n{}\n", valid + " {}\n", strings.Repeat(" ", MaxFrameBytes) + valid + "\n", valid,
	}
	for i, raw := range cases {
		conn, e := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
		if e != nil {
			t.Fatal(e)
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		_, _ = conn.Write([]byte(raw))
		_ = conn.CloseWrite()
		reply, _ := io.ReadAll(conn)
		_ = conn.Close()
		if len(reply) > 0 {
			t.Fatalf("invalid frame %d got response", i)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("malformed request dispatched %d", calls.Load())
	}
}
func TestUnixDisconnectDoesNotCancelAcceptedStart(t *testing.T) {
	s, b, root := newFixture(t)
	entered := make(chan context.Context)
	release := make(chan struct{})
	b.run = func(ctx context.Context, e Execution, p Publish) error {
		entered <- ctx
		<-release
		return completeRun(ctx, e, p)
	}
	path := filepath.Join(root, "preparation.sock")
	server, e := ListenUnix(context.Background(), path, s)
	if e != nil {
		t.Fatal(e)
	}
	defer server.Close(context.Background())
	j := create(t, s, "create")
	r := request(Start)
	r.JobID = j.ID
	r.ExpectedRevision = j.Revision
	r.IdempotencyKey = "start"
	raw, _ := json.Marshal(r)
	conn, e := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	_, _ = conn.Write(append(raw, '\n'))
	_ = conn.CloseWrite()
	_ = conn.Close()
	select {
	case ctx := <-entered:
		if ctx.Err() != nil {
			t.Fatal("connection disconnect cancelled job")
		}
	case <-time.After(time.Second):
		t.Fatal("accepted start missing")
	}
	client, e := NewUnixClient(path)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	same := start(t, client, j, "start")
	if same.Error != "" {
		t.Fatal(same.Error)
	}
	if b.calls.Load() != 1 {
		t.Fatal("disconnection replayed start")
	}
	close(release)
	awaitState(t, client, j.ID, Completed)
}
func TestUnixLeaseAndUnsafeSocket(t *testing.T) {
	root := privateDir(t)
	path := filepath.Join(root, "preparation.sock")
	epoint := endpointFunc(func(_ context.Context, r Request) (Response, error) {
		return Response{Version: Version, RequestID: r.RequestID}, nil
	})
	first, e := ListenUnix(context.Background(), path, epoint)
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close(context.Background())
	if second, e := ListenUnix(context.Background(), path, epoint); e == nil {
		_ = second.Close(context.Background())
		t.Fatal("second listener stole active socket")
	}
	client, e := NewUnixClient(path)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	if e = os.Chmod(path, 0666); e != nil {
		t.Fatal(e)
	}
	if _, e = client.Call(context.Background(), request(Catalog)); e == nil {
		t.Fatal("unprotected socket accepted")
	}
	_ = os.Chmod(path, 0600)
	uidRoot, e := openProtectedRoot(root)
	if e != nil {
		t.Fatal(e)
	}
	defer uidRoot.file.Close()
	if _, _, e = socketLocation("../preparation.sock"); e == nil {
		t.Fatal("relative endpoint accepted")
	}
}
func TestUnixClientRejectsMalformedResponse(t *testing.T) {
	for _, reply := range []string{
		`{"version":"diagnosis-preparation.v1","request_id":"wrong"}` + "\n",
		`{"version":"diagnosis-preparation.v1","request_id":"request","request_id":"request"}` + "\n",
		`{"version":"diagnosis-preparation.v1","request_id":"request","error":"private /secret"}` + "\n",
		`{"version":"diagnosis-preparation.v1","request_id":"request"}` + "\n{}",
	} {
		t.Run(reply, func(t *testing.T) {
			root := privateDir(t)
			path := filepath.Join(root, "preparation.sock")
			listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if e != nil {
				t.Fatal(e)
			}
			defer listener.Close()
			_ = os.Chmod(path, 0600)
			go func() {
				conn, e := listener.AcceptUnix()
				if e != nil {
					return
				}
				defer conn.Close()
				_, _ = io.ReadAll(conn)
				_, _ = conn.Write([]byte(reply))
			}()
			client, e := NewUnixClient(path)
			if e != nil {
				t.Fatal(e)
			}
			defer client.Close()
			if _, e = client.Call(context.Background(), request(Catalog)); e == nil {
				t.Fatal("malformed response accepted")
			}
		})
	}
}
