package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/controlledhost"
)

// 이 fixture는 소유 FD의 수명만 관찰하며 운영 RootTrust를 발급하지 않는다.
type firstDiagnosisAcceptanceRoot struct {
	ctx         context.Context
	directory   string
	anchor      *os.File
	released    atomic.Int64
	closeFailed atomic.Bool
}

func (r *firstDiagnosisAcceptanceRoot) Context() context.Context { return r.ctx }
func (r *firstDiagnosisAcceptanceRoot) Anchor() *os.File         { return r.anchor }
func (r *firstDiagnosisAcceptanceRoot) Directory() string        { return r.directory }
func (r *firstDiagnosisAcceptanceRoot) CheckRoot(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil || r.ctx.Err() != nil {
		return errors.New("synthetic root unavailable")
	}
	held, err := r.anchor.Stat()
	current, pathErr := os.Lstat(r.directory)
	if err != nil || pathErr != nil || !os.SameFile(held, current) {
		return errors.New("synthetic root continuity lost")
	}
	return nil
}
func (r *firstDiagnosisAcceptanceRoot) CheckTaskQuiescent(context.Context, int64) error {
	return errors.New("synthetic fixture has no task quiescence authority")
}
func (r *firstDiagnosisAcceptanceRoot) Release() {
	r.released.Add(1)
	if err := r.anchor.Close(); err != nil {
		r.closeFailed.Store(true)
	}
}

func firstDiagnosisAcceptanceRootLease(t *testing.T) *firstDiagnosisAcceptanceRoot {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "private-authority-canary")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	anchor, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	lease := &firstDiagnosisAcceptanceRoot{ctx: t.Context(), directory: directory, anchor: anchor}
	t.Cleanup(func() {
		// 실패하는 생성자가 소유권을 유실해도 시험 FD는 남기지 않는다.
		if lease.released.Load() == 0 {
			if err := anchor.Close(); err != nil {
				t.Error("미인수 시험 FD 정리 실패")
			}
		}
	})
	return lease
}

func TestFirstDiagnosisAssemblyMissingAuthorityRejectsAndReleases(t *testing.T) {
	for _, mode := range []string{"missing-sources", "nil-context", "cancelled-context"} {
		t.Run(mode, func(t *testing.T) {
			root := firstDiagnosisAcceptanceRootLease(t)
			ctx := t.Context()
			if mode == "nil-context" {
				ctx = nil
			}
			if mode == "cancelled-context" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			host, err := NewControlledReadAssemblyHost(ctx, ControlledReadAssemblyHostConfig{
				Sources: controlledhost.Sources{Root: root},
			})
			if host != nil || !errors.Is(err, ErrControlledReadHostConfiguration) {
				if host != nil {
					if closeErr := host.Close(t.Context()); closeErr != nil {
						t.Error("예기치 않은 조립 자원 정리 실패")
					}
				}
				t.Fatal("필수 공급이 없는 strict 조립이 기동 가능 상태를 반환함")
			}
			if got := root.released.Load(); got != 1 || root.closeFailed.Load() {
				t.Fatalf("거부된 Root 소유권 해제 횟수 오류: releases=%d close_failed=%t", got, root.closeFailed.Load())
			}
			if strings.Contains(err.Error(), root.directory) || strings.Contains(err.Error(), "private-authority-canary") {
				t.Fatal("조립 실패가 비공개 공급 경로를 노출함")
			}
			if _, statErr := root.anchor.Stat(); statErr == nil {
				t.Fatal("소유권을 반환한 Root FD가 열린 상태임")
			}
		})
	}
}

func TestFirstDiagnosisAssemblyTypedNilRootRejects(t *testing.T) {
	var root *firstDiagnosisAcceptanceRoot
	host, err := NewControlledReadAssemblyHost(t.Context(), ControlledReadAssemblyHostConfig{
		Sources: controlledhost.Sources{Root: root},
	})
	if host != nil || !errors.Is(err, ErrControlledReadHostConfiguration) {
		if host != nil {
			if closeErr := host.Close(t.Context()); closeErr != nil {
				t.Error("예기치 않은 조립 자원 정리 실패")
			}
		}
		t.Fatal("typed-nil Root를 실제 권위 공급자로 수용함")
	}
}
