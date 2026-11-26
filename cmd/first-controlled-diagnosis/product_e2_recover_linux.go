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
)

// runProductE2Recover is deliberately offline. It does not expose an IPC
// operation to preparation/API and never opens a target connection.
func runProductE2Recover(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("product-e2-recover", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	mode := flags.String("mode", "", "status or recover")
	root := flags.String("root", "", "protected Host volume")
	runRef := flags.String("run", "", "existing run reference")
	revision := flags.Uint64("revision", 0, "observed head revision")
	checkpoint := flags.String("checkpoint", "", "observed head MAC")
	operator := flags.String("operator", "", "operator reference")
	project := flags.String("project", "", "Compose project")
	dockerSocket := flags.String("docker-socket", "", "Docker daemon Unix socket")
	if flags.Parse(args) != nil || flags.NArg() != 0 || (*mode != "status" && *mode != "recover") ||
		*root != "/h" || *runRef == "" || os.Geteuid() != 1002 || ctx == nil || ctx.Err() != nil {
		return 2
	}
	info, err := os.Lstat(*root)
	owner, owned := infoOwner(info)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 ||
		!owned || owner != 1002 {
		fmt.Fprintln(stderr, "host_volume_unavailable")
		return 1
	}
	lockPath := filepath.Join(*root, "host.lock")
	lockInfo, err := os.Lstat(lockPath)
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm() != 0600 {
		fmt.Fprintln(stderr, "host_lock_unavailable")
		return 1
	}
	lockOwner, ok := infoOwner(lockInfo)
	if !ok || lockOwner != 1002 {
		fmt.Fprintln(stderr, "host_lock_unavailable")
		return 1
	}
	lock, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil || syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		if lock != nil {
			_ = lock.Close()
		}
		fmt.Fprintln(stderr, "host_active")
		return 1
	}
	defer lock.Close()
	// An old socket may survive a crash. It must not be accepting connections.
	socket, err := net.DialTimeout("unix", e2host.ProductSocketPath, time.Second)
	if err == nil {
		_ = socket.Close()
		fmt.Fprintln(stderr, "host_active")
		return 1
	}
	dsn, err := hostSecret(filepath.Join(*root, "host.dsn"), 4096)
	if err != nil {
		fmt.Fprintln(stderr, "host_credentials_unavailable")
		return 1
	}
	defer clear(dsn)
	key, err := hostSecret(filepath.Join(*root, "audit.key"), 4096)
	if err != nil || len(key) < 32 {
		clear(key)
		fmt.Fprintln(stderr, "host_credentials_unavailable")
		return 1
	}
	defer clear(key)
	epochRaw, err := hostSecret(filepath.Join(*root, "epoch.json"), 4096)
	if err != nil {
		fmt.Fprintln(stderr, "host_epoch_unavailable")
		return 1
	}
	var epoch e2checkpoint.Epoch
	decodeErr := json.Unmarshal(epochRaw, &epoch)
	clear(epochRaw)
	if decodeErr != nil {
		fmt.Fprintln(stderr, "host_epoch_unavailable")
		return 1
	}
	pool, err := sql.Open("pgx", strings.TrimSpace(string(dsn)))
	if err != nil {
		fmt.Fprintln(stderr, "host_storage_unavailable")
		return 1
	}
	defer pool.Close()
	pool.SetMaxOpenConns(4)
	pool.SetConnMaxLifetime(time.Minute)
	var role string
	if pool.QueryRowContext(ctx, "SELECT current_user").Scan(&role) != nil || role != "product_e2_host" {
		fmt.Fprintln(stderr, "host_role_unavailable")
		return 1
	}
	host, err := e2host.New(pool, epoch, "product-audit-key-v3", key, filepath.Join(*root, "journals"))
	if err != nil {
		fmt.Fprintln(stderr, "host_storage_unavailable")
		return 1
	}
	if *mode == "status" {
		status, err := host.InspectUnfinished(ctx, *runRef)
		if err != nil {
			fmt.Fprintln(stderr, "recovery_not_eligible")
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(status)
		return 0
	}
	if *revision == 0 || *checkpoint == "" || *operator == "" || *project == "" ||
		*dockerSocket != "/var/run/docker.sock" {
		return 2
	}
	selfID, err := os.Hostname()
	if err != nil {
		fmt.Fprintln(stderr, "termination_unverified")
		return 1
	}
	status, err := host.RecoverUnfinished(ctx, e2host.RecoveryRequest{
		RunRef: *runRef, ExpectedRevision: *revision, ExpectedCheckpoint: *checkpoint, Operator: *operator,
	}, *dockerSocket, *project, selfID)
	if err != nil {
		fmt.Fprintln(stderr, "recovery_not_eligible")
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(status)
	return 0
}
