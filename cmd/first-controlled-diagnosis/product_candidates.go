package main

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/server"
)

type urlLiveCandidates struct {
	handler                        func(*diagnosisprep.Job, uint64, func(context.Context) int) http.Handler
	jobID, sourceDigest, targetRef string
	targetRevision                 uint64
}

func (b *urlPreparationRuntime) serveLiveCandidates(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, valid := server.ProductCandidateRequestTask(r)
	if !valid {
		w.WriteHeader(400)
		return
	}
	if b.product == nil || b.endpoint == nil {
		w.WriteHeader(503)
		return
	}
	task := strconv.FormatInt(id, 10)
	b.mu.RLock()
	live := b.live[task]
	b.mu.RUnlock()
	job, status := b.candidateJob(r.Context(), task, live)
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	live.serve(w, r, live.candidates.handler(job, live.candidates.targetRevision, func(ctx context.Context) int {
		_, status := b.candidateJob(ctx, task, live)
		return status
	}))
}

func (b *urlPreparationRuntime) candidateJob(ctx context.Context, task string, live *urlLiveHandler) (*diagnosisprep.Job, int) {
	request := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-read", Command: diagnosisprep.AuthorizeTask, Principal: "ARTEX", TaskID: task}
	response, err := b.endpoint.Call(ctx, request)
	if err != nil || diagnosisprep.ValidateResponse(request, response) != nil || response.Error != "" {
		return nil, 503
	}
	job := response.Job
	if job == nil {
		return nil, 403
	}
	b.mu.RLock()
	current := b.live[task]
	b.mu.RUnlock()
	if live == nil || current != live || live.candidates == nil || !server.ProductCandidateJobActive(job) {
		return nil, 409
	}
	binding := live.candidates
	if binding.jobID != job.ID || binding.sourceDigest != job.SourceDigest || binding.targetRef != job.Scope.TargetRef {
		return nil, 403
	}
	registry := b.product.targets
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if ctx.Err() != nil || registry.check() != nil {
		return nil, 503
	}
	now := registry.now()
	record, found := registry.records[binding.targetRef]
	if !found || record.Principal != request.Principal || record.Target.State != "approved" ||
		record.Target.Revision != binding.targetRevision ||
		!now.Before(record.Target.ExpiresAt) || !now.Before(job.Scope.ExpiresAt) {
		return nil, 403
	}
	return job, 0
}
