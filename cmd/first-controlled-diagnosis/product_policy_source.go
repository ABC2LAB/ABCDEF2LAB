package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e3contract"
)

// policySource는 판정 상세·목록 조회(OD06, 제품 판정 목록 결정 2·3)가 제품 판정의 당시 정책 동결본을 대조할 때 쓰는 IPC다.
// 회차 소유는 기본 준비 서비스의 상태 조회(Principal·Job)로 확인하고, 동결본(runs/<job>/e3-declaration.json) 바이트의
// SHA-256과 그 동결본이 정한 계정 라벨 둘, 확정 단계 결과 파일(e3-confirmation.json)의 상태·사유만 돌려준다. 원문·선언
// 내용·판정은 내보내지 않는다. 동결본이 없으면(선언 없이 시작한 회차) present=false다.
func (e productPreparationEndpoint) policySource(ctx context.Context, r diagnosisprep.Request) (*diagnosisprep.ProductPolicySource, *diagnosisprep.ProductConfirmationState, error) {
	bound, err := e.base.Call(ctx, diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: r.RequestID, Principal: r.Principal,
		Command: diagnosisprep.Status, JobID: r.Product.JobID})
	if err != nil {
		return nil, nil, errors.New("policy_source_unavailable")
	}
	if bound.Error != "" || bound.Job == nil || bound.Job.ID != r.Product.JobID {
		return nil, nil, errors.New("not_found")
	}
	source, err := readPolicySource(e.product.config.RunsDir, bound.Job.ID)
	if err != nil {
		return nil, nil, errors.New("policy_source_unavailable")
	}
	return source, readConfirmationState(e.product.config.RunsDir, bound.Job.ID), nil
}

// productE3ConfirmationMaxBytes는 확정 단계 결과 파일을 읽는 상한이다. 결과는 종류 16·후보 64·쓰기 기록으로 이보다 훨씬
// 작다. 넘으면 읽지 않고 unreadable이다.
const productE3ConfirmationMaxBytes = 1 << 20

// readConfirmationState는 확정 단계 결과 파일의 상태·사유만 읽는다(판정·쓰기 기록·결속은 읽지 않는다). 파일이 없으면
// present=false, 있는데 읽지 못하거나 결과 형식·상태 값이 계약 밖이면 unreadable이다(없음과 섞지 않는다).
func readConfirmationState(runsDir, jobID string) *diagnosisprep.ProductConfirmationState {
	path := filepath.Join(runsDir, jobID, productE3ConfirmationFile)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return &diagnosisprep.ProductConfirmationState{}
	}
	unreadable := &diagnosisprep.ProductConfirmationState{Present: true, Status: diagnosisprep.ConfirmationUnreadable}
	raw, err := readPrivateFile(path, productE3ConfirmationMaxBytes)
	if err != nil {
		return unreadable
	}
	var head struct {
		Version string `json:"version"`
		Status  string `json:"status"`
		Reason  string `json:"reason"`
	}
	if json.Unmarshal(raw, &head) != nil || head.Version != e3contract.RunResultVersion {
		return unreadable
	}
	state := diagnosisprep.ProductConfirmationState{Present: true, Status: head.Status, Reason: head.Reason}
	if state.Status == diagnosisprep.ConfirmationUnreadable || !diagnosisprep.ValidProductConfirmationState(state) {
		return unreadable
	}
	return &state
}

// readPolicySource는 회차 디렉터리의 동결본 바이트 SHA-256을 계산한다. 회차 디렉터리나 동결본이 없으면 present=false,
// 있는데 읽지 못하면(권한·모양·크기) 오류다. 읽기 규칙은 확정 단계가 쓰는 productDeclarationReader와 같다.
func readPolicySource(runsDir, jobID string) (*diagnosisprep.ProductPolicySource, error) {
	if runsDir == "" || !managementReference.MatchString(jobID) || filepath.Base(jobID) != jobID || jobID == "." || jobID == ".." {
		return nil, errPreparation
	}
	path := filepath.Join(runsDir, jobID, productFrozenDeclarationFile)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return &diagnosisprep.ProductPolicySource{}, nil
	} else if err != nil {
		return nil, errPreparation
	}
	raw, err := readPrivateFile(path, e3contract.MaxDeclarationEncodedBytes)
	if err != nil {
		return nil, errPreparation
	}
	sum := sha256.Sum256(raw)
	source := &diagnosisprep.ProductPolicySource{Present: true, SHA256: hex.EncodeToString(sum[:])}
	// 계정 라벨은 이 동결본에서만 읽는다(현재 선언·등록으로 대신하지 않는다). 동결본 모양이 깨졌거나 라벨이 계약 모양이
	// 아니면 라벨 없이 digest만 준다(digest 대조는 그대로 진위를 가린다).
	var d e3contract.Declaration
	if decodeFrozen(raw, &d) == nil && d.OwnerAccountLabel != "" && d.CrossAccountLabel != "" {
		labeled := *source
		labeled.OwnerAccountLabel, labeled.CrossAccountLabel = d.OwnerAccountLabel, d.CrossAccountLabel
		if diagnosisprep.ValidProductPolicySourceValue(labeled) {
			source = &labeled
		}
	}
	return source, nil
}
