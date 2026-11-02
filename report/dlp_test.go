package report

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/dlp"
)

func TestExportRedactsFreeText(t *testing.T) {
	const secret = "private-export-canary-7e83"
	f := sampleDBFinding()
	f.Name, f.VulnClass, f.Summary = secret, secret, secret
	f.Evidence, f.Report, f.TaskDescription = secret, secret, secret
	f.TrafficBindings[0].Note = secret
	f.TrafficBindings[0].Snapshot.URL = "https://" + secret + "/?token=" + secret
	before, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{
		"markdown": FindingsMarkdown([]*db.DBFinding{f}, time.Time{}),
		"single":   SingleFindingMarkdown(f, time.Time{}),
		"csv":      string(FindingsCSV([]*db.DBFinding{f})),
		"filename": FindingFilename(f),
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(out, secret) {
				t.Fatal("export leaked a free-text canary")
			}
		})
	}
	after, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("export mutated source evidence")
	}
}

func TestGraphReportRedactsFreeText(t *testing.T) {
	const secret = "private-graph-canary-4b17"
	payload, err := json.Marshal(map[string]any{
		"name": secret, "vulnclass": secret, "severity": "high",
		"summary": secret, "evidence": map[string]string{"poc": secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := Markdown(Input{Title: secret, Goal: secret, AssetCounts: map[string]int{secret: 2}, Findings: []*db.Node{{Payload: payload}}})
	if strings.Contains(out, secret) {
		t.Fatal("graph report leaked a free-text canary")
	}
}

func TestProjectFindingsPreservesMetadataAndOwnsCopies(t *testing.T) {
	f := sampleDBFinding()
	task, node := int64(51), int64(61)
	f.TaskID, f.NodeID, f.AssetIDs = &task, &node, []int64{71}
	f.TrafficCount = 99 // The frozen bindings are the export count authority.
	f.ReportEvidenceVersion = 1
	f.TrafficBindings[0].Role = "proof"
	f.TrafficBindings[0].Snapshot.ReqLen, f.TrafficBindings[0].Snapshot.RespLen = 12, 34
	before, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ProjectFindings([]*db.DBFinding{f})
	if err != nil {
		t.Fatal(err)
	}
	v := out[0]
	if v.ID != 123 || *v.TaskID != task || *v.NodeID != node || v.AssetIDs[0] != 71 || v.TrafficCount != 1 ||
		v.EvidenceVersion != 2 || v.ReportEvidenceVersion != 1 || v.Severity != "high" || v.Status != "confirmed" ||
		!v.CreatedAt.Equal(f.CreatedAt) || v.TrafficBindings[0].ID != 9 || v.TrafficBindings[0].Role != "proof" ||
		v.TrafficBindings[0].Snapshot.Method != "POST" || v.TrafficBindings[0].Snapshot.Status != 200 ||
		v.TrafficBindings[0].Snapshot.ReqLen != 12 || v.TrafficBindings[0].Snapshot.RespLen != 34 {
		t.Fatal("safe metadata changed")
	}
	again, err := ProjectFindings(out)
	if err != nil || !reflect.DeepEqual(out, again) {
		t.Fatal("projection is not idempotent")
	}
	*v.TaskID, *v.NodeID, v.AssetIDs[0], v.TrafficBindings[0].ID = 1, 2, 3, 4
	after, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("export aliases source references or slices")
	}
}

func TestProjectFindingsUnknownEnums(t *testing.T) {
	for _, unknown := range []string{"", " HIGH ", "private-enum-canary", "GET\r\nAuthorization: private-enum-canary"} {
		f := sampleDBFinding()
		f.Severity, f.Status = unknown, unknown
		f.TrafficBindings[0].Role, f.TrafficBindings[0].Snapshot.Method = unknown, unknown
		out, err := ProjectFindings([]*db.DBFinding{f})
		if err != nil {
			t.Fatal(err)
		}
		v := out[0]
		if v.Severity != dlp.Redacted || v.Status != dlp.Redacted || v.TrafficBindings[0].Role != dlp.Redacted || v.TrafficBindings[0].Snapshot.Method != dlp.Redacted {
			t.Fatal("unknown enum was accepted or replaced with a known verdict")
		}
	}
}

func TestProjectFindingsBlocksInvalidMetadata(t *testing.T) {
	cases := map[string]func(*db.DBFinding){
		"finding-id":       func(f *db.DBFinding) { f.ID = -1 },
		"task-id":          func(f *db.DBFinding) { n := int64(-1); f.TaskID = &n },
		"node-id":          func(f *db.DBFinding) { n := int64(-1); f.NodeID = &n },
		"asset-id":         func(f *db.DBFinding) { f.AssetIDs = []int64{-1} },
		"version":          func(f *db.DBFinding) { f.EvidenceVersion = -1 },
		"report-version":   func(f *db.DBFinding) { f.ReportEvidenceVersion = -1 },
		"finding-time":     func(f *db.DBFinding) { f.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"binding-id":       func(f *db.DBFinding) { f.TrafficBindings[0].ID = -1 },
		"binding-owner":    func(f *db.DBFinding) { f.TrafficBindings[0].FindingID = 999 },
		"binding-position": func(f *db.DBFinding) { f.TrafficBindings[0].Position = -1 },
		"binding-time":     func(f *db.DBFinding) { f.TrafficBindings[0].CreatedAt = time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC) },
		"snapshot-time": func(f *db.DBFinding) {
			f.TrafficBindings[0].Snapshot.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		},
		"captured-time":   func(f *db.DBFinding) { f.TrafficBindings[0].Snapshot.CapturedAt = -1 },
		"request-length":  func(f *db.DBFinding) { f.TrafficBindings[0].Snapshot.ReqLen = -1 },
		"response-length": func(f *db.DBFinding) { f.TrafficBindings[0].Snapshot.RespLen = -1 },
		"status-low":      func(f *db.DBFinding) { f.TrafficBindings[0].Snapshot.Status = 99 },
		"status-high":     func(f *db.DBFinding) { f.TrafficBindings[0].Snapshot.Status = 600 },
		"bindings-limit":  func(f *db.DBFinding) { f.TrafficBindings = make([]db.FindingTrafficBinding, maxExportBindings+1) },
		"assets-limit":    func(f *db.DBFinding) { f.AssetIDs = make([]int64, maxExportAssets+1) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := sampleDBFinding()
			change(f)
			out, err := ProjectFindings([]*db.DBFinding{f})
			if !errors.Is(err, ErrExportBlocked) || out != nil {
				t.Fatal("invalid metadata was exported")
			}
			if FindingsMarkdown([]*db.DBFinding{f}, time.Time{}) != blockedReport || SingleFindingMarkdown(f, time.Time{}) != blockedReport || string(FindingsCSV([]*db.DBFinding{f})) != blockedReport {
				t.Fatal("renderer fell back after a blocked projection")
			}
		})
	}
	for _, fs := range [][]*db.DBFinding{{nil}, make([]*db.DBFinding, MaxFindings+1)} {
		if out, err := ProjectFindings(fs); out != nil || !errors.Is(err, ErrExportBlocked) {
			t.Fatal("invalid collection exported")
		}
	}
}

func TestGraphReportFailsClosed(t *testing.T) {
	for name, raw := range map[string]string{
		"syntax":           `{"summary":"private-json-canary"`,
		"duplicate":        `{"severity":"high","severity":"low"}`,
		"unknown-field":    `{"private":"private-json-canary"}`,
		"unknown-severity": `{"severity":"private-json-canary"}`,
		"type":             `{"severity":200}`,
		"size":             `{"summary":"` + strings.Repeat("x", 128<<10) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if Markdown(Input{Findings: []*db.Node{{Payload: json.RawMessage(raw)}}}) != blockedReport {
				t.Fatal("invalid graph payload did not fail closed")
			}
		})
	}
	for _, in := range []Input{
		{Findings: []*db.Node{nil}},
		{Findings: make([]*db.Node, MaxFindings+1)},
		{AssetCounts: map[string]int{"private-count": -1}},
		{AssetCounts: map[string]int{"a": maxExportCount, "b": 1}},
		{GeneratedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		if Markdown(in) != blockedReport {
			t.Fatal("invalid graph metadata exported")
		}
	}
}

func FuzzExportTextCannotBecomeMetadata(f *testing.F) {
	for _, seed := range []string{"Authorization: Bearer test", "=HYPERLINK(\"fixture\")", "```\n# injected", "\x00\xff", "한글 개인정보"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 128<<10 {
			return
		}
		secret := "fuzz-private-canary-" + text
		v := sampleDBFinding()
		v.Name, v.VulnClass, v.Summary, v.Evidence, v.Report, v.Worker, v.TaskDescription = secret, secret, secret, secret, secret, secret, secret
		b := &v.TrafficBindings[0]
		b.SnapshotID, b.Note = secret, secret
		b.Snapshot.ID, b.Snapshot.SourceTrafficID, b.Snapshot.URL, b.Snapshot.ContentType, b.Snapshot.ReqHead, b.Snapshot.RespHead, b.Snapshot.ReqHash, b.Snapshot.RespHash = secret, secret, secret, secret, secret, secret, secret, secret
		out, err := ProjectFindings([]*db.DBFinding{v})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "fuzz-private-canary-") || strings.Contains(FindingsMarkdown([]*db.DBFinding{v}, time.Time{}), "fuzz-private-canary-") || strings.Contains(string(FindingsCSV([]*db.DBFinding{v})), "fuzz-private-canary-") || strings.Contains(FindingFilename(v), "fuzz-private-canary-") {
			t.Fatal("free text leaked")
		}
		if out[0].Severity != "high" || out[0].Status != "confirmed" || out[0].TrafficBindings[0].Snapshot.Method != "POST" || out[0].TrafficBindings[0].Snapshot.Status != 200 {
			t.Fatal("safe metadata lost")
		}
	})
}
