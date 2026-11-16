//go:build linux

package exploreboundary

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/pathmask"
)

// 원자료 시험의 상한은 시험 안에서만 쓰는 값이다. 실제 상한은 회차 정책이 정한다.
const evidenceTestMaxBytes = 1 << 20

func evidenceKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, pathmask.KeyBytes)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func evidenceDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "boundary")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// evidenceManager는 원자료를 켠 경계 프로세스의 세션 소유자다. 시계는 시험 대상 만료 시각 안에 둔다.
func evidenceManager(t *testing.T, ev *Evidence, closeWait time.Duration) (*Manager, *fakeSource, *fakeClock) {
	t.Helper()
	_, source, clock := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	policy := Policy{Resolver: fakeResolver{}, PathPattern: func(p string) string { return "pattern:" + p }, MaxDeniedRecords: fixtureDenied, Now: clock.Now, Evidence: ev}
	return &Manager{Policy: policy, CloseWait: closeWait}, source, clock
}

func openVia(t *testing.T, m *Manager, source *fakeSource) ControlResponse {
	t.Helper()
	b := fixtureBinding(exploration.ReadOnly, exploration.FullLocal)
	source.mu.Lock()
	wt := WireTargetOf(source.target)
	source.mu.Unlock()
	return m.Handle(t.Context(), ControlRequest{Op: OpOpen, Binding: &b, Target: &wt, SourceDigest: fixtureDigest})
}

func readMarker(t *testing.T, dir string) EvidenceMarker {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, EvidenceMarkerName))
	if err != nil {
		t.Fatal("marker missing", err)
	}
	var m EvidenceMarker
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func readRows(t *testing.T, dir string) ([]Record, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, EvidenceRowsName))
	if err != nil {
		t.Fatal(err)
	}
	var rows []Record
	for _, line := range bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var r Record
		d := json.NewDecoder(bytes.NewReader(line))
		d.DisallowUnknownFields()
		if err := d.Decode(&r); err != nil {
			t.Fatal("row is not a boundary record", err)
		}
		rows = append(rows, r)
	}
	return rows, raw
}

// 메모리 차단 행 상한을 넘어도 원자료에는 모든 행이 남고, 표식이 결속·키 참조·hash를 행 파일과 맞게 남긴다.
func TestEvidenceKeepsEveryRowAndSealsLast(t *testing.T) {
	dir, key := evidenceDir(t), evidenceKey(t)
	ev, err := OpenEvidence(dir, evidenceTestMaxBytes, key)
	if err != nil {
		t.Fatal(err)
	}
	m, source, _ := evidenceManager(t, ev, time.Second)
	if resp := openVia(t, m, source); !resp.OK {
		t.Fatal("open", resp.Error)
	}
	s := m.Current()
	const denied = fixtureDenied + 4
	for i := 0; i < denied; i++ {
		s.Admit(t.Context(), approved("GET", "/outside/"+strings.Repeat("x", i+1)))
	}
	d, ticket := s.Admit(t.Context(), approved("GET", "/app/items"))
	if !d.Forward {
		t.Fatal(d)
	}
	ticket.Done(Result{Status: 200, ResponseReceived: true})
	memory, omitted := s.Records()
	if omitted == 0 || len(memory) != fixtureDenied+1 {
		t.Fatal("fixture must exceed the memory denied-row cap", len(memory), omitted)
	}
	if _, err := os.Stat(filepath.Join(dir, EvidenceMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("marker written before seal")
	}
	if err := m.Seal(SealedByShutdown, nil); err != nil {
		t.Fatal(err)
	}
	rows, raw := readRows(t, dir)
	if len(rows) != denied+1 {
		t.Fatal("evidence dropped rows the memory cap omitted", len(rows))
	}
	marker := readMarker(t, dir)
	sum := sha256.Sum256(raw)
	if marker.Format != EvidenceFormat || marker.PathProfile != pathmask.Profile || marker.PathKeyRef != pathmask.KeyRef(key) || marker.Rows != uint64(len(rows)) ||
		marker.Bytes != int64(len(raw)) || marker.RowsSHA256 != hex.EncodeToString(sum[:]) || !marker.LogComplete || marker.Truncated || marker.RowsNotWritten != 0 ||
		marker.InflightAtSeal != 0 || marker.SealedBy != SealedByShutdown || marker.MaxBytes != evidenceTestMaxBytes {
		t.Fatalf("marker does not describe the rows: %+v", marker)
	}
	b := fixtureBinding(exploration.ReadOnly, exploration.FullLocal)
	got := marker.Session
	if got == nil || got.SessionRef != b.SessionRef || got.RunRef != b.RunRef || got.JobID != b.JobID || got.TaskID != b.TaskID || got.TargetRef != b.TargetRef ||
		got.TargetRevision != b.TargetRevision || got.SourceDigest != fixtureDigest || got.ClosedAt == nil || got.ClosedReason != exploration.Cancelled || !got.ClosedAtSeal ||
		got.RowsAtClose == nil || *got.RowsAtClose != uint64(len(rows)) {
		t.Fatalf("marker session binding: %+v", got)
	}
	if rows[len(rows)-1].Decision != "forwarded" || rows[len(rows)-1].PathPattern != "pattern:/app/items" {
		t.Fatal("forwarded row content", rows[len(rows)-1])
	}
	info, err := os.Stat(filepath.Join(dir, EvidenceRowsName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("rows file mode", info, err)
	}
	if err := m.Seal(SealedByShutdown, nil); err == nil {
		t.Fatal("second seal accepted")
	}
	// 거부된 두 번째 봉인은 표식을 바꾸지 않고 반쯤 쓴 표식도 남기지 않는다.
	if again := readMarker(t, dir); again.SealedAt != marker.SealedAt || again.Rows != marker.Rows {
		t.Fatal("second seal changed the marker")
	}
	if _, err := os.Stat(filepath.Join(dir, EvidenceMarkerName+".partial")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("second seal left a partial marker")
	}
}

// 원자료 하나에는 세션 하나다. 결속을 두 번 남기거나 봉인 뒤에 남기지 않는다.
func TestEvidenceRecordsOneSessionOnly(t *testing.T) {
	ev, err := newEvidence(&failingEvidenceFile{syncErr: false}, func(string, []byte) error { return nil }, "key-ref", evidenceTestMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	b := fixtureFlow()
	if err := ev.opened(b, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ev.opened(b, time.Now()); err == nil {
		t.Fatal("second session recorded on the same evidence")
	}
	sealed, err := newEvidence(&failingEvidenceFile{syncErr: false}, func(string, []byte) error { return nil }, "key-ref", evidenceTestMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := sealed.seal(0, false, SealedByShutdown, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := sealed.opened(b, time.Now()); err == nil {
		t.Fatal("session recorded after seal")
	}
}

func fixtureFlow() diagnosisflow.ExplorationBinding {
	b := fixtureBinding(exploration.ReadOnly, exploration.FullLocal)
	return diagnosisflow.ExplorationBinding{SessionRef: b.SessionRef, RunRef: b.RunRef, JobID: b.JobID, TaskID: b.TaskID, TargetRef: b.TargetRef, TargetRevision: b.TargetRevision, SourceDigest: fixtureDigest}
}

// 엔진이 먼저 닫은 세션은 그 마감 사유가 남고, 봉인이 닫은 것으로 표시하지 않는다. 원자료 하나에 세션은 하나다.
func TestEvidenceSessionClosedByControlAndSingleSession(t *testing.T) {
	dir := evidenceDir(t)
	ev, err := OpenEvidence(dir, evidenceTestMaxBytes, evidenceKey(t))
	if err != nil {
		t.Fatal(err)
	}
	m, source, _ := evidenceManager(t, ev, time.Second)
	if resp := openVia(t, m, source); !resp.OK {
		t.Fatal(resp.Error)
	}
	if resp := m.Handle(t.Context(), ControlRequest{Op: OpClose, SessionRef: "session-1", Reason: exploration.Completed}); !resp.OK {
		t.Fatal(resp.Error)
	}
	if resp := openVia(t, m, source); resp.OK || resp.Error != ControlErrUnavailable {
		t.Fatal("second session opened on used evidence", resp)
	}
	if err := m.Seal(SealedByShutdown, nil); err != nil {
		t.Fatal(err)
	}
	got := readMarker(t, dir).Session
	if got == nil || got.ClosedReason != exploration.Completed || got.ClosedAtSeal || got.RowsAtClose == nil || *got.RowsAtClose != 0 {
		t.Fatalf("closed session marker: %+v", got)
	}
}

// 세션을 못 연 경계도 봉인 표식을 남긴다. 러너는 이것으로 '세션 없음'과 '경계가 죽음(표식 없음)'을 가른다.
func TestEvidenceSealWithoutSession(t *testing.T) {
	dir := evidenceDir(t)
	ev, err := OpenEvidence(dir, evidenceTestMaxBytes, evidenceKey(t))
	if err != nil {
		t.Fatal(err)
	}
	m, _, _ := evidenceManager(t, ev, time.Second)
	if err := m.Seal(SealedByFailure, nil); err != nil {
		t.Fatal(err)
	}
	marker := readMarker(t, dir)
	if marker.Session != nil || marker.Rows != 0 || marker.SealedBy != SealedByFailure || !marker.LogComplete {
		t.Fatalf("no-session marker: %+v", marker)
	}
	if resp := openVia(t, m, &fakeSource{}); resp.OK {
		t.Fatal("session opened after seal")
	}
}

// 바이트 상한에 닿으면 그 뒤 행은 쓰지 않고 수만 센다. 잘림은 송신을 멈추지 않는다(판정은 러너가 UNDECIDABLE로).
func TestEvidenceTruncatesAtByteCapWithoutStopping(t *testing.T) {
	dir := evidenceDir(t)
	probe, _ := json.Marshal(Record{SessionRef: "session-1", Seq: 1, Decision: "denied", Reason: ReasonOutOfScopePath, Ingress: BrowserIngress, Dest: DestApproved, Method: "GET", PathPattern: "pattern:/outside/x", FetchMode: "cors"})
	ev, err := OpenEvidence(dir, int64(len(probe))*2, evidenceKey(t))
	if err != nil {
		t.Fatal(err)
	}
	m, source, _ := evidenceManager(t, ev, time.Second)
	if resp := openVia(t, m, source); !resp.OK {
		t.Fatal(resp.Error)
	}
	s := m.Current()
	for i := 0; i < 6; i++ {
		s.Admit(t.Context(), approved("GET", "/outside/x"))
	}
	if !s.Status().Active {
		t.Fatal("truncation stopped the session")
	}
	if d, ticket := s.Admit(t.Context(), approved("GET", "/app/items")); !d.Forward {
		t.Fatal("truncation blocked an approved request", d)
	} else {
		ticket.Done(Result{Status: 200, ResponseReceived: true})
	}
	if err := m.Seal(SealedByShutdown, nil); err != nil {
		t.Fatal(err)
	}
	rows, raw := readRows(t, dir)
	marker := readMarker(t, dir)
	if !marker.Truncated || marker.RowsNotWritten == 0 || marker.Rows != uint64(len(rows)) || marker.Bytes > marker.MaxBytes || int64(len(raw)) != marker.Bytes ||
		marker.Rows+marker.RowsNotWritten != 7 || !marker.LogComplete {
		t.Fatalf("truncated marker: %+v rows=%d", marker, len(rows))
	}
}

type failingEvidenceFile struct{ syncErr bool }

func (f *failingEvidenceFile) Write(p []byte) (int, error) {
	if f.syncErr {
		return len(p), nil
	}
	return 0, errors.New("disk full")
}
func (f *failingEvidenceFile) Sync() error {
	if f.syncErr {
		return errors.New("sync failed")
	}
	return nil
}
func (f *failingEvidenceFile) Close() error { return nil }

// 행을 디스크에 쓰지 못하면 원자료가 불완전해졌으므로 세션은 새 송신을 멈추고, 표식은 log_complete=false다.
func TestEvidenceWriteFailureStopsNewSends(t *testing.T) {
	var published []byte
	ev, err := newEvidence(&failingEvidenceFile{}, func(_ string, body []byte) error { published = body; return nil }, "key-ref", evidenceTestMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	m, source, _ := evidenceManager(t, ev, time.Second)
	if resp := openVia(t, m, source); !resp.OK {
		t.Fatal(resp.Error)
	}
	s := m.Current()
	// 버퍼가 차서 실제 쓰기가 일어날 때까지 차단 행을 만든다. 상한은 버퍼보다 넉넉한 행 수다.
	for i := 0; i < evidenceBufferBytes && s.Status().Active; i++ {
		s.Admit(t.Context(), approved("GET", "/outside/"+strings.Repeat("y", 200)))
	}
	st := s.Status()
	if st.Active || st.ClosedReason != exploration.BoundaryUnavailable {
		t.Fatal("write failure did not stop the session", st)
	}
	if d, _ := s.Admit(t.Context(), approved("GET", "/app/items")); d.Forward {
		t.Fatal("request forwarded after evidence loss")
	}
	if err := m.Seal(SealedByShutdown, nil); err != nil {
		t.Fatal(err)
	}
	var marker EvidenceMarker
	if err := json.Unmarshal(published, &marker); err != nil || marker.LogComplete {
		t.Fatalf("marker after write failure: %+v %v", marker, err)
	}
}

// 마감·봉인 때 fsync가 실패해도 표식에 불완전으로 남는다.
func TestEvidenceSyncFailureMarksIncomplete(t *testing.T) {
	var published []byte
	ev, err := newEvidence(&failingEvidenceFile{syncErr: true}, func(_ string, body []byte) error { published = body; return nil }, "key-ref", evidenceTestMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	m, _, _ := evidenceManager(t, ev, time.Second)
	if err := m.Seal(SealedByShutdown, nil); err != nil {
		t.Fatal(err)
	}
	var marker EvidenceMarker
	if err := json.Unmarshal(published, &marker); err != nil || marker.LogComplete {
		t.Fatalf("marker after sync failure: %+v %v", marker, err)
	}
}

// 봉인 때 끝나지 않은 요청은 그 수를 표식에 남기고, 봉인 뒤에 끝나도 원자료에 쓰지 않는다(표식이 마지막이다).
func TestEvidenceInflightAtSealIsCountedNotAppended(t *testing.T) {
	dir := evidenceDir(t)
	ev, err := OpenEvidence(dir, evidenceTestMaxBytes, evidenceKey(t))
	if err != nil {
		t.Fatal(err)
	}
	m, source, _ := evidenceManager(t, ev, 10*time.Millisecond)
	if resp := openVia(t, m, source); !resp.OK {
		t.Fatal(resp.Error)
	}
	d, ticket := m.Current().Admit(t.Context(), approved("GET", "/app/items"))
	if !d.Forward {
		t.Fatal(d)
	}
	if err := m.Seal(SealedByShutdown, nil); err != nil {
		t.Fatal(err)
	}
	ticket.Done(Result{Status: 200, ResponseReceived: true})
	rows, _ := readRows(t, dir)
	marker := readMarker(t, dir)
	if marker.InflightAtSeal != 1 || len(rows) != 0 || marker.Rows != 0 {
		t.Fatalf("late row after seal: inflight=%d rows=%d", marker.InflightAtSeal, len(rows))
	}
}

// 원자료를 켜지 않은 경계(제품)는 봉인이 아무것도 쓰지 않는다.
func TestNoEvidenceSealIsNoop(t *testing.T) {
	m, _, _ := evidenceManager(t, nil, time.Second)
	if err := m.Seal(SealedByShutdown, nil); err != nil {
		t.Fatal(err)
	}
}

// 근거 디렉터리는 러너가 만든 정확한 0700 디렉터리여야 하고, 이전 회차 파일이 있으면 시작하지 않는다.
func TestOpenEvidenceRejectsUnsafeDirectory(t *testing.T) {
	key := evidenceKey(t)
	for name, prepare := range map[string]func(t *testing.T) string{
		"group_readable": func(t *testing.T) string {
			dir := evidenceDir(t)
			if err := os.Chmod(dir, 0o750); err != nil {
				t.Fatal(err)
			}
			return dir
		},
		"symlink": func(t *testing.T) string {
			dir := evidenceDir(t)
			link := filepath.Join(filepath.Dir(dir), "link")
			if err := os.Symlink(dir, link); err != nil {
				t.Fatal(err)
			}
			return link
		},
		"leftover_rows": func(t *testing.T) string {
			dir := evidenceDir(t)
			if err := os.WriteFile(filepath.Join(dir, EvidenceRowsName), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return dir
		},
		"leftover_marker": func(t *testing.T) string {
			dir := evidenceDir(t)
			if err := os.WriteFile(filepath.Join(dir, EvidenceMarkerName), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return dir
		},
		"leftover_partial": func(t *testing.T) string {
			dir := evidenceDir(t)
			if err := os.WriteFile(filepath.Join(dir, EvidenceMarkerName+".partial"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return dir
		},
		"not_directory": func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		},
	} {
		if _, err := OpenEvidence(prepare(t), evidenceTestMaxBytes, key); err == nil {
			t.Fatal("unsafe evidence directory accepted:", name)
		}
	}
	if _, err := OpenEvidence(evidenceDir(t), 0, key); err == nil {
		t.Fatal("zero byte cap accepted")
	}
	if _, err := OpenEvidence(evidenceDir(t), evidenceTestMaxBytes, key[:8]); err == nil {
		t.Fatal("short path key accepted")
	}
}
