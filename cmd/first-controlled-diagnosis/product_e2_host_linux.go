//go:build linux

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/e2checkpoint"
	"github.com/Autumn-27/artex/e2host"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func infoOwner(info os.FileInfo) (int, bool) {
	if info == nil {
		return 0, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}

func hostSecret(path string, max int64) ([]byte, error) {
	before, err := os.Lstat(path)
	var owned bool
	if err == nil {
		stat, ok := before.Sys().(*syscall.Stat_t)
		owned = ok && int(stat.Uid) == os.Geteuid()
	}
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() == 0 || before.Size() > max ||
		!owned {
		return nil, errPreparation
	}
	raw, err := os.ReadFile(path)
	after, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		clear(raw)
		return nil, errPreparation
	}
	return raw, nil
}

func runProductE2Host(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("product-e2-host", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "protected Host volume")
	socket := flags.String("socket", "", "private Host socket")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *root != "/h" || *socket != "/run/e2-host/audit.sock" ||
		os.Geteuid() != 1002 || ctx == nil || ctx.Err() != nil {
		return 2
	}
	info, err := os.Lstat(*root)
	owner, owned := infoOwner(info)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 || !owned || owner != os.Geteuid() {
		return 1
	}
	// 기존 설치에는 Host 자격이 없다. 무검증 자동 이관 없이 v2만 유지한다.
	// 새 설치의 Host 볼륨이 사라진 경우에도 v3 실행은 소켓 부재로 차단된다.
	if _, err := os.Lstat(filepath.Join(*root, "epoch.json")); os.IsNotExist(err) {
		entries, readErr := os.ReadDir(*root)
		if readErr != nil || len(entries) != 0 {
			return 1
		}
		marker := filepath.Join(filepath.Dir(*socket), "legacy-unavailable")
		if os.WriteFile(marker, []byte("v3_not_provisioned\n"), 0600) != nil {
			return 1
		}
		defer os.Remove(marker)
		fmt.Fprintln(stdout, "e2_host_not_provisioned")
		<-ctx.Done()
		return 0
	}
	_ = os.Remove(filepath.Join(filepath.Dir(*socket), "legacy-unavailable"))
	lock, err := os.OpenFile(filepath.Join(*root, "host.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil || syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		if lock != nil {
			_ = lock.Close()
		}
		return 1
	}
	defer lock.Close()
	if existing, statErr := os.Lstat(*socket); statErr == nil {
		owner, ok := existing.Sys().(*syscall.Stat_t)
		if !ok || existing.Mode()&os.ModeSocket == 0 || int(owner.Uid) != os.Geteuid() {
			return 1
		}
		probe, connectErr := net.DialTimeout("unix", *socket, time.Second)
		if connectErr == nil {
			_ = probe.Close()
			return 1
		}
		if os.Remove(*socket) != nil {
			return 1
		}
	} else if !os.IsNotExist(statErr) {
		return 1
	}
	dsn, err := hostSecret(filepath.Join(*root, "host.dsn"), 4096)
	if err != nil {
		fmt.Fprintln(stderr, "E② Host 자격을 확인할 수 없습니다")
		return 1
	}
	defer clear(dsn)
	key, err := hostSecret(filepath.Join(*root, "audit.key"), 4096)
	if err != nil || len(key) < 32 {
		clear(key)
		return 1
	}
	defer clear(key)
	digestKey, err := hostSecret(filepath.Join(*root, "digest.key"), 4096)
	if err != nil || len(digestKey) < 32 || string(key) == string(digestKey) {
		clear(digestKey)
		return 1
	}
	clear(digestKey) // E③ 결속 이전에는 사용하지 않는다.
	epochRaw, err := hostSecret(filepath.Join(*root, "epoch.json"), 4096)
	if err != nil {
		return 1
	}
	var epoch e2checkpoint.Epoch
	decodeErr := json.Unmarshal(epochRaw, &epoch)
	clear(epochRaw)
	if decodeErr != nil {
		return 1
	}
	pool, err := sql.Open("pgx", strings.TrimSpace(string(dsn)))
	if err != nil {
		return 1
	}
	defer pool.Close()
	pool.SetMaxOpenConns(4)
	pool.SetConnMaxLifetime(time.Minute)
	var role string
	if pool.QueryRowContext(ctx, "SELECT current_user").Scan(&role) != nil || role != "product_e2_host" {
		return 1
	}
	host, err := e2host.New(pool, epoch, "product-audit-key-v3", key, filepath.Join(*root, "journals"))
	if err != nil || host.CheckEpoch(ctx, pool) != nil || host.CheckRestoredHeads(ctx) != nil {
		fmt.Fprintln(stderr, "E② 현재 epoch를 확인할 수 없습니다")
		return 1
	}
	if err := host.Serve(ctx, *socket, 1000); err != nil {
		fmt.Fprintln(stderr, "E② Host 연결을 마감했습니다")
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "e2_host_stopped")
	return 0
}
