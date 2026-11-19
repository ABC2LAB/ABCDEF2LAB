package main

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3contract"
)

// targetRefsBase는 승인된 시험 대상 기록 하나다(세대·인스턴스를 모두 아는 상태).
func targetRefsBase(t *testing.T) productTargetRecord {
	t.Helper()
	r, _, target, _ := declarationTestTarget(t)
	r.mu.Lock()
	rec := r.records[target.ID]
	r.mu.Unlock()
	rec.Target.Addresses = append([]string(nil), rec.Target.Addresses...)
	if _, ok := productTargetGeneration(rec, nil); !ok {
		t.Fatalf("시험 대상 세대를 모름: %+v", rec.Target)
	}
	return rec
}

// 대상이 바뀌면 세대나 인스턴스 참조가 반드시 바뀐다. 같은 상태면 같은 참조다(v3 계약 §6, 사용자 지시 2026-11-19).
func TestProductTargetRefsChangeWithTarget(t *testing.T) {
	base := targetRefsBase(t)
	at := time.Date(2026, 11, 19, 1, 0, 0, 0, time.UTC)
	written := &productResetState{writesJob: "job-old", writesAt: at, writesRef: strings.Repeat("a", 64)}
	confirmed := &productResetState{writesJob: "job-old", writesAt: at, writesRef: strings.Repeat("a", 64), confirmedBy: "ARTEX", confirmedAt: at.Add(time.Hour)}
	refs := func(rec productTargetRecord, reset *productResetState) (string, string) {
		gen, ok := productTargetGeneration(rec, reset)
		if !ok {
			t.Fatalf("세대를 모름: %+v", rec.Target)
		}
		inst, ok := productTargetInstance(rec, gen)
		if !ok {
			t.Fatalf("인스턴스를 모름: %+v", rec)
		}
		return gen, inst
	}
	baseGen, baseInst := refs(base, nil)
	if g, i := refs(base, nil); g != baseGen || i != baseInst {
		t.Fatal("같은 상태가 다른 참조")
	}
	// 주소 집합 순서만 다르면 같은 인스턴스다.
	ordered, reversed := base, base
	ordered.Target.Addresses = []string{"192.0.2.250", "192.0.2.251"}
	reversed.Target.Addresses = []string{"192.0.2.251", "192.0.2.250"}
	if _, a := refs(ordered, nil); func() bool { _, b := refs(reversed, nil); return a != b }() {
		t.Fatal("주소 순서만 다른데 다른 인스턴스")
	}
	type change struct {
		edit       func(*productTargetRecord)
		reset      *productResetState
		generation bool // 세대가 바뀌어야 하면 참(그러면 인스턴스도 바뀜)
	}
	changes := map[string]change{
		"target_id":       {func(r *productTargetRecord) { r.Target.ID += "-other" }, nil, true},
		"origin":          {func(r *productTargetRecord) { r.Target.Origin = "https://b.example.test" }, nil, true},
		"reapproved":      {func(r *productTargetRecord) { r.Target.Revision++ }, nil, true},
		"approved_at":     {func(r *productTargetRecord) { r.Target.ApprovedAt = r.Target.ApprovedAt.Add(time.Second) }, nil, true},
		"writes_left":     {nil, written, true},
		"reset_confirmed": {nil, confirmed, true},
		"other_writes":    {nil, &productResetState{writesJob: "job-other", writesAt: at, writesRef: strings.Repeat("b", 64)}, true},
		"writes_time":     {nil, &productResetState{writesJob: "job-old", writesAt: at.Add(time.Second), writesRef: strings.Repeat("a", 64)}, true},
		"reset_by":        {nil, &productResetState{writesJob: "job-old", writesAt: at, writesRef: strings.Repeat("a", 64), confirmedBy: "OTHER", confirmedAt: at.Add(time.Hour)}, true},
		"reset_again":     {nil, &productResetState{writesJob: "job-old", writesAt: at, writesRef: strings.Repeat("a", 64), confirmedBy: "ARTEX", confirmedAt: at.Add(2 * time.Hour)}, true},
		"selected_ip":     {func(r *productTargetRecord) { r.SelectedIP = "192.0.2.251" }, nil, false},
		"address_set":     {func(r *productTargetRecord) { r.Target.Addresses = append(r.Target.Addresses, "192.0.2.252") }, nil, false},
		"port":            {func(r *productTargetRecord) { r.Target.Port++ }, nil, false},
		"tls_key":         {func(r *productTargetRecord) { r.Target.TLSSPKISHA256 = strings.Repeat("c", 64) }, nil, false},
	}
	for name, c := range changes {
		rec := base
		rec.Target.Addresses = append([]string(nil), base.Target.Addresses...)
		if c.edit != nil {
			c.edit(&rec)
		}
		gen, inst := refs(rec, c.reset)
		if inst == baseInst {
			t.Fatalf("%s: 대상이 바뀌었는데 인스턴스 참조가 그대로", name)
		}
		if c.generation && gen == baseGen {
			t.Fatalf("%s: 대상이 바뀌었는데 세대 참조가 그대로", name)
		}
		if !c.generation && gen != baseGen {
			t.Fatalf("%s: 접속만 바뀌었는데 세대가 바뀜", name)
		}
	}
	// 리셋을 확인하면 쓰기만 남은 세대와도 다르다.
	if g1, _ := refs(base, written); func() bool { g2, _ := refs(base, confirmed); return g1 == g2 }() {
		t.Fatal("리셋 확인 전후 세대가 같음")
	}

	// 모르는 상태: 승인 아님·판 없음·승인 시각 없음·쓰기 참조 없음은 세대를, 고른 IP·주소·포트·https TLS 키 없음은 인스턴스를 모른다.
	unknownGen := map[string]func(*productTargetRecord){
		"revoked":     func(r *productTargetRecord) { r.Target.State = "revoked" },
		"no_revision": func(r *productTargetRecord) { r.Target.Revision = 0 },
		"no_approved": func(r *productTargetRecord) { r.Target.ApprovedAt = time.Time{} },
	}
	for name, edit := range unknownGen {
		rec := base
		edit(&rec)
		if _, ok := productTargetGeneration(rec, nil); ok {
			t.Fatalf("%s: 세대를 안다고 봄", name)
		}
	}
	if _, ok := productTargetGeneration(base, &productResetState{writesJob: "job-old"}); ok {
		t.Fatal("쓰기 참조 없는 쓰기 기록에서 세대를 안다고 봄")
	}
	unknownInst := map[string]func(*productTargetRecord){
		"no_selected_ip": func(r *productTargetRecord) { r.SelectedIP = "" },
		"no_addresses":   func(r *productTargetRecord) { r.Target.Addresses = nil },
		"no_port":        func(r *productTargetRecord) { r.Target.Port = 0 },
		"https_no_key":   func(r *productTargetRecord) { r.Target.TLSSPKISHA256 = "" },
	}
	for name, edit := range unknownInst {
		rec := base
		edit(&rec)
		if !strings.HasPrefix(rec.Target.Origin, "https://") && name == "https_no_key" {
			continue
		}
		if _, ok := productTargetInstance(rec, baseGen); ok {
			t.Fatalf("%s: 인스턴스를 안다고 봄", name)
		}
	}
	if _, ok := productTargetInstance(base, ""); ok {
		t.Fatal("세대 없이 인스턴스를 안다고 봄")
	}
}

// 모르는 상태면 실행마다 새 난수 참조를 발급하고(같은 참조가 두 번 나오지 않음), 그 참조는 대조에서 늘 바뀐 것으로 본다.
func TestProductTargetRefsFreshWhenUnknown(t *testing.T) {
	r, _, target, _ := declarationTestTarget(t)
	known, err := r.issueTargetRefs(target.ID, rand.Reader)
	if err != nil || !known.GenerationKnown || !known.InstanceKnown {
		t.Fatalf("%+v %v", known, err)
	}
	again, _ := r.issueTargetRefs(target.ID, rand.Reader)
	if again != known {
		t.Fatal("같은 대상 상태에서 다른 참조")
	}
	r.mu.Lock()
	rec := r.records[target.ID]
	rec.SelectedIP = ""
	r.records[target.ID] = rec
	r.mu.Unlock()
	a, err := r.issueTargetRefs(target.ID, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.issueTargetRefs(target.ID, rand.Reader)
	if a.InstanceKnown || !strings.HasPrefix(a.InstanceRef, productFreshInstancePrefix) || a.InstanceRef == b.InstanceRef ||
		a.GenerationRef != known.GenerationRef || !a.GenerationKnown {
		t.Fatalf("모르는 인스턴스: %+v %+v", a, b)
	}
	binding := e3contract.RunBinding{GenerationRef: a.GenerationRef, InstanceRef: a.InstanceRef}
	if !r.targetRefsChanged(target.ID, binding) {
		t.Fatal("새로 발급한 참조를 같다고 봄")
	}
	// 대상이 없으면 둘 다 모른다.
	unknown, err := r.issueTargetRefs("missing-target", rand.Reader)
	if err != nil || unknown.GenerationKnown || unknown.InstanceKnown || !strings.HasPrefix(unknown.GenerationRef, productFreshGenerationPrefix) {
		t.Fatalf("없는 대상: %+v %v", unknown, err)
	}
	if !r.targetRefsChanged("missing-target", e3contract.RunBinding{GenerationRef: unknown.GenerationRef, InstanceRef: unknown.InstanceRef}) {
		t.Fatal("없는 대상을 같다고 봄")
	}
	// 난수를 못 읽으면 발급 실패다(지어내지 않음).
	if _, err := r.issueTargetRefs("missing-target", bytes.NewReader(nil)); !errors.Is(err, errTargetRefs) {
		t.Fatalf("난수 실패: %v", err)
	}
}

// 동결 파일은 우리가 발급한 모양만 읽는다. 깨졌으면 확정 단계 결속이 비어 꺼진다.
func TestReadTargetRefsStrict(t *testing.T) {
	good := productTargetRefs{Version: productTargetRefsVersion, GenerationRef: "gen-" + strings.Repeat("a", 32),
		InstanceRef: "inst-x-" + strings.Repeat("b", 32), GenerationKnown: true, InstanceKnown: false}
	write := func(t *testing.T, raw []byte) string {
		dir := t.TempDir()
		if os.Chmod(dir, 0700) != nil || os.Mkdir(filepath.Join(dir, "job"), 0700) != nil ||
			os.WriteFile(filepath.Join(dir, "job", productTargetRefsFile), raw, 0600) != nil {
			t.Fatal("fixture")
		}
		return dir
	}
	raw, _ := good.marshal()
	if got, ok := readTargetRefs(write(t, raw), "job"); !ok || got != good {
		t.Fatalf("정상 파일: %+v %v", got, ok)
	}
	bad := map[string]func(*productTargetRefs){
		"version":          func(r *productTargetRefs) { r.Version = "e3-target-refs/v0" },
		"generation_shape": func(r *productTargetRefs) { r.GenerationRef = "generation-job" },
		"instance_shape":   func(r *productTargetRefs) { r.InstanceRef = "inst-" + strings.Repeat("Z", 32) },
		"known_but_fresh":  func(r *productTargetRefs) { r.InstanceKnown = true },
		"fresh_but_known":  func(r *productTargetRefs) { r.GenerationKnown = false },
	}
	for name, edit := range bad {
		refs := good
		edit(&refs)
		raw, _ := refs.marshal()
		if _, ok := readTargetRefs(write(t, raw), "job"); ok {
			t.Fatalf("%s: 깨진 파일을 읽음", name)
		}
	}
	for name, raw := range map[string][]byte{"trailing": append(raw, []byte(` {}`)...), "unknown_field": []byte(`{"version":"e3-target-refs/v1","extra":1}`)} {
		if _, ok := readTargetRefs(write(t, raw), "job"); ok {
			t.Fatalf("%s: 깨진 파일을 읽음", name)
		}
	}
	if _, ok := readTargetRefs(t.TempDir(), "job"); ok {
		t.Fatal("없는 파일을 읽음")
	}
}
