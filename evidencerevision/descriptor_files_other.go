//go:build !linux

package evidencerevision

import "context"

func (*DescriptorFiles) putDescriptor(context.Context, string, []byte) error {
	return ErrDescriptorFileUnsupported
}
func (*DescriptorFiles) loadDescriptor(context.Context, string) (Descriptor, error) {
	return Descriptor{}, ErrDescriptorFileUnsupported
}
