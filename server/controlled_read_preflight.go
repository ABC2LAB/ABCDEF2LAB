package server

import (
	"context"

	"github.com/Autumn-27/artex/internal/privateinput"
	"github.com/Autumn-27/artex/transmit"
)

// CheckRegisteredReadPreparation은 기존 보호 파일의 결속만 검사한다.
// DB·모델·대상에 접속하거나 승인·실행 수명을 취득하지 않는다.
// 파일 검사 성공은 세션 인증·조회 비변경·실행 허가의 증명이 아니다.
func CheckRegisteredReadPreparation(ctx context.Context, metadataFile, snapshotDir, registryFile string) error {
	if ctx == nil || ctx.Err() != nil || privateinput.CheckDirectory(snapshotDir, true) != nil {
		return ErrControlledReadHostConfiguration
	}
	ctx, cancel := context.WithTimeout(ctx, controlledPreparationTimeout)
	defer cancel()
	body, err := privateinput.ReadFile(metadataFile, maxControlledAssemblyMetadataBytes)
	if err != nil {
		return ErrControlledReadHostConfiguration
	}
	defer clear(body)
	var doc ControlledReadAssemblyMetadata
	if decodeControlledAssemblyJSON(body, &doc) != nil || doc.Format != ControlledReadAssemblyMetadataFormat || !validControlledMetadataProjection(doc) {
		return ErrControlledReadHostConfiguration
	}
	files, err := cloneControlledReadFixtureRegistries(map[string]string{doc.SnapshotRef: registryFile})
	if err != nil {
		return ErrControlledReadHostConfiguration
	}
	store := controlledReadStore{snapshotDir: snapshotDir, fixtureRegistries: files}
	spec, err := store.load(doc.Source.Expected.Scope.TaskRef, doc.Source.Expected.IntentID, doc.SnapshotRef)
	if err != nil {
		return ErrControlledReadHostConfiguration
	}
	defer func() {
		clear(spec.Key)
		for i := range spec.HTTP.Sessions {
			clear(spec.HTTP.Sessions[i].Headers)
		}
		if spec.fixture != nil {
			spec.fixture.Invalidate()
		}
	}()
	if !sameControlledAssemblySnapshot(spec.fixture, doc.Source) ||
		transmit.CheckReadRegistration(spec.HTTP.Origin, spec.HTTP.Sessions, spec.HTTP.Resources) != nil {
		return ErrControlledReadHostConfiguration
	}
	p := ControlledReadAssemblyPreparation{SnapshotRef: doc.SnapshotRef, TargetOrigin: doc.TargetOrigin,
		ModelTag: doc.Source.ModelTag, Metadata: doc.Source, KG: spec.fixture}
	if checkControlledPreparationSnapshot(ctx, spec.HTTP, p) != nil || ctx.Err() != nil {
		return ErrControlledReadHostConfiguration
	}
	return nil
}
