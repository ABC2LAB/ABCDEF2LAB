//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/budgetlocal"
)

const operatorCommandTimeout = 10 * time.Second
const operatorFileLimit = 4096

func main() {
	code, message := resultDiagnostic(run(context.Background(), os.Args[1:], os.Stdout))
	if code != 0 {
		fmt.Fprintln(os.Stderr, message)
		os.Exit(code)
	}
}

func resultDiagnostic(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	if errors.Is(err, budgetlocal.ErrInvalid) || errors.Is(err, budgetlocal.ErrDenied) {
		return 1, "budget approval rejected"
	}
	return 3, "budget approval outcome unknown; retry the same approval_request_ref"
}

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("e2-budget-approve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "private trusted server identity JSON")
	requestPath := flags.String("request", "", "private approval request JSON")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 || *configPath == "" || *requestPath == "" {
		return budgetlocal.ErrInvalid
	}
	configData, err := readPrivateOperatorFile(*configPath)
	if err != nil {
		return budgetlocal.ErrDenied
	}
	config, err := budgetlocal.ParseClientConfig(configData)
	if err != nil {
		return err
	}
	data, err := readPrivateOperatorFile(*requestPath)
	if err != nil {
		return budgetlocal.ErrDenied
	}
	request, err := budgetlocal.ParseRequest(data)
	if err != nil {
		return err
	}
	issueCtx, cancel := context.WithTimeout(ctx, operatorCommandTimeout)
	defer cancel()
	response, err := budgetlocal.Submit(issueCtx, config.SocketPath, config.ServerUID, request)
	if err != nil {
		if errors.Is(err, budgetlocal.ErrDenied) || errors.Is(err, budgetlocal.ErrInvalid) {
			return err
		}
		return budgetlocal.ErrUnknown
	}
	data, err = json.Marshal(response)
	if err != nil {
		return budgetlocal.ErrUnknown
	}
	data = append(data, '\n')
	if n, err := out.Write(data); err != nil || n != len(data) {
		return budgetlocal.ErrUnknown
	}
	return nil
}

func readPrivateOperatorFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, budgetlocal.ErrInvalid
	}
	path = filepath.Clean(path)
	f, err := openPrivateOperatorInput(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, operatorFileLimit+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || len(data) == 0 || len(data) > operatorFileLimit {
		return nil, budgetlocal.ErrInvalid
	}
	return data, nil
}

func trustedPrivateRequestDirectoryWithLstat(dir string, operatorUID uint32, lstat func(string) (os.FileInfo, error)) bool {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return false
	}
	childOwner := operatorUID
	for current := dir; ; current = filepath.Dir(current) {
		info, err := lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 && stat.Uid != operatorUID {
			return false
		}
		if current == dir && (stat.Uid != operatorUID || info.Mode().Perm() != 0o700) {
			return false
		}
		if info.Mode().Perm()&0o022 != 0 &&
			(stat.Uid != 0 || info.Mode()&os.ModeSticky == 0 ||
				childOwner != 0 && childOwner != operatorUID) {
			return false
		}
		childOwner = stat.Uid
		if current == string(filepath.Separator) {
			return true
		}
	}
}
