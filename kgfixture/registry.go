// Package kgfixture is the stage-1 canonical KG reader for IDOR: a manually
// registered fixture registry, frozen once into an immutable Snapshot that
// implements idor.PolicyResolver (docs/contracts/kg-fixture-registry.md).
//
// The package does no I/O. The server reads the registry file through its own
// private-file reader (owner-only, size-bounded) and hands the bytes to Parse.
// Parse is fail-closed: any rule violation rejects the whole registry, and errors
// carry only a fixed code, never file content (canary values included).
package kgfixture

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
)

// FormatV1 is the only registry format this package reads.
const FormatV1 = "abc2lab.kg-fixture-registry/v1"

// MaxRegistryBytes bounds the input. A stage-1 registry holds two principals and
// a handful of resources; anything near this size is not a stage-1 registry.
const MaxRegistryBytes = 1 << 20

const (
	policyVersionPrefix = "kg-fixture/v1:"
	// maxRefBytes is the shared reference limit of evidencebind, e3record and
	// transmit, which store these refs.
	maxRefBytes = 128
	// maxSnapshotIDBytes keeps the policy version (prefix, id, ':', hex digest)
	// within maxRefBytes, so it passes the same reference checks.
	maxSnapshotIDBytes = maxRefBytes - len(policyVersionPrefix) - 1 - 2*sha256.Size
)

// refPattern is the shared reference grammar (evidencebind/check.go,
// transmit/config.go); the length bound and "://" ban are checked in validRef.
var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)

// timestampPattern admits RFC 3339 UTC instants with a literal Z only, so a
// validity bound has one spelling and no offset arithmetic.
var timestampPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`)

// Code is a fixed error code (contract §6). It is the whole error text.
type Code string

const (
	CodeUnreadable      Code = "registry_unreadable"
	CodeFormat          Code = "registry_format"
	CodeUnknownField    Code = "registry_unknown_field"
	CodeDuplicate       Code = "registry_duplicate"
	CodeRefFormat       Code = "ref_format"
	CodeCanaryNotUnique Code = "canary_value_not_unique"
	CodeTrivialMissing  Code = "canary_trivial_missing"
	CodeSnapshotInvalid Code = "snapshot_invalid"
)

// Error reports a rejected registry or an invalidated snapshot by code only.
type Error struct{ Code Code }

func (e *Error) Error() string { return "kgfixture: " + string(e.Code) }

func reject(code Code) error { return &Error{Code: code} }

var (
	rootFields      = []string{"format", "snapshot_id", "run_id", "reset_generation", "generation_ref", "instance_ref", "principals", "resources"}
	principalFields = []string{"label"}
	resourceFields  = []string{"resource_ref", "owner_ref", "policies", "canaries"}
	policyFields    = []string{"evaluated_ref", "expected_access", "sharing", "delegation", "valid_from", "valid_until"}
	canaryFields    = []string{"field", "value", "trivial"}
)

// Snapshot is one frozen registry. It never changes after Parse except for
// Invalidate, and is safe for concurrent use. The owner and cross lookups of one
// execution must share the same Snapshot (contract §4).
type Snapshot struct {
	ref             string
	digest          [sha256.Size]byte
	policyVersion   string
	runID           string
	resetGeneration string
	generationRef   string
	instanceRef     string
	principals      []string
	resources       map[string]*resource
	invalid         atomic.Bool
}

type resource struct {
	owner    string
	policies map[string]oracle.PolicyAssertion
	canaries []idor.Marker
}

var _ idor.PolicyResolver = (*Snapshot)(nil)

// Parse validates a registry and freezes it. Empty or oversized input is
// registry_unreadable; every other failure is one of the contract §6 codes.
func Parse(body []byte) (*Snapshot, error) {
	if len(body) == 0 || len(body) > MaxRegistryBytes {
		return nil, reject(CodeUnreadable)
	}
	decoded, err := decodeStrict(body)
	if errors.Is(err, errDuplicateKey) {
		return nil, reject(CodeDuplicate)
	}
	if err != nil {
		return nil, reject(CodeFormat)
	}
	root, err := object(decoded, rootFields)
	if err != nil {
		return nil, err
	}
	snapshot, err := parseRoot(root)
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalJSON(decoded)
	if err != nil {
		return nil, reject(CodeFormat)
	}
	snapshot.digest = sha256.Sum256(canonical)
	snapshot.policyVersion = policyVersionPrefix + snapshot.ref + ":" + hex.EncodeToString(snapshot.digest[:])
	return snapshot, nil
}

func parseRoot(root map[string]any) (*Snapshot, error) {
	format, err := text(root, "format")
	if err != nil {
		return nil, err
	}
	if format != FormatV1 {
		return nil, reject(CodeFormat)
	}
	s := &Snapshot{resources: map[string]*resource{}}
	if s.ref, err = ref(root, "snapshot_id"); err != nil {
		return nil, err
	}
	if len(s.ref) > maxSnapshotIDBytes {
		return nil, reject(CodeRefFormat)
	}
	for _, field := range []struct {
		key string
		dst *string
	}{{"run_id", &s.runID}, {"reset_generation", &s.resetGeneration}, {"generation_ref", &s.generationRef}, {"instance_ref", &s.instanceRef}} {
		if *field.dst, err = ref(root, field.key); err != nil {
			return nil, err
		}
	}
	principals, err := list(root, "principals")
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, item := range principals {
		principal, err := object(item, principalFields)
		if err != nil {
			return nil, err
		}
		label, err := ref(principal, "label")
		if err != nil {
			return nil, err
		}
		if known[label] {
			return nil, reject(CodeDuplicate)
		}
		known[label] = true
		s.principals = append(s.principals, label)
	}
	resources, err := list(root, "resources")
	if err != nil {
		return nil, err
	}
	canaryValues := map[string]bool{}
	for _, item := range resources {
		if err := s.addResource(item, known, canaryValues); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Snapshot) addResource(item any, principals, canaryValues map[string]bool) error {
	fields, err := object(item, resourceFields)
	if err != nil {
		return err
	}
	resourceRef, err := ref(fields, "resource_ref")
	if err != nil {
		return err
	}
	// Full "type:id", split at the last colon like e3record: both sides non-empty.
	separator := strings.LastIndex(resourceRef, ":")
	if separator <= 0 || separator == len(resourceRef)-1 {
		return reject(CodeRefFormat)
	}
	if _, duplicate := s.resources[resourceRef]; duplicate {
		return reject(CodeDuplicate)
	}
	owner, err := principalRef(fields, "owner_ref", principals)
	if err != nil {
		return err
	}
	r := &resource{owner: owner, policies: map[string]oracle.PolicyAssertion{}}
	policies, err := array(fields, "policies")
	if err != nil {
		return err
	}
	for _, item := range policies {
		evaluated, policy, err := parsePolicy(item, principals)
		if err != nil {
			return err
		}
		if _, duplicate := r.policies[evaluated]; duplicate {
			return reject(CodeDuplicate)
		}
		r.policies[evaluated] = policy
	}
	canaries, err := array(fields, "canaries")
	if err != nil {
		return err
	}
	// A canary is keyed by resource and field (kg-policy §3.2), and the E² view
	// that receives these markers refuses a repeated field (transmit/config.go),
	// so one field may carry only one value per resource.
	canaryFieldsSeen := map[string]bool{}
	for _, item := range canaries {
		marker, err := parseCanary(item)
		if err != nil {
			return err
		}
		if canaryFieldsSeen[marker.Field] {
			return reject(CodeDuplicate)
		}
		canaryFieldsSeen[marker.Field] = true
		// One generation's canary values are globally unique (kg-policy §3.2(3)),
		// so an observed value points back to exactly one resource.
		if canaryValues[marker.Value] {
			return reject(CodeCanaryNotUnique)
		}
		canaryValues[marker.Value] = true
		r.canaries = append(r.canaries, marker)
	}
	s.resources[resourceRef] = r
	return nil
}

func parsePolicy(item any, principals map[string]bool) (string, oracle.PolicyAssertion, error) {
	fields, err := object(item, policyFields)
	if err != nil {
		return "", oracle.PolicyAssertion{}, err
	}
	evaluated, err := principalRef(fields, "evaluated_ref", principals)
	if err != nil {
		return "", oracle.PolicyAssertion{}, err
	}
	access, err := text(fields, "expected_access")
	if err != nil {
		return "", oracle.PolicyAssertion{}, err
	}
	switch oracle.ExpectedAccess(access) {
	case oracle.AccessAllow, oracle.AccessDeny, oracle.AccessUnknown:
	default:
		return "", oracle.PolicyAssertion{}, reject(CodeFormat)
	}
	sharing, err := resolution(fields, "sharing")
	if err != nil {
		return "", oracle.PolicyAssertion{}, err
	}
	delegation, err := resolution(fields, "delegation")
	if err != nil {
		return "", oracle.PolicyAssertion{}, err
	}
	from, err := instant(fields, "valid_from")
	if err != nil {
		return "", oracle.PolicyAssertion{}, err
	}
	until, err := instant(fields, "valid_until")
	if err != nil {
		return "", oracle.PolicyAssertion{}, err
	}
	if !from.Before(until) {
		return "", oracle.PolicyAssertion{}, reject(CodeFormat)
	}
	// The registry is the canonical source in stage 1, so the hint trivially
	// matches it (contract §5); the loader checks are the only defence.
	return evaluated, oracle.PolicyAssertion{
		ExpectedAccess:       oracle.ExpectedAccess(access),
		Sharing:              sharing,
		Delegation:           delegation,
		HasRegisteredSource:  true,
		ValidFrom:            from,
		ValidUntil:           until,
		HintMatchesCanonical: true,
	}, nil
}

func parseCanary(item any) (idor.Marker, error) {
	fields, ok := item.(map[string]any)
	if !ok {
		return idor.Marker{}, reject(CodeFormat)
	}
	if err := knownFields(fields, canaryFields); err != nil {
		return idor.Marker{}, err
	}
	// trivial has no safe default (kg-policy §3.1: unsure means true), so its
	// absence is its own code rather than a silent false.
	trivialValue, present := fields["trivial"]
	if !present {
		return idor.Marker{}, reject(CodeTrivialMissing)
	}
	trivial, ok := trivialValue.(bool)
	if !ok {
		return idor.Marker{}, reject(CodeFormat)
	}
	field, err := text(fields, "field")
	if err != nil {
		return idor.Marker{}, err
	}
	value, err := text(fields, "value")
	if err != nil {
		return idor.Marker{}, err
	}
	return idor.Marker{Field: field, Value: value, Trivial: trivial}, nil
}

// object requires an object whose keys are exactly allowed: an unknown key is
// registry_unknown_field, a missing one registry_format.
func object(value any, allowed []string) (map[string]any, error) {
	fields, ok := value.(map[string]any)
	if !ok {
		return nil, reject(CodeFormat)
	}
	if err := knownFields(fields, allowed); err != nil {
		return nil, err
	}
	for _, key := range allowed {
		if _, present := fields[key]; !present {
			return nil, reject(CodeFormat)
		}
	}
	return fields, nil
}

func knownFields(fields map[string]any, allowed []string) error {
	for key := range fields {
		if !contains(allowed, key) {
			return reject(CodeUnknownField)
		}
	}
	return nil
}

func contains(list []string, key string) bool {
	for _, item := range list {
		if item == key {
			return true
		}
	}
	return false
}

func text(fields map[string]any, key string) (string, error) {
	value, ok := fields[key].(string)
	if !ok || value == "" {
		return "", reject(CodeFormat)
	}
	return value, nil
}

func ref(fields map[string]any, key string) (string, error) {
	value, err := text(fields, key)
	if err != nil {
		return "", err
	}
	if !validRef(value) {
		return "", reject(CodeRefFormat)
	}
	return value, nil
}

func validRef(value string) bool {
	return len(value) <= maxRefBytes && refPattern.MatchString(value) && !strings.Contains(value, "://")
}

func principalRef(fields map[string]any, key string, principals map[string]bool) (string, error) {
	label, err := ref(fields, key)
	if err != nil {
		return "", err
	}
	if !principals[label] {
		return "", reject(CodeFormat)
	}
	return label, nil
}

func array(fields map[string]any, key string) ([]any, error) {
	items, ok := fields[key].([]any)
	if !ok {
		return nil, reject(CodeFormat)
	}
	return items, nil
}

// list is a non-empty array: a registry without principals or resources can
// answer nothing and is an operator mistake, not a usable snapshot.
func list(fields map[string]any, key string) ([]any, error) {
	items, err := array(fields, key)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, reject(CodeFormat)
	}
	return items, nil
}

func resolution(fields map[string]any, key string) (oracle.ResolutionState, error) {
	value, err := text(fields, key)
	if err != nil {
		return "", err
	}
	switch state := oracle.ResolutionState(value); state {
	case oracle.ResolvedNone, oracle.ResolvedAllow, oracle.ResolutionUnknown:
		return state, nil
	}
	return "", reject(CodeFormat)
}

func instant(fields map[string]any, key string) (time.Time, error) {
	value, err := text(fields, key)
	if err != nil {
		return time.Time{}, err
	}
	if !timestampPattern.MatchString(value) {
		return time.Time{}, reject(CodeFormat)
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	// The oracle requires nonzero UTC instants in years 1..9999.
	if err != nil || parsed.Year() < 1 {
		return time.Time{}, reject(CodeFormat)
	}
	return parsed.UTC(), nil
}

// Ref is the registry snapshot_id (e3record Source.KGSnapshotRef).
func (s *Snapshot) Ref() string { return s.ref }

// Digest is the SHA-256 of the registry's canonical JSON (Source.KGSnapshotDigest).
func (s *Snapshot) Digest() [sha256.Size]byte { return s.digest }

// PolicyVersion is kg-fixture/v1:<snapshot_id>:<digest hex> (Context.KGPolicyVersion).
func (s *Snapshot) PolicyVersion() string { return s.policyVersion }

// RunID is the preparation run ID, not E² Scope.RunRef nor EvalRunID (contract §2).
func (s *Snapshot) RunID() string { return s.runID }

// ResetGeneration pairs with the E² snapshot reset_ref (contract §9).
func (s *Snapshot) ResetGeneration() string { return s.resetGeneration }

// GenerationRef is the harness-issued generation reference; never re-issued here.
func (s *Snapshot) GenerationRef() string { return s.generationRef }

// InstanceRef is the target instance reference.
func (s *Snapshot) InstanceRef() string { return s.instanceRef }

// Principals returns the registered labels (a copy), for the §9 cross-check
// against E² sessions[].label.
func (s *Snapshot) Principals() []string { return append([]string(nil), s.principals...) }

// ResourceRefs returns the registered resource refs in byte order (a copy), for
// the §9 cross-check against E² resources[].ref.
func (s *Snapshot) ResourceRefs() []string {
	refs := make([]string, 0, len(s.resources))
	for resourceRef := range s.resources {
		refs = append(refs, resourceRef)
	}
	sort.Strings(refs)
	return refs
}

// Markers returns a resource's registered canaries (a copy) for the §9 check
// that they equal the E² view markers. It is false for an unknown resource or
// an invalidated snapshot.
func (s *Snapshot) Markers(resourceRef string) ([]idor.Marker, bool) {
	if s.invalid.Load() {
		return nil, false
	}
	r, ok := s.resources[resourceRef]
	if !ok {
		return nil, false
	}
	return append([]idor.Marker{}, r.canaries...), true
}

// Invalidate ends the snapshot (reset generation changed, contract §4). Every
// later lookup on it fails, for every holder of the same Snapshot.
func (s *Snapshot) Invalidate() { s.invalid.Store(true) }

// Err is snapshot_invalid after Invalidate, otherwise nil.
func (s *Snapshot) Err() error {
	if s.invalid.Load() {
		return reject(CodeSnapshotInvalid)
	}
	return nil
}

// ResolvePolicy returns the registered policy for evaluated on the resource that
// owner owns. It matches the full resource ref exactly (no prefix, case or
// partial-id matching) and never guesses: any mismatch or a missing policy is
// ok=false, which the verifier turns into UNDECIDABLE.
func (s *Snapshot) ResolvePolicy(resourceRef string, owner, evaluated idor.AccountRef) (oracle.PolicyAssertion, bool) {
	if s.invalid.Load() {
		return oracle.PolicyAssertion{}, false
	}
	r, ok := s.resources[resourceRef]
	if !ok || owner.Label != r.owner {
		return oracle.PolicyAssertion{}, false
	}
	policy, ok := r.policies[evaluated.Label]
	if !ok {
		return oracle.PolicyAssertion{}, false
	}
	return policy, true
}

// Canaries returns the registered markers of owner's resource as registered
// (no reclassification, kg-policy §3.1). No canary is ok=false.
func (s *Snapshot) Canaries(resourceRef string, owner idor.AccountRef) ([]idor.Marker, bool) {
	if s.invalid.Load() {
		return nil, false
	}
	r, ok := s.resources[resourceRef]
	if !ok || owner.Label != r.owner || len(r.canaries) == 0 {
		return nil, false
	}
	return append([]idor.Marker{}, r.canaries...), true
}
