// Command e2-audit-keygen provisions a new private binary audit key. It never
// prints key material, changes existing keys, or sends network traffic.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"syscall"

	"github.com/Autumn-27/artex/transmit/auditlog"
)

const usage = "Usage: e2-audit-keygen --key-file NEW_PRIVATE_FILE\n"

const (
	created             = "audit_key_created"
	invalidInput        = "audit_key_input_invalid"
	unsupportedPlatform = "audit_key_platform_unsupported"
	entropyUnavailable  = "audit_key_entropy_unavailable"
	privateDirRequired  = "audit_key_private_directory_required"
	alreadyExists       = "audit_key_exists"
	storageUnavailable  = "audit_key_storage_unavailable"
	generationCanceled  = "audit_key_generation_canceled"
	outputUnavailable   = "audit_key_output_unavailable"
)

var keyName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type syncCloser interface {
	Sync() error
	Close() error
}

type keyWriter interface {
	io.Writer
	syncCloser
}

type keyOpener func(string) (keyWriter, syncCloser, string)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("e2-audit-keygen", flag.ContinueOnError)
	// The default diagnostics echo private paths and unknown argument values.
	flags.SetOutput(io.Discard)
	path := flags.String("key-file", "", "new private 32-byte binary key file")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		if !writeMessage(stdout, usage) {
			return failure(stderr, outputUnavailable)
		}
		return 0
	} else if err != nil || flags.NArg() != 0 || !validKeyPath(*path) {
		if !writeMessage(stderr, usage) {
			return 1
		}
		return 2
	}
	if runtime.GOOS != "linux" {
		return failure(stderr, unsupportedPlatform)
	}
	if ctx == nil {
		return failure(stderr, invalidInput)
	}
	if code := generateKey(ctx, *path, rand.Reader, openPrivateKey); code != "" {
		return failure(stderr, code)
	}
	if ctx.Err() != nil {
		return failure(stderr, generationCanceled)
	}
	if !writeMessage(stdout, created+"\n") {
		// A durable key remains even when its acknowledgement cannot be delivered.
		return failure(stderr, outputUnavailable)
	}
	return 0
}

func validKeyPath(path string) bool {
	_, name := filepath.Split(path)
	return keyName.MatchString(name)
}

func generateKey(ctx context.Context, path string, entropy io.Reader, open keyOpener) string {
	if ctx == nil || !validKeyPath(path) {
		return invalidInput
	}
	if ctx.Err() != nil {
		return generationCanceled
	}
	var key [auditlog.KeyBytes]byte
	defer clear(key[:])
	if _, err := io.ReadFull(entropy, key[:]); err != nil {
		return entropyUnavailable
	}
	if ctx.Err() != nil {
		return generationCanceled
	}
	file, directory, code := open(path)
	if code != "" {
		return code
	}
	return persistKey(ctx, file, directory, key[:])
}

// persistKey owns both handles. File I/O stays synchronous even if canceled;
// abandoning a goroutine could report failure while it still writes a key.
// Failed or unacknowledged files are deliberately never removed or overwritten.
func persistKey(ctx context.Context, file keyWriter, directory syncCloser, key []byte) (code string) {
	defer func() {
		if err := directory.Close(); err != nil && code == "" {
			code = storageUnavailable
		}
	}()
	fileClosed := false
	defer func() {
		if !fileClosed {
			if err := file.Close(); err != nil && code == "" {
				code = storageUnavailable
			}
		}
	}()
	if ctx.Err() != nil {
		return generationCanceled
	}
	if n, err := file.Write(key); err != nil || n != len(key) {
		return storageUnavailable
	}
	if err := file.Sync(); err != nil {
		return storageUnavailable
	}
	err := file.Close()
	fileClosed = true
	if err != nil {
		return storageUnavailable
	}
	if err := directory.Sync(); err != nil {
		return storageUnavailable
	}
	if ctx.Err() != nil {
		return generationCanceled
	}
	return ""
}

func writeMessage(output io.Writer, message string) bool {
	n, err := io.WriteString(output, message)
	return err == nil && n == len(message)
}

func failure(stderr io.Writer, code string) int {
	if !writeMessage(stderr, code+"\n") {
		return 1 // A broken diagnostic sink cannot report its own failure.
	}
	return 1
}
