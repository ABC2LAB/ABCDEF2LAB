//go:build linux

package e2runner

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Fill each command's real hard NPROC limit before its first proxy request.
// The helper must still create relay threads, return the response and drain.
func TestRunnerParallelTaskPressureKeepsRelayAndOtherIntents(t *testing.T) {
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	var once sync.Once
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
			io.WriteString(w, "owned")
		case <-r.Context().Done():
		}
	}))
	defer target.Close()
	defer once.Do(func() { close(release) })
	f := parallelRunnerFixture(t, target.URL, 3, 3, nil, true)
	const script = `import os,sys,time,signal,socket,urllib.parse,resource
if resource.getrlimit(resource.RLIMIT_NPROC)!=(48,48):raise RuntimeError("wrong command limits")
try:resource.setrlimit(resource.RLIMIT_NPROC,(64,64))
except (ValueError,PermissionError):pass
else:raise RuntimeError("command stole helper headroom")
children=[]
try:
 for _ in range(256):
  try:pid=os.fork()
  except OSError:break
  if pid==0:
   time.sleep(15)
   os._exit(0)
  children.append(pid)
 if not children or len(children)==256:raise RuntimeError("limit not reached")
 proxy=urllib.parse.urlsplit(os.environ["HTTP_PROXY"])
 target=urllib.parse.urlsplit(sys.argv[1])
 with socket.create_connection((proxy.hostname,proxy.port),timeout=5) as conn:
  conn.sendall(("GET "+sys.argv[1]+"/allowed HTTP/1.1\r\nHost: "+target.netloc+"\r\nAccept-Encoding: identity\r\nConnection: close\r\n\r\n").encode())
  data=b""
  while True:
   part=conn.recv(4096)
   if not part:break
   data+=part
  if b"owned" not in data:raise RuntimeError("relay failed under pressure")
 print("bounded",resource.getrlimit(resource.RLIMIT_NPROC)[0])
finally:
 for pid in children:
  try:os.kill(pid,signal.SIGKILL)
  except ProcessLookupError:pass
 for pid in children:
  try:os.waitpid(pid,0)
  except ChildProcessError:pass`
	done := make([]<-chan bashOutcome, 3)
	for i := range 3 {
		done[i] = startBash(f.runner, t.Context(), int64(i+1), "python3 -c '"+script+"' '"+target.URL+"'")
	}
	for range 3 {
		awaitSignal(t, entered)
	}
	once.Do(func() { close(release) })
	for _, ch := range done {
		out := awaitBash(t, ch)
		if out.err != nil || !out.result.Trusted.Final || !out.result.Trusted.Call.AuditComplete || out.result.Trusted.Call.Counts.Responses != 1 || !strings.Contains(out.result.ToolOutput, "bounded 48") {
			t.Fatalf("호출 task 상한에서 relay·다른 intent·감사 마감 실패: %v class=%s", out.err, out.result.Trusted.Classification)
		}
	}
}
