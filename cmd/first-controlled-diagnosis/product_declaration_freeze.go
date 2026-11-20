package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e3contract"
)

// 실행 시작 때의 정책 선언 동결(E③ 확정 경로 결정 §4.2, 계약 §3). 회차 디렉터리(runs/<job>)에 두 파일을 쓴다.
//   - e3-declaration.json: 선언의 정본 바이트 그대로. 이 파일의 SHA-256이 정책 출처 digest(policy_version)다.
//   - e3-policy.json: 정책 출처(product_declaration)와 유효창(대상 승인 기간과 실행 기간이 겹치는 구간).
//
// 준비 저널은 압축될 수 있어(#580) 옛 선언 줄을 나중에 다시 찾을 수 없으므로, 회차마다 사본을 둔다. 판정을 다시 읽을 때는
// 이 사본을 읽고, 현재 대상 등록의 선언으로 대신하지 않는다(OD06).
const (
	productFrozenDeclarationFile = "e3-declaration.json"
	productFrozenPolicyFile      = "e3-policy.json"
	productFrozenPolicyFormat    = "abc2lab.e3-frozen-policy/v1"
	productFrozenPolicyRefPrefix = "declaration-"
	// productFrozenPolicyMaxBytes는 e3-policy.json 상한이다. 참조·digest·시각 몇 개뿐이다.
	productFrozenPolicyMaxBytes = 4 << 10
)

// productFrozenPolicy는 e3-policy.json의 모양이다.
type productFrozenPolicy struct {
	Format     string                  `json:"format"`
	TargetRef  string                  `json:"target_ref"`
	Policy     e3contract.PolicySource `json:"policy"`
	ValidFrom  time.Time               `json:"valid_from"`
	ValidUntil time.Time               `json:"valid_until"`
}

// activeDeclaration은 지금 대상 판에 유효한 선언이다. 없거나 무효면 ok=false다.
func (r *productTargetRegistry) activeDeclaration(rec productTargetRecord) (e3contract.Declaration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := declarationView(rec, r.declarations[rec.Target.ID], r.now())
	if v.State != diagnosisprep.DeclarationActive || v.Declaration == nil {
		return e3contract.Declaration{}, false
	}
	return *v.Declaration, true
}

// freezeDeclaration은 동결 파일 두 개의 바이트를 만든다. 유효창은 실행 기간과 대상 승인 기간이 겹치는 구간이고, 겹치지
// 않으면 동결하지 않는다.
func freezeDeclaration(d e3contract.Declaration, jobID string, runFrom, runUntil, approvedAt, expiresAt time.Time) (declaration, policy []byte, err error) {
	from, until := runFrom, runUntil
	if approvedAt.After(from) {
		from = approvedAt
	}
	if expiresAt.Before(until) {
		until = expiresAt
	}
	if !from.Before(until) {
		return nil, nil, errPreparation
	}
	declaration, err = d.CanonicalBytes()
	if err != nil {
		return nil, nil, errPreparation
	}
	source, err := e3contract.Freeze(d, productFrozenPolicyRefPrefix+jobID)
	if err != nil {
		return nil, nil, errPreparation
	}
	// 정책 출처 digest는 동결 파일 바이트의 SHA-256과 같다(Digest = SHA-256(CanonicalBytes)).
	if sum := sha256.Sum256(declaration); hex.EncodeToString(sum[:]) != source.DigestSHA256 {
		return nil, nil, errPreparation
	}
	policy, err = json.Marshal(productFrozenPolicy{Format: productFrozenPolicyFormat, TargetRef: d.TargetRef, Policy: source,
		ValidFrom: from.UTC(), ValidUntil: until.UTC()})
	if err != nil {
		return nil, nil, errPreparation
	}
	return declaration, policy, nil
}

// productDeclarationReader는 e3contract.DeclarationReader다. 정책 출처 참조(declaration-<job>)의 회차 디렉터리에서 동결
// 사본을 그대로 읽는다. 대조(digest·대상 판)는 e3contract.VerifyFrozen이 한다.
type productDeclarationReader struct {
	runsDir string
}

func frozenJobID(ref string) (string, bool) {
	job, ok := strings.CutPrefix(ref, productFrozenPolicyRefPrefix)
	return job, ok && managementReference.MatchString(job) && filepath.Base(job) == job && job != "." && job != ".."
}

func (p productDeclarationReader) ReadFrozen(ctx context.Context, source e3contract.PolicySource) (e3contract.Declaration, error) {
	if err := ctx.Err(); err != nil {
		return e3contract.Declaration{}, err
	}
	job, ok := frozenJobID(source.Ref)
	if !ok || source.Kind != e3contract.PolicyProductDeclaration {
		return e3contract.Declaration{}, e3contract.ErrPolicyUnavailable
	}
	raw, err := readPrivateFile(filepath.Join(p.runsDir, job, productFrozenDeclarationFile), e3contract.MaxDeclarationEncodedBytes)
	if err != nil {
		return e3contract.Declaration{}, fmt.Errorf("%w: 동결 사본을 읽지 못함", e3contract.ErrPolicyUnavailable)
	}
	var d e3contract.Declaration
	if err := decodeFrozen(raw, &d); err != nil {
		// 읽었는데 모양이 깨졌으면 진위 실패 쪽이다. 빈 선언을 돌려주면 VerifyFrozen이 digest 불일치로 잡는다.
		return e3contract.Declaration{}, nil
	}
	return d, nil
}

// readFrozenPolicy는 회차의 동결 정책 출처를 읽는다(확정 단계가 RunBinding.Policy·유효창을 만들 때 쓴다). 동결 사본이
// 없으면 ok=false다(선언 없이 시작한 회차: 확정 단계는 꺼진다).
func readFrozenPolicy(runsDir, jobID string) (productFrozenPolicy, bool, error) {
	if !managementReference.MatchString(jobID) || filepath.Base(jobID) != jobID {
		return productFrozenPolicy{}, false, errPreparation
	}
	raw, err := readPrivateFile(filepath.Join(runsDir, jobID, productFrozenPolicyFile), productFrozenPolicyMaxBytes)
	if err != nil {
		return productFrozenPolicy{}, false, nil
	}
	var p productFrozenPolicy
	if decodeFrozen(raw, &p) != nil || p.Format != productFrozenPolicyFormat || p.Policy.ValidateProduct() != nil ||
		p.Policy.Ref != productFrozenPolicyRefPrefix+jobID || !p.ValidFrom.Before(p.ValidUntil) {
		return productFrozenPolicy{}, false, errPreparation
	}
	return p, true, nil
}

// decodeFrozen은 동결 파일을 엄격히 읽는다(모르는 칸·뒤에 붙은 값 거부). 정본 바이트는 빈 목록을 null로 쓰므로
// null을 거부하는 decodeLaunchJSON은 쓰지 않는다.
func decodeFrozen(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("frozen trailing data")
	}
	return nil
}

// 리셋 확인(결정 §6.1). 확정 단계가 대상에 객체를 만들면 그 사실을 선언 저널에 남기고(confirmation_writes), 운영자가
// "지난 회차 뒤 대상을 리셋했다"고 진술하면 그것도 남긴다(reset_confirmed). 쓰기 뒤 진술이 없으면 다음 회차를 시작하지
// 않는다(e3contract.CheckRunStart).
const (
	declarationConfirmationWrites = "confirmation_writes"
	declarationResetConfirmed     = "reset_confirmed"
	// declarationConfirmationWriteSettled는 미리 남긴 쓰기(ID 모름) 하나를 생성 결과로 갱신한 줄이다(기록하고 나서 보내기,
	// 사용자 결정 2026-11-19). created면 객체 ID digest를 채우고, 보내지 않은 것이 확실하면 그 쓰기를 거둔다(not_sent).
	declarationConfirmationWriteSettled = "confirmation_write_settled"
	// productResetStatement는 리셋 확인 때 운영자가 동의하는 진술이다. 저널에는 이 문장의 SHA-256만 남긴다.
	productResetStatement = "지난 확정 단계가 이 대상에 만든 시험 객체가 남지 않도록 대상을 리셋했다(처음 상태로 되돌렸다)."
	// maxConfirmationObjects는 한 회차가 만든 객체 기록 상한이다(종류마다 하나).
	maxConfirmationObjects = e3contract.MaxDeclarationTypes
)

func productResetStatementSHA256() string {
	sum := sha256.Sum256([]byte(productResetStatement))
	return hex.EncodeToString(sum[:])
}

// productConfirmationObject는 확정 단계가 만든(또는 만들었을 수 있는) 객체 하나다(종류·ID digest, 원문 없음). 생성 요청이
// 시간 초과·결과 불명이면 객체가 생겼는지·ID가 무엇인지 모르므로 IDUnknown이고 IDDigest는 비어 있다(사용자 결정
// 2026-11-18). 그런 객체는 잔존을 읽어 볼 수 없어 운영자 리셋 확인에만 기댄다.
type productConfirmationObject struct {
	Type     string `json:"type"`
	IDDigest string `json:"id_digest,omitempty"`
	// ID는 요청 본문 칸 ID(선언 create.id_field)다. 확정 단계가 보내기 전에 만들어 예약 때부터 남긴다. 비밀이 아니다(표식과
	// 다른 난수). 리셋 확인 때 이 ID로 남은 객체를 조회해 볼 수 있다. 응답 경로 ID(id_path)면 비어 있다.
	ID        string `json:"id,omitempty"`
	IDUnknown bool   `json:"id_unknown,omitempty"`
	// NotSent는 미리 남긴 쓰기를 거둔 것이다(생성 요청을 보내지 않은 것이 확실). 남은 쓰기로 세지 않는다.
	NotSent bool `json:"not_sent,omitempty"`
}

func (o productConfirmationObject) valid() bool {
	if o.Type == "" || len(o.Type) > 64 || o.ID != "" && !productObjectIDPattern.MatchString(o.ID) {
		return false
	}
	if o.IDUnknown {
		return o.IDDigest == "" && o.ID == ""
	}
	if o.NotSent {
		// ID를 아는 예약을 거둔 것(id_field). ID 모름 예약을 거둔 것은 위 갈래다.
		return o.IDDigest == "" && o.ID != ""
	}
	if o.IDDigest == "" {
		// ID를 아는 예약(결과 전, 또는 결과 불명): ID가 있어야 한다.
		return o.ID != ""
	}
	return urlDigestValid(o.IDDigest)
}

// pending은 결과로 아직 갱신하지 않은 예약이다(ID 모름 또는 ID만 앎).
func (o productConfirmationObject) pending() bool { return o.IDDigest == "" && !o.NotSent }

// productObjectIDPattern은 확정 단계가 만든 객체 ID 모양이다(e3contract.NewObjectID).
var productObjectIDPattern = regexp.MustCompile(`^` + regexp.QuoteMeta(e3contract.ObjectIDPrefix) + `[0-9a-f]{32}$`)

// liveConfirmationObjects는 거두지 않은(대상에 남았을 수 있는) 쓰기다.
func liveConfirmationObjects(objects []productConfirmationObject) []productConfirmationObject {
	var live []productConfirmationObject
	for _, o := range objects {
		if !o.NotSent {
			live = append(live, o)
		}
	}
	return live
}

// productResetState는 대상의 마지막 확정 단계 쓰기와 그 뒤 리셋 확인이다.
type productResetState struct {
	writesJob     string
	writesAt      time.Time
	writesObjects []productConfirmationObject
	// writesRef는 마지막 쓰기 기록 줄의 고유 참조다(회차·시각·객체 집합의 SHA-256). 리셋 동의는 이 값에 묶인다.
	writesRef   string
	confirmedBy string
	confirmedAt time.Time
}

// confirmationWritesRef는 쓰기 기록 줄 하나의 고유 참조다. 같은 회차라도 시각·객체 집합이 다르면 다른 값이다.
// 객체는 칸 전체(거둠 여부 포함)를 JSON으로 넣는다. 같은 시각의 정산이 살아 있는 쓰기를 바꾸면 참조도 바뀌어, 정산 전에 본
// 참조로는 리셋을 확인할 수 없다(#738 이동찬 리뷰). 칸이 늘어도 참조에서 빠지지 않는다.
func confirmationWritesRef(job string, at time.Time, objects []productConfirmationObject) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", job, at.UTC().Format(time.RFC3339Nano))
	if objects == nil {
		objects = []productConfirmationObject{}
	}
	raw, err := json.Marshal(objects)
	if err != nil {
		// 고정 모양 구조체라 실패하지 않는다. 실패하면 어느 기록과도 맞지 않는 참조를 돌려 확인을 막는다.
		return ""
	}
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

// productConfirmationActor는 확정 단계 쓰기 기록의 행위자다(사람이 아니라 프로그램).
const productConfirmationActor = "e3-confirmation"

// applyReset은 리셋 관련 저널 줄 하나를 상태에 적용한 새 상태다. 받지 않을 줄이면 오류이고 상태는 그대로다. jobs는 이 대상에
// 쓰기 기록을 이미 남긴 회차이고 읽기만 한다(받은 줄의 회차는 호출자가 반영이 끝난 뒤 하나만 더한다).
func applyReset(rec productTargetRecord, st *productResetState, jobs map[string]bool, e productDeclarationEvent) (*productResetState, error) {
	if e.At.IsZero() || e.TargetRevision < 1 || e.TargetRevision > rec.Target.Revision || e.DigestSHA256 != "" || e.Declaration != nil {
		return nil, errPreparation
	}
	next := productResetState{}
	if st != nil {
		next = *st
	}
	switch e.Action {
	case declarationConfirmationWrites:
		if e.Actor != productConfirmationActor || !managementReference.MatchString(e.JobRef) || len(e.Objects) == 0 ||
			len(e.Objects) > maxConfirmationObjects || e.StatementSHA256 != "" {
			return nil, errPreparation
		}
		for _, o := range e.Objects {
			if !o.valid() || o.NotSent {
				return nil, errPreparation
			}
		}
		// 지금 쓰기 기록의 회차는 생성 요청마다 줄을 더 남긴다(기록하고 나서 보내기). 지나간 회차는 더 받지 않는다(#708
		// 이동찬 리뷰: 이미 끝난 회차의 기록이 나중에 바뀌지 않게).
		if jobs[e.JobRef] && e.JobRef != next.writesJob || e.WritesRef != "" {
			return nil, errPreparation
		}
		// 다른 회차의 미해결 쓰기(리셋 확인 전, 거두지 않은 쓰기)는 새 회차의 예약이 덮을 수 없다(#738 이동찬 리뷰). 같은 대상에
		// 실행이 둘 붙어 있으면 앞 회차의 기록이 지워져 리셋 없이 다음 회차가 열릴 수 있기 때문이다. 받지 않으면 그 회차는
		// 생성 요청을 보내지 않는다(기록하고 나서 보내기).
		if e.JobRef != next.writesJob && next.writesJob != "" && next.confirmedAt.IsZero() && len(liveConfirmationObjects(next.writesObjects)) > 0 {
			return nil, errPreparation
		}
		objects := append([]productConfirmationObject(nil), e.Objects...)
		if e.JobRef == next.writesJob {
			objects = append(append([]productConfirmationObject(nil), next.writesObjects...), e.Objects...)
		}
		if len(objects) > maxConfirmationObjects {
			return nil, errPreparation
		}
		// 새 쓰기는 앞선 리셋 확인을 덮는다(이번 객체는 아직 리셋되지 않았다). 기록이 바뀌면 참조도 바뀌어, 바뀌기 전 기록을 본
		// 동의는 반영되지 않는다.
		next = productResetState{writesJob: e.JobRef, writesAt: e.At, writesObjects: objects, writesRef: confirmationWritesRef(e.JobRef, e.At, objects)}
	case declarationConfirmationWriteSettled:
		// 지금 회차의 미리 남긴 쓰기(ID 모름) 하나를 결과로 갱신한다: created면 ID digest, not_sent면 거둠.
		if e.Actor != productConfirmationActor || e.JobRef == "" || e.JobRef != next.writesJob || len(e.Objects) != 1 ||
			e.StatementSHA256 != "" || e.WritesRef != "" {
			return nil, errPreparation
		}
		o := e.Objects[0]
		// 갱신은 created(ID digest)나 not_sent(거둠)뿐이다. 예약과 같은 ID여야 한다(ID를 아는 예약을 다른 ID로 바꾸지 않음).
		if !o.valid() || o.pending() {
			return nil, errPreparation
		}
		objects := append([]productConfirmationObject(nil), next.writesObjects...)
		i := slices.IndexFunc(objects, func(p productConfirmationObject) bool { return p.Type == o.Type && p.pending() && p.ID == o.ID })
		if i < 0 {
			return nil, errPreparation
		}
		objects[i] = o
		next = productResetState{writesJob: e.JobRef, writesAt: e.At, writesObjects: objects, writesRef: confirmationWritesRef(e.JobRef, e.At, objects)}
	case declarationResetConfirmed:
		// 확인은 운영자가 본 쓰기 기록 줄(고유 참조)에만 묶인다. 그 뒤 새 쓰기가 들어왔으면 그 확인 줄은 받지 않는다.
		if e.Actor != rec.Principal || e.StatementSHA256 != productResetStatementSHA256() || e.JobRef == "" || e.JobRef != next.writesJob ||
			e.WritesRef == "" || e.WritesRef != next.writesRef || len(e.Objects) != 0 || !next.confirmedAt.IsZero() {
			return nil, errPreparation
		}
		next.confirmedBy, next.confirmedAt = e.Actor, e.At
	default:
		return nil, errPreparation
	}
	return &next, nil
}

// replayReset은 리셋 관련 저널 줄을 반영한다.
func (r *productTargetRegistry) replayReset(e productDeclarationEvent) error {
	rec, ok := r.records[e.TargetID]
	if !ok {
		return errPreparation
	}
	next, err := applyReset(rec, r.resets[e.TargetID], r.resetJobs[e.TargetID], e)
	if err != nil {
		return err
	}
	r.setReset(e, next)
	return nil
}

// setReset은 받은 줄의 결과를 상태에 반영한다. 쓰기 기록이면 그 회차 하나만 대상의 회차 목록에 더한다(기존 목록은 복사하지
// 않는다, #708 이동찬 리뷰: 재생 비용이 줄 수의 제곱이 되지 않게).
func (r *productTargetRegistry) setReset(e productDeclarationEvent, next *productResetState) {
	r.resets[e.TargetID] = next
	if e.Action != declarationConfirmationWrites {
		return
	}
	if r.resetJobs[e.TargetID] == nil {
		r.resetJobs[e.TargetID] = map[string]bool{}
	}
	r.resetJobs[e.TargetID][e.JobRef] = true
}

// recordConfirmationWrites는 확정 단계가 대상에 만들(었을 수 있는) 객체를 남긴다. 이 기록이 있으면 운영자의 리셋 확인
// 전까지 다음 회차를 시작하지 않는다. 확정 단계는 생성 요청을 보내기 전에 그 종류를 ID 모름으로 먼저 남긴다
// (productWriteRecorder.Reserve).
func (r *productTargetRegistry) recordConfirmationWrites(targetID, jobID string, objects []productConfirmationObject) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[targetID]
	if !ok {
		return errPreparation
	}
	e := productDeclarationEvent{TargetID: targetID, Action: declarationConfirmationWrites, Actor: productConfirmationActor,
		At: r.now().UTC().Truncate(time.Microsecond), TargetRevision: rec.Target.Revision, JobRef: jobID, Objects: objects}
	return r.appendReset(rec, e)
}

// settleConfirmationWrite는 지금 회차의 미리 남긴 쓰기 하나를 생성 결과로 갱신한다(productWriteRecorder.Settle).
func (r *productTargetRegistry) settleConfirmationWrite(targetID, jobID string, object productConfirmationObject) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[targetID]
	if !ok {
		return errPreparation
	}
	e := productDeclarationEvent{TargetID: targetID, Action: declarationConfirmationWriteSettled, Actor: productConfirmationActor,
		At: r.now().UTC().Truncate(time.Microsecond), TargetRevision: rec.Target.Revision, JobRef: jobID, Objects: []productConfirmationObject{object}}
	return r.appendReset(rec, e)
}

// appendReset은 줄을 먼저 검사하고(받지 않을 줄은 저널에 쓰지 않는다) 저널에 쓴 뒤 상태에 반영한다. r.mu를 잡은 채로 부른다.
func (r *productTargetRegistry) appendReset(rec productTargetRecord, e productDeclarationEvent) error {
	next, err := applyReset(rec, r.resets[e.TargetID], r.resetJobs[e.TargetID], e)
	if err != nil {
		return errPreparation
	}
	raw, err := json.Marshal(e)
	if err != nil || r.check() != nil {
		return errPreparation
	}
	if err := r.declarationJournal.Append(raw); err != nil {
		return errPreparation
	}
	r.setReset(e, next)
	return nil
}

func resetView(st *productResetState) diagnosisprep.ProductResetState {
	v := diagnosisprep.ProductResetState{Statement: productResetStatement}
	live := 0
	if st != nil {
		live = len(liveConfirmationObjects(st.writesObjects))
	}
	// 미리 남긴 쓰기를 모두 거뒀으면(보내지 않음) 남은 쓰기가 없다.
	if st == nil || st.writesJob == "" || live == 0 {
		return v
	}
	v.LastWritesJob, v.LastWritesAt, v.LastWritesObjects, v.LastWritesRef = st.writesJob, st.writesAt, live, st.writesRef
	v.ResetRequired = st.confirmedAt.IsZero()
	v.ConfirmedBy, v.ConfirmedAt = st.confirmedBy, st.confirmedAt
	return v
}

func (r *productTargetRegistry) resetConfirmation(principal, id string) (diagnosisprep.ProductResetState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, _, err := r.accountTarget(principal, id); err != nil {
		return diagnosisprep.ProductResetState{}, err
	}
	return resetView(r.resets[id]), nil
}

// confirmReset은 대상 승인자가 리셋 진술에 동의해 남긴다. 동의는 운영자가 화면에서 본 쓰기 기록 줄(writesRef, 고유
// 참조)에만 묶인다: 지금 기록이 다르면(조회와 동의 사이에 새 쓰기가 들어옴) reset_record_changed로 반영하지 않는다. 같은
// 기록을 이미 확인했으면 줄을 더 남기지 않는다. 리셋은 더 안전한 쪽이라 대상 승인 상태와 관계없이 받는다.
func (r *productTargetRegistry) confirmReset(principal, id string, statementConfirmed bool, writesRef string) (diagnosisprep.ProductResetState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, _, err := r.accountTarget(principal, id)
	if err != nil {
		return diagnosisprep.ProductResetState{}, err
	}
	if !statementConfirmed {
		return diagnosisprep.ProductResetState{}, errors.New("statement_required")
	}
	st := r.resets[id]
	if st == nil || st.writesJob == "" || st.writesRef != writesRef || len(liveConfirmationObjects(st.writesObjects)) == 0 {
		return diagnosisprep.ProductResetState{}, errors.New("reset_record_changed")
	}
	if !st.confirmedAt.IsZero() {
		return resetView(st), nil
	}
	e := productDeclarationEvent{TargetID: id, Action: declarationResetConfirmed, Actor: principal,
		At: r.now().UTC().Truncate(time.Microsecond), TargetRevision: rec.Target.Revision, JobRef: st.writesJob,
		WritesRef: st.writesRef, StatementSHA256: productResetStatementSHA256()}
	if err := r.appendReset(rec, e); err != nil {
		return diagnosisprep.ProductResetState{}, errPreparation
	}
	return resetView(r.resets[id]), nil
}

// productResetGate는 e3contract.ResetGate다. 리셋 확인 기록은 선언 저널에서, 지난 표식 잔존은 residue(E²가 지난 객체를
// 읽어 보는 점검, 결정 §6.1 (b))에서 얻는다. residue가 없으면 지난 쓰기가 있는 대상은 잔존을 모르므로 시작하지 않는다.
type productResetGate struct {
	registry *productTargetRegistry
	residue  func(ctx context.Context, binding e3contract.RunBinding, objects []productConfirmationObject) (e3contract.Residue, error)
}

// Check는 리셋 확인·표식 잔존과 함께, 결속에 세대·인스턴스 참조가 있으면(확정 단계 시작) 그 참조가 지금 대상과 같은지
// 본다(v3 계약 §6). 서버의 회차 시작 점검은 참조 없는 결속으로 불러 대조하지 않는다.
func (g productResetGate) Check(ctx context.Context, binding e3contract.RunBinding, targetRef string) (e3contract.ResetState, error) {
	state, err := g.check(ctx, binding, targetRef)
	if err == nil && (binding.GenerationRef != "" || binding.InstanceRef != "") {
		state.TargetChanged = g.registry.targetRefsChanged(targetRef, binding)
	}
	return state, err
}

func (g productResetGate) check(ctx context.Context, binding e3contract.RunBinding, targetRef string) (e3contract.ResetState, error) {
	if g.registry == nil {
		return e3contract.ResetState{}, errPreparation
	}
	g.registry.mu.Lock()
	_, known := g.registry.records[targetRef]
	var st productResetState
	if cur := g.registry.resets[targetRef]; cur != nil {
		st = *cur
		st.writesObjects = append([]productConfirmationObject(nil), cur.writesObjects...)
	}
	g.registry.mu.Unlock()
	if !known {
		return e3contract.ResetState{}, errPreparation
	}
	live := liveConfirmationObjects(st.writesObjects)
	if st.writesJob == "" || len(live) == 0 {
		return e3contract.ResetState{Residue: e3contract.ResidueAbsent}, nil
	}
	// 지난 쓰기가 있으면 리셋 확인과 별개로 지난 객체를 읽어 표식 잔존을 본다(결정 §6.1 (b)). 점검이 없거나 실패하면 모른다.
	state := e3contract.ResetState{PreviousWrites: true, ResetConfirmed: !st.confirmedAt.IsZero(), Residue: e3contract.ResidueUnknown}
	// 잔존은 ID를 아는 객체만 읽어 본다. ID 모르는 객체(생성 시간 초과·결과 불명)는 읽을 수 없어 리셋 확인에만 기댄다.
	// 표식은 회차마다 새 난수라 지난 객체가 남아도 다음 회차 판정을 확정으로 만들지 못한다.
	var readable []productConfirmationObject
	for _, o := range live {
		if !o.IDUnknown {
			readable = append(readable, o)
		}
	}
	if len(readable) == 0 {
		state.Residue = e3contract.ResidueAbsent
		return state, nil
	}
	if g.residue == nil {
		// 잔존 점검(E² 조회)이 아직 없다. ID를 아는 객체도 ID 모르는 객체와 같이 리셋 확인에만 기댄다(사용자 결정
		// 2026-11-20): 그러지 않으면 같은 대상을 두 번째 회차부터 점검할 수 없다. 점검은 측정 뒤 개선(계약 §12).
		state.Residue = e3contract.ResidueNotChecked
		return state, nil
	}
	if residue, err := g.residue(ctx, binding, readable); err == nil {
		state.Residue = residue
	}
	return state, nil
}
