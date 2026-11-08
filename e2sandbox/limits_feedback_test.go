//go:build linux

package e2sandbox

import (
	"strings"
	"testing"
	"time"
)

func TestLimitsForCapsTasksInsideUserNamespace(t *testing.T) {
	const maxTasks = 24
	l, err := limitsFor(Config{Timeout: time.Minute, MaxProcesses: maxTasks})
	if err != nil || l.TaskCeiling != maxTasks {
		t.Fatalf("namespace task ceiling: %d, %v", l.TaskCeiling, err)
	}
}

func TestSandboxAppliesNamespaceTaskCeiling(t *testing.T) {
	f := newSandboxFixture(t, `if [ "$(ulimit -u)" != "24" ]; then exit 1; fi; printf namespace-task-cap-ok`)
	f.cfg.MaxProcesses = 24
	r := f.run(t)
	if r.ExitCode != 0 || !r.Drained || !strings.Contains(string(r.Output), "namespace-task-cap-ok") {
		t.Fatalf("isolated task ceiling was not installed: %+v", r)
	}
}

func TestSandboxNamespaceTaskCeilingBoundsForks(t *testing.T) {
	const script = `import os,signal,time
children=[]
try:
 for _ in range(48):
  try: pid=os.fork()
  except OSError: break
  if pid==0:
   time.sleep(10)
   os._exit(0)
  children.append(pid)
finally:
 for pid in children:
  try: os.kill(pid,signal.SIGKILL)
  except ProcessLookupError: pass
 for pid in children:
  try: os.waitpid(pid,0)
  except ChildProcessError: pass
print("bounded-forks",len(children))
if len(children)>=24: raise SystemExit(1)`
	f := newSandboxFixture(t, "python3 -c '"+script+"'")
	f.cfg.MaxProcesses = 24
	r := f.run(t)
	if r.ExitCode != 0 || !r.Drained || !strings.Contains(string(r.Output), "bounded-forks") {
		t.Fatalf("namespace task limit did not bound child creation: %+v", r)
	}
}
