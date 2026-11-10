package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/report"
	"github.com/Autumn-27/artex/reportcapture"
	"github.com/jackc/pgx/v5/pgconn"
)

type reportFixtureFrame struct{ png []byte }

func (f reportFixtureFrame) CurrentFrame(context.Context) (reportcapture.Frame, error) {
	return reportcapture.Frame{PNG: f.png, CapturedAt: time.Now()}, nil
}
func wordParts(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	z, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil {
		t.Fatal(e)
	}
	out := map[string][]byte{}
	for _, f := range z.File {
		r, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		out[f.Name], e = io.ReadAll(r)
		r.Close()
		if e != nil {
			t.Fatal(e)
		}
	}
	return out
}

func diagnosticWordText(t *testing.T, data []byte) string {
	t.Helper()
	var out strings.Builder
	d := xml.NewDecoder(bytes.NewReader(data))
	inText := false
	for {
		token, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		switch n := token.(type) {
		case xml.StartElement:
			if n.Name.Local == "t" {
				inText = true
			}
			if n.Name.Local == "br" {
				out.WriteString("\n")
			}
		case xml.EndElement:
			if n.Name.Local == "t" {
				inText = false
			}
			if n.Name.Local == "p" {
				out.WriteString("\n")
			}
		case xml.CharData:
			if inText {
				out.Write([]byte(n))
			}
		}
	}
	return out.String()
}

// 합성 E③ 입력·픽셀로 PostgreSQL 결속과 출력 계약만 검사한다. 실제 브라우저 캡처 수용과 구분한다.
func TestPracticalReportStoredE3CapturePromptAndRestart(t *testing.T) {
	s, _, req := trafficEvidenceServer(t)
	f := diagnosticE3FixtureFor(t, s.m.pg, oracle.Confirmed, true)
	record, e := s.m.pg.RecordControlledFinding(t.Context(), f.prepared, f.markers)
	if e != nil {
		t.Fatal(e)
	}
	entry := fmt.Sprintf("finding-%d", record.FindingID)
	data, e := loadDiagnosticReport(t.Context(), s.m.pg, f.taskID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if len(data.Entries) != 1 || data.Entries[0].Outcome != "confirmed" || len(data.Entries[0].Steps) != 3 {
		t.Fatal("stored E3 steps missing")
	}
	steps := data.Entries[0].Steps
	if steps[0].SourceStepID != "owner-read" || steps[1].SourceStepID != "cross-read" || steps[0].CallRef != "" || steps[0].Path != "" {
		t.Fatal("invented or reordered E3 read")
	}
	source, e := db.LoadDiagnosticRemediationSource(t.Context(), s.m.pg.DB, f.taskID, entry)
	if e != nil {
		t.Fatal(e)
	}
	bundle, e := s.m.pg.SaveDiagnosticRemediation(t.Context(), f.taskID, entry, source.SourceDigest, nil)
	if e != nil {
		t.Fatal(e)
	}
	im := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			im.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var pngData bytes.Buffer
	png.Encode(&pngData, im)
	binding := reportcapture.Binding{TaskID: f.taskID, EntryID: entry, Step: steps[0]}
	policy := reportcapture.Policy{ApprovedNonsensitiveRegions: []image.Rectangle{image.Rect(0, 0, 1, 1)}}
	frame := reportFixtureFrame{pngData.Bytes()}
	if e = reportcapture.Collect(t.Context(), binding, policy, frame, s.m.pg); e != nil {
		t.Fatal(e)
	}
	if e = reportcapture.Collect(t.Context(), binding, policy, frame, s.m.pg); !errors.Is(e, db.ErrDiagnosticCapture) {
		t.Fatal("duplicate overwrote capture", e)
	}
	for _, change := range []func(*reportcapture.Binding){func(b *reportcapture.Binding) { b.TaskID++ }, func(b *reportcapture.Binding) { b.EntryID = "finding-99999999" }, func(b *reportcapture.Binding) { b.Step.RunRef = "foreign-run" }, func(b *reportcapture.Binding) { b.Step.CallRef = "foreign-call" }, func(b *reportcapture.Binding) { b.Step.EvidenceVersion++ }, func(b *reportcapture.Binding) { b.Step.EvidenceDigest = strings.Repeat("f", 64) }} {
		other := binding
		change(&other)
		if e = reportcapture.Collect(t.Context(), other, policy, frame, s.m.pg); !errors.Is(e, db.ErrDiagnosticCapture) {
			t.Fatal("foreign or stale binding saved", e)
		}
	}
	if e := s.m.pg.SaveDiagnosticCapture(t.Context(), reportcapture.Asset{}); !errors.Is(e, db.ErrDiagnosticCapture) {
		t.Fatal("unsealed capture accepted", e)
	}
	missing := reportcapture.Binding{TaskID: f.taskID, EntryID: entry, Step: steps[1]}
	if e = reportcapture.Collect(t.Context(), missing, reportcapture.Policy{}, frame, s.m.pg); e != nil {
		t.Fatal(e)
	}
	data, e = loadDiagnosticReport(t.Context(), s.m.pg, f.taskID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	capture := data.Entries[0].Steps[0].Capture
	if reportcapture.Verify(capture) != nil || data.Entries[0].Steps[1].Capture.Status != "not_supported" || data.Entries[0].RemediationBundle == nil {
		t.Fatal("capture or remediation persistence")
	}
	decoded, _ := png.Decode(bytes.NewReader(capture.PNG))
	r, _, _, _ := decoded.At(3, 3).RGBA()
	if r != 0 {
		t.Fatal("private pixels stored")
	}
	// 새 DB 연결로 같은 행을 읽어 프로세스의 메모리 사본에 의존하지 않음을 확인한다.
	reopened, e := db.Open(os.Getenv("ARTEX_PG_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	again, e := loadDiagnosticReport(t.Context(), reopened, f.taskID, time.Now())
	reopened.Close()
	if e != nil || again.Entries[0].RemediationBundle.InputDigest != bundle.Bundle.InputDigest || again.Entries[0].Steps[0].Capture.Digest != capture.Digest {
		t.Fatal("reopened report differs", e)
	}
	imageURL := fmt.Sprintf("/api/report/diagnostic/image?task=%d&audience=internal&entry=%s&step=%s&digest=%s", f.taskID, entry, steps[0].ID, capture.Digest)
	got := req("GET", imageURL, "")
	if got.Code != 200 || !bytes.Equal(got.Body.Bytes(), capture.PNG) || got.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("protected image read")
	}
	for _, bad := range []string{strings.Replace(imageURL, "audience=internal", "audience=shared", 1), strings.Replace(imageURL, "task="+strconv.FormatInt(f.taskID, 10), "task=99999999", 1), strings.Replace(imageURL, entry, "finding-99999999", 1), strings.Replace(imageURL, capture.Digest, strings.Repeat("e", 64), 1), imageURL + "&digest=" + capture.Digest, imageURL + "&bad=%zz"} {
		if req("GET", bad, "").Code == 200 {
			t.Fatal("foreign image exposed")
		}
	}
	if _, err := s.m.pg.Exec(`UPDATE diagnostic_captures_v1 SET width=5000 WHERE task_id=$1 AND step_id=$2`, f.taskID, steps[0].ID); err == nil {
		t.Fatal("capture dimension SQL guard absent")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatal("unexpected dimension guard SQLSTATE")
		}
	}
	if _, err := s.m.pg.Exec(`UPDATE diagnostic_captures_v1 SET png=set_byte(png,0,0) WHERE task_id=$1 AND step_id=$2`, f.taskID, steps[0].ID); err != nil {
		t.Fatal("tamper fixture preparation")
	}
	if req("GET", imageURL, "").Code != 422 {
		t.Fatal("image byte digest guard absent")
	}
	if _, err := s.m.pg.Exec(`UPDATE diagnostic_captures_v1 SET png=$3 WHERE task_id=$1 AND step_id=$2`, f.taskID, steps[0].ID, capture.PNG); err != nil {
		t.Fatal("tamper fixture restore")
	}
	unauth := httptest.NewRecorder()
	s.Handler().ServeHTTP(unauth, httptest.NewRequest("GET", imageURL, nil))
	if unauth.Code != 401 {
		t.Fatal("anonymous image read")
	}
	base := fmt.Sprintf("/api/report/diagnostic?task=%d&audience=internal", f.taskID)
	h := req("GET", base, "")
	w := req("GET", base+"&format=docx", "")
	if h.Code != 200 || w.Code != 200 {
		t.Fatal("exports failed", h.Code, w.Code)
	}
	parts := wordParts(t, w.Body.Bytes())
	text := diagnosticWordText(t, parts["word/document.xml"])
	if !strings.Contains(html.UnescapeString(h.Body.String()), bundle.Bundle.AIPrompt) || !strings.Contains(text, bundle.Bundle.AIPrompt) || !bytes.Equal(parts["word/media/capture-1.png"], capture.PNG) || !strings.Contains(string(parts["word/_rels/document.xml.rels"]), "media/capture-1.png") {
		t.Fatal("HTML/Word diverged from stored prompt/image")
	}
	copyURL := fmt.Sprintf("/api/report/diagnostic/remediation/prompt?task=%d&audience=internal&entry=%s&record=%d&digest=%s", f.taskID, entry, bundle.ID, bundle.Bundle.InputDigest)
	copied := req("GET", copyURL, "")
	var p struct {
		Prompt string `json:"ai_prompt"`
	}
	json.Unmarshal(copied.Body.Bytes(), &p)
	if copied.Code != 200 || p.Prompt != bundle.Bundle.AIPrompt {
		t.Fatal("stored copy mismatch")
	}
	shared := req("GET", strings.Replace(base, "internal", "shared", 1)+"&format=docx", "")
	sharedParts := wordParts(t, shared.Body.Bytes())
	if len(sharedParts) != 5 || strings.Contains(string(sharedParts["word/document.xml"]), capture.Digest) || strings.Contains(string(sharedParts["word/document.xml"]), bundle.Bundle.InputDigest) {
		t.Fatal("shared capture or prompt leaked")
	}
	for _, suffix := range []string{"&format=docx&format=html", "&format=pdf", "&bad=%zz"} {
		if req("GET", base+suffix, "").Code != 400 {
			t.Fatal("ambiguous export query")
		}
	}
	// 선택적 로컬 수용 자료에는 이 합성 fixture만 저장한다.
	if dir := os.Getenv("ARTEX_REPORT_ACCEPTANCE_DIR"); dir != "" {
		if e = os.WriteFile(filepath.Join(dir, "report.docx"), w.Body.Bytes(), 0600); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, "report.html"), h.Body.Bytes(), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.m.pg.Exec(`UPDATE diagnostic_captures_v1 SET run_ref='foreign-run' WHERE task_id=$1 AND step_id=$2`, f.taskID, steps[0].ID); e != nil {
		t.Fatal(e)
	}
	if req("GET", imageURL, "").Code == 200 {
		t.Fatal("tampered run accepted")
	}
	if _, e = s.m.pg.Exec(`UPDATE diagnostic_captures_v1 SET run_ref=$3 WHERE task_id=$1 AND step_id=$2`, f.taskID, steps[0].ID, steps[0].RunRef); e != nil {
		t.Fatal(e)
	}
	if _, e = s.m.pg.Exec(`UPDATE findings SET evidence_version=evidence_version+1 WHERE id=$1`, record.FindingID); e != nil {
		t.Fatal(e)
	}
	if req("GET", copyURL, "").Code != 409 || req("GET", imageURL, "").Code == 200 {
		t.Fatal("stale image or prompt accepted")
	}
	stale := req("GET", base+"&format=docx", "")
	if stale.Code != 200 || strings.Contains(string(wordParts(t, stale.Body.Bytes())["word/document.xml"]), bundle.Bundle.InputDigest) {
		t.Fatal("stale prompt exported")
	}
}

// 실제 E② owner/cross HTTP·저장 E③ 후 보고서까지 검증한다. 모델·신원 정책은 소유 fixture다.
func assertPracticalReportFromExecutedE3(t *testing.T, s *Server, task int64, hits *atomic.Int64) {
	t.Helper()
	before := hits.Load()
	data, e := loadDiagnosticReport(t.Context(), s.m.pg, task, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if len(data.Entries) != 1 || data.Entries[0].Outcome != "confirmed" || len(data.Entries[0].Steps) != 3 {
		t.Fatal("executed evidence not projected")
	}
	entry := data.Entries[0]
	source, e := db.LoadDiagnosticRemediationSource(t.Context(), s.m.pg.DB, task, entry.ID)
	if e != nil {
		t.Fatal(e)
	}
	saved, e := s.m.pg.SaveDiagnosticRemediation(t.Context(), task, entry.ID, source.SourceDigest, nil)
	if e != nil {
		t.Fatal(e)
	}
	token, e := signJWT(s.jwtKey)
	if e != nil {
		t.Fatal(e)
	}
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	base := fmt.Sprintf("/api/report/diagnostic?task=%d&audience=internal", task)
	h, w := get(base), get(base+"&format=docx")
	if h.Code != 200 || w.Code != 200 || !strings.Contains(html.UnescapeString(h.Body.String()), saved.Bundle.AIPrompt) || !strings.Contains(diagnosticWordText(t, wordParts(t, w.Body.Bytes())["word/document.xml"]), saved.Bundle.AIPrompt) {
		t.Fatal("executed report export mismatch")
	}
	copy := get(fmt.Sprintf("/api/report/diagnostic/remediation/prompt?task=%d&audience=internal&entry=%s&record=%d&digest=%s", task, entry.ID, saved.ID, saved.Bundle.InputDigest))
	if copy.Code != 200 {
		t.Fatal("executed prompt copy refused")
	}
	if hits.Load() != before {
		t.Fatal("report read triggered target request")
	}
}

func TestPracticalReportOverviewBindsStoredScopeAndModelRecords(t *testing.T) {
	s, f, _ := trafficEvidenceServer(t)
	finding, e := s.m.pg.GetFinding(f.FindingID)
	if e != nil {
		t.Fatal(e)
	}
	task := *finding.TaskID
	scope := diagnosisprep.Scope{Origin: "https://owned.test", PathPrefixes: []string{"/read"}, Methods: []string{"GET"}, ApprovalMode: "user_approval_v1", Model: &diagnosisprep.ModelInfo{Model: "owned-model", Provider: "local"}}
	ctx := WithDiagnosticReportScope(t.Context(), strconv.FormatInt(task, 10), scope)
	scope.PathPrefixes[0] = "/foreign"
	scope.Model.Model = "foreign-model"
	if _, e = s.m.pg.Exec(`INSERT INTO llm_usage(task_id,model,worker) VALUES($1,'actual-model','actual-worker'),($2,'foreign-model','foreign-worker')`, strconv.FormatInt(task, 10), strconv.FormatInt(task+99999, 10)); e != nil {
		t.Fatal(e)
	}
	data, e := loadDiagnosticReport(ctx, s.m.pg, task, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if data.ApprovedScope == nil || data.ApprovedScope.PathPrefixes[0] != "/read" || data.ApprovedScope.Model.Model != "owned-model" || data.ModelRecordState != "recorded" || len(data.ModelRecords) != 1 || data.ModelRecords[0].Model != "actual-model" {
		t.Fatal("overview scope or actual usage mixed")
	}
	foreignCtx := WithDiagnosticReportScope(t.Context(), strconv.FormatInt(task+99999, 10), scope)
	unbound, e := loadDiagnosticReport(foreignCtx, s.m.pg, task, time.Now())
	if e != nil || unbound.ApprovedScope != nil {
		t.Fatal("foreign scope included", e)
	}
	data.Shared = true
	shared, e := report.PrepareDiagnostic(data)
	if e != nil || shared.ApprovedScope != nil || len(shared.ModelRecords) > 0 {
		t.Fatal("shared overview leak", e)
	}
}
