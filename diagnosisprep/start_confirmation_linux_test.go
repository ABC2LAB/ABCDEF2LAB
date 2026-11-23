package diagnosisprep

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 시작 확인 진술이 없는 입력은 이 칸이 생기기 전과 같은 바이트로 기록돼야 한다. 바뀌면 옛 초안의 원본 요약·저널 대조가
// 어긋난다(CLI·평가 러너는 이 칸을 보내지 않는다).
func TestStartConfirmationAbsentKeepsPreviousEncoding(t *testing.T) {
	input := Input{WorkflowMode: "ai_auto_v1", ExcludedPaths: "/logout", TargetURL: "https://example.test/read", ProfileID: "local", DisplayName: "name", ExternalTestAccounts: true}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	const previous = `{"workflow_mode":"ai_auto_v1","excluded_paths":"/logout","target_url":"https://example.test/read","profile_id":"local","display_name":"name","external_test_accounts":true}`
	if string(raw) != previous {
		t.Fatalf("encoding changed: %s", raw)
	}
	// 원본 요약도 같아야 한다. 구조체를 거치지 않고 이 칸이 생기기 전의 기록 문법을 글자 그대로 적어 대조한다.
	installation := strings.Repeat("a", 64)
	sum := sha256.Sum256([]byte(`{"Input":{"target_url":"https://example.test/read","profile_id":"local"},"Installation":"` + installation + `"}`))
	legacy := SourceDigest(Input{TargetURL: "https://example.test/read", ProfileID: "local"}, installation)
	if legacy != hex.EncodeToString(sum[:]) {
		t.Fatal("source digest of an input without statements changed")
	}
	// 진술은 초안 입력의 일부라 원본 요약에 결속된다(기록 뒤에 바꿀 수 없다).
	for _, stated := range []Input{
		{TargetURL: "https://example.test/read", ProfileID: "local", ExclusionsReviewed: true},
		{TargetURL: "https://example.test/read", ProfileID: "local", BackupConfirmed: true},
	} {
		if SourceDigest(stated, installation) == legacy {
			t.Fatalf("statement is not bound to the source digest: %#v", stated)
		}
	}
}

// 두 진술은 초안 입력과 함께 저널에 남고(주체와 같은 기록), 재시작 뒤에도 그대로 읽혀야 한다.
func TestStartConfirmationIsJournaledWithDraftInput(t *testing.T) {
	s, _, root := newFixture(t)
	r := request(Create)
	r.IdempotencyKey = "create"
	r.Input = &Input{TargetURL: "https://example.test/read", ProfileID: "local", ExclusionsReviewed: true, BackupConfirmed: true}
	res := call(t, s, r)
	if res.Error != "" || !res.Job.Input.ExclusionsReviewed || !res.Job.Input.BackupConfirmed {
		t.Fatalf("created job lost statements: %#v", res)
	}
	file, err := os.Open(filepath.Join(root, journalName))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	found := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, maxRecordBytes)
	for scanner.Scan() {
		var record journalRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		var stored receipt
		if err := json.Unmarshal(record.Payload, &stored); err != nil || stored.Job.ID != res.Job.ID {
			continue
		}
		found++
		if stored.Principal != "owner" || !stored.Job.Input.ExclusionsReviewed || !stored.Job.Input.BackupConfirmed {
			t.Fatalf("journal record: %s", record.Payload)
		}
		if !strings.Contains(string(record.Payload), `"exclusions_reviewed":true`) || !strings.Contains(string(record.Payload), `"backup_confirmed":true`) {
			t.Fatalf("journal bytes: %s", record.Payload)
		}
	}
	if scanner.Err() != nil || found == 0 {
		t.Fatalf("journal records %d err %v", found, scanner.Err())
	}
	crash(t, s)
	recovered, err := New(context.Background(), root, &fakeBackend{inspection: fixtureInspection()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recovered.Shutdown(context.Background()) })
	if got := status(t, recovered, res.Job.ID).Input; !got.ExclusionsReviewed || !got.BackupConfirmed {
		t.Fatalf("recovered input: %#v", got)
	}
}

// 진술이 없는 초안도 받는다. 서버가 이 칸으로 시작을 막지 않는다는 계약이다.
func TestStartConfirmationAbsentIsAccepted(t *testing.T) {
	s, _, root := newFixture(t)
	j := create(t, s, "create")
	if j.Input.ExclusionsReviewed || j.Input.BackupConfirmed {
		t.Fatal("statements invented")
	}
	raw, err := os.ReadFile(filepath.Join(root, journalName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "exclusions_reviewed") || strings.Contains(string(raw), "backup_confirmed") {
		t.Fatal("absent statements were written")
	}
}
