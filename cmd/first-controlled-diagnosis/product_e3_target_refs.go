package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Autumn-27/artex/e3contract"
)

// E③ 세대·인스턴스 참조(E③↔E② v3 계약 §6, 결정 §5 0단계, 규칙안 docs/e3-target-refs-rules.md). 실행 시작 때 서버가
// 확인한 대상 상태로 발급해 runs/<job>/e3-target-refs.json에 한 번 쓰고, 확정 단계를 시작할 때 리셋 게이트가 지금 대상으로
// 다시 계산해 대조한다.
//
//   - 세대: 대상 ID·origin·승인 판·승인 시각과 마지막 확정 쓰기 기록·리셋 확인, 그리고 그 회차(job). 대상을 다시
//     승인하거나, 우리 확정 쓰기가 남거나, 리셋을 확인하면 바뀐다. 회차가 바뀌어도 바뀐다.
//   - 인스턴스: 세대와, 실행이 고정한 접속 주소(고른 IP·주소 집합·포트)·TLS 공개 키(https). 같은 세대라도 주소나 키가
//     바뀌면 바뀐다.
//   - 회차 사이에는 재사용하지 않는다: 회차와 회차 사이에 대상이 이어졌는지 서버는 증명할 수 없다(확인 없는 리셋, 같은
//     IP·TLS 키로 다시 만든 인스턴스, 다른 회차의 진단 쓰기). 그래서 같은 대상 상태여도 회차가 다르면 다른 참조다.
//   - 모르면 새로 발급: 입력 중 하나라도 확인할 수 없으면 실행마다 새 난수 참조를 쓴다. 대조 때도 지금 상태를 확인하지
//     못하면 바뀐 것으로 본다. 그래서 대상이 바뀌었는데 같은 참조가 남는 경우가 없다.
//
// 한 회차 안에서 같은 IP·TLS 키로 운영자 확인 없이 인스턴스를 다시 만들면 서버는 알 수 없다(규칙안 §4: 확정 단계는 자기
// 객체를 만들고 곧바로 읽으므로 그 경우 조회가 실패해 CONFIRMED가 나오지 않는다).
const (
	productTargetRefsFile = "e3-target-refs.json"
	// v2: 참조가 회차(job)에 결속된다(v1은 회차를 섞지 않아 같은 대상 상태면 회차 사이에 같은 참조였다).
	productTargetRefsVersion  = "e3-target-refs/v2"
	productTargetRefsMaxBytes = 4096

	productGenerationDomain = "e3-target-generation/v2"
	productInstanceDomain   = "e3-target-instance/v2"
	// 참조 머리말. 아는 상태에서 나온 참조와 모르는 상태에서 새로 발급한 참조를 이름으로도 가른다.
	productGenerationPrefix      = "gen-"
	productInstancePrefix        = "inst-"
	productFreshGenerationPrefix = "gen-x-"
	productFreshInstancePrefix   = "inst-x-"
	// productTargetRefHexLen은 digest·난수에서 쓰는 16진 길이다(128비트).
	productTargetRefHexLen = 32
)

// productTargetRefs는 e3-target-refs.json의 모양이다. Known이 거짓이면 그 참조는 모르는 상태에서 새로 발급한 것이다.
type productTargetRefs struct {
	Version         string `json:"version"`
	GenerationRef   string `json:"generation_ref"`
	InstanceRef     string `json:"instance_ref"`
	GenerationKnown bool   `json:"generation_known"`
	InstanceKnown   bool   `json:"instance_known"`
}

var (
	errTargetRefs = errors.New("product: e3 target refs")
	// 동결 파일은 우리가 발급한 형식만 받는다.
	productGenerationRefPattern = regexp.MustCompile(`^gen-(x-)?[0-9a-f]{32}$`)
	productInstanceRefPattern   = regexp.MustCompile(`^inst-(x-)?[0-9a-f]{32}$`)
)

// refHasher는 길이 접두 필드로 digest를 만든다(필드 경계가 섞이지 않게).
type refHasher struct{ h []byte }

func newRefHasher(domain string) *refHasher {
	r := &refHasher{}
	r.add(domain)
	return r
}

func (r *refHasher) add(fields ...string) {
	for _, f := range fields {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		r.h = append(r.h, n[:]...)
		r.h = append(r.h, f...)
	}
}

func (r *refHasher) ref(prefix string) string {
	sum := sha256.Sum256(r.h)
	return prefix + hex.EncodeToString(sum[:])[:productTargetRefHexLen]
}

// productTargetGeneration은 대상 기록·리셋 상태와 회차(job)로 세대 참조를 만든다. 승인된 대상이 아니거나 승인 근거가
// 없거나 회차를 모르면 모른다.
func productTargetGeneration(rec productTargetRecord, reset *productResetState, jobID string) (string, bool) {
	t := rec.Target
	if t.ID == "" || t.Origin == "" || t.State != "approved" || t.Revision == 0 || t.ApprovedAt.IsZero() || jobID == "" {
		return "", false
	}
	h := newRefHasher(productGenerationDomain)
	h.add(t.ID, t.Origin, uintString(t.Revision), timeString(t.ApprovedAt.UnixNano()))
	if reset == nil || reset.writesJob == "" {
		h.add("writes:none")
	} else {
		if reset.writesRef == "" {
			return "", false
		}
		h.add("writes", reset.writesRef, reset.writesJob, timeString(reset.writesAt.UnixNano()))
		if reset.confirmedAt.IsZero() {
			h.add("reset:none")
		} else {
			h.add("reset", reset.confirmedBy, timeString(reset.confirmedAt.UnixNano()))
		}
	}
	// 회차 경계: 회차 사이 대상 연속성은 증명할 수 없으므로 같은 상태여도 회차가 다르면 다른 세대다(인스턴스도 따라 바뀜).
	h.add("run", jobID)
	return h.ref(productGenerationPrefix), true
}

// productTargetInstance는 세대와 실행이 고정한 접속 주소·TLS 키로 인스턴스 참조를 만든다. 고른 IP·주소·포트가 없거나
// https인데 TLS 키가 없으면 모른다.
func productTargetInstance(rec productTargetRecord, generation string) (string, bool) {
	t := rec.Target
	https := strings.HasPrefix(t.Origin, "https://")
	if generation == "" || rec.SelectedIP == "" || len(t.Addresses) == 0 || t.Port <= 0 || https && t.TLSSPKISHA256 == "" {
		return "", false
	}
	addresses := slices.Clone(t.Addresses)
	slices.Sort(addresses)
	h := newRefHasher(productInstanceDomain)
	h.add(generation, t.Origin, uintString(uint64(t.Port)), rec.SelectedIP, t.TLSSPKISHA256)
	h.add(addresses...)
	return h.ref(productInstancePrefix), true
}

func uintString(v uint64) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return string(b[:])
}

func timeString(v int64) string { return uintString(uint64(v)) }

// currentTargetRefs는 그 회차에서 본 지금 대상 상태의 참조다(잠금 안에서 읽는다). 대상이 없으면 둘 다 모른다.
func (r *productTargetRegistry) currentTargetRefs(targetID, jobID string) (generation string, genKnown bool, instance string, instKnown bool) {
	r.mu.Lock()
	rec, ok := r.records[targetID]
	var reset *productResetState
	if cur := r.resets[targetID]; cur != nil {
		copied := *cur
		reset = &copied
	}
	r.mu.Unlock()
	if !ok {
		return "", false, "", false
	}
	generation, genKnown = productTargetGeneration(rec, reset, jobID)
	if !genKnown {
		return generation, false, "", false
	}
	instance, instKnown = productTargetInstance(rec, generation)
	return generation, genKnown, instance, instKnown
}

// issueTargetRefs는 실행(job) 시작 때 참조를 발급한다. 모르는 참조는 random에서 새로 만든다(실행마다 다름).
func (r *productTargetRegistry) issueTargetRefs(targetID, jobID string, random io.Reader) (productTargetRefs, error) {
	gen, genKnown, inst, instKnown := r.currentTargetRefs(targetID, jobID)
	out := productTargetRefs{Version: productTargetRefsVersion, GenerationRef: gen, InstanceRef: inst,
		GenerationKnown: genKnown, InstanceKnown: instKnown}
	fresh := func(prefix string) (string, error) {
		b := make([]byte, productTargetRefHexLen/2)
		if random == nil {
			return "", errTargetRefs
		}
		if _, err := io.ReadFull(random, b); err != nil {
			return "", errTargetRefs
		}
		return prefix + hex.EncodeToString(b), nil
	}
	var err error
	if !genKnown {
		if out.GenerationRef, err = fresh(productFreshGenerationPrefix); err != nil {
			return productTargetRefs{}, err
		}
	}
	if !instKnown {
		if out.InstanceRef, err = fresh(productFreshInstancePrefix); err != nil {
			return productTargetRefs{}, err
		}
	}
	return out, nil
}

// targetRefsChanged는 결속의 참조가 그 회차(binding.JobRef)에서 본 지금 대상과 다른지다. 지금 상태를 확인하지 못하거나,
// 발급 때 몰라서 새로 만든 참조면 바뀐 것으로 본다(같다고 증명할 수 없음).
func (r *productTargetRegistry) targetRefsChanged(targetID string, binding e3contract.RunBinding) bool {
	gen, genKnown, inst, instKnown := r.currentTargetRefs(targetID, binding.JobRef)
	return !genKnown || !instKnown || gen != binding.GenerationRef || inst != binding.InstanceRef
}

// readTargetRefs는 그 회차에 동결한 참조를 읽는다. 없거나 모양이 깨졌으면 ok=false다(확정 단계 결속이 비어 꺼진다).
func readTargetRefs(runsDir, jobID string) (productTargetRefs, bool) {
	raw, err := readPrivateFile(filepath.Join(runsDir, jobID, productTargetRefsFile), productTargetRefsMaxBytes)
	if err != nil {
		return productTargetRefs{}, false
	}
	var refs productTargetRefs
	if decodeFrozen(raw, &refs) != nil || refs.Version != productTargetRefsVersion ||
		!productGenerationRefPattern.MatchString(refs.GenerationRef) || !productInstanceRefPattern.MatchString(refs.InstanceRef) ||
		refs.GenerationKnown == strings.HasPrefix(refs.GenerationRef, productFreshGenerationPrefix) ||
		refs.InstanceKnown == strings.HasPrefix(refs.InstanceRef, productFreshInstancePrefix) {
		return productTargetRefs{}, false
	}
	return refs, true
}

func (refs productTargetRefs) marshal() ([]byte, error) { return json.Marshal(refs) }
