package main

import (
	"errors"
	"github.com/Autumn-27/artex/server"
	"testing"
)

func TestPolicyModeChoosesExactlyOneStartupPath(t *testing.T) {
	for _, tc := range []struct {
		mode            string
		local, external int
		invalid         bool
	}{
		{"", 1, 0, false}, {"local-only", 1, 0, false}, {"external", 0, 1, false}, {" external", 0, 0, true}, {"bogus", 0, 0, true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			localCalls, externalCalls := 0, 0
			localServer, externalServer := &server.Server{}, &server.Server{}
			got, err := newPolicyServer(tc.mode, func() (*server.Server, error) { localCalls++; return localServer, nil }, func() *server.Server { externalCalls++; return externalServer })
			if localCalls != tc.local || externalCalls != tc.external || (err != nil) != tc.invalid {
				t.Fatalf("mode %q: local=%d external=%d error=%v", tc.mode, localCalls, externalCalls, err)
			}
			if tc.invalid {
				if got != nil {
					t.Fatal("invalid mode constructed server")
				}
				return
			}
			want := localServer
			if tc.external == 1 {
				want = externalServer
			}
			if got != want {
				t.Fatal("wrong startup server returned")
			}
		})
	}
}

func TestLocalPolicyFailureStopsStartupWithoutExternalFallback(t *testing.T) {
	denied := errors.New("synthetic policy failure")
	for _, mode := range []string{"", "local-only"} {
		externalCalls := 0
		got, err := newPolicyServer(mode, func() (*server.Server, error) { return nil, denied }, func() *server.Server { externalCalls++; return &server.Server{} })
		if got != nil || !errors.Is(err, denied) || externalCalls != 0 {
			t.Fatalf("failed startup selected fallback: %p %v calls=%d", got, err, externalCalls)
		}
	}
}
