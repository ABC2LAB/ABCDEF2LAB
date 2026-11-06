package server

import (
	"context"
	"errors"
)

var ErrServerBackgroundDrain = errors.New("server: background drain incomplete")

// startBackground and WaitBackground serialize Add against the final Wait.
// A canceled server never starts a replacement startup/maintenance worker.
func (s *Server) startBackground(run func()) bool {
	if s == nil || run == nil {
		return false
	}
	s.backgroundMu.Lock()
	if s.backgroundClosing || s.ctx == nil || s.ctx.Err() != nil {
		s.backgroundMu.Unlock()
		return false
	}
	s.backgroundWG.Add(1)
	s.backgroundMu.Unlock()
	go func() { defer s.backgroundWG.Done(); run() }()
	return true
}

// WaitBackground closes admission and joins server-owned startup/maintenance
// jobs. Cancel the server first; join these producers before draining task,
// side-question and archive consumers so reconciliation cannot restart work.
// A deadline keeps the one waiter and ownership alive for a later join.
func (s *Server) WaitBackground(ctx context.Context) error {
	if s == nil || ctx == nil {
		return ErrServerBackgroundDrain
	}
	s.backgroundMu.Lock()
	s.backgroundClosing = true
	if s.backgroundDone == nil {
		s.backgroundDone = make(chan struct{})
		done := s.backgroundDone
		go func() { s.backgroundWG.Wait(); close(done) }()
	}
	done := s.backgroundDone
	s.backgroundMu.Unlock()
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ErrServerBackgroundDrain
	}
}
