package server

import (
	"context"
	"path/filepath"
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/kgfixture"
)

func cloneControlledReadFixtureRegistries(files map[string]string) (map[string]string, error) {
	if files == nil {
		return nil, nil
	}
	if len(files) == 0 {
		return nil, ErrControlledReadHostConfiguration
	}
	out := make(map[string]string, len(files))
	for ref, name := range files {
		if !safeSnapshotRef.MatchString(ref) || !safeFile.MatchString(name) || name == ref+".json" {
			return nil, ErrControlledReadHostConfiguration
		}
		out[ref] = name
	}
	return out, nil
}

// Bootstrap runs before restored work or background admission. The mapping is
// trusted host input; neither snapshot JSON nor an HTTP/model request selects a
// registry path. The existing private-file reader supplies the I/O boundary.
func (s *Server) configureControlledReadFixtureRegistries(files map[string]string) error {
	if files == nil {
		return nil
	}
	frozen, err := cloneControlledReadFixtureRegistries(files)
	if err != nil || s == nil || s.engine == nil || s.engine.controlledReads == nil {
		return ErrControlledReadHostConfiguration
	}
	r := s.engine.controlledReads
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.store == nil || r.store.fixtureRegistries != nil || len(r.pending) != 0 || len(r.active) != 0 || len(r.tokens) != 0 {
		return ErrControlledReadHostConfiguration
	}
	r.store.fixtureRegistries = frozen
	if s.m != nil && s.m.pg != nil && s.m.pg.DB != nil {
		r.mapStore = &accessMapStore{pool: s.m.pg.DB}
	}
	return nil
}

func (s *controlledReadStore) bindFixtureRegistry(spec *controlledReadSpec, ref string) error {
	if s.fixtureRegistries == nil {
		return nil
	}
	name, ok := s.fixtureRegistries[ref]
	if !ok {
		return &kgfixture.Error{Code: kgfixture.CodeUnreadable}
	}
	body, err := readPrivate(filepath.Join(s.snapshotDir, name), kgfixture.MaxRegistryBytes)
	if err != nil {
		return &kgfixture.Error{Code: kgfixture.CodeUnreadable}
	}
	defer clear(body)
	if s.authorizeInput != nil && s.authorizeInput(filepath.Join(s.snapshotDir, name), body) != nil {
		return &kgfixture.Error{Code: kgfixture.CodeUnreadable}
	}
	snapshot, err := kgfixture.Parse(body)
	if err != nil {
		return err
	} // kgfixture errors contain fixed codes only
	pairing := kgfixture.Pairing{ResetRef: spec.ResetRef, Resources: make(map[string][]idor.Marker, len(spec.HTTP.Resources))}
	for _, session := range spec.HTTP.Sessions {
		pairing.Labels = append(pairing.Labels, session.Account.Label)
	}
	for _, resource := range spec.HTTP.Resources {
		var markers []idor.Marker
		if resource.View != nil {
			markers = resource.View.Markers
		}
		pairing.Resources[resource.Ref] = markers
	}
	if err := snapshot.CheckPairing(pairing); err != nil {
		return err
	}
	spec.fixture = snapshot
	return nil
}

// FixtureSnapshot returns the registry frozen and paired at this registration,
// not a reloaded file. It is a trusted Go-host seam for the existing resolver and
// Source assembly. It grants no host/key authority and is never in request JSON.
// New lookups require the actual live, request-pinned runtime handle. A captured
// snapshot is invalidated after execution finalization returns; Invalidate by
// itself is not a network-stop or host-lease revocation mechanism.
func (r ControlledReadVerificationRequest) FixtureSnapshot(ctx context.Context) (*kgfixture.Snapshot, error) {
	if _, err := r.ConfigView(ctx); err != nil {
		return nil, ErrControlledFindingUnavailable
	}
	s := r.config.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil || s.closed || s.life == nil || s.life.Err() != nil || !time.Now().Before(s.until) ||
		s.fixture == nil || s.fixture.Err() != nil {
		return nil, ErrControlledFindingUnavailable
	}
	return s.fixture, nil
}

func sameControlledReadFixture(resolver idor.PolicyResolver, registered *kgfixture.Snapshot) bool {
	snapshot, ok := resolver.(*kgfixture.Snapshot)
	return ok && snapshot != nil && snapshot == registered && snapshot.Err() == nil
}
