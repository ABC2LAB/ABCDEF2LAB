// Command eval-meter is the runner-side front for one evaluation run. It runs
// as one of three roles:
//
//   - engine: forwards each request it receives to the target unchanged and
//     counts it, so the runner can seal new sends and show that in-flight
//     requests through this front reached zero before the post snapshot. It
//     can only account for requests that reach it; network isolation and the
//     probe are what show the engine had no other path to the target.
//   - observer: forwards only the single registered scoreboard GET and rejects
//     everything else, logging each decision.
//   - probe: reports which network paths are reachable from inside the engine
//     container's namespace; the runner, not the probe, owns the expectations.
//
// It never logs paths, queries, headers or bodies, never receives the runner
// signing key and never needs the Docker socket.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const usage = "Usage: eval-meter -role engine|observer -listen ADDR -upstream http://HOST:PORT -log-dir PRIVATE_DIR [-allow-path /PATH]\n       eval-meter -role probe < PLAN_JSON\n"

const (
	invalidInput        = "eval_meter_input_invalid"
	unsupportedPlatform = "eval_meter_platform_unsupported"
	privateDirRequired  = "eval_meter_private_directory_required"
	logExists           = "eval_meter_log_exists"
	storageUnavailable  = "eval_meter_storage_unavailable"
	listenUnavailable   = "eval_meter_listen_unavailable"
	serveFailed         = "eval_meter_serve_failed"
	probeOutputFailed   = "eval_meter_probe_output_unavailable"
)

const (
	roleEngine   = "engine"
	roleObserver = "observer"
	roleProbe    = "probe"
)

// Slow clients must not hold the front open indefinitely; the target's own
// behaviour is not limited by these values.
const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 10 * time.Second
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("eval-meter", flag.ContinueOnError)
	// The default diagnostics echo argument values, which may include paths.
	flags.SetOutput(io.Discard)
	role := flags.String("role", "", "engine, observer or probe")
	listen := flags.String("listen", "", "listen address")
	upstream := flags.String("upstream", "", "target origin http://HOST:PORT")
	logDir := flags.String("log-dir", "", "existing private 0700 directory for evidence")
	allowPath := flags.String("allow-path", "", "observer: the single registered GET path")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fail(stderr, invalidInput)
	}
	switch *role {
	case roleProbe:
		if *listen != "" || *upstream != "" || *logDir != "" || *allowPath != "" {
			return fail(stderr, invalidInput)
		}
		return runProbe(stdin, stdout, stderr)
	case roleEngine, roleObserver:
	default:
		return fail(stderr, invalidInput)
	}
	target, ok := parseUpstream(*upstream)
	if !ok || *listen == "" || *logDir == "" || (*role == roleEngine) != (*allowPath == "") {
		return fail(stderr, invalidInput)
	}
	if *role == roleObserver && !validAllowPath(*allowPath) {
		return fail(stderr, invalidInput)
	}
	dir, code := openPrivateDir(*logDir)
	if code != "" {
		return fail(stderr, code)
	}
	defer closeOrReport(dir, stderr)

	var handler http.Handler
	var seal func()
	var terminate func(string)
	var finish func() error
	switch *role {
	case roleEngine:
		m, code := newEngineMeter(dir, target, time.Now)
		if code != "" {
			return fail(stderr, code)
		}
		handler, seal, terminate, finish = m, m.seal, m.terminate, m.close
	case roleObserver:
		o, code := newObserver(dir, target, *allowPath, time.Now)
		if code != "" {
			return fail(stderr, code)
		}
		handler, finish = o, o.close
	}

	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		if err := finish(); err != nil {
			fmt.Fprintln(stderr, storageUnavailable)
		}
		return fail(stderr, listenUnavailable)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: readHeaderTimeout}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, append([]os.Signal{syscall.SIGTERM, os.Interrupt}, sealSignals...)...)
	defer signal.Stop(signals)
	exit := 0
	reason := "signal"
loop:
	for {
		select {
		case sig := <-signals:
			if isSealSignal(sig) {
				if seal != nil {
					seal()
				}
				continue
			}
			break loop
		case err := <-serveErr:
			if !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintln(stderr, serveFailed)
				exit = 1
			}
			reason = "serve_error"
			break loop
		}
	}
	// Record in-flight state before shutdown waits for it, so a stop that was
	// never sealed or drained is distinguishable afterwards.
	if terminate != nil {
		terminate(reason)
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		fmt.Fprintln(stderr, serveFailed)
		exit = 1
	}
	if err := finish(); err != nil {
		fmt.Fprintln(stderr, storageUnavailable)
		exit = 1
	}
	return exit
}

func fail(stderr io.Writer, code string) int {
	fmt.Fprintln(stderr, code)
	if code == invalidInput {
		fmt.Fprint(stderr, usage)
		return 2
	}
	return 1
}

func closeOrReport(c io.Closer, stderr io.Writer) {
	if err := c.Close(); err != nil {
		fmt.Fprintln(stderr, storageUnavailable)
	}
}

// parseUpstream accepts only a bare http origin; the front adds nothing to the
// request path and never talks TLS to the target.
func parseUpstream(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.Opaque != "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Port() == "" {
		return nil, false
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, true
}

func validAllowPath(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#%\\ ") || strings.Contains(path, "//") {
		return false
	}
	for _, segment := range strings.Split(path[1:], "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	for i := 0; i < len(path); i++ {
		if path[i] < 0x21 || path[i] > 0x7e {
			return false
		}
	}
	return true
}
