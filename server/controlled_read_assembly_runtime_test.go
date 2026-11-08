package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestControlledReadAssemblyRuntimeShutdownOrdering(t *testing.T) {
	cfg, root := assemblyProvidersFixture(t)
	host, err := NewControlledReadAssemblyHost(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(t.Context())
	stages := []string{}
	r := &ControlledReadAssemblyRuntime{host: host, cancel: cancel, server: &Server{}}
	r.shutdown = ControlledReadAssemblyShutdown{
		BlockAdmission: func(ctx context.Context, _ *Server) error {
			stages = append(stages, "admission")
			if !errors.Is(r.Close(ctx), ErrControlledReadAssemblyClosing) {
				t.Fatal("reentrant close waited on a callback")
			}
			return nil
		},
		CancelAndDrain: func(context.Context, *Server) error { stages = append(stages, "drain"); return nil },
		CheckResults: func(context.Context, *Server) error {
			stages = append(stages, "results")
			if root.releases.Load() != 0 {
				t.Fatal("released before storage check")
			}
			return nil
		},
	}
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stages, []string{"admission", "drain", "results"}) || root.releases.Load() != 1 {
		t.Fatal("shutdown order or ownership changed")
	}
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("closed runtime admitted a request")
	}
}

func TestControlledReadAssemblyRuntimeRetainsOnFailure(t *testing.T) {
	for _, mode := range []string{"drain-timeout", "unknown-result", "pending-provider"} {
		t.Run(mode, func(t *testing.T) {
			cfg, root := assemblyProvidersFixture(t)
			host, err := NewControlledReadAssemblyHost(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			_, cancel := context.WithCancel(t.Context())
			r := &ControlledReadAssemblyRuntime{host: host, cancel: cancel, server: &Server{}}
			blocked, drained, checks := 0, 0, 0
			retry := false
			r.shutdown = ControlledReadAssemblyShutdown{
				BlockAdmission: func(context.Context, *Server) error { blocked++; return nil },
				CancelAndDrain: func(ctx context.Context, _ *Server) error {
					drained++
					if mode == "drain-timeout" && !retry {
						<-ctx.Done()
						return ctx.Err()
					}
					return nil
				},
				CheckResults: func(context.Context, *Server) error {
					checks++
					if mode == "unknown-result" && !retry {
						return ErrControlledFindingOutcomeUnknown
					}
					return nil
				},
			}
			var finish func()
			if mode == "pending-provider" {
				_, finish, err = host.resources.(*controlledReadAssemblyResources).enter(t.Context())
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx, stop := context.WithTimeout(t.Context(), 10*time.Millisecond)
			err = r.Close(ctx)
			stop()
			if err == nil || root.releases.Load() != 0 || !r.admissionStopped.Load() {
				t.Fatal("failed shutdown discarded ownership")
			}
			if mode == "unknown-result" && !errors.Is(err, ErrControlledFindingOutcomeUnknown) {
				t.Fatal("unknown commit became rollback")
			}
			if strings.Contains(err.Error(), ErrControlledFindingOutcomeUnknown.Error()) {
				t.Fatal("shutdown exposed raw provider error")
			}
			if finish != nil {
				finish()
			}
			retry = true
			if err = r.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if root.releases.Load() != 1 || blocked != 1 || drained > 2 || checks > 2 {
				t.Fatal("completed phases repeated or root release lost")
			}
		})
	}
}

func TestControlledReadAssemblyRuntimeAuthorityLossBlocksHandler(t *testing.T) {
	cfg, _ := assemblyProvidersFixture(t)
	host, err := NewControlledReadAssemblyHost(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	r := &ControlledReadAssemblyRuntime{host: host, handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusNoContent) })}
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if calls != 1 || w.Code != http.StatusNoContent {
		t.Fatal("live root unavailable")
	}
	host.Revoke()
	w = httptest.NewRecorder()
	r.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if calls != 1 || w.Code != http.StatusServiceUnavailable {
		t.Fatal("revoked authority admitted work")
	}
	if err = host.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type assemblyRuntimePrivateError struct{ secret string }

func (e assemblyRuntimePrivateError) Error() string { return e.secret }
func TestControlledReadAssemblyShutdownErrorRedacted(t *testing.T) {
	secret := "SYNTHETIC-PRIVATE-PROVIDER-DATA"
	cause := assemblyRuntimePrivateError{secret}
	err := controlledReadAssemblyStageError{"results", cause}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		if strings.Contains(fmt.Sprintf(format, err), secret) {
			t.Fatal("shutdown failure formatting exposed provider data")
		}
	}
	if !errors.Is(err, cause) {
		t.Fatal("result classification lost its cause")
	}
}

func TestControlledReadAssemblyStartConsumesRootOnPreflightFailure(t *testing.T) {
	for _, mode := range []string{"missing-manager", "cancelled", "nil-context"} {
		t.Run(mode, func(t *testing.T) {
			cfg, root := assemblyProvidersFixture(t)
			ctx := t.Context()
			if mode == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if mode == "nil-context" {
				ctx = nil
			}
			runtime, err := StartControlledReadAssembly(ctx, nil, ControlledReadAssemblyStartOptions{Host: cfg})
			if err == nil || runtime != nil || root.releases.Load() != 1 {
				t.Fatal("ambiguous root ownership on startup failure")
			}
		})
	}
}

func TestControlledReadAssemblyRuntimeReleasesAfterTypedOutcome(t *testing.T) {
	cfg, root := assemblyProvidersFixture(t)
	host, err := NewControlledReadAssemblyHost(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(t.Context())
	checks := 0
	r := &ControlledReadAssemblyRuntime{host: host, cancel: cancel, server: &Server{}, shutdown: ControlledReadAssemblyShutdown{BlockAdmission: func(context.Context, *Server) error { return nil }, CancelAndDrain: func(context.Context, *Server) error { return nil }, CheckResults: func(context.Context, *Server) error { checks++; return ErrFirstDiagnosisOutcome }}}
	for i := 0; i < 2; i++ {
		if err := r.Close(t.Context()); !errors.Is(err, ErrFirstDiagnosisOutcome) {
			t.Fatal("실제 마감 뒤 판정 오류를 잃었습니다")
		}
	}
	if !r.closed.Load() || root.releases.Load() != 1 || checks != 1 {
		t.Fatal("안전한 자원 반환과 오류 보존이 분리되지 않았습니다")
	}
}

func TestControlledReadAssemblyRuntimeCanceledCloseFencesWithoutRelease(t *testing.T) {
	cfg, root := assemblyProvidersFixture(t)
	host, err := NewControlledReadAssemblyHost(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(t.Context())
	r := &ControlledReadAssemblyRuntime{host: host, cancel: cancel, server: &Server{}, shutdown: ControlledReadAssemblyShutdown{BlockAdmission: func(context.Context, *Server) error { return nil }, CancelAndDrain: func(context.Context, *Server) error { return nil }, CheckResults: func(context.Context, *Server) error { return nil }}}
	ctx, stop := context.WithCancel(t.Context())
	stop()
	if !errors.Is(r.Close(ctx), context.Canceled) || !r.admissionStopped.Load() || root.releases.Load() != 0 {
		t.Fatal("취소된 Close가 admission을 차단하지 않거나 자원을 반환했습니다")
	}
	if err := r.Close(t.Context()); err != nil || root.releases.Load() != 1 {
		t.Fatal("실제 drain 뒤 자원을 반환하지 못했습니다")
	}
}
