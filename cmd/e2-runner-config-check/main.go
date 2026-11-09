// Command e2-runner-config-check는 평가 준비 입력만 검사한다.
// service_ready, 실행 승인, 제품 수용 성공을 발급하지 않는다.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Autumn-27/artex/e2config"
)

const usage = "Usage: e2-runner-config-check --config FILE\n"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("e2-runner-config-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "protected runner configuration")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		if n, e := io.WriteString(stdout, usage); e != nil || n != len(usage) {
			return 1
		}
		return 0
	} else if err != nil || flags.NArg() != 0 || *path == "" {
		_, _ = io.WriteString(stderr, usage)
		return 2
	}
	cfg, err := e2config.Load(*path)
	if err != nil || cfg.Check() != nil {
		_, _ = fmt.Fprintln(stderr, "e2_runner_config_invalid")
		return 1
	}
	// 원문·경로·키·회차 참조는 진단 출력에서 제외한다.
	result := struct {
		State                  string `json:"state"`
		ConfigSHA256           string `json:"config_sha256"`
		PolicyFileSHA256       string `json:"policy_file_sha256"`
		ServiceReady           bool   `json:"service_ready"`
		ProductPolicyValidated bool   `json:"product_policy_validated"`
	}{"inputs_validated", cfg.ConfigSHA256, cfg.PolicySHA256, false, false}
	data, err := json.Marshal(result)
	if err != nil {
		return 1
	}
	data = append(data, '\n')
	if n, e := stdout.Write(data); e != nil || n != len(data) {
		_, _ = fmt.Fprintln(stderr, "e2_runner_config_output_unavailable")
		return 1
	}
	return 0
}
