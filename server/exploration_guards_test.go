package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
)

func TestE2HTTPExplorationRejectsBusyOrLeasedTaskBeforeStage(t *testing.T) {
	for _, occupied := range []string{"active-work", "existing-lease"} {
		for _, entry := range []string{"planning", "work"} {
			t.Run(occupied+"/"+entry, func(t *testing.T) {
				var calls atomic.Int32
				runtime, task := e2HTTPRuntimeFixture(t, func(o *E2HTTPRuntimeOptions) {
					o.Exploration = func(context.Context, *Task) error { calls.Add(1); return ErrExplorationClosure }
				})
				if err := runtime.AdmitTask(t.Context(), task); err != nil {
					t.Fatal(err)
				}
				runtime.task.mu.Lock()
				if occupied == "active-work" {
					runtime.task.active[19] = make(chan struct{})
				} else {
					runtime.task.lease = &E2HTTPRunOptions{}
				}
				runtime.task.mu.Unlock()
				defer func() {
					runtime.task.mu.Lock()
					delete(runtime.task.active, 19)
					runtime.task.lease = nil
					runtime.task.mu.Unlock()
				}()
				var err error
				if entry == "planning" {
					_, err = runtime.PreparePlanning(t.Context(), task)
				} else {
					_, _, err = runtime.PrepareWork(t.Context(), task, &db.Node{ID: 19, Kind: db.KindIntent})
				}
				runtime.task.mu.Lock()
				done := runtime.task.explorationDone
				runtime.task.mu.Unlock()
				if !errors.Is(err, ErrE2HTTPBusy) || calls.Load() != 0 || done {
					t.Fatal("exploration ran while diagnostic authority or work was active")
				}
			})
		}
	}
}

// stage 검사 제거가 nil DB panic에 기대지 않고 실제 금지된 조회 시도로 드러나게 한다.
type explorationGuardLookupConnector struct{ calls atomic.Int32 }

func (c *explorationGuardLookupConnector) Connect(context.Context) (driver.Conn, error) {
	c.calls.Add(1)
	return nil, errors.New("owned lookup must not precede exploration stage")
}
func (*explorationGuardLookupConnector) Driver() driver.Driver { return explorationGuardLookupDriver{} }

type explorationGuardLookupDriver struct{}

func (explorationGuardLookupDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("owned lookup connector required")
}

func TestE2HTTPExplorationInitialWorkMustRunStageBeforeIntentLookup(t *testing.T) {
	var calls atomic.Int32
	runtime, task := e2HTTPRuntimeFixture(t, func(o *E2HTTPRuntimeOptions) {
		o.Exploration = func(context.Context, *Task) error { calls.Add(1); return ErrExplorationClosure }
	})
	lookup := &explorationGuardLookupConnector{}
	pool := sql.OpenDB(lookup)
	defer pool.Close()
	task.Store = (&db.DB{DB: pool}).Exploration(task.ExpID)
	if err := runtime.AdmitTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	_, finish, err := runtime.PrepareWork(t.Context(), task, &db.Node{ID: 19, Kind: db.KindIntent})
	if !errors.Is(err, ErrExplorationClosure) || finish != nil || calls.Load() != 1 || lookup.calls.Load() != 0 || runtime.task.lease != nil {
		t.Fatal("initial Worker skipped exploration stage before lookup and authority acquisition")
	}
	if _, err := runtime.PreparePlanning(t.Context(), task); !errors.Is(err, ErrExplorationClosure) || calls.Load() != 1 {
		t.Fatal("initial Worker stage failure was not shared with Planner")
	}
}

func TestExplorationPipelineRejectsOverCapPrivateSequencesBeforeIngestion(t *testing.T) {
	server, task, o, b, browser, mcp, provider := pipelineFixture(t)
	factory := o.OpenHost
	o.OpenHost = func(ctx context.Context, binding exploration.Binding, w *agent.Worker) (ExplorationPipelineHost, error) {
		host, err := factory(ctx, binding, w)
		reader := host.Reader.(*pipelinePrivateReader)
		sequences := host.Sequences
		host.Sequences = func(ctx context.Context, b diagnosisflow.ExplorationBinding) ([]uint64, error) {
			seqs, err := sequences(ctx, b)
			if err != nil || mcp.navigate.Load() == 0 {
				return seqs, err
			}
			reader.mu.Lock()
			defer reader.mu.Unlock()
			// 모든 행을 유효한 private metadata로 만들어 다른 거부가 개수 검사 제거를 숨기지 않게 한다.
			for uint64(len(reader.rows)) <= binding.Limits.MaxRequests {
				reader.add("/read/cap/"+strconv.Itoa(len(reader.rows)), diagnosisflow.SourceBrowserXHR, nil)
			}
			seqs = seqs[:0]
			for seq := range reader.rows {
				seqs = append(seqs, seq)
			}
			return seqs, nil
		}
		return host, err
	}
	_, err := server.RunExplorationPipeline(t.Context(), task, o)
	snapshot, _ := o.Workflow.Snapshot()
	if !errors.Is(err, ErrExplorationObservation) || snapshot.Counts.ExplorationRequests != 2 || provider.calls.Load() != 1 || mcp.navigate.Load() != 1 || b.lease.closed() != 1 || browser.closes.Load() != 1 || mcp.deleted.Load() != 1 {
		t.Fatal("over-cap private rows were accepted, reached another model call or lost cleanup")
	}
}
