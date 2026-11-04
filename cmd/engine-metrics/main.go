package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const defaultTimeout = 10 * time.Second
const maximumTimeout = time.Minute
const dsnEnvironment = "ARTEX_ENGINE_METRICS_DSN"

type selection struct{ task, exploration int64 }

type report struct {
	SchemaVersion        int             `json:"schema_version"`
	Purpose              string          `json:"purpose"`
	TaskID               *string         `json:"task_id"`
	ExplorationID        string          `json:"exploration_id"`
	TransactionStartedAt time.Time       `json:"transaction_started_at"`
	Intents              intentMetrics   `json:"intents"`
	AllActivity          activityMetrics `json:"all_recorded_activity"`
	ResultActivity       activityMetrics `json:"recorded_result_activity"`
	Limitations          []string        `json:"limitations"`
}

type stateCounts struct {
	Open      int64 `json:"open"`
	Running   int64 `json:"running"`
	Paused    int64 `json:"paused"`
	Done      int64 `json:"done"`
	Blocked   int64 `json:"blocked"`
	Exhausted int64 `json:"exhausted"`
	Stopped   int64 `json:"stopped"`
	Deleted   int64 `json:"deleted"`
	Unknown   int64 `json:"unknown_nonnull_state"`
	Null      int64 `json:"null_state"`
}

type intentMetrics struct {
	Recorded                 int64          `json:"recorded_count"`
	States                   stateCounts    `json:"states"`
	MissingCreated           int64          `json:"created_at_missing_count"`
	MissingCompleted         int64          `json:"completed_at_missing_count"`
	TerminalMissingCompleted int64          `json:"terminal_completed_at_missing_count"`
	NonFiniteTime            int64          `json:"non_finite_timestamp_count"`
	NegativeElapsed          int64          `json:"completion_before_creation_count"`
	Elapsed                  elapsedMetrics `json:"creation_to_completion_including_wait"`
}

type elapsedMetrics struct {
	RecordedPairs  int64    `json:"valid_pair_count"`
	MinimumSeconds *float64 `json:"minimum_seconds"`
	MaximumSeconds *float64 `json:"maximum_seconds"`
	MeanSeconds    *float64 `json:"mean_seconds"`
	SumSeconds     *float64 `json:"sum_seconds"`
}

type tokenMetrics struct {
	RecordedSum *int64 `json:"recorded_sum"`
	Missing     int64  `json:"null_record_count"`
	Negative    int64  `json:"negative_record_count"`
}

type activityMetrics struct {
	Recorded      int64        `json:"recorded_count"`
	PlannerRounds int64        `json:"planner_round_record_count"`
	Input         tokenMetrics `json:"input_tokens"`
	Output        tokenMetrics `json:"output_tokens"`
	CacheRead     tokenMetrics `json:"cache_read_tokens"`
	CacheWrite    tokenMetrics `json:"cache_write_tokens"`
}

// Failures never wrap driver/flag/connection errors: those can carry DSNs or data.
type failure string

func (e failure) Error() string { return string(e) }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(parent context.Context, args []string, getenv func(string) string, out, errOut io.Writer) int {
	fail := func(reason string) int {
		_ = json.NewEncoder(errOut).Encode(struct {
			Error string `json:"error"`
		}{reason})
		return 1
	}
	flags := flag.NewFlagSet("engine-metrics", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var scope selection
	var timeout time.Duration
	flags.Int64Var(&scope.task, "task-id", 0, "one task ID")
	flags.Int64Var(&scope.exploration, "exploration-id", 0, "one exploration ID")
	flags.DurationVar(&timeout, "timeout", defaultTimeout, "total connection/query context deadline")
	if flags.Parse(args) != nil || flags.NArg() != 0 || !scope.valid() || timeout <= 0 || timeout > maximumTimeout {
		return fail("invalid_arguments")
	}
	dsn := getenv(dsnEnvironment)
	if strings.TrimSpace(dsn) == "" {
		return fail("dsn_required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fail("connection_configuration_failed")
	}
	// Fixed public relations and pg_catalog-only lookup avoid evaluating a
	// user-supplied operator/function through a connection's search_path.
	cfg.RuntimeParams["search_path"] = "pg_catalog"
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	result, queryErr := collect(ctx, db, scope)
	closeErr := db.Close()
	if queryErr != nil {
		return fail(queryErr.Error())
	}
	if closeErr != nil {
		return fail("connection_close_failed")
	}
	// Buffer before writing: JSON encoding errors must not publish a partial report.
	encoded, err := json.Marshal(result)
	if err != nil {
		return fail("report_encoding_failed")
	}
	if _, err = out.Write(append(encoded, '\n')); err != nil {
		return fail("output_failed")
	}
	return 0
}

func (s selection) valid() bool {
	return s.task >= 0 && s.exploration >= 0 && ((s.task > 0) != (s.exploration > 0))
}

func queryFailure(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return failure("deadline_exceeded")
	}
	if ctx.Err() != nil {
		return failure("cancelled")
	}
	return failure("query_failed")
}

func collect(ctx context.Context, db *sql.DB, scope selection) (result report, err error) {
	if ctx == nil || db == nil || !scope.valid() {
		return report{}, failure("invalid_configuration")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return report{}, queryFailure(ctx)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && err == nil {
			result, err = report{}, queryFailure(ctx)
		}
	}()
	// Verify the effective setting too: connection options must not silently
	// override the catalog-only lookup used by the fixed aggregate queries.
	var searchPath string
	if tx.QueryRowContext(ctx, "SELECT pg_catalog.current_setting('search_path')").Scan(&searchPath) != nil {
		return report{}, queryFailure(ctx)
	}
	if searchPath != "pg_catalog" {
		return report{}, failure("connection_configuration_failed")
	}
	var exp int64
	var task sql.NullInt64
	err = tx.QueryRowContext(ctx, scopeSQL, scope.task, scope.exploration).Scan(&exp, &task, &result.TransactionStartedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return report{}, failure("scope_not_found")
	}
	if err != nil {
		return report{}, queryFailure(ctx)
	}
	result.SchemaVersion, result.Purpose = 1, "engine_internal_observation"
	result.ExplorationID = strconv.FormatInt(exp, 10)
	if task.Valid {
		id := strconv.FormatInt(task.Int64, 10)
		result.TaskID = &id
	}
	i := &result.Intents
	s := &i.States
	var minimum, maximum, mean, sum sql.NullFloat64
	err = tx.QueryRowContext(ctx, intentsSQL, exp).Scan(
		&i.Recorded, &s.Open, &s.Running, &s.Paused, &s.Done, &s.Blocked, &s.Exhausted, &s.Stopped, &s.Deleted, &s.Unknown, &s.Null,
		&i.MissingCreated, &i.MissingCompleted, &i.TerminalMissingCompleted, &i.NonFiniteTime, &i.NegativeElapsed,
		&i.Elapsed.RecordedPairs, &minimum, &maximum, &mean, &sum)
	if err != nil {
		return report{}, queryFailure(ctx)
	}
	i.Elapsed.MinimumSeconds, i.Elapsed.MaximumSeconds = nullableFloat(minimum), nullableFloat(maximum)
	i.Elapsed.MeanSeconds, i.Elapsed.SumSeconds = nullableFloat(mean), nullableFloat(sum)
	rows, err := tx.QueryContext(ctx, activitySQL, exp)
	if err != nil {
		return report{}, queryFailure(ctx)
	}
	sets := []*activityMetrics{&result.AllActivity, &result.ResultActivity}
	seen := 0
	for rows.Next() {
		var bucket int
		var metrics activityMetrics
		var sums [4]sql.NullInt64
		err = rows.Scan(&bucket, &metrics.Recorded, &metrics.PlannerRounds,
			&sums[0], &metrics.Input.Missing, &metrics.Input.Negative,
			&sums[1], &metrics.Output.Missing, &metrics.Output.Negative,
			&sums[2], &metrics.CacheRead.Missing, &metrics.CacheRead.Negative,
			&sums[3], &metrics.CacheWrite.Missing, &metrics.CacheWrite.Negative)
		if err != nil || bucket != seen || seen >= len(sets) {
			_ = rows.Close()
			return report{}, queryFailure(ctx)
		}
		metrics.Input.RecordedSum, metrics.Output.RecordedSum = nullableInt(sums[0]), nullableInt(sums[1])
		metrics.CacheRead.RecordedSum, metrics.CacheWrite.RecordedSum = nullableInt(sums[2]), nullableInt(sums[3])
		*sets[seen] = metrics
		seen++
	}
	rowsErr, closeErr := rows.Err(), rows.Close()
	if rowsErr != nil || closeErr != nil || seen != len(sets) || ctx.Err() != nil {
		return report{}, queryFailure(ctx)
	}
	result.Limitations = []string{
		"recorded_rows_only_not_official_evaluation_metrics",
		"creation_to_completion_includes_wait_not_execution_time",
		"activity_round_result_counts_are_observed_records_not_llm_calls",
		"null_tokens_unrecorded_usage_not_zero_partial_sums_not_total_usage",
		"resume_rerun_deleted_history_and_inherited_records_not_reconstructed",
		"exact_llm_calls_retries_concurrency_dedup_reuse_unavailable",
		"engine_llmCalls_tracks_current_inflight_not_cumulative_calls",
	}
	return result, nil
}

func nullableFloat(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}
func nullableInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}
