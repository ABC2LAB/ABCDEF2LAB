package evidencerevision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
)

const DescriptorFileMaxBytes = 4096

type DescriptorFilePurpose string

const (
	PersistDescriptorFile DescriptorFilePurpose = "persist_descriptor"
	ReadDescriptorFile    DescriptorFilePurpose = "read_descriptor"
)

type DescriptorFileAccess struct {
	Purpose      DescriptorFilePurpose
	OperationRef string
}

// DescriptorRootProvider authenticates the actual caller, exact purpose/op and
// namespace before granting a root. It must hold the real writer/reader and
// restore-continuity lease through Release. Modes, matching inodes or repeated
// callback values do not establish that authority. There is no default host.
// On error it cleans its own partial resources; no scope ownership transfers.
type DescriptorRootProvider interface {
	AcquireDescriptorRoot(context.Context, DescriptorFileAccess) (DescriptorRootScope, error)
}

// DescriptorRootScope borrows a host's long-lived anchor. The host must keep it
// open and its namespace/exclusive-writer scope valid until Release; it must not
// replace it with a newly opened path to manufacture restore continuity.
// Successful acquisition transfers exactly one Release obligation, even if
// subsequent validation fails. Release happens after all owned FD closes.
// Directory's ancestors and their namespace are already trusted by the host.
type DescriptorRootScope interface {
	Context() context.Context
	Anchor() *os.File
	Directory() string
	Release()
}

var (
	ErrDescriptorFileStorage     = errors.New("evidence_descriptor_file_unavailable")
	ErrDescriptorFileUnknown     = errors.New("evidence_descriptor_file_outcome_unknown")
	ErrDescriptorFileUnsupported = errors.New("evidence_descriptor_file_platform_unsupported")
)

// DescriptorFiles stores data only. It cannot create Collected, call Issue,
// grant ReadReceipt authority or activate a server. No directory is created.
type DescriptorFiles struct {
	provider DescriptorRootProvider
	// Private test-only fault seams. Production constructors never install them.
	fault func(string) error
}

func NewDescriptorFiles(provider DescriptorRootProvider) (*DescriptorFiles, error) {
	if descriptorNil(provider) {
		return nil, ErrScope
	}
	return &DescriptorFiles{provider: provider}, nil
}

// Put acknowledges file Sync + atomic no-replace publication + directory Sync.
// Any uncertain I/O outcome preserves artifacts and prohibits the caller from
// starting Issue. An exact retry must complete synchronization again.
func (s *DescriptorFiles) Put(ctx context.Context, d Descriptor) error {
	body, err := descriptorFileBytes(d)
	if err != nil {
		return err
	}
	return s.putDescriptor(ctx, d.OperationRef, body)
}

// Load requires fresh real ReadDescriptorFile scope. The returned data still
// requires the SQL Store's independent current ReadReceipt lease for Reconcile.
func (s *DescriptorFiles) Load(ctx context.Context, operationRef string) (Descriptor, error) {
	if !validRef(operationRef) {
		return Descriptor{}, ErrInput
	}
	return s.loadDescriptor(ctx, operationRef)
}

func descriptorFileBytes(d Descriptor) ([]byte, error) {
	if _, err := descriptorAccess(d); err != nil {
		return nil, ErrInput
	}
	body, err := json.Marshal(d)
	if err != nil || len(body) > DescriptorFileMaxBytes {
		return nil, ErrInput
	}
	return body, nil
}

func decodeDescriptorFile(body []byte, operation string) (Descriptor, error) {
	if len(body) == 0 || len(body) > DescriptorFileMaxBytes {
		return Descriptor{}, ErrInput
	}
	var d Descriptor
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		return Descriptor{}, ErrInput
	}
	canonical, err := descriptorFileBytes(d)
	// Exact re-encoding rejects duplicate keys, trailing input, whitespace,
	// noncanonical scalar representations and metadata format drift.
	if err != nil || d.OperationRef != operation || !bytes.Equal(body, canonical) {
		return Descriptor{}, ErrInput
	}
	return d, nil
}

func descriptorNil(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return r.IsNil()
	}
	return false
}

type descriptorFileError struct {
	stage string
	kind  error
}

func (e descriptorFileError) Error() string { return "evidence_descriptor_file_" + e.stage }
func (e descriptorFileError) Unwrap() error { return e.kind }

func (s *DescriptorFiles) fail(stage string) bool { return s.fault != nil && s.fault(stage) != nil }
