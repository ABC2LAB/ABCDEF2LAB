//go:build !linux

package llmpolicy

import "errors"

var ErrInvalidProfileManifestFile = errors.New("llmpolicy: invalid profile manifest file")

// LoadProfileManifestFile fails closed where the Linux file identity and
// no-follow checks are unavailable.
func LoadProfileManifestFile(string) (*ProfileResolver, error) {
	return nil, ErrInvalidProfileManifestFile
}
