package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/server"
)

// 제품 회차의 E③ 확정 단계 연결(계약 docs/contracts/e3-product-confirmation-interface.md §7·§8, 결정 §5·§6.1).
//   - 감독자가 모델 단계가 끝난 뒤(진행 중 작업·모델 호출 0, 실행 허가 닫힘) E② 마감 전에 run을 부른다.
//   - 결과(e3contract.RunResult)는 회차 디렉터리의 e3-confirmation.json에 0600으로 한 번 쓴다. 표식 값은 결과에 없다.
//   - 생성 요청이 대상에 남겼을 수 있는 쓰기는 상태와 관계없이 대상 선언 저널에 남긴다(리셋 확인 전까지 다음 회차 거절).
//   - E② Transport·Journal(이동찬 갈래)은 아직 없어서 지금은 늘 disabled/not_wired로 끝나고 요청은 0건이다.
const (
	productE3ConfirmationFile = "e3-confirmation.json"
	// 대상 세대·인스턴스 참조는 서버가 실행마다 발급한다(결정 §5 0단계). 제품 회차에는 아직 따로 발급한 값이 없어서 회차
	// 번호에서 만든다. 실행 정책의 다른 참조(run-·policy-)와 같은 방식이다.
	// TODO(최민준): 임시 값이다. E② Transport·Journal v3가 관찰·저널 결속에 쓰는 값이라, 이동찬님이 발급하는 값을 그대로
	// 받도록 합의한 뒤 바꾼다(#738 질문). 합의 전에는 Transport가 없어 이 값이 대상 요청에 쓰이지 않는다.
	productE3GenerationRefPrefix = "generation-"
	productE3InstanceRefPrefix   = "instance-"
)

// productE3Confirmation은 제품 회차 하나의 확정 단계다.
type productE3Confirmation struct {
	runsDir  string
	jobID    string
	target   productTargetRecord
	registry *productTargetRegistry
	// accounts는 그 실행의 시험 계정별 인증 준비 결과다(라벨 → 결과, 값 없음).
	accounts func() map[string]string
	deps     e3contract.Deps
}

// newProductE3Confirmation은 제품 의존(동결 선언 읽기·리셋 게이트)으로 확정 단계를 만든다. Transport·Journal은 비워 둔다:
// 실제 E② 연동이 들어오기 전에는 확정 단계가 not_wired로 꺼진다(요청 0건). 가짜를 넣는 일은 시험만 한다.
func newProductE3Confirmation(runsDir, jobID string, target productTargetRecord, registry *productTargetRegistry, accounts func() map[string]string) *productE3Confirmation {
	c := &productE3Confirmation{runsDir: runsDir, jobID: jobID, target: target, registry: registry, accounts: accounts,
		deps: e3contract.Deps{Declarations: productDeclarationReader{runsDir: runsDir}, Reset: productResetGate{registry: registry}}}
	if registry != nil {
		c.deps.Writes = productWriteRecorder{registry: registry, targetID: target.Target.ID, jobID: jobID}
	}
	return c
}

// productWriteRecorder는 확정 단계의 쓰기 기록(e3contract.WriteRecorder)을 대상 선언 저널에 남긴다(기록하고 나서 보내기,
// 사용자 결정 2026-11-19). 생성 요청 전에 종류를 ID 모름으로 남기고, 결과를 받으면 갱신한다. 갱신을 못 해도 먼저 남긴
// 기록이 리셋 확인 전까지 다음 회차를 막는다.
type productWriteRecorder struct {
	registry *productTargetRegistry
	targetID string
	jobID    string
}

func (w productWriteRecorder) Reserve(_ context.Context, b e3contract.RunBinding, resourceType string) error {
	if w.registry == nil || b.JobRef != w.jobID {
		return errPreparation
	}
	return w.registry.recordConfirmationWrites(w.targetID, w.jobID, []productConfirmationObject{{Type: resourceType, IDUnknown: true}})
}

func (w productWriteRecorder) Settle(_ context.Context, b e3contract.RunBinding, write e3contract.ConfirmationWrite) error {
	if w.registry == nil || b.JobRef != w.jobID {
		return errPreparation
	}
	var o productConfirmationObject
	switch {
	case write.Outcome == e3contract.WriteCreated && urlDigestValid(write.ObjectIDDigest):
		o = productConfirmationObject{Type: write.ResourceType, IDDigest: write.ObjectIDDigest}
	case write.Outcome == e3contract.WriteNotSent:
		o = productConfirmationObject{Type: write.ResourceType, IDUnknown: true, NotSent: true}
	default:
		// 결과 불명이나 모양이 틀린 digest는 미리 남긴 ID 모름 그대로 둔다.
		return nil
	}
	return w.registry.settleConfirmationWrite(w.targetID, w.jobID, o)
}

// run은 감독자 훅이다. 오류는 감독자가 단계·상태만 남기고 모델 단계 결과로 마감을 이어 간다. begin은 확정 단계가 실제로
// 대상 요청을 시작할 때만 불린다(e3contract.Deps.Started). 꺼진 회차는 부르지 않는다.
func (c *productE3Confirmation) run(ctx context.Context, taskID string, begin func()) error {
	in, share := c.input(taskID)
	confirmCtx := ctx
	if share > 0 {
		// 확정 단계 시간 몫은 Run에 넘기는 문맥의 시한이다(계약 §12). 몫이 0이면 요청을 보내지 않으므로 시한을 두지 않는다.
		var cancel context.CancelFunc
		confirmCtx, cancel = context.WithTimeout(ctx, share)
		defer cancel()
	}
	deps := c.deps
	deps.Started = begin
	res := e3contract.Run(confirmCtx, deps, in)
	// TODO(최민준): 제품 판정 저장(db.RecordProductVerdict)과 실행 역할 권한은 4b-3b에서 붙인다.
	// 쓰기 기록은 생성 요청마다 보내기 전에 이미 남겼다(productWriteRecorder). 여기서는 결과 파일만 쓴다.
	raw, err := json.Marshal(res)
	if err == nil {
		err = writeRunPrivateFile(c.runsDir, c.jobID, productE3ConfirmationFile, raw)
	}
	log.Printf("[e3-confirmation] 회차 %s 확정 단계 %s %s 요청 %d 쓰기 %d", c.jobID, res.Status, res.Reason, res.Requests, len(res.Writes))
	if err != nil {
		log.Printf("[e3-confirmation] 회차 %s 기록 실패: e3_result_unrecorded", c.jobID)
		return errors.New("e3_result_unrecorded")
	}
	return nil
}

// stopped는 감독자의 중지 기록 훅이다. 확정 단계를 돌리지 못하고 실행이 끝났을 때(중지·취소·실패) 요청 없이 그 회차의 확정
// 단계 상태를 결과 파일에 남긴다(e3contract.RunStopped). 비활성 근거가 있으면 disabled(사유), 돌 수 있었으면 interrupted다.
// 결과 파일이 없을 때 비활성으로 추정하지 않게 하려는 것이다(#738 이동찬 리뷰).
func (c *productE3Confirmation) stopped(ctx context.Context, taskID string) error {
	in, _ := c.input(taskID)
	res := e3contract.RunStopped(ctx, c.deps, in)
	raw, err := json.Marshal(res)
	if err == nil {
		err = writeRunPrivateFile(c.runsDir, c.jobID, productE3ConfirmationFile, raw)
	}
	log.Printf("[e3-confirmation] 회차 %s 확정 단계 중지 기록 %s %s", c.jobID, res.Status, res.Reason)
	if err != nil {
		log.Printf("[e3-confirmation] 회차 %s 기록 실패: e3_result_unrecorded", c.jobID)
		return errors.New("e3_result_unrecorded")
	}
	return nil
}

// input은 확정 단계 입력을 회차 기록에서 만든다. 모두 서버가 발급·동결한 값이고 모델이 쓴 값은 없다. 읽지 못한 칸은 비워 두고
// 지어내지 않는다: e3contract.Run이 binding_invalid·confirmation_share_unavailable 등으로 확정 단계를 끈다.
func (c *productE3Confirmation) input(taskID string) (e3contract.Input, time.Duration) {
	in := e3contract.Input{TargetRef: c.target.Target.ID,
		// 쓰기 동의(write_consent)와 리셋할 수 있는 쓰기(reversible_writes)가 둘 다 등록돼야 한다(결정 §3 조건 1).
		WriteConsent:   c.target.ReversibleWrites && c.target.WriteConsent != "",
		RequestTimeout: server.E3ConfirmationRequestTimeoutDefault, AdmittedTypes: -1,
		Binding: e3contract.RunBinding{JobRef: c.jobID, TaskRef: taskID,
			GenerationRef: productE3GenerationRefPrefix + c.jobID, InstanceRef: productE3InstanceRefPrefix + c.jobID}}
	var share time.Duration
	// 실행 범위 참조와 확정 몫은 운영자 승인(authority.json)에 결속된 실행 정책에서 읽는다(E② 실행 범위와 같은 값).
	if binding, err := openRunBinding(c.runsDir, c.jobID); err != nil {
		log.Printf("[e3-confirmation] 회차 %s 실행 정책을 읽지 못함", c.jobID)
	} else {
		in.Binding.RunRef, in.Binding.E2PolicyRef = binding.policy.RunRef, binding.policy.PolicyRef
		in.AdmittedTypes, share = productE3Admitted(binding.policy.Limits)
		if binding.Close() != nil {
			log.Printf("[e3-confirmation] 회차 %s 실행 정책 닫기 실패", c.jobID)
		}
	}
	frozen, ok, err := readFrozenPolicy(c.runsDir, c.jobID)
	switch {
	case err != nil:
		// 동결 파일이 있는데 모양이 깨졌다. "선언 없음"으로 보이지 않게 출처 참조만 두어 결속이 계약 모양이 아니게 한다.
		log.Printf("[e3-confirmation] 회차 %s 동결 정책 출처가 깨짐", c.jobID)
		in.Binding.Policy = e3contract.PolicySource{Kind: e3contract.PolicyProductDeclaration, Ref: productFrozenPolicyRefPrefix + c.jobID}
	case ok:
		in.Binding.Policy = frozen.Policy
		in.PolicyValidFrom, in.PolicyValidUntil = frozen.ValidFrom, frozen.ValidUntil
	}
	in.Owner, in.Cross = productE3Accounts(c.accounts)
	return in, share
}

// productE3Admitted는 발급한 실행 정책의 확정 몫(withE3ConfirmationShare)에서 몫에 든 종류 수와 시간 몫을 되읽는다.
// 요청 몫 = 신원 2 + 종류×3, 시간 몫 = 요청 몫 × 확정 요청 시간 상한(기본값)이다. 칸이 없거나 이 모양이 아니면 -1(몫을 받지
// 못함)이고, 둘 다 0이면 든 종류가 없다.
func productE3Admitted(l server.E2HTTPFileLimits) (int, time.Duration) {
	if l.E3ConfirmationRequests == nil || l.E3ConfirmationTimeoutMS == nil {
		return -1, 0
	}
	requests, timeout := *l.E3ConfirmationRequests, time.Duration(*l.E3ConfirmationTimeoutMS)*time.Millisecond
	if requests == 0 && timeout == 0 {
		return 0, 0
	}
	identity, perType := uint64(server.E3ConfirmationIdentityRequests), uint64(server.E3ConfirmationRequestsPerType)
	if requests <= identity || (requests-identity)%perType != 0 || timeout != time.Duration(requests)*server.E3ConfirmationRequestTimeoutDefault {
		return -1, 0
	}
	return int((requests - identity) / perType), timeout
}

// productE3Accounts는 인증 준비가 성공한(authenticated) 시험 계정을 이름순으로 소유 계정(A)·교차 계정(B)에 둔다. 셋 이상이면
// 앞의 둘만 쓴다. 둘이 안 되면 빈 칸이 남아 확정 단계는 test_accounts_unavailable로 꺼진다.
// TODO(최민준): A·B 배정 규칙은 판정 의미에 닿아 아직 확정하지 않았다(계약 §8 열린 결정). E② Transport 연결 전에 정한다.
func productE3Accounts(read func() map[string]string) (owner, cross e3contract.Account) {
	if read == nil {
		return owner, cross
	}
	var labels []string
	for label, result := range read() {
		if result == server.E2HTTPAuthAuthenticated {
			labels = append(labels, label)
		}
	}
	slices.Sort(labels)
	accounts := make([]e3contract.Account, 2)
	for i := 0; i < len(labels) && i < len(accounts); i++ {
		accounts[i] = e3contract.Account{Label: labels[i], Authenticated: true}
	}
	return accounts[0], accounts[1]
}

// e3RunStartRefusal은 회차 시작 때(모델 단계 전)의 리셋 게이트다(결정 §6.1 (a), 계약 §8). 지금 대상 판에 유효한 선언이 있는
// 대상만 본다(선언이 없으면 확정 단계가 꺼진 회차라 게이트도 없다). 지난 표식 잔존 점검(결정 §6.1 (b), E² 쪽)은 아직
// 없어서, ID를 아는 지난 객체가 있는 대상은 리셋을 확인해도 잔존을 모르므로 시작하지 않는다(reset_state_unavailable).
// 지난 객체가 모두 ID 모름이면 리셋 확인만으로 시작한다(계약 §12).
func (r *productTargetRegistry) e3RunStartRefusal(ctx context.Context, rec productTargetRecord, jobID string) e3contract.StartRefusal {
	if _, active := r.activeDeclaration(rec); !active {
		return ""
	}
	// 실행 결속은 아직 발급 전이다(작업 번호 없음). 리셋 게이트는 대상 기록만 읽으므로 회차 참조만 싣는다.
	return e3contract.CheckRunStart(ctx, productResetGate{registry: r}, e3contract.RunBinding{JobRef: jobID}, rec.Target.ID)
}

// attachRun은 회차를 대상에 묶고 모델 단계 전에 E③ 리셋 게이트를 본다. 대상이 바뀌었으면 target_approval_changed, 게이트가
// 거절하면 묶음을 풀고 그 사유(e3contract.StartRefusal)를 돌려준다. 호출자는 이 사유로 회차를 실패로 마감한다.
func (p *productRuntime) attachRun(ctx context.Context, rec productTargetRecord, jobID string, cancel context.CancelFunc) (func(), string) {
	release, err := p.attach(rec, jobID, cancel)
	if err != nil {
		return nil, "target_approval_changed"
	}
	if refusal := p.targets.e3RunStartRefusal(ctx, rec, jobID); refusal != "" {
		release()
		return nil, string(refusal)
	}
	return release, ""
}

// writeRunPrivateFile은 회차 디렉터리(runs/<job>)에 새 파일을 0600으로 한 번만 쓰고(있으면 덮어쓰지 않음) 디렉터리까지
// fsync한다. 회차 디렉터리 밖을 가리키는 이름은 받지 않는다(os.Root).
func writeRunPrivateFile(runsDir, jobID, name string, raw []byte) error {
	if !managementReference.MatchString(jobID) || filepath.Base(jobID) != jobID {
		return errPreparation
	}
	parent, err := os.OpenRoot(runsDir)
	if err != nil {
		return errPreparation
	}
	defer parent.Close()
	root, err := parent.OpenRoot(jobID)
	if err != nil {
		return errPreparation
	}
	defer root.Close()
	if err := writeRootPrivateFile(root, name, raw); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return errPreparation
	}
	syncErr, closeErr := dir.Sync(), dir.Close()
	if syncErr != nil || closeErr != nil {
		return errPreparation
	}
	return nil
}

// writeRootPrivateFile은 열린 회차 디렉터리에 새 파일을 0600으로 한 번만 쓰고 fsync한다.
func writeRootPrivateFile(root *os.Root, name string, raw []byte) error {
	if !managementReference.MatchString(name) {
		return errPreparation
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errPreparation
	}
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errPreparation
	}
	return nil
}
