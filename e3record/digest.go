package e3record

import (
	"crypto/sha256"
	"github.com/Autumn-27/artex/evidencebind"
)

// EvidenceDigest must be issued by the host's private collection event, not
// from a later caller-provided DTO. No body, credential or canary value enters.
func EvidenceDigest(input evidencebind.Input) ([32]byte, error) {
	data, err := canonical(input)
	if err != nil {
		return [32]byte{}, err
	}
	hash := sha256.New()
	hash.Write([]byte("artex.e3.execution-evidence.v1\x00"))
	hash.Write(data)
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}
