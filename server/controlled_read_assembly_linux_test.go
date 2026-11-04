package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
)

func TestControlledReadAssemblyBindsActualServerAndReadsFresh(t *testing.T) {
	f := newFinalAuditSourceFixture(t)
	expected := evidencebind.Context{Scope: f.scope}
	metadataCalls, resolverCalls := 0, 0
	metadata := e3record.Source{Expected: expected, KGSnapshotRef: "synthetic-kg", ModelTag: "synthetic-model"}
	resolve, err := f.s.bindControlledReadAuditSource(func(context.Context, ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
		resolverCalls++
		return ControlledReadFindingPlan{Expected: expected, Source: func(context.Context, evidencebind.Context) (e3record.Source, error) {
			metadataCalls++
			return metadata, nil
		}}, nil
	}, &f.options)
	if err != nil || resolverCalls != 0 || metadataCalls != 0 {
		t.Fatalf("bootstrap read finding metadata: %v", err)
	}
	plan, err := resolve(context.Background(), ControlledReadVerificationRequest{})
	if err != nil || resolverCalls != 1 || metadataCalls != 0 {
		t.Fatalf("resolver eagerly consumed Source: %v", err)
	}
	got, err := plan.Source(context.Background(), expected)
	if err != nil || got.FinalCheckpointRef != f.anchor.Ref || got.FinalCheckpointSequence != 4 ||
		got.KGSnapshotRef != metadata.KGSnapshotRef || got.ModelTag != metadata.ModelTag ||
		got.Completed || got.Finalized || got.EvidenceRevision != 0 || got.EvidenceDigest != ([32]byte{}) ||
		!got.RecordCreatedAt.IsZero() || got.EvalRunID != "" {
		t.Fatalf("audit assembly altered producer facts: %v", err)
	}
	// A later head revocation is observed by the same plan's fresh Source.
	f.options.LatestCheckpoint = nil // reader owns a copy of options
	f.cancelLifecycle()
	if got, err := plan.Source(context.Background(), expected); !errors.Is(err, ErrControlledReadAuditSource) || got.FinalCheckpointRef != "" {
		t.Fatal("revoked server lifecycle retained audit authority")
	}
	if _, err := resolve(context.Background(), ControlledReadVerificationRequest{}); !errors.Is(err, ErrControlledReadAuditSource) || resolverCalls != 1 {
		t.Fatal("revoked server called the input resolver")
	}
}

func TestControlledReadAssemblyRejectsForeignOrMissingRuntime(t *testing.T) {
	f := newFinalAuditSourceFixture(t)
	calls := 0
	resolver := func(context.Context, ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
		calls++
		return ControlledReadFindingPlan{}, nil
	}
	for _, s := range []*Server{nil, {}, {engine: &Engine{}}, {engine: &Engine{controlledReads: newControlledReadRuntime(f.store)}}} {
		if got, err := s.bindControlledReadAuditSource(resolver, &f.options); err == nil || got != nil {
			t.Fatal("unconfigured server bound another server's Source")
		}
	}
	if calls != 0 {
		t.Fatal("failed bootstrap invoked resolver")
	}
}

func TestControlledReadAssemblyResolverFailureAndCancellation(t *testing.T) {
	for _, name := range []string{"nil-source", "resolver-error", "cancel-before", "cancel-during", "root-lost"} {
		t.Run(name, func(t *testing.T) {
			f := newFinalAuditSourceFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			private := errors.New("synthetic resolver failure")
			resolve, err := f.s.bindControlledReadAuditSource(func(context.Context, ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
				calls++
				if name == "resolver-error" {
					return ControlledReadFindingPlan{}, private
				}
				if name == "cancel-during" {
					cancel()
				}
				return ControlledReadFindingPlan{}, nil
			}, &f.options)
			if err != nil {
				t.Fatal(err)
			}
			if name == "cancel-before" {
				cancel()
			}
			if name == "root-lost" {
				f.cancelLifecycle()
			}
			plan, err := resolve(ctx, ControlledReadVerificationRequest{})
			if err == nil || plan.Source != nil {
				t.Fatal("failed resolver exposed a usable plan")
			}
			if name == "resolver-error" && !errors.Is(err, private) {
				t.Fatal("resolver failure lost its cause")
			}
			if (name == "cancel-before" || name == "root-lost") && calls != 0 {
				t.Fatal("invalid lifecycle invoked resolver")
			}
		})
	}
}

func TestControlledReadAssemblyCompatibilityAndInvalidOptions(t *testing.T) {
	f := newFinalAuditSourceFixture(t)
	calls := 0
	resolver := func(context.Context, ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
		calls++
		return ControlledReadFindingPlan{}, nil
	}
	plain, err := f.s.bindControlledReadAuditSource(resolver, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plain(context.Background(), ControlledReadVerificationRequest{}); err != nil || calls != 1 {
		t.Fatal("compatibility resolver changed")
	}
	if _, err := f.s.bindControlledReadAuditSource(nil, &f.options); err == nil {
		t.Fatal("missing resolver accepted")
	}
	options := f.options
	options.ResolveKey = nil
	if _, err := f.s.bindControlledReadAuditSource(resolver, &options); err == nil || calls != 1 {
		t.Fatal("invalid audit options invoked input adapter")
	}
}

func TestControlledReadAssemblyStartupRejectsBeforeCreatingFiles(t *testing.T) {
	root := t.TempDir()
	for _, ctx := range []context.Context{nil, context.Background()} {
		s, err := NewWithControlledReadAssembly(ctx, nil, filepath.Join(root, "skills"), filepath.Join(root, "data"), filepath.Join(root, "keys"), ControlledReadRevisionHostOptions{})
		if s != nil || !errors.Is(err, ErrControlledReadHostConfiguration) {
			t.Fatal("incomplete assembly started")
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("incomplete assembly created startup resources")
	}
}
