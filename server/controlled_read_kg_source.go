package server

import (
	"context"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/kgfixture"
)

// The opt-in fixture host checks consistency with its registered KG. The real
// Source still authenticates every field and the final audit/authority; this
// wrapper neither issues provenance nor supplies missing metadata.
func bindControlledReadFixtureFindings(resolve ControlledReadFindingResolver) ControlledReadFindingResolver {
	if resolve == nil {
		return nil
	}
	return func(ctx context.Context, request ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
		snapshot, err := request.FixtureSnapshot(ctx)
		if err != nil {
			return ControlledReadFindingPlan{}, ErrControlledFindingUnavailable
		}
		plan, err := resolve(ctx, request)
		if err != nil || plan.Source == nil || !sameControlledReadFixture(plan.KG, snapshot) ||
			plan.Expected.KGPolicyVersion != snapshot.PolicyVersion() {
			return ControlledReadFindingPlan{}, ErrControlledFindingUnavailable
		}
		// A slow resolver must not publish a plan after close, cancellation or expiry.
		current, err := request.FixtureSnapshot(ctx)
		if err != nil || current != snapshot {
			return ControlledReadFindingPlan{}, ErrControlledFindingUnavailable
		}
		plan.Source = bindControlledReadFixtureSource(snapshot, plan.Expected, plan.Source)
		return plan, nil
	}
}

func bindControlledReadFixtureSource(snapshot *kgfixture.Snapshot, expected evidencebind.Context, read e3record.SourceReader) e3record.SourceReader {
	return func(ctx context.Context, request evidencebind.Context) (e3record.Source, error) {
		if ctx == nil || ctx.Err() != nil || snapshot == nil || snapshot.Err() != nil || read == nil || request != expected {
			return e3record.Source{}, ErrControlledFindingUnavailable
		}
		// Use the finalizer's context unchanged, including its real host read scope.
		// The Config handle is already closed at this phase; the captured snapshot
		// remains live until finalization returns. Never reopen an input file here.
		source, err := read(ctx, request)
		if err != nil || ctx.Err() != nil || snapshot.Err() != nil ||
			source.Expected != expected || source.KGSnapshotRef != snapshot.Ref() ||
			source.KGSnapshotDigest != snapshot.Digest() || source.GenerationRef != snapshot.GenerationRef() ||
			source.InstanceRef != snapshot.InstanceRef() || source.ResetGeneration != snapshot.ResetGeneration() {
			return e3record.Source{}, ErrControlledFindingUnavailable
		}
		// Preserve ModelTag/EvalRunID, evidence/revision, completion and audit truth.
		// Existing E3 preparation still checks policy/canary evidence and storage.
		return source, nil
	}
}
