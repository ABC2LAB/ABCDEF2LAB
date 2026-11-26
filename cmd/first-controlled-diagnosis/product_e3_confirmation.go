package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/oracle"
	"github.com/jackc/pgx/v5/pgconn"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3v3"
	"github.com/Autumn-27/artex/e3contract/e3v3/e2live"
	"github.com/Autumn-27/artex/server"
)

// 제품 회차의 E③ 확정 단계 연결(계약 docs/contracts/e3-product-confirmation-interface.md §7·§8, 결정 §5·§6.1).
//   - 감독자가 모델 단계가 끝난 뒤(진행 중 작업·모델 호출 0, 실행 허가 닫힘) E② 마감 전에 run을 부른다.
//   - 결과(e3contract.RunResult)는 회차 디렉터리의 e3-confirmation.json에 0600으로 한 번 쓴다. 표식 값은 결과에 없다.
//   - 생성 요청이 대상에 남겼을 수 있는 쓰기는 상태와 관계없이 대상 선언 저널에 남긴다(리셋 확인 전까지 다음 회차 거절).
const (
	productE3ConfirmationFile = "e3-confirmation.json"
)

// productE3Confirmation은 제품 회차 하나의 확정 단계다.
type productE3Confirmation struct {
	runsDir  string
	jobID    string
	target   productTargetRecord
	registry *productTargetRegistry
	// accounts는 그 실행의 시험 계정별 인증 준비 결과다(라벨 → 결과, 값 없음).
	accounts func() map[string]string
	// handle은 모델 단계가 끝난 뒤 현재 실행과 C14 세션을 함께 조회한다. 못 주면 그 사유 코드다(server.E3Handle*).
	handle func(string) (*e2proxy.Execution, server.E2HTTPAccountBindings, string)
	// store는 판정 하나를 실행 역할로 저장한다(server.FirstDiagnosisHTTPRuntime.RecordProductVerdict, INSERT만). 없으면 저장하지
	// 않는다(결과 파일만).
	store func(context.Context, db.ProductVerdict) (db.ProductVerdictResult, error)
	// modelTag는 이 회차 모델 단계의 모델 프로파일 식별자다(저장 판정의 model_tag).
	modelTag string
	// newBridge는 실제 E² 브리지를 만든다(e2live.NewLive). 시험만 바꾼다.
	newBridge func(*e2proxy.Execution, server.E2HTTPAccountBindings, e3contract.RunBinding) (productE3Bridge, error)
	deps      e3contract.Deps
	// identityPath는 동결 선언의 신원 경로다(input이 채움). 학습 로그인 검증의 세션 없는 대조 요청 경로다.
	identityPath string
	// learned는 이 회차 학습 로그인 재사용 상태다(product_learned_login.go). learnedBindings는 확정 단계가 쓴 계정 결속이다.
	learned         *productLearnedLogin
	learnedBindings server.E2HTTPAccountBindings
}

// productE3Bridge는 확정 단계가 쓰는 E² 브리지(e2live.Live)다.
type productE3Bridge = e3v3.Bridge

func newProductE3Bridge(exec *e2proxy.Execution, accounts server.E2HTTPAccountBindings, binding e3contract.RunBinding) (productE3Bridge, error) {
	if exec == nil || accounts == nil {
		return nil, e2proxy.ErrE3JournalUnavailable
	}
	return e2live.NewLive(exec, accounts, binding)
}

// newProductE3Confirmation은 제품 의존(동결 선언 읽기·리셋 게이트)으로 확정 단계를 만든다.
// 실제 E② handle은 모델 단계 뒤에만 얻으므로 run에서 연결한다.
func newProductE3Confirmation(runsDir, jobID string, target productTargetRecord, registry *productTargetRegistry, accounts func() map[string]string) *productE3Confirmation {
	c := &productE3Confirmation{runsDir: runsDir, jobID: jobID, target: target, registry: registry, accounts: accounts,
		newBridge: newProductE3Bridge, deps: e3contract.Deps{Declarations: productDeclarationReader{runsDir: runsDir}, Reset: productResetGate{registry: registry}}}
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

func (w productWriteRecorder) Reserve(_ context.Context, b e3contract.RunBinding, resourceType, objectID string) error {
	if w.registry == nil || b.JobRef != w.jobID {
		return errPreparation
	}
	o := productConfirmationObject{Type: resourceType, IDUnknown: true}
	if objectID != "" {
		// 요청 본문 칸 ID는 보내기 전에 알므로 ID와 함께 남긴다(결과 불명이어도 리셋 확인 때 이 ID로 조회해 볼 수 있음).
		o = productConfirmationObject{Type: resourceType, ID: objectID}
	}
	return w.registry.recordConfirmationWrites(w.targetID, w.jobID, []productConfirmationObject{o})
}

func (w productWriteRecorder) Settle(_ context.Context, b e3contract.RunBinding, write e3contract.ConfirmationWrite) error {
	if w.registry == nil || b.JobRef != w.jobID {
		return errPreparation
	}
	var o productConfirmationObject
	switch {
	case write.Outcome == e3contract.WriteCreated && urlDigestValid(write.ObjectIDDigest):
		o = productConfirmationObject{Type: write.ResourceType, IDDigest: write.ObjectIDDigest, ID: write.ObjectID}
	case write.Outcome == e3contract.WriteNotSent && write.ObjectID != "":
		o = productConfirmationObject{Type: write.ResourceType, ID: write.ObjectID, NotSent: true}
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
	if c.handle != nil && c.newBridge != nil {
		if execution, _, reason := c.handle(taskID); execution == nil && reason == server.E3HandleLeaseMissing {
			// 모델 단계가 E② 실행을 한 번도 잡지 않은 채 스스로 끝났다(intent 없음 등, 사용자 결정 2026-11-20 분기점 2). 연결이
			// 없는 판(not_wired)이 아니므로 닫힌 연결을 붙여 실제 상태를 남긴다: 꺼진 회차는 실제 사유, 켜진 회차는
			// e2_execution_unavailable이다(요청 0, 2026-11-22 F12).
			deps := c.deps
			deps.Transport, deps.Journal = productEndedE3Connection{}, productEndedE3Connection{}
			res := e3contract.RunWithoutExecution(ctx, deps, in)
			c.finishLearned(nil)
			raw, err := json.Marshal(res)
			if err == nil {
				err = writeRunPrivateFile(c.runsDir, c.jobID, productE3ConfirmationFile, raw)
			}
			log.Printf("[e3-confirmation] 회차 %s 확정 단계 실행 없음 기록 %s %s", c.jobID, res.Status, res.Reason)
			if err != nil {
				log.Printf("[e3-confirmation] 회차 %s 기록 실패: e3_result_unrecorded", c.jobID)
				return errors.New("e3_result_unrecorded")
			}
			return nil
		}
	}
	confirmCtx := ctx
	if share > 0 {
		// 확정 단계 시간 몫은 Run에 넘기는 문맥의 시한이다(계약 §12). 몫이 0이면 요청을 보내지 않으므로 시한을 두지 않는다.
		var cancel context.CancelFunc
		confirmCtx, cancel = context.WithTimeout(ctx, share)
		defer cancel()
	}
	deps, release, checkpoint := c.wiredDeps(taskID)
	defer release()
	deps.Started = begin
	res := e3contract.Run(confirmCtx, deps, in)
	// 배운 로그인 방법과 프로그램 재현 검증을 남긴다(모델 로그인 계약 §6-1·§6-2). 판정·결과 파일과 상관없다. 검증은 확정
	// 단계가 completed로 끝나고 신원 확인을 통과시킨 판정의 근거 관찰로만 세운다.
	c.finishLearned(acceptedIdentityEvidence(res))
	// 판정은 확정 단계에서 Host가 검증한 근거 체크포인트와 함께 바로 저장한다(E1 (가), 봉인 확인은 따로). 쓰기 기록은 생성
	// 요청마다 보내기 전에 이미 남겼다(productWriteRecorder).
	c.storeVerdicts(ctx, taskID, res, checkpoint)
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
	deps, release, noExecution := c.stoppedDeps(taskID)
	defer release()
	var res e3contract.RunResult
	if noExecution {
		// 이 회차는 E② 실행을 한 번도 잡지 않았다(lease_missing). 중단(interrupted)이나 인증 결과 없음이 아니라 실행 없음으로
		// 남긴다(리허설 F11·F12: 의도 0인 점검이 실패로 끝나 test_accounts_unavailable로 가려짐).
		res = e3contract.RunWithoutExecution(ctx, deps, in)
	} else {
		res = e3contract.RunStopped(ctx, deps, in)
	}
	// 확정 단계를 돌지 못한 회차도 모델 단계에서 배운 로그인 방법은 남긴다(검증은 없음).
	c.finishLearned(nil)
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

// stoppedDeps는 중지 기록용 의존이다. 중지 기록 훅은 실행이 끝난 뒤(중지·취소·실패) 불려서, 연결이 있던 판이어도 E② 실행이
// 이미 끝나 연결을 받지 못한다(lease_missing 등). 그때 not_wired로 남기면 연결이 없는 판과 구별되지 않는다(Go 조건 2는 최종
// 이미지의 not_wired를 No-Go로 본다). 그래서 연결이 있던 판이고 사유가 "실행 끝남"이면 닫힌 연결을 붙여 실제 상태(돌 수
// 있었으면 interrupted, 아니면 입력 사유)를 남긴다. RunStopped는 요청을 보내지 않고 연결은 nil 여부만 본다.
func (c *productE3Confirmation) stoppedDeps(taskID string) (e3contract.Deps, func(), bool) {
	if c.handle != nil && c.newBridge != nil {
		if execution, _, reason := c.handle(taskID); execution == nil && e3HandleExecutionEnded(reason) {
			log.Printf("[e3-confirmation] task %s 중지 기록: E② 실행이 이미 끝남(사유=%s)", taskID, reason)
			deps := c.deps
			deps.Transport, deps.Journal = productEndedE3Connection{}, productEndedE3Connection{}
			return deps, func() {}, reason == server.E3HandleLeaseMissing
		}
	}
	deps, release, _ := c.wiredDeps(taskID)
	return deps, release, false
}

// e3HandleExecutionEnded는 E② 연결을 주지 못한 사유가 "이 작업의 실행이 이미 끝남"인지다. 호스트가 닫히는 중·닫힘
// (runtime_closing·runtime_closed)도 연결이 있던 판이라 여기 든다(#825 윤재상 봇 리뷰). 실행기·계정 저장소가 없는 사유
// (runner_missing·runner_not_local·accounts_missing·lease_invalid·runtime_unavailable)는 연결이 없는 판이라 여기 들지 않는다.
func e3HandleExecutionEnded(reason string) bool {
	switch reason {
	case server.E3HandleTaskMissing, server.E3HandleTaskMismatch, server.E3HandleTaskFenced, server.E3HandleLeaseMissing,
		server.E3HandleExecutionStopped, server.E3HandleRuntimeClosing, server.E3HandleRuntimeClosed:
		return true
	}
	return false
}

// productEndedE3Connection은 실행이 끝난 뒤의 닫힌 확정 단계 연결이다. 중지 기록에만 쓰고, 부르면 늘 실패한다.
type productEndedE3Connection struct{}

var errProductE3ExecutionEnded = errors.New("e3_execution_ended")

func (productEndedE3Connection) RegisterMarker(context.Context, e3contract.RunBinding, e3contract.Marker) error {
	return errProductE3ExecutionEnded
}

func (productEndedE3Connection) Identity(context.Context, e3contract.IdentityRequest) (e3contract.Observation, error) {
	return e3contract.Observation{}, errProductE3ExecutionEnded
}

func (productEndedE3Connection) Create(context.Context, e3contract.CreateRequest) (e3contract.Observation, error) {
	return e3contract.Observation{}, errProductE3ExecutionEnded
}

func (productEndedE3Connection) Read(context.Context, e3contract.ReadRequest) (e3contract.Observation, error) {
	return e3contract.Observation{}, errProductE3ExecutionEnded
}

func (productEndedE3Connection) Sealed(context.Context, e3contract.RunBinding, e3contract.CallRef) (e3contract.Observation, error) {
	return e3contract.Observation{}, errProductE3ExecutionEnded
}

// wiredDeps는 E② 실행 연결이 있으면 몫·선언과 관계없이 어댑터를 붙인 의존이다. 그래야 꺼진 회차가 not_wired 대신 실제
// 사유(declaration_missing·write_not_consented 등)를 내고, not_wired는 연결이 정말 없는 판에서만 나온다(Go 조건 2). 실제
// 브리지는 표식 등록 때만 만든다(productLazyE2). release는 만든 브리지를 닫는다.
func (c *productE3Confirmation) wiredDeps(taskID string) (e3contract.Deps, func(), productE3CheckpointFunc) {
	deps := c.deps
	if c.handle == nil || c.newBridge == nil {
		log.Printf("[e3-confirmation] task %s 확정 단계에 E② 연결 없음: 사유=handle_not_configured", taskID)
		return deps, func() {}, nil
	}
	execution, accounts, reason := c.handle(taskID)
	if reason != "" || execution == nil {
		if reason == "" {
			reason = "execution_missing"
		}
		// 확정 단계는 not_wired로 꺼진다. 어느 조건인지 고정 코드로 남긴다(값 없음).
		log.Printf("[e3-confirmation] task %s 확정 단계에 E② 연결 없음: 사유=%s", taskID, reason)
		return deps, func() {}, nil
	}
	newBridge := c.newBridge
	lazy, err := e3v3.NewLazy(func(binding e3contract.RunBinding) (e3v3.Bridge, error) {
		return newBridge(execution, accounts, binding)
	})
	if err != nil {
		log.Printf("[e3-confirmation] task %s 확정 단계 어댑터 생성 실패", taskID)
		return deps, func() {}, nil
	}
	adapter, err := e3v3.New(lazy)
	if err != nil {
		log.Printf("[e3-confirmation] task %s 확정 단계 어댑터 생성 실패", taskID)
		return deps, func() {}, nil
	}
	deps.Transport, deps.Journal = adapter, adapter
	legacy, hasLegacy := accounts.(server.E2HTTPAccountRelogin)
	learned, hasLearned := accounts.(server.E2HTTPLearnedRelogin)
	// 결속된 실행 정책을 읽을 수 없거나 계정이 없으면 기존 설정 경로도 연결하지 않는다.
	if ll := c.learnedLoginState(); ll != nil && (hasLearned || hasLegacy) {
		// 기존 설정만 제공하는 계정 저장소도 같은 이력 차단을 거쳐야 한다.
		deps.Relogin = func(ctx context.Context, label string) int { return ll.relogin(ctx, label, learned, legacy) }
		if hasLearned {
			c.learnedBindings = accounts
			deps.Transport = productIdentityTap{Transport: adapter, observe: ll.observeIdentity}
		}
	}
	checkpoint := func(ctx context.Context, binding e3contract.RunBinding, ref e3contract.CallRef) (e3v3.Checkpoint, error) {
		_, cp, err := lazy.VerifiedCheckpoint(ctx, binding, ref)
		return cp, err
	}
	return deps, lazy.Close, checkpoint
}

// productE3CheckpointFunc는 확정 단계 근거 호출 하나를 Host에 다시 검증받아 그 검증에 쓴 현재 head 체크포인트를 받는다.
type productE3CheckpointFunc func(context.Context, e3contract.RunBinding, e3contract.CallRef) (e3v3.Checkpoint, error)

// storeVerdicts는 확정 단계 판정을 실행 역할로 저장한다(docs/contracts/e3-product-verdict-store.md §2·§4). 근거 체크포인트는
// 회차당 하나다: 마지막 근거 호출을 Host에 다시 검증받아, 그 검증에 쓴 현재 head를 받는다(그 head는 앞선 근거 사건을 모두
// 앞부분으로 포함한다). 받지 못하면 체크포인트 없이 저장한다(표시는 봉인 미확인). 저장이 실패해도 결과 파일과 모델 단계
// 결과는 그대로이고, 고정 문구로 로그만 남긴다.
func (c *productE3Confirmation) storeVerdicts(ctx context.Context, taskID string, res e3contract.RunResult, checkpoint productE3CheckpointFunc) {
	if len(res.Verdicts) == 0 || res.Binding == nil || res.Status != e3contract.StatusCompleted && res.Status != e3contract.StatusInterrupted {
		return
	}
	if c.store == nil {
		log.Printf("[e3-confirmation] 회차 %s 판정 저장 연결 없음", c.jobID)
		return
	}
	task, err := strconv.ParseInt(taskID, 10, 64)
	policyDigest, digestErr := hex.DecodeString(res.Binding.Policy.DigestSHA256)
	if err != nil || task <= 0 || digestErr != nil || len(policyDigest) != sha256.Size {
		log.Printf("[e3-confirmation] 회차 %s 판정 저장 입력이 계약 모양이 아님", c.jobID)
		return
	}
	var cp *db.ProductCheckpoint
	if ref, ok := lastE3Evidence(res.Verdicts); ok {
		var got e3v3.Checkpoint
		err := e3v3.ErrNoCheckpoint
		if checkpoint != nil {
			got, err = checkpoint(ctx, *res.Binding, ref)
		}
		if err == nil {
			cp = &db.ProductCheckpoint{JournalID: got.JournalID, Sequence: got.Sequence, MAC: got.MAC}
		} else {
			log.Printf("[e3-confirmation] 회차 %s 근거 체크포인트를 받지 못함(판정은 봉인 미확인으로 저장)", c.jobID)
		}
	}
	b := res.Binding
	binding := db.ProductBinding{JobRef: b.JobRef, RunRef: b.RunRef, E2PolicyRef: b.E2PolicyRef, GenerationRef: b.GenerationRef, InstanceRef: b.InstanceRef,
		Policy: db.ProductPolicySource{Ref: b.Policy.Ref, TargetApprovalRevision: b.Policy.TargetApprovalRevision}}
	copy(binding.Policy.DigestSHA256[:], policyDigest)
	stored := 0
	for _, v := range res.Verdicts {
		pv := db.ProductVerdict{TaskID: task, Binding: binding, Checkpoint: cp, ResourceType: v.ResourceType,
			ResourceRef: productE3ResourceRef(v), ModelTag: c.modelTag, Value: v.Value,
			PolicyCheck: v.PolicyCheck, IdentityCheck: v.IdentityCheck, DataCheck: v.DataCheck,
			OracleReasons: append([]oracle.JudgeReason{}, v.OracleReasons...), ProductReasons: make([]string, 0, len(v.ProductReasons)),
			CandidateRefs: append([]string{}, v.CandidateRefs...), Evidence: make([]db.ProductEvidence, 0, len(v.Evidence)), ObservedAt: v.ObservedAt}
		for _, r := range v.ProductReasons {
			pv.ProductReasons = append(pv.ProductReasons, string(r))
		}
		for _, e := range v.Evidence {
			pv.Evidence = append(pv.Evidence, db.ProductEvidence{CallRef: e.CallRef, AttemptID: e.AttemptID, Sequence: e.Sequence})
		}
		if _, err := c.store(ctx, pv); err != nil {
			log.Printf("[e3-confirmation] 회차 %s 판정 저장 실패(종류 하나): 사유=%s", c.jobID, productE3StoreFailure(pv, err))
			continue
		}
		stored++
	}
	log.Printf("[e3-confirmation] 회차 %s 판정 저장 %d/%d 근거 체크포인트 %v", c.jobID, stored, len(res.Verdicts), cp != nil)
}

// productE3ModelTag는 저장 판정의 model_tag다(참조 모양 [A-Za-z0-9._:-], 128자 이하). 외부 모델은 "<방식>:<모델 ID>"이고
// 모델 ID의 "/"는 ":"로, 그 밖의 문자는 "-"로 바꾼다. 실행 범위에 모델 이름이 없는 로컬 모델은 모델 프로파일 번호다.
func productE3ModelTag(scope *diagnosisprep.Scope, profileID int64) string {
	tag := "profile-" + strconv.FormatInt(profileID, 10)
	if scope != nil && scope.Model != nil && scope.Model.Mode != "" && scope.Model.Model != "" {
		tag = scope.Model.Mode + ":" + strings.ReplaceAll(scope.Model.Model, "/", ":")
	}
	out := []byte(tag)
	for i, ch := range out {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '.' || ch == '_' || ch == ':' || ch == '-') {
			out[i] = '-'
		}
	}
	if len(out) > 128 {
		out = out[:128]
	}
	return string(out)
}

// productE3StoreFailure는 판정 저장 실패의 고정 사유 코드다(값 없음): 입력 칸 문제는 input_<칸>, 같은 회차·종류의 다른
// 판정은 conflict, DB 거부는 sqlstate_<코드>.
func productE3StoreFailure(v db.ProductVerdict, err error) string {
	if problem := db.ProductVerdictProblem(v); problem != "" {
		return "input_" + problem
	}
	if errors.Is(err, db.ErrProductVerdictConflict) {
		return "conflict"
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code != "" {
		return "sqlstate_" + pgErr.Code
	}
	return "store_error"
}

// lastE3Evidence는 판정들의 근거 호출 중 마지막 것이다. 근거 호출이 없으면 false다(봉인 확인 해당 없음).
func lastE3Evidence(verdicts []e3contract.Verdict) (e3contract.CallRef, bool) {
	for i := len(verdicts) - 1; i >= 0; i-- {
		if n := len(verdicts[i].Evidence); n > 0 {
			return verdicts[i].Evidence[n-1], true
		}
	}
	return e3contract.CallRef{}, false
}

// productE3ResourceRef는 저장 판정의 자원 참조 <종류>:<생성 객체 ID digest>다(OD11). 객체를 만들지 못한 종류는 unknown이다.
func productE3ResourceRef(v e3contract.Verdict) string {
	if v.ObjectIDDigest == "" {
		return v.ResourceType + ":unknown"
	}
	return v.ResourceType + ":" + v.ObjectIDDigest
}

// input은 확정 단계 입력을 회차 기록에서 만든다. 모두 서버가 발급·동결한 값이고 모델이 쓴 값은 없다. 읽지 못한 칸은 비워 두고
// 지어내지 않는다: e3contract.Run이 binding_invalid·confirmation_share_unavailable 등으로 확정 단계를 끈다.
func (c *productE3Confirmation) input(taskID string) (e3contract.Input, time.Duration) {
	in := e3contract.Input{TargetRef: c.target.Target.ID,
		// 쓰기 동의(write_consent)와 리셋할 수 있는 쓰기(reversible_writes)가 둘 다 등록돼야 한다(결정 §3 조건 1).
		WriteConsent:   c.target.ReversibleWrites && c.target.WriteConsent != "",
		RequestTimeout: server.E3ConfirmationRequestTimeoutDefault, AdmittedTypes: -1,
		Binding: e3contract.RunBinding{JobRef: c.jobID, TaskRef: taskID}}
	// 세대·인스턴스 참조는 실행 시작 때 동결한 값이다(v3 계약 §6). 없거나 깨졌으면 비워 둔다(binding_invalid로 꺼짐).
	if refs, ok := readTargetRefs(c.runsDir, c.jobID); ok {
		in.Binding.GenerationRef, in.Binding.InstanceRef = refs.GenerationRef, refs.InstanceRef
	} else {
		log.Printf("[e3-confirmation] 회차 %s 세대·인스턴스 참조를 읽지 못함", c.jobID)
	}
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
	// 계정 역할(A·B)은 동결한 선언이 정한다(계약 §4). 여기서는 그 라벨을 읽어 인증 결과를 붙이기만 하고, 동결본 대조와 역할·
	// 등급 확인은 e3contract.Run이 한다. 선언을 못 읽으면 역할을 비워 둔다(확정 단계는 정책 출처·계정 사유로 꺼진다).
	var decl e3contract.Declaration
	if ok {
		var err error
		if decl, err = (productDeclarationReader{runsDir: c.runsDir}).ReadFrozen(context.Background(), frozen.Policy); err != nil {
			// 역할을 비워 두면 Run이 동결본을 다시 읽어 대조하고 꺼진다. 원인은 여기서 한 줄 남긴다(값 없음).
			log.Printf("[e3-confirmation] 회차 %s 동결 선언을 읽지 못해 계정 역할을 비움", c.jobID)
		}
	}
	c.identityPath = decl.Identity.Path
	in.Owner, in.Cross = productE3Roles(decl, c.accounts)
	// 이 회차 인증 결과가 없어도 배운 방법(이 회차 성공·저장된 방법)이 있는 계정은 확정 단계에 쓴다(모델 로그인 계약 §6-1).
	if ll := c.learnedLoginState(); ll != nil {
		// 이 회차 성공 행의 출처는 그 실행의 E② 감사 근거와 대조한다. 실행을 받지 못하면(끝남·연결 없음) 진단 행은 쓰지 않는다.
		if c.handle != nil {
			if execution, _, reason := c.handle(taskID); reason == "" && execution != nil {
				ll.setAudit(e2AuditLookup(execution))
			}
		}
		in.Owner, in.Cross = usableLearnedAccounts(in.Owner, in.Cross, ll.usable)
	}
	return in, share
}

// learnedLoginState는 이 회차 학습 로그인 재사용 상태를 한 번 만든다. 실행 정책에 결속 계정이 없으면 nil이다(배울 계정 없음).
func (c *productE3Confirmation) learnedLoginState() *productLearnedLogin {
	if c.learned != nil {
		if c.learned.identityPath == "" {
			c.learned.identityPath = c.identityPath
		}
		return c.learned
	}
	binding, err := openRunBinding(c.runsDir, c.jobID)
	if err != nil {
		return nil
	}
	policy := binding.policy
	if binding.Close() != nil {
		log.Printf("[e3-confirmation] 회차 %s 실행 정책 닫기 실패", c.jobID)
	}
	if policy.Auth == nil || len(policy.Auth.Accounts) == 0 {
		return nil
	}
	credentials := map[string]string{}
	for _, a := range policy.Auth.Accounts {
		credentials[a.Label] = a.Credential.SHA256
	}
	c.learned = newProductLearnedLogin(c.registry, c.runsDir, c.jobID, c.target, policy.RunRef, credentials, c.identityPath)
	return c.learned
}

// finishLearned는 프로그램 로그인 성공 판정과 verified 기록을 한다. accepted는 확정 단계가 신원 근거로 받아들인 호출이다.
func (c *productE3Confirmation) finishLearned(accepted map[productEvidenceKey]bool) {
	ll := c.learnedLoginState()
	if ll == nil {
		return
	}
	bindings := c.learnedBindings
	ll.finish(func(label string) string {
		if bindings == nil {
			return ""
		}
		ref, _ := bindings.AccountBindingRef(ll.runRef, label)
		return ref
	}, accepted)
}

// timeShare는 실행 시작 때 발급한 실행 정책에 적힌 확정 단계 시간 몫이다. input이 확정 단계에 주는 몫과 같은 파일·같은
// 함수(productE3Admitted)로 읽는다. 감독자는 이 몫을 모델 단계 시간 상한에서 뺀다(urlTimeBudgetClose). 몫을 받지 못한
// 정책(-1)은 확정 단계도 요청을 보내지 않으므로 0이다. 정책을 읽지 못하면 오류다.
func (c *productE3Confirmation) timeShare() (time.Duration, error) {
	binding, err := openRunBinding(c.runsDir, c.jobID)
	if err != nil {
		return 0, err
	}
	_, share := productE3Admitted(binding.policy.Limits)
	if err := binding.Close(); err != nil {
		log.Printf("[e3-confirmation] 회차 %s 실행 정책 닫기 실패", c.jobID)
	}
	return share, nil
}

// productE3Admitted는 발급한 실행 정책의 확정 몫(withE3ConfirmationShare)에서 몫에 든 종류 수와 시간 몫을 되읽는다.
// 요청 몫 = 고정 7(다시 로그인 5 + 신원 2) + 종류×4, 시간 몫 = 요청 몫 × 확정 요청 시간 상한(기본값)이다. 칸이 없거나 이 모양이 아니면 -1(몫을 받지
// 못함)이고, 둘 다 0이면 든 종류가 없다.
func productE3Admitted(l server.E2HTTPFileLimits) (int, time.Duration) {
	if l.E3ConfirmationRequests == nil || l.E3ConfirmationTimeoutMS == nil {
		return -1, 0
	}
	requests, timeout := *l.E3ConfirmationRequests, time.Duration(*l.E3ConfirmationTimeoutMS)*time.Millisecond
	if requests == 0 && timeout == 0 {
		return 0, 0
	}
	fixed, perType := uint64(server.E3ConfirmationFixedRequests), uint64(server.E3ConfirmationRequestsPerType)
	if requests <= fixed || (requests-fixed)%perType != 0 || timeout != time.Duration(requests)*server.E3ConfirmationRequestTimeoutDefault {
		return -1, 0
	}
	return int((requests - fixed) / perType), timeout
}

// productE3Roles는 선언한 소유(A)·교차(B) 계정 라벨에 그 실행의 인증 준비 결과를 붙인다. 인증 결과가 authenticated가 아니면
// Authenticated가 거짓이라 확정 단계는 test_accounts_unavailable로 꺼진다. 계정을 이름순 등으로 고르지 않는다(관리자 계정이
// 교차 조회자가 되어 거짓 CONFIRMED가 날 수 있어서, 사용자 결정 2026-11-19).
func productE3Roles(decl e3contract.Declaration, read func() map[string]string) (owner, cross e3contract.Account) {
	results := map[string]string{}
	if read != nil {
		results = read()
	}
	role := func(label string) e3contract.Account {
		if label == "" {
			return e3contract.Account{}
		}
		return e3contract.Account{Label: label, Authenticated: results[label] == server.E2HTTPAuthAuthenticated}
	}
	return role(decl.OwnerAccountLabel), role(decl.CrossAccountLabel)
}

// e3RunStartRefusal은 회차 시작 때(모델 단계 전)의 리셋 게이트다(결정 §6.1 (a), 계약 §8). 지금 대상 판에 유효한 선언이 있는
// 대상만 본다(선언이 없으면 확정 단계가 꺼진 회차라 게이트도 없다). 지난 표식 잔존 점검(결정 §6.1 (b), E² 쪽)은 아직
// 없어서, 지난 객체가 있으면 ID를 알든 모르든 리셋 확인만으로 시작한다(계약 §12, 사용자 결정 2026-11-20).
func (r *productTargetRegistry) e3RunStartRefusal(ctx context.Context, rec productTargetRecord, jobID string) e3contract.StartRefusal {
	if _, active := r.activeDeclaration(rec); !active {
		return ""
	}
	// 실행 결속은 아직 발급 전이다(작업 번호 없음). 리셋 게이트는 대상 기록만 읽으므로 회차 참조만 싣는다.
	return e3contract.CheckRunStart(ctx, productResetGate{registry: r}, e3contract.RunBinding{JobRef: jobID}, rec.Target.ID)
}

// attachRun은 회차를 대상에 묶고 모델 단계 전에 E③ 리셋 게이트를 본다. 대상이 바뀌었으면 target_approval_changed, 게이트가
// 거절하면 묶음을 풀고 그 사유(e3contract.StartRefusal)를 확정 단계 결과 파일에 남긴 뒤(runsDir/<job>, 남기지 못해도 로그만)
// 돌려준다. 호출자는 이 사유로 회차를 실패로 마감한다.
func (p *productRuntime) attachRun(ctx context.Context, rec productTargetRecord, jobID, runsDir string, cancel context.CancelFunc) (func(), string) {
	release, err := p.attach(rec, jobID, cancel)
	if err != nil {
		return nil, "target_approval_changed"
	}
	if refusal := p.targets.e3RunStartRefusal(ctx, rec, jobID); refusal != "" {
		release()
		if recordRunStartRefusal(runsDir, jobID, refusal) != nil {
			log.Printf("[e3-confirmation] 회차 %s 시작 거부 기록 실패: e3_result_unrecorded", jobID)
		}
		return nil, string(refusal)
	}
	return release, ""
}

// recordRunStartRefusal은 회차 시작 리셋 게이트가 거절한 회차의 확정 단계 결과(start_refused, 요청 0)를 회차 디렉터리에
// 남긴다. 거절은 회차 디렉터리를 만들기 전이라 여기서 만든다(이미 있으면 그대로 쓴다). 결과 파일이 이미 있으면 덮어쓰지
// 않는다.
func recordRunStartRefusal(runsDir, jobID string, refusal e3contract.StartRefusal) error {
	if !refusal.Valid() || !managementReference.MatchString(jobID) || filepath.Base(jobID) != jobID || jobID == "." || jobID == ".." {
		return errPreparation
	}
	parent, err := os.OpenRoot(runsDir)
	if err != nil {
		return errPreparation
	}
	mkdirErr := parent.Mkdir(jobID, 0700)
	closeErr := parent.Close()
	if mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) || closeErr != nil {
		return errPreparation
	}
	raw, err := json.Marshal(e3contract.RefusedAtRunStart(refusal))
	if err != nil {
		return errPreparation
	}
	return writeRunPrivateFile(runsDir, jobID, productE3ConfirmationFile, raw)
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
