//go:build !linux

package evidencerevision

import (
	"context"
	"errors"
	"testing"
)

func TestDescriptorFilesUnsupportedPlatform(t *testing.T) {
	s := &DescriptorFiles{}
	if err := s.putDescriptor(context.Background(), "operation:test", []byte("metadata")); !errors.Is(err, ErrDescriptorFileUnsupported) {
		t.Fatal("platform accepted write")
	}
	if got, err := s.Load(context.Background(), "operation:test"); !errors.Is(err, ErrDescriptorFileUnsupported) || got != (Descriptor{}) {
		t.Fatal("platform accepted read")
	}
}
