package e3record

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/oracle"
)

// Manifest는 불변 근거이며 자기 자신을 기대 권위로 사용하지 않는다.
type Manifest struct {
	Version                                   int64
	EvidenceDigest                            [32]byte
	Evidence                                  evidencebind.Input
	KGSnapshotRef                             string
	KGSnapshotDigest                          [32]byte
	GenerationRef, InstanceRef, CheckpointRef string
	CheckpointDigest                          [32]byte
}

// DecodeBoundManifest는 독립 저장된 실행 권위와 근거를 대조한다.
// 호출자는 manifest 해시와 발급·범위 열에 대한 Source 대조도 수행한다.
func DecodeBoundManifest(raw []byte, source Source, value oracle.VerdictValue) (Manifest, error) {
	var m Manifest
	if len(raw) == 0 || len(raw) > MaxCanonicalBytes {
		return m, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || !CheckManifest(m, source, value) {
		return Manifest{}, ErrInvalid
	}
	return m, nil
}

func CheckManifest(m Manifest, s Source, value oracle.VerdictValue) bool {
	digest, err := EvidenceDigest(m.Evidence)
	if err != nil || m.Version != 1 || digest != m.EvidenceDigest || digest != s.EvidenceDigest ||
		m.KGSnapshotRef != s.KGSnapshotRef || m.KGSnapshotDigest != s.KGSnapshotDigest || m.GenerationRef != s.GenerationRef || m.InstanceRef != s.InstanceRef ||
		m.CheckpointRef != s.FinalCheckpointRef || m.CheckpointDigest != s.FinalCheckpointDigest || s.FinalCheckpointScope != s.Expected.Scope || s.FinalCheckpointSequence == 0 || !s.Completed || !s.Finalized ||
		m.Evidence.Policy.Context != s.Expected || m.Evidence.Owner.Context != s.Expected || m.Evidence.Cross.Context != s.Expected || m.Evidence.Result.Context != s.Expected || m.Evidence.Policy.Assertion != s.Policy {
		return false
	}
	checked := evidencebind.Check(s.Expected, m.Evidence)
	return checked.BindingValid && checked.EffectiveValue == value
}
