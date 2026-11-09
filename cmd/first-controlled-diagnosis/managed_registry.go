package main

import (
	"context"
	"encoding/hex"
	"regexp"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/diagnosisapi"
)

const managementRegistryFormat = "abc2lab.diagnosis-management-registry/v1"
const maxManagementRegistryBytes = 256 << 10

var managementReference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type managementApproval struct {
	Principal      string    `json:"principal"`
	DraftID        string    `json:"draft_id"`
	Revision       uint64    `json:"revision"`
	SourceDigest   string    `json:"source_digest"`
	TaskID         string    `json:"task_id"`
	PreparationRef string    `json:"preparation_ref"`
	ExpiresAt      time.Time `json:"expires_at"`
}
type managementRegistryDocument struct {
	Format    string                 `json:"format"`
	Profiles  []diagnosisapi.Profile `json:"profiles"`
	Approvals []managementApproval   `json:"approvals"`
}
type managedRegistry struct{ path string }

// 매 조회마다 보호 파일을 다시 읽는다. UI는 이 파일·승인행·경로를 쓸 수 없다.
func (r managedRegistry) read() (managementRegistryDocument, error) {
	var doc managementRegistryDocument
	raw, err := readPrivateFile(r.path, maxManagementRegistryBytes)
	if err != nil {
		return doc, errPreparation
	}
	defer clear(raw)
	if decodeLaunchJSON(raw, &doc) != nil || doc.Format != managementRegistryFormat || len(doc.Profiles) > 128 || len(doc.Approvals) > 1000 {
		return managementRegistryDocument{}, errPreparation
	}
	ids := map[string]bool{}
	digests := map[string]bool{}
	bindings := map[string]bool{}
	for _, p := range doc.Profiles {
		digest, err := diagnosisapi.ConfigurationDigest(p.Configuration)
		if err != nil || !managementReference.MatchString(p.ID) || p.Label == "" || len(p.Label) > 256 || ids[p.ID] {
			return managementRegistryDocument{}, errPreparation
		}
		ids[p.ID] = true
		digests[digest] = true
	}
	for _, a := range doc.Approvals {
		hash, hashErr := hex.DecodeString(a.SourceDigest)
		task, taskErr := strconv.ParseInt(a.TaskID, 10, 64)
		key := a.Principal + ":" + a.DraftID + ":" + strconv.FormatUint(a.Revision, 10)
		if a.Principal != "ARTEX" || !managementReference.MatchString(a.DraftID) || a.Revision == 0 || hashErr != nil || len(hash) != 32 || !digests[a.SourceDigest] ||
			taskErr != nil || task <= 0 || strconv.FormatInt(task, 10) != a.TaskID || !managementReference.MatchString(a.PreparationRef) || a.ExpiresAt.IsZero() || bindings[key] {
			return managementRegistryDocument{}, errPreparation
		}
		bindings[key] = true
	}
	return doc, nil
}
func (r managedRegistry) Profiles(ctx context.Context, principal string) ([]diagnosisapi.Profile, error) {
	if ctx == nil || ctx.Err() != nil || principal != "ARTEX" {
		return nil, errPreparation
	}
	doc, err := r.read()
	return doc.Profiles, err
}
func (r managedRegistry) Approval(ctx context.Context, principal string, d diagnosisapi.Draft) (*diagnosisapi.Binding, error) {
	if ctx == nil || ctx.Err() != nil || principal != "ARTEX" {
		return nil, errPreparation
	}
	doc, err := r.read()
	if err != nil {
		return nil, err
	}
	for _, a := range doc.Approvals {
		if a.Principal == principal && a.DraftID == d.ID && a.Revision == d.Revision && a.SourceDigest == d.SourceDigest {
			return &diagnosisapi.Binding{Principal: a.Principal, DraftID: a.DraftID, Revision: a.Revision, SourceDigest: a.SourceDigest, TaskID: a.TaskID, PreparationRef: a.PreparationRef, ExpiresAt: a.ExpiresAt}, nil
		}
	}
	return nil, nil
}
