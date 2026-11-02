package report

import (
	"errors"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/dlp"
)

var ErrExportBlocked = errors.New("report: export blocked")

const (
	// MaxFindings is shared with the reader so excess rows are rejected before
	// evidence staging, rather than only after projection has received them.
	MaxFindings       = 10000
	maxExportBindings = 10000
	maxExportAssets   = 10000
	maxExportCount    = 1<<31 - 1
	maxGraphBytes     = 8 << 20
	blockedReport     = "민감정보 보호 정책으로 보고서를 내보낼 수 없습니다.\n"
	exportNotice      = "> 공유용 사본: 자유문장·URL·원문 PoC와 요청·응답은 [REDACTED]로 가렸습니다. 재현에는 권한이 있는 원본 증거를 별도로 확인하세요.\n\n"
)

// ProjectFindings builds an export-only copy from a closed set of fields. A
// shallow copy would silently expose future DB fields and retain raw evidence.
// This does not change the evidence used by the oracle or the internal UI.
func ProjectFindings(in []*db.DBFinding) ([]*db.DBFinding, error) {
	if len(in) > MaxFindings {
		return nil, ErrExportBlocked
	}
	out := make([]*db.DBFinding, 0, len(in))
	bindings, assets := 0, 0
	for _, f := range in {
		if f == nil || f.ID < 0 || f.EvidenceVersion < 0 || f.ReportEvidenceVersion < 0 || !validExportTime(f.CreatedAt) {
			return nil, ErrExportBlocked
		}
		bindings += len(f.TrafficBindings)
		assets += len(f.AssetIDs)
		if bindings > maxExportBindings || assets > maxExportAssets {
			return nil, ErrExportBlocked
		}
		v := &db.DBFinding{
			ID: f.ID, EvidenceVersion: f.EvidenceVersion, ReportEvidenceVersion: f.ReportEvidenceVersion,
			VulnClass: maskText(f.VulnClass), Name: maskText(f.Name), Severity: exportSeverity(f.Severity),
			Summary: maskText(f.Summary), Evidence: maskText(f.Evidence), Report: maskText(f.Report),
			Status: exportStatus(f.Status), TaskDescription: maskText(f.TaskDescription), CreatedAt: f.CreatedAt.UTC(),
		}
		if f.TaskID != nil {
			id := *f.TaskID
			if id < 0 {
				return nil, ErrExportBlocked
			}
			v.TaskID = &id
		}
		if f.NodeID != nil {
			id := *f.NodeID
			if id < 0 {
				return nil, ErrExportBlocked
			}
			v.NodeID = &id
		}
		for _, id := range f.AssetIDs {
			if id < 0 {
				return nil, ErrExportBlocked
			}
			v.AssetIDs = append(v.AssetIDs, id)
		}
		for _, b := range f.TrafficBindings {
			s := b.Snapshot
			if b.ID < 0 || b.FindingID < 0 || (b.FindingID != 0 && b.FindingID != f.ID) || b.Position < 0 ||
				s.CapturedAt < 0 || s.ReqLen < 0 || s.RespLen < 0 || (s.Status != 0 && (s.Status < 100 || s.Status > 599)) ||
				!validExportTime(b.CreatedAt) || !validExportTime(s.CreatedAt) {
				return nil, ErrExportBlocked
			}
			role := dlp.Redacted
			if db.ValidTrafficRole(b.Role) {
				role = b.Role
			}
			v.TrafficBindings = append(v.TrafficBindings, db.FindingTrafficBinding{
				ID: b.ID, FindingID: b.FindingID, SnapshotID: maskText(b.SnapshotID),
				Role: role, Note: maskText(b.Note), Position: b.Position, CreatedAt: b.CreatedAt.UTC(),
				Snapshot: db.TrafficEvidenceSnapshot{
					ID: maskText(s.ID), SourceTrafficID: maskText(s.SourceTrafficID), CapturedAt: s.CapturedAt,
					URL: maskText(s.URL), Method: exportMethod(s.Method), Status: s.Status,
					ContentType: maskText(s.ContentType), ReqHead: maskText(s.ReqHead), RespHead: maskText(s.RespHead),
					ReqHash: maskText(s.ReqHash), RespHash: maskText(s.RespHash), ReqLen: s.ReqLen, RespLen: s.RespLen,
					CreatedAt: s.CreatedAt.UTC(),
				},
			})
		}
		v.TrafficCount = len(v.TrafficBindings)
		out = append(out, v)
	}
	return out, nil
}

func maskText(s string) string {
	if s == "" {
		return ""
	}
	return dlp.Redacted
}

func exportSeverity(s string) string {
	if db.ValidSeverity(s) || s == "info" {
		return s
	}
	return dlp.Redacted
}

func exportStatus(s string) string {
	if db.ValidFindingStatus(s) {
		return s
	}
	return dlp.Redacted
}

func exportMethod(s string) string {
	switch s {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
		return s
	default:
		return dlp.Redacted
	}
}

func validExportTime(t time.Time) bool {
	return t.UTC().Year() >= 0 && t.UTC().Year() <= 9999
}

func exportAssetCounts(in map[string]int) (map[string]int, error) {
	if len(in) > maxExportAssets {
		return nil, ErrExportBlocked
	}
	out := make(map[string]int)
	for name, count := range in {
		if count < 0 || count > maxExportCount {
			return nil, ErrExportBlocked
		}
		switch name {
		case "root_domain", "ip", "subdomain", "app", "service", "endpoint", "host", "url":
		default:
			name = "other"
		}
		if out[name] > maxExportCount-count {
			return nil, ErrExportBlocked
		}
		out[name] += count
	}
	return out, nil
}
