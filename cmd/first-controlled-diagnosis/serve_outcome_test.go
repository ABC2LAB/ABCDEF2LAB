package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

type savedRunRuntime struct {
	closed bool
	err    error
	called bool
}

func (r *savedRunRuntime) Handler() http.Handler       { return http.NotFoundHandler() }
func (r *savedRunRuntime) TaskID() int64               { return 7 }
func (r *savedRunRuntime) ResourcesClosed() bool       { return r.closed }
func (r *savedRunRuntime) Close(context.Context) error { r.called = true; return r.err }
func TestCloseAndSaveDiagnosisOutcome(t *testing.T) {
	failure := errors.New("synthetic failure")
	storage := errors.New("synthetic storage failure")
	for _, tc := range []struct {
		name              string
		closed            bool
		closeErr, saveErr error
	}{
		{"success", true, nil, nil},
		{"cancelled", true, failure, nil},
		{"closure-failed", false, failure, nil},
		{"storage-failed", true, nil, storage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := &savedRunRuntime{closed: tc.closed, err: tc.closeErr}
			calls := 0
			err := closeAndSaveDiagnosis(t.Context(), runtime, func(ctx context.Context, closureErr error) error {
				calls++
				if !runtime.called {
					t.Fatal("persist before close")
				}
				if tc.closed && closureErr != nil || !tc.closed && closureErr != tc.closeErr {
					t.Fatal("closure misclassified")
				}
				return tc.saveErr
			})
			if calls != 1 || (tc.closeErr != nil && !errors.Is(err, tc.closeErr)) || (tc.saveErr != nil && !errors.Is(err, tc.saveErr)) || (tc.closeErr == nil && tc.saveErr == nil && err != nil) {
				t.Fatal("close/save result lost", err)
			}
		})
	}
}
