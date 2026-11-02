package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

func TestControlledReadStoreDirectoriesAndMarkerSurviveRestart(t *testing.T) {
	root := t.TempDir()
	snapshots := filepath.Join(root, "snapshots")
	state := filepath.Join(root, "state")
	s, err := openControlledReadStore(snapshots, state)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{snapshots, state, filepath.Join(state, "journals")} {
		st, e := os.Stat(p)
		if e != nil || st.Mode().Perm() != 0o700 {
			t.Fatalf("private dir %s: %v %v", p, st, e)
		}
	}
	if err := s.mark("17", 22); err != nil {
		t.Fatal(err)
	}
	s, err = openControlledReadStore(snapshots, state)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.marked("17", 22); err != nil || !ok {
		t.Fatalf("marker=%v err=%v", ok, err)
	}
	if ok, err := s.marked("17", 23); err != nil || !ok {
		t.Fatalf("task marker did not cover unregistered intent: %v %v", ok, err)
	}
	if ok, err := s.taskMarked("17"); err != nil || !ok {
		t.Fatalf("task marker=%v err=%v", ok, err)
	}
	if err := s.mark("17", 23); err != nil {
		t.Fatal(err)
	}
	if _, err := s.marked("../17", 22); err == nil {
		t.Fatal("accepted traversal")
	}
}

func TestControlledReadStoreRejectsDuplicateAndSymlinkSnapshot(t *testing.T) {
	root := t.TempDir()
	snapshots := filepath.Join(root, "snapshots")
	state := filepath.Join(root, "state")
	s, err := openControlledReadStore(snapshots, state)
	if err != nil {
		t.Fatal(err)
	}
	dup := []byte(`{"task_id":"1","task_id":"1"}`)
	if err := os.WriteFile(filepath.Join(snapshots, "dup.json"), dup, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.load("1", 1, "dup"); err == nil {
		t.Fatal("duplicate JSON accepted")
	}
	valid := filepath.Join(snapshots, "valid.json")
	if err := os.WriteFile(valid, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(snapshots, "link.json")
	if err := os.Symlink(valid, link); err != nil {
		t.Fatal(err)
	}
	if _, err := s.load("1", 1, "link"); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestControlledReadStoreLoadKeepsKeyOutOfJSON(t *testing.T) {
	root := t.TempDir()
	snapshots := filepath.Join(root, "snapshots")
	state := filepath.Join(root, "state")
	s, err := openControlledReadStore(snapshots, state)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	if err := os.WriteFile(filepath.Join(snapshots, "key.bin"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	in := snapshotJSON{TaskID: "1", IntentID: 2, CaseRef: "case", ResetRef: "reset", PolicyRef: "policy", Origin: "http://127.0.0.1", ConnectIP: "127.0.0.1", ExpiresAt: time.Now().Add(time.Hour), MaxRequests: 1, RequestTimeoutMS: 1000, MaxBodyBytes: 1024, KeyRef: "key-ref", KeyFile: "key.bin", Sessions: []sessionJSON{{Label: "owner", Handle: "session", Status: "verified", ExpiresAt: time.Now().Add(time.Hour), HeaderFiles: map[string]string{"X-Test": "header.txt"}}}, Resources: []resourceJSON{{Ref: "item:1", URL: "http://127.0.0.1/item/1", ApprovalRef: "approval", ReadOnly: true, Accounts: []string{"owner"}}}}
	if err := os.WriteFile(filepath.Join(snapshots, "header.txt"), []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(in)
	if err := os.WriteFile(filepath.Join(snapshots, "profile.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.load("1", 2, "profile")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Key) == "" || len(got.Key) != 32 || string(got.Key) == string(b) {
		t.Fatal("key serialized or not loaded")
	}
	if string(b) == "" {
		t.Fatal("unreachable")
	}
}

func TestControlledReadStoreCheckpointIsDurable(t *testing.T) {
	root := t.TempDir()
	s, err := openControlledReadStore(filepath.Join(root, "snap"), filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	cp := auditlog.Checkpoint{Version: 1, JournalID: "0123456789abcdef0123456789abcdef", Scope: transmit.Scope{RunRef: "run", TaskRef: "1", CaseRef: "case", ResetRef: "reset", PolicyRef: "policy"}, KeyRef: "key", Sequence: 3, MAC: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	if err := s.saveCheckpoint(context.Background(), cp); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.journalDirectory(), cp.JournalID+".checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	var got auditlog.Checkpoint
	if err := json.Unmarshal(b, &got); err != nil || got != cp {
		t.Fatalf("checkpoint=%+v err=%v", got, err)
	}
}
