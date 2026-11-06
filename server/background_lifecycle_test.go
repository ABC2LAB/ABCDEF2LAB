package server

import (
	"context"
	"errors"
	"testing"
)

func TestBackgroundDrainJoinsAndClosesAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Server{ctx: ctx}
	started, release := make(chan struct{}), make(chan struct{})
	if !s.startBackground(func() { close(started); <-release }) {
		t.Fatal("background job rejected before drain")
	}
	<-started
	expired, end := context.WithCancel(context.Background())
	end()
	if err := s.WaitBackground(expired); !errors.Is(err, ErrServerBackgroundDrain) {
		t.Fatalf("unfinished drain: %v", err)
	}
	if s.startBackground(func() { t.Error("job admitted after drain") }) {
		t.Fatal("drain did not close admission")
	}
	close(release)
	if err := s.WaitBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitBackground(context.Background()); err != nil {
		t.Fatalf("second join: %v", err)
	}
}

func TestBackgroundCanceledServerRejectsWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Server{ctx: ctx}
	if s.startBackground(func() { t.Error("canceled server admitted work") }) {
		t.Fatal("canceled server admitted work")
	}
	if err := s.WaitBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	if (&Server{}).startBackground(func() {}) || s.startBackground(nil) {
		t.Fatal("invalid work admitted")
	}
	var absent *Server
	if absent.startBackground(func() {}) || absent.WaitBackground(ctx) == nil || s.WaitBackground(nil) == nil {
		t.Fatal("invalid owner or join context accepted")
	}
}
