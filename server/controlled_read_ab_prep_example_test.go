package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Autumn-27/artex/kgfixture"
	"github.com/Autumn-27/artex/transmit"
)

// 운영자가 따라 쓰는 준비 문서의 예시가 실제 로더와 KG 대조를 통과해야 한다.
// 예시가 코드와 어긋나면 그대로 만든 파일이 등록 때 거부된다.
const (
	abPrepSnapshotExample = "../docs/contracts/ab-session-prep-stage1.snapshot.example.json"
	abPrepRegistryExample = "../docs/contracts/kg-registry-stage1.skeleton.json"
	abPrepSnapshotRef     = "ab-prep-example"
	abPrepRegistryFile    = "ab-prep-registry.json"
	abPrepSecretA         = "synthetic-session-a"
	abPrepSecretB         = "synthetic-session-b"
)

type abPrepRecorder struct{}

func (abPrepRecorder) Record(context.Context, transmit.Event) error { return nil }

func abPrepStore(t *testing.T, snapshot []byte) *controlledReadStore {
	t.Helper()
	dir := t.TempDir()
	store, err := openControlledReadStore(filepath.Join(dir, "snapshots"), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := os.ReadFile(abPrepRegistryExample)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		abPrepSnapshotRef + ".json": snapshot,
		abPrepRegistryFile:          registry,
		"audit.key":                 bytes.Repeat([]byte{0x71}, 32),
		"account-a-session.secret":  []byte(abPrepSecretA + "\n"),
		"account-b-session.secret":  []byte(abPrepSecretB),
	} {
		if err := os.WriteFile(filepath.Join(store.snapshotDir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store.fixtureRegistries = map[string]string{abPrepSnapshotRef: abPrepRegistryFile}
	return store
}

func abPrepExample(t *testing.T) snapshotJSON {
	t.Helper()
	body, err := os.ReadFile(abPrepSnapshotExample)
	if err != nil {
		t.Fatal(err)
	}
	var in snapshotJSON
	if err := strictJSON(body, &in); err != nil {
		t.Fatal(err)
	}
	return in
}

// 예시의 만료 시각은 고정값이라 지나간다. 로더가 등록 시점에 만료 전인지 보므로 시각만 바꿔 넣는다.
func abPrepRegistrable(t *testing.T, in snapshotJSON) []byte {
	t.Helper()
	expires := time.Now().Add(time.Hour).UTC()
	in.ExpiresAt = expires
	for i := range in.Sessions {
		in.Sessions[i].ExpiresAt = expires
	}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestABPrepExampleLoadsAndPairs(t *testing.T) {
	in := abPrepExample(t)
	body, err := os.ReadFile(abPrepSnapshotExample)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := abPrepStore(t, abPrepRegistrable(t, in)).load(in.TaskID, in.IntentID, abPrepSnapshotRef)
	if err != nil {
		t.Fatalf("예시가 로더를 통과하지 못함: %v", err)
	}
	if spec.fixture == nil {
		t.Fatal("registry가 등록에 동결되지 않음")
	}
	if len(spec.HTTP.Sessions) != 2 {
		t.Fatalf("세션 %d개, A·B 둘이어야 함", len(spec.HTTP.Sessions))
	}
	want := map[string]string{"account-a": abPrepSecretA, "account-b": abPrepSecretB}
	for _, session := range spec.HTTP.Sessions {
		if got := session.Headers.Get("Authorization"); got != want[session.Account.Label] {
			t.Fatalf("%s 세션 헤더가 보호 파일 값과 다름", session.Account.Label)
		}
	}
	if bytes.Contains(body, []byte(abPrepSecretA)) || bytes.Contains(body, []byte("Bearer")) {
		t.Fatal("snapshot JSON에 세션 원문이 들어 있음")
	}
	// 로더가 보지 않는 자원·계정·view 규칙은 전송기가 만들어질 때 검사한다.
	cfg := spec.HTTP
	cfg.Recorder = abPrepRecorder{}
	cfg.Scope.RunRef = "run-example"
	tx, err := transmit.NewHTTP(t.Context(), cfg)
	if err != nil {
		t.Fatalf("예시가 전송기 검사를 통과하지 못함: %v", err)
	}
	t.Cleanup(tx.StopAndWait)
}

func TestABPrepExamplePairingIsEnforced(t *testing.T) {
	cases := []struct {
		name   string
		change func(*snapshotJSON)
		code   kgfixture.Code
	}{
		{"label", func(in *snapshotJSON) { in.Sessions[1].Label = "account-c" }, kgfixture.CodePairingLabels},
		{"reset", func(in *snapshotJSON) { in.ResetRef = "reset-gen-other" }, kgfixture.CodePairingReset},
		{"resource", func(in *snapshotJSON) { in.Resources[0].Ref = "basket:other" }, kgfixture.CodePairingResources},
		{"marker", func(in *snapshotJSON) { in.Resources[0].View.Markers[0].Value = "other" }, kgfixture.CodePairingMarkers},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := abPrepExample(t)
			c.change(&in)
			_, err := abPrepStore(t, abPrepRegistrable(t, in)).load(in.TaskID, in.IntentID, abPrepSnapshotRef)
			var fixtureErr *kgfixture.Error
			if !errors.As(err, &fixtureErr) || fixtureErr.Code != c.code {
				t.Fatalf("대조 실패 코드 = %v, 기대 %s", err, c.code)
			}
		})
	}
}
