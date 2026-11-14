//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProductInstallCapacityBoundariesBeforeSideEffects(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("installer requires non-root owner")
	}
	for _, tc := range []struct {
		name        string
		hours, runs int64
		want        int
	}{
		{"existing", 24, 3, 1}, {"upper", 8760, 100000, 1},
		{"hours_over", 8761, 3, 2}, {"runs_over", 24, 100001, 2},
		{"hours_zero", 0, 3, 2}, {"runs_zero", 24, 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if os.Chmod(root, 0700) != nil {
				t.Fatal("fixture")
			}
			input := map[string]any{"format": "abc2lab.product-install/v2", "database_host": "192.0.2.10", "database_name": "owned", "admin_password": "synthetic-only-install-password", "valid_for_hours": tc.hours, "max_runs": tc.runs, "local_model": map[string]any{"model": "owned-synthetic", "base_url": "http://192.0.2.30:8080/v1", "addresses": []string{"192.0.2.30"}, "context_window_k": 128, "max_tokens": 2048}}
			raw, _ := json.Marshal(input)
			path := filepath.Join(root, "input.json")
			if os.WriteFile(path, raw, 0600) != nil {
				t.Fatal("fixture")
			}
			var out, errs bytes.Buffer
			// 정상 입력은 설치 디렉터리가 없어 1로 끝난다. DB·모델에는 접촉하지 않는다.
			if got := runProductInstall(t.Context(), []string{"-input", path, "-root", root}, &out, &errs); got != tc.want {
				t.Fatalf("입력 경계: got %d want %d", got, tc.want)
			}
			if _, err := os.Stat(filepath.Join(root, "config", ".installing")); !os.IsNotExist(err) {
				t.Fatal("입력 시험이 설치 부작용을 만듦")
			}
		})
	}
}

func TestProductProfileDelegatedRunCapacity(t *testing.T) {
	f := newURLNegativeFixture(t)
	for _, runs := range []int64{1, 1000, 100000, 100001, 0} {
		f.config.Profiles[0].MaxRuns = runs
		f.write()
		loaded, err := f.load()
		if runs > 0 && runs <= 100000 {
			if err != nil || loaded == nil || loaded.inspection.MaxRuns != runs {
				t.Fatal("선택한 위임 횟수를 읽지 못함")
			}
			loaded.clearMaterials()
		} else if err == nil {
			if loaded != nil {
				loaded.clearMaterials()
			}
			t.Fatal("위임 코드 상한 밖 입력 수용")
		}
	}
	f.assertNoConnections()
}

func TestProductActiveTargetPolicyCapacity(t *testing.T) {
	p := withTargetPolicyDefaults(productTargetPolicy{})
	if p.MaxTargets != 32 {
		t.Fatal("기본 대상 용량 변경")
	}
	for _, n := range []int{2, 32, 64, 1024, 1025, 1, 0} {
		p.MaxTargets = n
		if validProductTargetPolicy(p) != (n >= 2 && n <= 1024) {
			t.Fatalf("대상 상한 경계: %d", n)
		}
	}
}

func TestProductTargetCapacityReusesOnlyInactiveRecords(t *testing.T) {
	for _, cause := range []string{"revoked", "approval_expired", "pending_expired"} {
		t.Run(cause, func(t *testing.T) {
			r, root := targetTestRegistry(t)
			r.policy.MaxTargets = 2
			now := time.Now().UTC()
			r.now = func() time.Time { return now }
			txt := map[string][]string{}
			r.lookupTXT = func(_ context.Context, n string) ([]string, error) { return txt[n], nil }
			r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("10.200.1.10")}, nil
			}
			r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
				return strings.Repeat("a", 64), now.Add(24 * time.Hour), nil
			}
			spec := targetTestSpec("a.example.test")
			spec.ValidHours = 1
			a, err := r.register("ARTEX", spec)
			if err != nil {
				t.Fatal(err)
			}
			if cause != "pending_expired" {
				txt[a.ChallengeName] = []string{a.ChallengeValue}
				a, err = r.approve(t.Context(), "ARTEX", a.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = r.registerAccount("ARTEX", a.ID, "tester-a"); err != nil {
					t.Fatal(err)
				}
				if _, err = r.markTestAccount("ARTEX", a.ID, "tester-a", true); err != nil {
					t.Fatal(err)
				}
			}
			b, err := r.register("ARTEX", targetTestSpec("b.example.test"))
			if err != nil {
				t.Fatal(err)
			}
			txt[b.ChallengeName] = []string{b.ChallengeValue}
			if _, err = r.approve(t.Context(), "ARTEX", b.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = r.register("ARTEX", targetTestSpec("full.example.test")); err == nil || err.Error() != "target_capacity" {
				t.Fatal("활성 두 자리 제한 우회")
			}
			switch cause {
			case "revoked":
				if _, err = r.revoke("ARTEX", a.ID); err != nil {
					t.Fatal(err)
				}
			case "approval_expired":
				now = a.ExpiresAt
			case "pending_expired":
				now = r.records[a.ID].ProofExpiresAt
			}
			old := productTargetCopy(r.records[a.ID])
			replacement, err := r.register("ARTEX", targetTestSpec("a.example.test"))
			if err != nil {
				t.Fatal("비활성 자리 재사용 실패", err)
			}
			if replacement.ID == a.ID || replacement.State != "pending" || replacement.ChallengeValue == a.ChallengeValue || len(r.accounts[replacement.ID]) != 0 {
				t.Fatal("과거 권한·계정·소유 증명 재사용")
			}
			if len(r.records) != 3 || !reflect.DeepEqual(r.records[a.ID], old) {
				t.Fatal("비활성 이력 변경·삭제")
			}
			if _, err = r.lookup("ARTEX", "https://a.example.test/read", ""); err == nil {
				t.Fatal("새 미승인 등록에 실행 권위 생성")
			}
			if _, err = r.register("ARTEX", targetTestSpec("full.example.test")); err == nil || err.Error() != "target_capacity" {
				t.Fatal("재사용 후 활성 상한 우회")
			}
			if cause != "pending_expired" {
				if historical, err := r.lookup("ARTEX", "https://a.example.test/read", a.ID); err != nil || historical.Target.ID != a.ID {
					t.Fatal("과거 보고서 승인 분실")
				}
			}
			if r.Close() != nil {
				t.Fatal("종료")
			}
			reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reopened.now = func() time.Time { return now }
			if len(reopened.records) != 3 || !reflect.DeepEqual(reopened.records[a.ID], old) {
				t.Fatal("재시작 시 감사 이력 손실")
			}
			if _, err = reopened.register("ARTEX", targetTestSpec("full.example.test")); err == nil || err.Error() != "target_capacity" {
				t.Fatal("재시작 후 활성 상한 손실")
			}
			// 기한 만료 pending은 TXT를 지금 맞춰도 승인할 수 없다.
			if cause == "pending_expired" {
				reopened.lookupTXT = r.lookupTXT
				txt[a.ChallengeName] = []string{a.ChallengeValue}
				if _, err = reopened.approve(t.Context(), "ARTEX", a.ID); err == nil {
					t.Fatal("만료된 소유 증명 복원")
				}
			}
		})
	}
}
