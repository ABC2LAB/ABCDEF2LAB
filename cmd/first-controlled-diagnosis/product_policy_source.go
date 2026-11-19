package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e3contract"
)

// policySource는 판정 상세 조회(OD06)가 제품 판정의 당시 정책 동결본을 대조할 때 쓰는 IPC다. 회차 소유는 기본 준비
// 서비스의 상태 조회(Principal·Job)로 확인하고, 동결본(runs/<job>/e3-declaration.json) 바이트의 SHA-256만 돌려준다.
// 원문·선언 내용은 내보내지 않는다. 동결본이 없으면(선언 없이 시작한 회차) present=false다.
func (e productPreparationEndpoint) policySource(ctx context.Context, r diagnosisprep.Request) (*diagnosisprep.ProductPolicySource, error) {
	bound, err := e.base.Call(ctx, diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: r.RequestID, Principal: r.Principal,
		Command: diagnosisprep.Status, JobID: r.Product.JobID})
	if err != nil {
		return nil, errors.New("policy_source_unavailable")
	}
	if bound.Error != "" || bound.Job == nil || bound.Job.ID != r.Product.JobID {
		return nil, errors.New("not_found")
	}
	source, err := readPolicySource(e.product.config.RunsDir, bound.Job.ID)
	if err != nil {
		return nil, errors.New("policy_source_unavailable")
	}
	return source, nil
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
	return &diagnosisprep.ProductPolicySource{Present: true, SHA256: hex.EncodeToString(sum[:])}, nil
}
