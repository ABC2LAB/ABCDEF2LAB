package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const usage = "usage: first-controlled-diagnosis check -config <protected-file> [-database] | check-registered-get -metadata <protected-file> -snapshot-dir <protected-directory> -registry <filename> | prepare-task -config <launch-file> -task <protected-task-file> | prepare-intents -config <launch-file> -task-id <actual-id> | serve -config <launch-file> | run -config <launch-file> | report -config <launch-file> -task-id <actual-id> | gateway -config <launch-file> -preparation-ref <operator-reference> | manage -config <protected-launch> -registry <protected-registry> -gateway-read-fd <inherited-fd> -gateway-write-fd <inherited-fd>"

type checkReport struct {
	Configuration string `json:"configuration"`
	Paths         string `json:"paths"`
	ModelPin      string `json:"model_pin"`
	Database      string `json:"database"`
	Providers     string `json:"providers"`
	Diagnosis     string `json:"diagnosis"`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv, checkDatabase)
	cancel()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string, databaseCheck func(context.Context, string) error) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		if _, err := fmt.Fprintln(stdout, usage); err != nil {
			return 1
		}
		return 0
	}
	if len(args) > 0 && args[0] == "manage" {
		return runManagement(ctx, args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "gateway" {
		return runGateway(ctx, args[1:], os.Stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "report" {
		return runReport(ctx, args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "check-registered-get" {
		return runRegisteredCheck(ctx, args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "prepare-intents" {
		return runPrepareIntents(ctx, args[1:], stdout, stderr)
	}
	if len(args) > 0 && (args[0] == "serve" || args[0] == "run") {
		return runServe(ctx, args[1:], stdout, stderr, args[0] == "run")
	}
	if len(args) > 0 && args[0] == "prepare-task" {
		return runPrepareTask(ctx, args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "protected preparation configuration")
	database := flags.Bool("database", false, "check existing PostgreSQL schema read-only")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *path == "" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if ctx == nil || ctx.Err() != nil {
		fmt.Fprintln(stderr, "check_cancelled")
		return 1
	}
	data, err := readPrivateFile(*path, maxLaunchBytes)
	if err != nil {
		fmt.Fprintln(stderr, "configuration_unavailable")
		return 1
	}
	config, err := parseConfig(data)
	if err != nil {
		launch, launchErr := parseLaunchConfig(data)
		clear(data)
		if launchErr != nil {
			fmt.Fprintln(stderr, "configuration_invalid")
			return 1
		}
		return checkLaunch(ctx, launch, *database, stdout, stderr)
	}
	clear(data)
	if err = checkFiles(config); err != nil || ctx.Err() != nil {
		fmt.Fprintln(stderr, "preparation_files_unavailable")
		return 1
	}
	report := checkReport{"PASS", "PASS", "PASS", "SKIP", "NOT_VERIFIED", "NOT_RUN"}
	if *database {
		if getenv == nil || databaseCheck == nil {
			fmt.Fprintln(stderr, "database_unavailable")
			return 1
		}
		dsn := getenv("ARTEX_PG_DSN")
		if dsn == "" || databaseCheck(ctx, dsn) != nil {
			fmt.Fprintln(stderr, "database_unavailable")
			return 1
		}
		report.Database = "PASS"
	}
	if ctx.Err() != nil {
		fmt.Fprintln(stderr, "check_cancelled")
		return 1
	}
	if json.NewEncoder(stdout).Encode(report) != nil {
		fmt.Fprintln(stderr, "output_unavailable")
		return 1
	}
	return 0
}
