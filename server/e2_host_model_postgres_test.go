//go:build linux

package server

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/norma/llm"
)

type e2HostModelCounter struct{ calls atomic.Int64 }

func (p *e2HostModelCounter) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls.Add(1)
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("synthetic model response")}}, "stop", llm.Usage{}, nil
}
func (p *e2HostModelCounter) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.calls.Add(1)
		yield(llm.StreamEvent{}, errors.New("synthetic stream counter"))
	}
}

// 실제 독립 LOGIN/SQL과 단일 profile guard를 검증한다. 카운터 공급자는 시험 전용이며 네트워크를 호출하지 않는다.
func TestE2HostModelOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	ctx, cancel := context.WithTimeout(f.Context, 60*time.Second)
	defer cancel()
	installer := &pgdb.DB{DB: f.Installer}
	var networkCalls atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { networkCalls.Add(1) }))
	defer endpoint.Close()
	save := func(label string) int64 {
		t.Helper()
		id, err := installer.SaveProfile(&pgdb.LLMProfile{Name: fmt.Sprintf("owned-model-guard-%s-%d", label, time.Now().UnixNano()), Format: "openai", Model: "local:host-guard-" + label, BaseURL: endpoint.URL + "/v1", APIKey: "synthetic-host-model-key", Streaming: false})
		if err != nil || id <= 0 {
			t.Fatal("owned model profile installation failed")
		}
		return id
	}
	first, other := save("first"), save("other")
	for _, role := range []string{"goals", "planner", "worker"} {
		row, err := installer.GetAgentByKey(role)
		if err != nil {
			t.Fatal("owned role fixture read failed")
		}
		if row == nil {
			if _, err = installer.CreateAgent(role, "Owned model role "+role, "Synthetic owned model guard fixture"); err != nil {
				t.Fatal("owned role fixture installation failed")
			}
		}
		if _, err = installer.ExecContext(ctx, "UPDATE agents SET llm_profile_id=NULL WHERE key=$1", role); err != nil {
			t.Fatal("owned role fixture initialization failed")
		}
	}
	manager, err := NewFirstDiagnosisManager(ctx, t.TempDir(), f.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	makeTask := func(t *testing.T) *Task {
		t.Helper()
		made, err := PrepareFirstDiagnosisTask(ctx, f.Runtime, "Owned single-model guard fixture", "Verify one fixed profile", first, 60)
		if err != nil {
			t.Fatal("actual task preparation failed")
		}
		row, err := manager.pg.GetTask(made.TaskID)
		if err != nil || row == nil {
			t.Fatal("actual task read failed")
		}
		task := taskFromPG(row, manager.pg.Exploration(row.ExplorationID), manager.interceptor)
		manager.mu.Lock()
		manager.tasks[task.ID] = task
		manager.mu.Unlock()
		return task
	}
	task := makeTask(t)
	serverContext, serverCancel := context.WithCancel(ctx)
	defer serverCancel()
	s := &Server{ctx: serverContext, m: manager, firstDiagnosis: true, provByProfile: map[int64]*provEntry{}}
	counters := map[int64]*e2HostModelCounter{first: {}, other: {}}
	var pins []llmpolicy.ProfilePin
	for _, id := range []int64{first, other} {
		cfg, ok := s.loadProfileConfig(id)
		if !ok {
			t.Fatal("actual runtime profile config read failed")
		}
		pins = append(pins, llmpolicy.ProfilePin{Target: llmpolicy.ProfileTarget{ID: id, Format: string(cfg.Format), Model: cfg.Model, BaseURL: cfg.BaseURL}, Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}})
		s.provByProfile[id] = &provEntry{prov: counters[id], cfg: cfg}
	}
	s.localResolver, err = llmpolicy.NewProfileResolver(pins)
	if err != nil {
		t.Fatal("owned local manifest setup failed")
	}
	runtime := &taskLLMRuntime{s: s, taskID: task.ID, agentKey: "planner"}
	if _, err = runtime.current(); !errors.Is(err, ErrFirstDiagnosisModel) {
		t.Fatal("first model call was available before binding")
	}
	if CheckFirstDiagnosisTaskProfileBinding(ctx, f.Runtime, mustFirstDiagnosisTaskNumber(task.ID), first) != nil || s.bindFirstDiagnosisModel(ctx, task) != nil || s.bindFirstDiagnosisModel(ctx, task) != nil {
		t.Fatal("actual single-model binding rejected")
	}
	if _, _, _, err = runtime.Complete(ctx, llm.CompletionRequest{}); err != nil || counters[first].calls.Load() != 1 {
		t.Fatal("bound actual profile did not reach its test-only provider")
	}
	selected, err := runtime.current()
	if err != nil {
		t.Fatal(err)
	}
	exec := func(t *testing.T, query string, args ...any) {
		t.Helper()
		if _, err := installer.ExecContext(ctx, query, args...); err != nil {
			t.Fatal("owned model mutation fixture failed")
		}
	}
	deny := func(t *testing.T) {
		t.Helper()
		before1, before2 := counters[first].calls.Load(), counters[other].calls.Load()
		if _, _, _, err := runtime.Complete(ctx, llm.CompletionRequest{}); !errors.Is(err, ErrFirstDiagnosisModel) {
			t.Fatal("mismatching binding reached completion provider")
		}
		observed := false
		for _, err := range runtime.Stream(ctx, llm.CompletionRequest{}) {
			observed = true
			if !errors.Is(err, ErrFirstDiagnosisModel) {
				t.Fatal("mismatching binding stream was not refused")
			}
		}
		if _, _, _, err := selected.provider.Complete(ctx, llm.CompletionRequest{}); !errors.Is(err, ErrFirstDiagnosisModel) {
			t.Fatal("stored selection bypassed changed binding")
		}
		for _, err := range selected.provider.Stream(ctx, llm.CompletionRequest{}) {
			if !errors.Is(err, ErrFirstDiagnosisModel) {
				t.Fatal("stored stream selection bypassed changed binding")
			}
		}
		if !observed || counters[first].calls.Load() != before1 || counters[other].calls.Load() != before2 || s.taskRuntimeAvailable(task, "planner", "worker") {
			t.Fatal("binding denial called a provider or stayed ready")
		}
		if _, ok := runtime.activeCfg(); ok {
			t.Fatal("invalid binding selected runtime configuration")
		}
	}
	for _, role := range []string{"goals", "planner", "worker"} {
		t.Run("role-"+role, func(t *testing.T) {
			result, err := installer.ExecContext(ctx, "UPDATE agents SET llm_profile_id=$1 WHERE key=$2", other, role)
			if err != nil {
				t.Fatal("actual role profile mutation failed")
			}
			count, _ := result.RowsAffected()
			if count != 1 {
				t.Fatal("owned role fixture missing")
			}
			if CheckFirstDiagnosisModelRoleBinding(ctx, f.Runtime, first) == nil || CheckFirstDiagnosisTaskProfileBinding(ctx, f.Runtime, mustFirstDiagnosisTaskNumber(task.ID), first) == nil {
				t.Fatal("role override was declared matching")
			}
			deny(t)
			exec(t, "UPDATE agents SET llm_profile_id=$1 WHERE key=$2", first, role)
			if s.checkFirstDiagnosisModel(ctx, task) != nil {
				t.Fatal("same exact role profile was refused")
			}
			exec(t, "UPDATE agents SET llm_profile_id=NULL WHERE key=$1", role)
		})
	}
	t.Run("active-profile-change", func(t *testing.T) {
		exec(t, "UPDATE tasks SET active_llm_profile_id=$1 WHERE id=$2", other, task.ID)
		deny(t)
		exec(t, "UPDATE tasks SET active_llm_profile_id=$1 WHERE id=$2", first, task.ID)
	})
	t.Run("extra-chain-profile", func(t *testing.T) {
		exec(t, "INSERT INTO task_llm_profiles(task_id,profile_id,position) VALUES($1,$2,1)", task.ID, other)
		deny(t)
		exec(t, "DELETE FROM task_llm_profiles WHERE task_id=$1 AND profile_id=$2", task.ID, other)
	})
	t.Run("missing-chain", func(t *testing.T) {
		exec(t, "DELETE FROM task_llm_profiles WHERE task_id=$1", task.ID)
		deny(t)
		exec(t, "INSERT INTO task_llm_profiles(task_id,profile_id,position) VALUES($1,$2,0)", task.ID, first)
	})
	t.Run("immutable-profile", func(t *testing.T) {
		exec(t, "UPDATE tasks SET llm_profile_id=$1,active_llm_profile_id=$1 WHERE id=$2", other, task.ID)
		exec(t, "UPDATE task_llm_profiles SET profile_id=$1 WHERE task_id=$2", other, task.ID)
		if s.bindFirstDiagnosisModel(ctx, task) == nil {
			t.Fatal("existing model pin was replaced")
		}
		deny(t)
		exec(t, "UPDATE tasks SET llm_profile_id=$1,active_llm_profile_id=$1 WHERE id=$2", first, task.ID)
		exec(t, "UPDATE task_llm_profiles SET profile_id=$1 WHERE task_id=$2", first, task.ID)
	})
	t.Run("immutable-task", func(t *testing.T) {
		foreign := makeTask(t)
		if s.bindFirstDiagnosisModel(ctx, foreign) == nil || s.checkFirstDiagnosisModel(ctx, foreign) == nil {
			t.Fatal("model pin was reused by another actual task")
		}
	})
	t.Run("baseline-role-priority", func(t *testing.T) {
		exec(t, "UPDATE agents SET llm_profile_id=$1 WHERE key='planner'", other)
		legacy := &Server{ctx: ctx, m: manager, localResolver: s.localResolver, provByProfile: s.provByProfile}
		oldRuntime := &taskLLMRuntime{s: legacy, taskID: task.ID, agentKey: "planner"}
		selected, err := oldRuntime.current()
		if err != nil || selected.provider == nil {
			t.Fatal("ordinary role provider priority changed")
		}
		_, _, _, err = oldRuntime.Complete(ctx, llm.CompletionRequest{})
		if err != nil || counters[other].calls.Load() != 1 {
			t.Fatal("ordinary role provider was blocked by first-mode guard")
		}
		exec(t, "UPDATE agents SET llm_profile_id=NULL WHERE key='planner'")
	})
	serverCancel()
	before := counters[first].calls.Load()
	if _, _, _, err := selected.provider.Complete(ctx, llm.CompletionRequest{}); !errors.Is(err, ErrFirstDiagnosisModel) || counters[first].calls.Load() != before {
		t.Fatal("stored provider outlived its server")
	}
	if networkCalls.Load() != 0 {
		t.Fatal("guard test made a model network request")
	}
}
