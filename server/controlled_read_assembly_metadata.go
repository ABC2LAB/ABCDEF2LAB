package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/internal/privateinput"
	"github.com/Autumn-27/artex/kgfixture"
	"github.com/Autumn-27/artex/taskmode"
	"github.com/Autumn-27/artex/transmit"
)

const ControlledReadAssemblyMetadataFormat = "abc2lab.first-controlled-read-inputs/v1"
const maxControlledAssemblyMetadataBytes = 64 << 10

// 파일의 비공개 여부는 권한 증명이 아니므로 설치자 앵커에 핀된 입력 승인을 별도로 요구한다.
type ControlledReadAssemblyInputAuthority interface {
	CheckRoot(context.Context) error
	AuthorizeInput(context.Context, string, [32]byte) error
	AuthorizeExecution(context.Context, transmit.Scope, int64) error
}

type ControlledReadAssemblyMetadata struct {
	Format       string          `json:"format"`
	SnapshotRef  string          `json:"snapshot_ref"`
	TargetOrigin string          `json:"target_origin"`
	Source       e3record.Source `json:"source"`
}

// 실행 전 빈 collector 목록은 null 대신 빈 배열로 직렬화해 폐쇄 입력과 왕복 가능하게 한다.
func (m ControlledReadAssemblyMetadata) MarshalJSON() ([]byte, error) {
	type metadataJSON ControlledReadAssemblyMetadata
	copy := m
	if copy.Source.TrafficEvidenceIDs == nil {
		copy.Source.TrafficEvidenceIDs = []int64{}
	}
	return json.Marshal(metadataJSON(copy))
}

func LoadControlledReadAssemblyInputs(ctx context.Context, pool *sql.DB, authority ControlledReadAssemblyInputAuthority, path string) (ControlledReadAssemblyMetadata, ControlledReadAssemblyInputResolver, error) {
	refuse := func() (ControlledReadAssemblyMetadata, ControlledReadAssemblyInputResolver, error) {
		return ControlledReadAssemblyMetadata{}, nil, ErrControlledReadHostConfiguration
	}
	if ctx == nil || ctx.Err() != nil || pool == nil || nilFinalizationValue(authority) || authority.CheckRoot(ctx) != nil {
		return refuse()
	}
	data, err := privateinput.ReadFile(path, maxControlledAssemblyMetadataBytes)
	if err != nil {
		return refuse()
	}
	defer clear(data)
	if authority.AuthorizeInput(ctx, path, sha256.Sum256(data)) != nil {
		return refuse()
	}
	var doc ControlledReadAssemblyMetadata
	if decodeControlledAssemblyJSON(data, &doc) != nil || doc.Format != ControlledReadAssemblyMetadataFormat {
		return refuse()
	}
	// KG는 파일 DTO에서 재생성하지 않고 등록 시 동결된 같은 객체를 받는다.
	if !validControlledMetadataProjection(doc) {
		return refuse()
	}
	fixed := doc.Source
	fixed.CanaryFields = append([]string(nil), fixed.CanaryFields...)
	resolve := func(ctx context.Context, request ControlledReadVerificationRequest) (ControlledReadAssemblyInput, error) {
		reject := func() (ControlledReadAssemblyInput, error) {
			return ControlledReadAssemblyInput{}, ErrControlledFindingUnavailable
		}
		if ctx == nil || ctx.Err() != nil || authority.CheckRoot(ctx) != nil {
			return reject()
		}
		view, err := request.ConfigView(ctx)
		if err != nil {
			return reject()
		}
		expected := fixed.Expected
		expected.Scope.RunRef = view.Scope.RunRef
		if expected.Scope != view.Scope || expected.IntentID != request.IntentID || expected.Scope.TaskRef != strconv.FormatInt(request.TaskID, 10) || expected.ResourceRef != request.ResourceRef || expected.Evaluated.Label != request.AccountRef || authority.AuthorizeExecution(ctx, expected.Scope, request.IntentID) != nil {
			return reject()
		}
		snapshot, err := request.FixtureSnapshot(ctx)
		if err != nil || !sameControlledAssemblySnapshot(snapshot, fixed) {
			return reject()
		}
		p := ControlledReadAssemblyPreparation{SnapshotRef: doc.SnapshotRef, TargetOrigin: doc.TargetOrigin, ModelTag: fixed.ModelTag, Metadata: fixed, KG: snapshot}
		if !validControlledPreparation(p) || checkControlledPreparationKG(ctx, snapshot, fixed) != nil {
			return reject()
		}
		source := func(ctx context.Context, actual evidencebind.Context) (e3record.Source, error) {
			if ctx == nil || ctx.Err() != nil || actual != expected || authority.CheckRoot(ctx) != nil || authority.AuthorizeExecution(ctx, actual.Scope, actual.IntentID) != nil || !sameControlledAssemblySnapshot(snapshot, fixed) || checkControlledPreparationKG(ctx, snapshot, fixed) != nil {
				return e3record.Source{}, ErrControlledFindingUnavailable
			}
			exploration, version, err := readControlledAssemblyExecution(ctx, pool, request.TaskID, actual.IntentID)
			if err != nil || exploration != fixed.ExplorationID || version == 0 {
				return e3record.Source{}, ErrControlledFindingUnavailable
			}
			out := fixed
			out.Expected = actual
			out.ExplorationID = exploration
			out.ModeVersion = version
			out.CanaryFields = append([]string(nil), fixed.CanaryFields...)
			return out, nil
		}
		return ControlledReadAssemblyInput{Expected: expected, KG: snapshot, Source: source}, nil
	}
	doc.Source.CanaryFields = append([]string(nil), fixed.CanaryFields...)
	return doc, resolve, nil
}

func validControlledMetadataProjection(doc ControlledReadAssemblyMetadata) bool {
	s := doc.Source
	e := s.Expected
	if !safeSnapshotRef.MatchString(doc.SnapshotRef) || doc.TargetOrigin == "" || e.Scope.RunRef != "" || s.KGSnapshotRef == "" || s.KGSnapshotDigest == ([32]byte{}) || s.ExplorationID <= 0 || s.EvalRunID != "" || s.ModelTag == "" || s.Completed || s.Finalized || s.EvidenceRevision != 0 || s.EvidenceDigest != ([32]byte{}) || !s.RecordCreatedAt.IsZero() || len(s.TrafficEvidenceIDs) != 0 || s.FinalCheckpointRef != "" || s.FinalCheckpointDigest != ([32]byte{}) || s.FinalCheckpointScope != (transmit.Scope{}) || s.FinalCheckpointSequence != 0 {
		return false
	}
	return len(s.CanaryFields) > 0
}

func sameControlledAssemblySnapshot(snapshot *kgfixture.Snapshot, s e3record.Source) bool {
	return snapshot != nil && snapshot.Err() == nil && snapshot.Ref() == s.KGSnapshotRef && snapshot.Digest() == s.KGSnapshotDigest && snapshot.PolicyVersion() == s.Expected.KGPolicyVersion && snapshot.GenerationRef() == s.GenerationRef && snapshot.InstanceRef() == s.InstanceRef && snapshot.ResetGeneration() == s.ResetGeneration && snapshot.ResetGeneration() == s.Expected.Scope.ResetRef
}

func readControlledAssemblyExecution(ctx context.Context, pool *sql.DB, taskID, intentID int64) (int64, uint64, error) {
	var exploration, intentExploration int64
	var version uint64
	var mode taskmode.Mode
	err := pool.QueryRowContext(ctx, `SELECT t.exploration_id,n.exploration_id,m.version,m.mode FROM tasks t JOIN exploration_nodes n ON n.id=$2 AND n.kind='intent' JOIN task_control_modes m ON m.task_id=t.id WHERE t.id=$1`, taskID, intentID).Scan(&exploration, &intentExploration, &version, &mode)
	if err != nil || exploration <= 0 || exploration != intentExploration || version == 0 || mode != taskmode.ModeControlled || ctx.Err() != nil {
		return 0, 0, ErrControlledFindingUnavailable
	}
	return exploration, version, nil
}

// 중복 키와 대소문자 별칭을 포함한 애매한 입력을 거부한다.
func decodeControlledAssemblyJSON(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func() error
	value = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		if token == nil {
			return ErrControlledReadHostConfiguration
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				s, ok := key.(string)
				if err != nil || !ok || seen[s] {
					return ErrControlledReadHostConfiguration
				}
				seen[s] = true
				if err = value(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err = value(); err != nil {
					return err
				}
			}
		default:
			return ErrControlledReadHostConfiguration
		}
		_, err = d.Token()
		return err
	}
	if err := value(); err != nil {
		return ErrControlledReadHostConfiguration
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrControlledReadHostConfiguration
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return ErrControlledReadHostConfiguration
	}
	// 정확한 필드명은 구조체의 표준 JSON 재인코딩 키와 대조한다.
	var original any
	_ = json.Unmarshal(data, &original)
	encoded, err := json.Marshal(dst)
	if err != nil {
		return ErrControlledReadHostConfiguration
	}
	var canonical any
	_ = json.Unmarshal(encoded, &canonical)
	if !controlledAssemblyJSONKeys(original, canonical) {
		return ErrControlledReadHostConfiguration
	}
	return nil
}
func controlledAssemblyJSONKeys(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range x {
			other, found := y[key]
			if !found || !controlledAssemblyJSONKeys(value, other) {
				return false
			}
		}
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !controlledAssemblyJSONKeys(x[i], y[i]) {
				return false
			}
		}
	}
	return true
}

// 보호된 registry/snapshot의 실제 바이트 승인과 pairing은 기동 전에 별도로 확인한다.
func CheckControlledReadAssemblyInputFiles(ctx context.Context, authority ControlledReadAssemblyInputAuthority, snapshotDir, snapshotRef, registryFile string) error {
	if nilFinalizationValue(authority) || !safeSnapshotRef.MatchString(snapshotRef) || !safeFile.MatchString(registryFile) {
		return ErrControlledReadHostConfiguration
	}
	for _, name := range []string{snapshotRef + ".json", registryFile} {
		path := filepath.Join(snapshotDir, name)
		body, err := privateinput.ReadFile(path, 1<<20)
		if err != nil {
			return ErrControlledReadHostConfiguration
		}
		digest := sha256.Sum256(body)
		clear(body)
		if authority.AuthorizeInput(ctx, path, digest) != nil {
			return ErrControlledReadHostConfiguration
		}
	}
	return nil
}
