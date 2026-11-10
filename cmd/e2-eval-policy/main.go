// Command e2-eval-policy는 평가 회차 준비에 필요한 두 가지 일만 한다.
// ca는 회차별 E② CA를 새 비공개 파일로 쓰고, fingerprint는 정책의 내용 지문을 출력한다.
// 키 값과 입력 경로는 출력하지 않고 네트워크를 쓰지 않는다.
package main

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/server"
)

const usage = "Usage: e2-eval-policy ca --cert-file NEW --key-file NEW --run-ref REF --expires RFC3339\n" +
	"       e2-eval-policy fingerprint --policy-file PATH [--prepared]\n"

const (
	caCreated           = "e2_eval_ca_created"
	invalidInput        = "e2_eval_policy_input_invalid"
	unsupportedPlatform = "e2_eval_policy_platform_unsupported"
	generationFailed    = "e2_eval_ca_generation_failed"
	privateDirRequired  = "e2_eval_policy_private_directory_required"
	alreadyExists       = "e2_eval_policy_file_exists"
	storageUnavailable  = "e2_eval_policy_storage_unavailable"
	policyInvalid       = "e2_eval_policy_invalid"
	policyUnavailable   = "e2_eval_policy_unavailable"
	outputUnavailable   = "e2_eval_policy_output_unavailable"
)

// 정책의 파일 pin이 단일 basename만 받으므로 같은 규칙으로 막는다.
var fileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return usageFailure(stderr)
	}
	switch args[0] {
	case "ca":
		return runCA(args[1:], stdout, stderr)
	case "fingerprint":
		return runFingerprint(args[1:], stdout, stderr)
	case "-h", "--help", "-help":
		if !writeMessage(stdout, usage) {
			return failure(stderr, outputUnavailable)
		}
		return 0
	}
	return usageFailure(stderr)
}

func newFlags(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	// 기본 진단은 비공개 경로와 입력값을 그대로 출력한다.
	flags.SetOutput(io.Discard)
	return flags
}

func runCA(args []string, stdout, stderr io.Writer) int {
	flags := newFlags("ca")
	certPath := flags.String("cert-file", "", "new PEM certificate file")
	keyPath := flags.String("key-file", "", "new PEM private key file")
	runRef := flags.String("run-ref", "", "engine run reference")
	expires := flags.String("expires", "", "RFC3339 expiry")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !validPair(*certPath, *keyPath) {
		return usageFailure(stderr)
	}
	expiry, err := time.Parse(time.RFC3339, *expires)
	if err != nil {
		return failure(stderr, invalidInput)
	}
	if runtime.GOOS != "linux" {
		return failure(stderr, unsupportedPlatform)
	}
	authority, certificate, err := e2proxy.NewExecutionCA(*runRef, expiry)
	if err != nil {
		return failure(stderr, invalidInput)
	}
	der, err := x509.MarshalPKCS8PrivateKey(authority.PrivateKey)
	if err != nil {
		return failure(stderr, generationFailed)
	}
	defer clear(der)
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	defer clear(key)
	// 회차 디렉터리는 재사용하지 않으므로 중간 실패로 남은 파일은 지우지 않는다.
	for _, file := range []struct {
		path string
		data []byte
	}{{*keyPath, key}, {*certPath, certificate}} {
		if code := writeNew(file.path, file.data); code != "" {
			return failure(stderr, code)
		}
	}
	if !writeMessage(stdout, caCreated+"\n") {
		return failure(stderr, outputUnavailable)
	}
	return 0
}

func validPair(certPath, keyPath string) bool {
	certDir, certName := filepath.Split(certPath)
	keyDir, keyName := filepath.Split(keyPath)
	return filepath.IsAbs(certPath) && certDir == keyDir && certName != keyName && fileName.MatchString(certName) && fileName.MatchString(keyName)
}

func runFingerprint(args []string, stdout, stderr io.Writer) int {
	flags := newFlags("fingerprint")
	path := flags.String("policy-file", "", "policy JSON file")
	prepared := flags.Bool("prepared", false, "policy already holds the audit key pin")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *path == "" {
		return usageFailure(stderr)
	}
	raw, err := readPolicy(*path)
	if err != nil {
		return failure(stderr, policyUnavailable)
	}
	fingerprint, err := server.E2HTTPPolicyContentFingerprint(raw, *prepared)
	if err != nil {
		return failure(stderr, policyInvalid)
	}
	if !writeMessage(stdout, fingerprint+"\n") {
		return failure(stderr, outputUnavailable)
	}
	return 0
}

// 상한을 하나 넘겨 읽어 크기 초과를 검증기가 거부하게 둔다.
func readPolicy(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, e2config.MaxPolicyBytes+1))
}

func usageFailure(stderr io.Writer) int {
	if !writeMessage(stderr, usage) {
		return 1
	}
	return 2
}

func writeMessage(output io.Writer, message string) bool {
	n, err := io.WriteString(output, message)
	return err == nil && n == len(message)
}

func failure(stderr io.Writer, code string) int {
	_ = writeMessage(stderr, code+"\n")
	return 1
}
