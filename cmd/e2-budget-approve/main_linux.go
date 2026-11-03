//go:build linux

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/budgetlocal"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "budget approval rejected")
		os.Exit(1)
	}
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
		return err
	}
	config, err := budgetlocal.ParseClientConfig(configData)
	if err != nil {
		return err
	}
	data, err := readPrivateOperatorFile(*requestPath)
	if err != nil {
		return err
	}
	request, err := budgetlocal.ParseRequest(data)
	if err != nil {
		return err
	}
	issueCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, err := budgetlocal.Submit(issueCtx, config.SocketPath, config.ServerUID, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(response)
}

func readPrivateOperatorFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, budgetlocal.ErrInvalid
	}
	path = filepath.Clean(path)
	if !trustedPrivateRequestDirectory(filepath.Dir(path), uint32(os.Geteuid())) {
		return nil, budgetlocal.ErrUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, budgetlocal.ErrUnavailable
	}
	fileStat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || fileStat.Uid != uint32(os.Geteuid()) {
		return nil, budgetlocal.ErrUnavailable
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, budgetlocal.ErrUnavailable
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, budgetlocal.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) == 0 || len(data) > 4096 {
		return nil, budgetlocal.ErrInvalid
	}
	return data, nil
}

func trustedPrivateRequestDirectory(dir string, operatorUID uint32) bool {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return false
	}
	childOwner := operatorUID
	for current := dir; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
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
