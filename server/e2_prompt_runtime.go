package server

import (
	"context"
	"log"
	"time"

	"github.com/Autumn-27/artex/db"
)

const e2PromptPersistenceTimeout = 2 * time.Second

// gate 안에서 실제 lease 검증 뒤 호출한다. 관측 장애는 송신 판정과 별도로 보존한다.
func (r *E2HTTPRuntime) promptObserver(ctx context.Context, t *e2HTTPRuntimeTask, intent int64) func(context.Context, string, []byte) error {
	if !r.options.PromptProjection {
		return nil
	}
	recordCtx, cancel := context.WithTimeout(ctx, e2PromptPersistenceTimeout)
	defer cancel()
	if !t.promptRegistered {
		t.promptRegistered = true
		t.promptOwned = t.store.RegisterE2PromptRun(recordCtx, t.number, r.options.Expected.RunRef) == nil
	}
	var seq int64
	var recordErr error
	if t.promptOwned {
		seq, recordErr = t.store.BeginE2PromptWork(recordCtx, t.number, r.options.Expected.RunRef, intent)
	} else {
		recordErr = db.ErrE2Prompt
	}
	if recordErr != nil {
		t.mu.Lock()
		t.promptFailed = true
		t.mu.Unlock()
		log.Print("E② 평가 프롬프트 기대 관측 등록 실패")
	}
	return func(ctx context.Context, worker string, detail []byte) error {
		persistCtx, cancel := context.WithTimeout(ctx, e2PromptPersistenceTimeout)
		defer cancel()
		err := recordErr
		if err == nil {
			err = t.store.SaveE2PromptActivity(persistCtx, t.number, r.options.Expected.RunRef, seq, intent, worker, detail)
		}
		if err != nil {
			t.mu.Lock()
			t.promptFailed = true
			t.mu.Unlock()
		}
		return err
	}
}

func (r *E2HTTPRuntime) sealPromptProjection(ctx context.Context, t *e2HTTPRuntimeTask, closed bool) {
	if !r.options.PromptProjection || !t.promptOwned {
		return
	}
	t.mu.Lock()
	healthy := !t.promptFailed && closed
	t.mu.Unlock()
	persistCtx, cancel := context.WithTimeout(ctx, e2PromptPersistenceTimeout)
	defer cancel()
	if err := t.store.SealE2PromptRun(persistCtx, t.number, r.options.Expected.RunRef, healthy); err != nil {
		log.Print("E② 평가 프롬프트 마감 저장 실패")
	}
}
