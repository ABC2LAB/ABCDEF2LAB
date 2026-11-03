package server

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/transmit"
	"github.com/google/uuid"
)

type heldDurableResult struct {
	inner            *controlledCheckpointRecorder
	entered, release chan struct{}
}

func (r *heldDurableResult) Record(ctx context.Context, event transmit.Event) error {
	err := r.inner.Record(ctx, event)
	if event.Stage == "result" {
		close(r.entered)
		<-r.release
	}
	return err
}

func TestDurableAuditActualStopAndWaitPrecedesWitnessAndAbort(t *testing.T) {
	target := controlledReadTarget(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":{"note":"synthetic"}}`))
	}))
	f := newControlledVerificationDBFixture(t, target.URL)
	h := installDurableTestPublisher(t, f, "success")
	r := f.e.controlledReads
	spec, err := f.store.load(f.task.ID, f.intent.ID, "approved")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(spec.Key)
	scope := transmit.Scope{RunRef: "e2-" + uuid.NewString(), TaskRef: f.task.ID, CaseRef: spec.CaseRef, ResetRef: spec.ResetRef, PolicyRef: spec.PolicyRef}
	p, err := r.openPublishedJournal(context.Background(), r.publisher, spec, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer p.abort()
	recorder := &controlledCheckpointRecorder{published: p, store: f.store}
	held := &heldDurableResult{inner: recorder, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(held.release) }) }
	defer release()
	cfg := controlledReadHTTPConfig(spec, scope, held)
	tx, err := transmit.NewHTTP(p.httpContext(context.Background()), cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.drain.bind(tx)
	done := make(chan struct{})
	go func() {
		_, _, _ = tx.SendDetailed(idor.AccessRequest{As: cfg.Sessions[0].Account, ResourceRef: cfg.Resources[0].Ref, Method: "GET"})
		close(done)
	}()
	select {
	case <-held.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("actual result Record did not reach drain barrier")
	}
	l := &controlledReadLease{tx: tx, recorder: recorder, store: f.store, scope: scope, timeout: time.Second, runtime: r, work: controlledWorkKey{task: f.task.ID, intent: f.intent.ID}}
	closing := make(chan error, 1)
	go func() { closing <- l.close(false) }()
	select {
	case <-closing:
		t.Fatal("close returned while actual result Record remained in flight")
	case <-time.After(30 * time.Millisecond):
	}
	if p.drain.check(scope) || h.seals.Load() != 0 {
		t.Fatal("drain/Seal was certified before actual StopAndWait completed")
	}
	release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("actual sender did not drain")
	}
	select {
	case err := <-closing:
		if err == nil {
			t.Fatal("cleanup promoted abandoned execution")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not finish after actual drain")
	}
	if !p.drain.check(scope) || h.seals.Load() != 0 || h.rootReleased.Load() != 1 {
		t.Fatal("actual drain did not precede Abort-only cleanup")
	}
	wrong := scope
	wrong.RunRef = "other"
	if p.drain.check(wrong) {
		t.Fatal("completed HTTP drain crossed execution scope")
	}
}
