package main

import (
	"context"
	"log"
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
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if b.product == nil || b.product.targets == nil || b.endpoint == nil {
		log.Print("[candidate-read] stage=runtime")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	task := strconv.FormatInt(id, 10)
	b.mu.RLock()
	live := b.live[task]
	b.mu.RUnlock()
	if live == nil {
		_, status := b.candidateJob(r.Context(), task, nil)
		w.WriteHeader(status)
		return
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		job, status := b.candidateJob(r.Context(), task, live)
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		live.candidates.handler(job, live.candidates.targetRevision, func(ctx context.Context) int {
			_, status := b.candidateJob(ctx, task, live)
			return status
		}).ServeHTTP(w, r)
	})
	if !live.serve(w, r, handler) {
		w.WriteHeader(http.StatusConflict)
	}
}

func (b *urlPreparationRuntime) candidateJob(ctx context.Context, task string, live *urlLiveHandler) (*diagnosisprep.Job, int) {
	if live != nil && live.isClosed() {
		return nil, http.StatusConflict
	}
	request := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-read", Command: diagnosisprep.AuthorizeTask, Principal: "ARTEX", TaskID: task}
	response, err := b.endpoint.Call(ctx, request)
	if live != nil && live.isClosed() {
		return nil, http.StatusConflict
	}
	if err != nil || diagnosisprep.ValidateResponse(request, response) != nil || response.Error != "" {
		log.Print("[candidate-read] stage=preparation")
		return nil, http.StatusServiceUnavailable
	}
	job := response.Job
	if job == nil {
		return nil, http.StatusForbidden
	}
	b.mu.RLock()
	current := b.live[task]
	b.mu.RUnlock()
	if live == nil || current != live || live.candidates == nil || !server.ProductCandidateJobActive(job) {
		return nil, http.StatusConflict
	}
	binding := live.candidates
	if binding.jobID != job.ID || binding.sourceDigest != job.SourceDigest || binding.targetRef != job.Scope.TargetRef {
		return nil, http.StatusForbidden
	}
	registry := b.product.targets
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if ctx.Err() != nil || registry.check() != nil {
		log.Print("[candidate-read] stage=registry")
		return nil, http.StatusServiceUnavailable
	}
	now := registry.now()
	record, found := registry.records[binding.targetRef]
	if !found || record.Principal != request.Principal || record.Target.State != "approved" ||
		record.Target.Revision != binding.targetRevision ||
		!now.Before(record.Target.ExpiresAt) || !now.Before(job.Scope.ExpiresAt) {
		return nil, http.StatusForbidden
	}
	return job, 0
}
