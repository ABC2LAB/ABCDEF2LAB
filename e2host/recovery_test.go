//go:build linux

package e2host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/e2checkpoint"
)

const testSelfID = "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"

func dockerFixture(t *testing.T, prepState string) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	mounts := []dockerMount{
		{Type: "volume", Name: "testproject_e2-host-state", Destination: "/h"},
		{Type: "volume", Name: "testproject_e2-host-ipc", Destination: "/run/e2-host"},
	}
	labels := map[string]string{"com.docker.compose.project": "testproject", "com.docker.compose.service": "e2-recovery"}
	mux := http.NewServeMux()
	mux.HandleFunc("/containers/"+testSelfID+"/json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(dockerInspect{ID: testSelfID, Mounts: mounts, Config: struct {
			Labels map[string]string `json:"Labels"`
		}{Labels: labels}, State: struct {
			Running bool `json:"Running"`
		}{Running: true}})
	})
	mux.HandleFunc("/containers/json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]dockerContainer{
			{ID: testSelfID, State: "running", Labels: labels, Mounts: mounts},
			{ID: strings.Repeat("1", 64), State: prepState,
				Labels: map[string]string{"com.docker.compose.project": "testproject", "com.docker.compose.service": "preparation"},
				Mounts: []dockerMount{{Type: "volume", Name: "testproject_e2-host-ipc", Destination: "/run/e2-host"}}},
			{ID: strings.Repeat("2", 64), State: "running",
				Labels: map[string]string{"com.docker.compose.project": "testproject", "com.docker.compose.service": "postgres"}},
		})
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return socket
}

func TestDockerTerminationRejectsLiveSender(t *testing.T) {
	if _, err := VerifyDockerTermination(t.Context(), dockerFixture(t, "running"), "testproject", testSelfID); err == nil {
		t.Fatal("live preparation was accepted")
	}
	if proof, err := VerifyDockerTermination(t.Context(), dockerFixture(t, "exited"), "testproject", testSelfID); err != nil || len(proof) != 64 {
		t.Fatal("stopped installation was rejected", err)
	}
}

func TestOperatorRecoveryPreservesOpenJournalAndNewRun(t *testing.T) {
	host, pool, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	intent, _ := hostSentEvents(scope)
	if err := session.Record(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	if err := session.Abort(); err != nil {
		t.Fatal(err)
	}
	before, err := host.InspectUnfinished(t.Context(), scope.RunRef)
	if err != nil || before.Recovered || len(before.Pending) != 1 {
		t.Fatal("pending intent inspection failed", err)
	}
	if _, err := host.RecoverUnfinished(t.Context(), RecoveryRequest{
		RunRef: scope.RunRef, ExpectedRevision: before.Head.Revision,
		ExpectedCheckpoint: before.Head.Checkpoint.MAC, Operator: "operator-1",
	}, dockerFixture(t, "running"), "testproject", testSelfID); err == nil {
		t.Fatal("live sender recovery succeeded")
	}
	socket := dockerFixture(t, "exited")
	req := RecoveryRequest{RunRef: scope.RunRef, ExpectedRevision: before.Head.Revision,
		ExpectedCheckpoint: before.Head.Checkpoint.MAC, Operator: "operator-1"}
	after, err := host.RecoverUnfinished(t.Context(), req, socket, "testproject", testSelfID)
	if err != nil || !after.Recovered {
		t.Fatal("operator recovery failed", err)
	}
	if _, err := host.RecoverUnfinished(t.Context(), req, socket, "testproject", testSelfID); err != nil {
		t.Fatal("idempotent retry failed", err)
	}
	var phase string
	if err := pool.QueryRowContext(t.Context(), "SELECT phase FROM e2_http_audit_heads WHERE run_ref=$1", scope.RunRef).Scan(&phase); err != nil ||
		phase != string(e2checkpoint.Open) {
		t.Fatal("original journal phase changed", phase, err)
	}
	restarted, err := New(pool, host.epoch, host.keyRef, host.key, host.root)
	if err != nil || restarted.CheckRestoredHeads(t.Context()) != nil {
		t.Fatal("restarted Host rejected recovery record", err)
	}
	next := scope
	next.RunRef = "run-after-operator-recovery"
	later, err := restarted.Open(t.Context(), next, "revision-host")
	if err != nil {
		t.Fatal("new run blocked", err)
	}
	if err := later.Seal(t.Context()); err != nil {
		t.Fatal("new run not sealed", err)
	}
	if got, err := restarted.InspectUnfinished(t.Context(), scope.RunRef); err != nil || !got.Recovered {
		t.Fatal("old uncertainty was lost", err)
	}
}

func TestRecoveryRecordDamageFailsClosed(t *testing.T) {
	for _, mutation := range []string{"record_deleted", "record_tampered", "head_changed", "journal_changed", "epoch_changed"} {
		t.Run(mutation, func(t *testing.T) {
			host, pool, scope := fixture(t)
			session, err := host.Open(t.Context(), scope, "revision-host")
			if err != nil {
				t.Fatal(err)
			}
			intent, _ := hostSentEvents(scope)
			if err := session.Record(t.Context(), intent); err != nil {
				t.Fatal(err)
			}
			_ = session.Abort()
			status, err := host.InspectUnfinished(t.Context(), scope.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			_, err = host.RecoverUnfinished(t.Context(), RecoveryRequest{RunRef: scope.RunRef,
				ExpectedRevision: status.Head.Revision, ExpectedCheckpoint: status.Head.Checkpoint.MAC,
				Operator: "operator-1"}, dockerFixture(t, "exited"), "testproject", testSelfID)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte(scope.RunRef))
			dir := filepath.Join(host.root, hex.EncodeToString(digest[:]))
			switch mutation {
			case "record_deleted":
				err = os.Remove(filepath.Join(dir, "operator-recovery.json"))
			case "record_tampered":
				err = os.WriteFile(filepath.Join(dir, "operator-recovery.json"), []byte("{}"), 0600)
			case "head_changed":
				_, err = pool.Exec("UPDATE e2_http_audit_heads SET revision=revision+1 WHERE run_ref=$1", scope.RunRef)
			case "journal_changed":
				err = os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("{}"), 0600)
			case "epoch_changed":
				_, err = pool.Exec("UPDATE e2_http_audit_epochs SET generation=generation+1 WHERE store_ref=$1", host.epoch.StoreRef)
			}
			if err != nil {
				t.Fatal(err)
			}
			restarted, err := New(pool, host.epoch, host.keyRef, host.key, host.root)
			if err != nil {
				t.Fatal(err)
			}
			if restarted.CheckRestoredHeads(t.Context()) == nil {
				t.Fatal("damaged recovery was accepted")
			}
			next := scope
			next.RunRef = "run-after-damage"
			if _, err := restarted.Open(t.Context(), next, "revision-host"); err == nil {
				t.Fatal("new send permitted after damage")
			}
		})
	}
}

func TestConcurrentOperatorRecoveryDoesNotDuplicate(t *testing.T) {
	host, _, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	intent, _ := hostSentEvents(scope)
	if err := session.Record(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	_ = session.Abort()
	status, err := host.InspectUnfinished(t.Context(), scope.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	req := RecoveryRequest{RunRef: scope.RunRef, ExpectedRevision: status.Head.Revision,
		ExpectedCheckpoint: status.Head.Checkpoint.MAC, Operator: "operator-1"}
	socket := dockerFixture(t, "exited")
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := host.RecoverUnfinished(context.Background(), req, socket, "testproject", testSelfID)
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal("concurrent retry failed", err)
		}
	}
}

func TestOperatorRecoveryRefusesUnknownWrite(t *testing.T) {
	host, _, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	intent, _ := hostSentEvents(scope)
	intent.Method = "POST"
	if err := session.Record(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	_ = session.Abort()
	status, err := host.InspectUnfinished(t.Context(), scope.RunRef)
	if err != nil || !status.PendingStateChange {
		t.Fatal("unknown write was not classified", err)
	}
	if _, err := host.RecoverUnfinished(t.Context(), RecoveryRequest{RunRef: scope.RunRef,
		ExpectedRevision: status.Head.Revision, ExpectedCheckpoint: status.Head.Checkpoint.MAC,
		Operator: "operator-1"}, dockerFixture(t, "exited"), "testproject", testSelfID); err == nil {
		t.Fatal("unknown write was released")
	}
	if host.CheckRestoredHeads(t.Context()) == nil {
		t.Fatal("unknown write ceased to block new runs")
	}
}
