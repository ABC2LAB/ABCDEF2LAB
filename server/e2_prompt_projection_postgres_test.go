//go:build linux

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2prompt"
	"github.com/golang-jwt/jwt/v5"
	"time"
)

func TestE2PromptProjectionWorkerPostgresPython(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("synthetic PostgreSQL not configured")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal("configured PostgreSQL unavailable")
	}
	defer d.Close()
	row, err := d.CreateTask("synthetic prompt projection", "synthetic", nil, 0, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(row.ID)
	task := taskFromPG(row, d.Exploration(row.ExplorationID), nil)
	host, _, policy := e2HostPolicyFixture(t)
	policy.RunRef = fmt.Sprintf("projection-%d", row.ID)
	raw, _ := json.Marshal(policy)
	if os.WriteFile(host.PolicyFile, raw, 0600) != nil {
		t.Fatal("policy write")
	}
	options, err := LoadE2HTTPRuntimeOptions(t.Context(), E2HTTPFileOptions{PolicyFile: host.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}})
	if err != nil {
		t.Fatal(err)
	}
	if !options.PromptProjection {
		t.Fatal("production factory did not enable projection")
	}
	runtime, err := NewE2HTTPRuntime(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.AdmitTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	provider := &e2HostModelCounter{}
	worker := agent.NewWorker(provider, "synthetic", t.TempDir(), nil, 10000, 2)
	worker.SetNonStreaming(func() bool { return true })
	for i := 1; i <= 2; i++ {
		intentID, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "synthetic-private-intent"}, 0, "open", "planner", nil)
		if err != nil {
			t.Fatal(err)
		}
		intent, err := task.Store.GetNode(intentID)
		if err != nil {
			t.Fatal(err)
		}
		ctx, finish, err := runtime.PrepareWork(t.Context(), task, intent)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _ = worker.Execute(ctx, fmt.Sprintf("work#%d", i), row.ID, nil, task.Store, intent, nil, func(a db.Activity) {
			if a.Kind == "e2_prompt" {
				t.Error("duplicate generic prompt emission")
			}
		}, nil, nil)
		if err = finish(); err != nil {
			t.Fatal(err)
		}
	}
	if provider.calls.Load() != 2 {
		t.Fatal("actual Worker prompt path did not run")
	}
	before, err := task.Store.E2PromptProjection(t.Context(), row.ID, policy.RunRef)
	if err != nil || before.State != "collecting" || before.Complete {
		t.Fatal("early complete projection")
	}
	if err = runtime.CloseTask(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	p, err := task.Store.E2PromptProjection(t.Context(), row.ID, policy.RunRef)
	if err != nil || !p.Complete || len(p.Observations) != 2 {
		t.Fatal("complete projection unavailable")
	}
	wire, _ := json.Marshal(p)
	for _, private := range []string{"synthetic-private-intent", "system_digest", "SystemPrompt", "detail", "command"} {
		if bytes.Contains(wire, []byte(private)) {
			t.Fatal("private data leaked")
		}
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 required for compatibility test")
	}
	scripts, _ := filepath.Abs("../scripts/eval")
	script := `import json,sys
sys.path.insert(0,sys.argv[1])
from e2_prompt_identity import run_prompt_identity, condition_prompt_identity, PromptIdentityError
p=json.load(sys.stdin)
assert set(p)=={'format','task_id','exploration_id','run_ref','state','complete','expected_count','observed_count','snapshot_sequence','observations'}
assert p['format']=='abc2lab.e2-prompt-projection/v1' and p['task_id']==int(sys.argv[2]) and p['run_ref']==sys.argv[3]
rows=p['observations']; assert p['state']=='complete' and p['complete'] is True
assert len(rows)==p['expected_count']==p['observed_count']==p['snapshot_sequence']==2
assert [o['sequence'] for o in rows]==list(range(1,len(rows)+1))
assert len({o['activity_id'] for o in rows})==len(rows)
for o in rows:
 assert set(o)=={'sequence','activity_id','intent_id','worker','version','key','source','template_digest','guidance_digest','control_digest','tools_digest'}
 assert o['version']=='e2-worker-prompt/v1' and o['key']=='e2_worker'
assert run_prompt_identity(rows)==condition_prompt_identity([rows,rows])
other=[dict(rows[0],source='override')]
try: condition_prompt_identity([rows,other])
except PromptIdentityError as e: assert e.code=='runs_differ'
else: raise AssertionError('different condition accepted')
`
	cmd := exec.CommandContext(t.Context(), python, "-I", "-c", script, scripts, strconv.FormatInt(row.ID, 10), policy.RunRef)
	cmd.Stdin = bytes.NewReader(wire)
	if _, err = cmd.CombinedOutput(); err != nil {
		t.Fatal("Python projection consumer rejected Go output")
	}
	s := &Server{m: &Manager{pg: d, tasks: map[string]*Task{task.ID: task}}, jwtKey: []byte(strings.Repeat("k", 32))}
	s.setPassHash("")
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	s.firstDiagnosis = true
	s.firstModel = &firstDiagnosisModelPin{TaskID: task.ID, TaskNumber: row.ID, ExplorationID: row.ExplorationID}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tasks/{id}/e2-prompt-projection", s.e2PromptProjection)
	request := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/e2-prompt-projection?run_ref="+policy.RunRef, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	s.requireAuth(s.firstDiagnosisRoutes(mux)).ServeHTTP(recorder, request)
	if recorder.Code != 200 {
		t.Fatal("production first-diagnosis route rejected projection")
	}
	wrongSubject, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "other", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}).SignedString(s.jwtKey)
	for _, tc := range []struct {
		name, auth, query string
		want              int
	}{{"normal", token, "run_ref=" + policy.RunRef, 200}, {"no-auth", "", "run_ref=" + policy.RunRef, 401}, {"foreign-subject", wrongSubject, "run_ref=" + policy.RunRef, 401}, {"wrong-run", token, "run_ref=other", 503}, {"unknown-query", token, "run_ref=" + policy.RunRef + "&raw=true", 400}, {"duplicate-query", token, "run_ref=a&run_ref=b", 400}, {"oversize", token, "run_ref=" + strings.Repeat("x", 300), 400}} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/e2-prompt-projection?"+tc.query, nil)
			req.SetPathValue("id", task.ID)
			if tc.auth != "" {
				req.Header.Set("Authorization", "Bearer "+tc.auth)
			}
			rec := httptest.NewRecorder()
			s.e2PromptProjection(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d", rec.Code, tc.want)
			}
			if tc.want == 200 {
				var response e2prompt.Projection
				if json.Unmarshal(rec.Body.Bytes(), &response) != nil || response.RunRef != p.RunRef || !response.Complete {
					t.Fatal("API projection mismatch")
				}
			}
		})
	}
}
