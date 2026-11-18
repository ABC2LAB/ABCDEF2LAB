package diagnosisprep

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func timingJob() Job {
	return Job{ID: "prep-timing", Revision: 1, SourceDigest: strings.Repeat("a", 64),
		Input: Input{TargetURL: "https://example.test/", ProfileID: "local"},
		State: Unknown, Phase: Closure, TaskID: "42", PreparationStarted: true,
		AuthorityState: AuthorityConsumed, Started: true, DrainComplete: true}
}

func TestRunTimingValidationMatchesFactsAndUTC(t *testing.T) {
	at := time.Date(2026, 11, 17, 1, 2, 3, 456, time.UTC)
	zero := time.Time{}
	local := at.In(time.FixedZone("local", 9*60*60))
	past := at.Add(-time.Nanosecond)
	outside := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name             string
		started, drained bool
		start, finish    *time.Time
		valid            bool
	}{
		{"legacy", true, true, nil, nil, true},
		{"running", true, false, &at, nil, true},
		{"finished", true, true, &at, &at, true},
		{"closed_before_start", false, true, nil, &at, true},
		{"legacy_start_unknown", true, true, nil, &at, true},
		{"start_not_confirmed", false, true, &at, &at, false},
		{"drain_not_confirmed", true, false, &at, &at, false},
		{"zero_start", true, true, &zero, nil, false},
		{"zero_finish", true, true, nil, &zero, false},
		{"non_utc_start", true, true, &local, nil, false},
		{"non_utc_finish", true, true, nil, &local, false},
		{"reversed", true, true, &at, &past, false},
		{"out_of_rfc3339", true, true, &outside, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := timingJob()
			job.Started, job.DrainComplete, job.StartedAt, job.FinishedAt = tc.started, tc.drained, tc.start, tc.finish
			if validJob(job) != tc.valid {
				t.Fatal("Job이 시각·확인 사실 계약을 지키지 않습니다")
			}
			run := RunSummary{ID: job.ID, Revision: 1, State: Unknown, Phase: Closure, TaskID: "42", Started: tc.started,
				DrainComplete: tc.drained, StartedAt: tc.start, FinishedAt: tc.finish, ApprovalStatus: "not_revalidated", ActionRequired: true}
			if validRunSummary(run) != tc.valid {
				t.Fatal("목록이 Job과 다른 시각 계약을 사용합니다")
			}
		})
	}
}

func TestRunTimingStrictJSONRejectsNullAndMalformedValues(t *testing.T) {
	for _, key := range []string{"started_at", "finished_at"} {
		for _, value := range []string{`null`, `""`, `17`, `{}`, `[]`, `"not-a-date"`, `"2026-11-17T01:02:03+09:00"`, `"0001-01-01T00:00:00Z"`} {
			t.Run(key+"/"+value, func(t *testing.T) {
				raw, err := json.Marshal(timingJob())
				if err != nil {
					t.Fatal(err)
				}
				raw = append([]byte(`{"`+key+`":`+value+`,`), raw[1:]...)
				var job Job
				if strictDecode(raw, &job) == nil && validJob(job) {
					t.Fatal("잘못된 시각을 포함한 보호 응답을 수용했습니다")
				}
				var run RunSummary
				row := `{"id":"prep-timing","revision":1,"state":"unknown","phase":"closure","task_id":"42","started":true,"drain_complete":true,"result_ready":false,"report_available":false,"approval_status":"not_revalidated","action_required":true,"` + key + `":` + value + `}`
				if strictDecode([]byte(row), &run) == nil && validRunSummary(run) {
					t.Fatal("잘못된 시각을 포함한 목록을 수용했습니다")
				}
			})
		}
	}
	job := timingJob()
	at := time.Date(2026, 11, 17, 1, 2, 3, 0, time.UTC)
	job.StartedAt = &at
	raw, _ := json.Marshal(job)
	for _, replacement := range []string{`"StartedAt"`, `"started_at":"2026-11-17T01:02:03Z","started_at"`} {
		bad := strings.Replace(string(raw), `"started_at"`, replacement, 1)
		if strictDecode([]byte(bad), new(Job)) == nil {
			t.Fatal("중복·비정규 시각 필드를 수용했습니다")
		}
	}
}

func TestRunTimingMonotonicityRejectsRewriteAndBackfill(t *testing.T) {
	at := time.Date(2026, 11, 17, 1, 2, 3, 0, time.UTC)
	later := at.Add(time.Second)
	for _, field := range []string{"started", "finished"} {
		for _, change := range []string{"rewrite", "remove", "backfill"} {
			t.Run(field+"/"+change, func(t *testing.T) {
				old := timingJob()
				old.StartedAt, old.FinishedAt = &at, &later
				next := old
				before, after := &old.StartedAt, &next.StartedAt
				if field == "finished" {
					before, after = &old.FinishedAt, &next.FinishedAt
				}
				switch change {
				case "rewrite":
					copy := (**after).Add(time.Nanosecond)
					*after = &copy
				case "remove":
					*after = nil
				case "backfill":
					*before = nil
				}
				if !validJob(old) || !validJob(next) {
					t.Fatal("시각 불변성 외의 조건에 걸린 잘못된 음성 대조입니다")
				}
				if monotonicRunTiming(old, next) {
					t.Fatal("이미 확인된 사실의 시각을 변경했습니다")
				}
			})
		}
	}
}
