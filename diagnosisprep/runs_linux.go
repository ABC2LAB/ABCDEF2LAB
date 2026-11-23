//go:build linux

package diagnosisprep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

// listRunsLocked는 s.mu 안에서 보호 저널의 복원 projection만 읽는다. 실행·예약·위임·저널은 건드리지 않는다.
// cursor는 권위가 아니며 매번 principal로 모은 전체 집합의 지문과 대조한다. 상태가 바뀌면 섞인 페이지 대신 충돌이다.
func (s *Service) listRunsLocked(ctx context.Context, req Request) Response {
	runs := make([]RunSummary, 0)
	now := time.Now()
	owned := make([]Job, 0)
	// 지운 회차(Purged)는 목록에 두지 않는다. 기록은 저널에 남아 실행 횟수·예산에는 계속 센다.
	for _, record := range s.jobs {
		if record.Principal == req.Principal && !record.Purged {
			owned = append(owned, record.Job)
		}
	}
	approvals := map[string]string{}
	if reader, ok := s.backend.(RunApprovalReader); ok {
		approvals = reader.ReadRunApprovals(req.Principal, owned)
	}

	for _, record := range s.jobs {
		if record.Principal != req.Principal || record.Purged {
			continue
		}
		if ctx.Err() != nil {
			return responseError(req, "unavailable")
		}
		j := record.Job
		approval := "not_revalidated"
		if j.Scope != nil && !now.Before(j.Scope.ExpiresAt) {
			approval = "expired"
		} else if status, ok := approvals[j.ID]; ok {
			approval = status
			if !validApprovalStatus(approval) {
				approval = "unavailable"
			}
		}
		run := RunSummary{ID: j.ID, Revision: j.Revision, DisplayName: j.Input.DisplayName, State: j.State, Phase: j.Phase, ReasonCode: j.ReasonCode, TaskID: j.TaskID, Started: j.Started, DrainComplete: j.DrainComplete, ResultReady: j.ResultReady, ReportAvailable: j.ResultReady && j.TaskID != "", ApprovalStatus: approval, ActionRequired: runActive(j.State) || j.State == Unknown || j.State == Failed || j.State == AwaitingApproval || approval != "not_revalidated"}
		run.StartedAt, run.FinishedAt = copyRunTime(j.StartedAt), copyRunTime(j.FinishedAt)
		if !validJob(j) || !validRunSummary(run) {
			return responseError(req, "unavailable")
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { return runBefore(runs[i], runs[j]) })
	// 주체도 지문에 넣어 다른 주체의 cursor를 권한으로 사용하지 못한다.
	hash := sha256.New()
	hash.Write([]byte("diagnosis-preparation/list-runs/v1\x00" + req.Principal + "\x00"))
	raw, _ := json.Marshal(runs)
	hash.Write(raw)
	snapshot := hex.EncodeToString(hash.Sum(nil))
	limit := req.Runs.Limit
	if limit == 0 {
		limit = DefaultRunPageSize
	}
	start := 0
	if req.Runs.Cursor != "" {
		cursor, ok := decodeRunCursor(req.Runs.Cursor)
		if !ok {
			return responseError(req, "invalid_request")
		}
		if cursor.Snapshot != snapshot {
			return responseError(req, "snapshot_changed")
		}
		limit = cursor.Limit
		start = -1
		for i, run := range runs {
			if run.ID == cursor.After {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return responseError(req, "snapshot_changed")
		}
	}
	page := &RunPage{Runs: []RunSummary{}, Snapshot: snapshot, Limit: limit}
	for i := start; i < len(runs) && len(page.Runs) < limit; i++ {
		page.Runs = append(page.Runs, runs[i])
		page.More = i+1 < len(runs)
		if page.More {
			page.NextCursor = encodeRunCursor(runCursor{Snapshot: snapshot, After: runs[i].ID, Limit: limit})
		} else {
			page.NextCursor = ""
		}
		raw, _ := json.Marshal(page)
		if len(raw) > MaxRunPageBytes {
			page.Runs = page.Runs[:len(page.Runs)-1]
			if len(page.Runs) == 0 {
				return responseError(req, "capacity")
			}
			page.More = true
			page.NextCursor = encodeRunCursor(runCursor{Snapshot: snapshot, After: page.Runs[len(page.Runs)-1].ID, Limit: limit})
			break
		}
	}
	result := Response{Version: Version, RequestID: req.RequestID, RunPage: page}
	if ValidateResponse(req, result) != nil {
		return responseError(req, "unavailable")
	}
	return result
}
