// Command e2-audit-verify authenticates a closed audit journal against an
// explicitly supplied, independently trusted checkpoint. It never sends traffic.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/Autumn-27/artex/transmit/auditlog"
)

const maxCheckpointBytes = 4096

const usage = "Usage: e2-audit-verify --journal FILE --checkpoint TRUSTED_FILE --key-file PRIVATE_FILE [--journal-format registered-get-v1|e2-http-v2|e2-http-v1] [--timeline [--max-events N]]\n"

var errInput = errors.New("audit_input_unavailable")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("e2-audit-verify", flag.ContinueOnError)
	// flag's default diagnostics echo arguments, which may contain private paths.
	flags.SetOutput(io.Discard)
	journal := flags.String("journal", "", "closed journal file")
	checkpoint := flags.String("checkpoint", "", "independently trusted latest checkpoint")
	key := flags.String("key-file", "", "private binary HMAC key file")
	format := flags.String("journal-format", registeredGETJournalFormat, "journal wire format")
	timeline := flags.Bool("timeline", false, "print authenticated event timeline")
	maxEvents := flags.Int("max-events", auditlog.MaxTimelineEvents, "maximum timeline events")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		if n, err := io.WriteString(stdout, usage); err != nil || n != len(usage) {
			return failure(stderr, "audit_output_unavailable")
		}
		return 0
	} else if err != nil || flags.NArg() != 0 || *journal == "" || *checkpoint == "" || *key == "" || *maxEvents < 1 || *maxEvents > auditlog.MaxTimelineEvents ||
		*format != registeredGETJournalFormat && !httpJournalFormat(*format) {
		if n, err := io.WriteString(stderr, usage); err != nil || n != len(usage) {
			return 1 // A broken diagnostic sink cannot receive its own error code.
		}
		return 2
	}
	usedLimit := false
	flags.Visit(func(f *flag.Flag) { usedLimit = usedLimit || f.Name == "max-events" })
	if usedLimit && !*timeline || httpJournalFormat(*format) && *timeline {
		if n, err := io.WriteString(stderr, usage); err != nil || n != len(usage) {
			return 1
		}
		return 2
	}
	if runtime.GOOS != "linux" {
		return failure(stderr, "audit_platform_unsupported")
	}
	if ctx == nil {
		return failure(stderr, "audit_input_invalid")
	}
	var result any
	var code string
	if httpJournalFormat(*format) {
		result, code = verifyHTTPFiles(ctx, *format, *journal, *checkpoint, *key)
	} else {
		result, code = verifyFiles(ctx, *journal, *checkpoint, *key, *timeline, *maxEvents)
	}
	if code != "" {
		return failure(stderr, code)
	}
	if ctx.Err() != nil {
		return failure(stderr, "audit_verification_canceled")
	}
	body, err := json.Marshal(result)
	if err != nil {
		return failure(stderr, "audit_output_unavailable")
	}
	body = append(body, '\n')
	if n, err := stdout.Write(body); err != nil || n != len(body) {
		return failure(stderr, "audit_output_unavailable")
	}
	return 0
}

func failure(stderr io.Writer, code string) int {
	fmt.Fprintln(stderr, code)
	return 1
}

func verifyFiles(ctx context.Context, journalPath, checkpointPath, keyPath string, timeline bool, maxEvents int) (result any, code string) {
	if ctx.Err() != nil {
		return nil, "audit_verification_canceled"
	}
	key, err := readBounded(keyPath, auditlog.KeyBytes, true)
	if err != nil {
		return nil, "audit_input_unavailable"
	}
	defer clear(key)
	if len(key) != auditlog.KeyBytes {
		return nil, "audit_input_invalid"
	}
	encoded, err := readBounded(checkpointPath, maxCheckpointBytes, false)
	if err != nil {
		return nil, "audit_input_unavailable"
	}
	checkpoint, err := decodeCheckpoint(encoded)
	if err != nil {
		return nil, "audit_input_invalid"
	}
	journal, err := openRegular(journalPath, false)
	if err != nil {
		return nil, "audit_input_unavailable"
	}
	defer func() {
		if err := journal.Close(); err != nil && code == "" {
			result, code = nil, "audit_input_unavailable"
		}
	}()
	if timeline {
		result, err = auditlog.VerifyTimeline(ctx, journal, key, checkpoint, maxEvents)
	} else {
		result, err = auditlog.Verify(ctx, journal, key, checkpoint)
	}
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			code = "audit_verification_canceled"
		case errors.Is(err, auditlog.ErrIntegrity):
			code = "audit_integrity_failed"
		case errors.Is(err, auditlog.ErrConfig):
			code = "audit_input_invalid"
		case errors.Is(err, auditlog.ErrTimelineLimit):
			code = "audit_timeline_limit_exceeded"
		default:
			code = "audit_input_unavailable"
		}
		return nil, code
	}
	return result, ""
}

func readBounded(path string, limit int64, private bool) ([]byte, error) {
	file, err := openRegular(path, private)
	if err != nil {
		return nil, errInput
	}
	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(data)) > limit {
		clear(data)
		return nil, errInput
	}
	return data, nil
}

func decodeCheckpoint(encoded []byte) (auditlog.Checkpoint, error) {
	var checkpoint auditlog.Checkpoint
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&checkpoint) != nil {
		return checkpoint, errInput
	}
	var compact bytes.Buffer
	if json.Compact(&compact, encoded) != nil {
		return checkpoint, errInput
	}
	canonical, err := json.Marshal(checkpoint)
	// Compare the complete serialization to reject duplicates, aliases, missing
	// fields and trailing values without silently normalizing the trust anchor.
	if err != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return auditlog.Checkpoint{}, errInput
	}
	return checkpoint, nil
}
