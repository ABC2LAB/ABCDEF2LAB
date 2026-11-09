package main

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// 실행 handler 소유를 보고서 map 잠금과 분리해 느린 조회가 마감을 무기한 붙잡지 않게 한다.
type urlLiveHandler struct {
	mu      sync.Mutex
	handler http.Handler
	active  int
	closed  bool
	idle    chan struct{}
}

func newURLLiveHandler(handler http.Handler) *urlLiveHandler {
	idle := make(chan struct{})
	close(idle)
	return &urlLiveHandler{handler: handler, idle: idle}
}
func (h *urlLiveHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		http.Error(w, "진단 실행이 마감 중입니다", 503)
		return
	}
	if h.active == 0 {
		h.idle = make(chan struct{})
	}
	h.active++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.active--
		if h.active == 0 {
			close(h.idle)
		}
		h.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	h.handler.ServeHTTP(w, r.WithContext(ctx))
}
func (h *urlLiveHandler) drain(ctx context.Context) error {
	h.mu.Lock()
	h.closed = true
	idle := h.idle
	h.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
