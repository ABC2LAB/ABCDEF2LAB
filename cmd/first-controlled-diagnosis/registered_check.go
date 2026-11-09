package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/Autumn-27/artex/server"
)

func runRegisteredCheck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("check-registered-get", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	metadata := flags.String("metadata", "", "protected existing preparation metadata")
	snapshots := flags.String("snapshot-dir", "", "protected existing snapshot directory")
	registry := flags.String("registry", "", "registry filename within the snapshot directory")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *metadata == "" || *snapshots == "" || *registry == "" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if server.CheckRegisteredReadPreparation(ctx, *metadata, *snapshots, *registry) != nil {
		fmt.Fprintln(stderr, "등록 GET 준비 자료의 파일·조회 규칙·결속을 확인할 수 없습니다")
		return 1
	}
	// 준비 검사와 실제 세션·응답·권위 수용을 구분한다.
	report := struct {
		Registration   string `json:"registration"`
		ResponseRule   string `json:"response_rule"`
		Pairing        string `json:"pairing"`
		Metadata       string `json:"metadata"`
		Authentication string `json:"authentication"`
		Authority      string `json:"authority"`
		Database       string `json:"database"`
		Model          string `json:"model"`
		TargetRequests int    `json:"target_requests"`
		Diagnosis      string `json:"diagnosis"`
	}{"PASS", "PASS", "PASS", "PASS", "NOT_VERIFIED", "NOT_ACQUIRED", "NOT_CHECKED", "NOT_CALLED", 0, "NOT_RUN"}
	if ctx.Err() != nil {
		fmt.Fprintln(stderr, "등록 GET 준비 검사가 취소되었습니다")
		return 1
	}
	if json.NewEncoder(stdout).Encode(report) != nil {
		fmt.Fprintln(stderr, "등록 GET 준비 검사 결과를 기록할 수 없습니다")
		return 1
	}
	return 0
}
