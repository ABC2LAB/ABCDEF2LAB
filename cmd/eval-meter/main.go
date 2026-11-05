// Command eval-meter is the runner-side front for one evaluation run. It runs
// as one of these roles:
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
//   - llm-gateway: the engine's only route to its LLM. api pins the serving
//     provider on every request and holds the API key; local forwards as is.
//   - api-front: publishes the engine API on the host loopback, because a
//     container on internal networks only cannot publish a port itself.
//   - e2-stub: a pass-through fixture in the improved engine's E② slot. It admits
//     every request and issues an attempt ID; it proves wiring, never control.
//
// It never logs paths, queries, headers or bodies, never receives the runner
// signing key and never needs the Docker socket. Only the llm-gateway role may
// see the LLM API key; every other role refuses to start if it is present.
package main

import (
	"context"
	"crypto/rand"
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

const usage = "Usage: eval-meter -role engine|observer -listen ADDR -upstream http://HOST:PORT -log-dir PRIVATE_DIR [-allow-path /PATH]\n" +
	"                  [engine: -drain-grace DURATION]\n" +
	"       eval-meter -role llm-gateway -backend api|local -listen ADDR -upstream ORIGIN -allow-path /PATH -log-dir PRIVATE_DIR\n" +
	"                  [api: -provider SLUG -generation-path /PATH -verify-attempts N -verify-interval DURATION, key in " + llmKeyEnv + "]\n" +
	"       eval-meter -role api-front -listen ADDR -upstream http://ENGINE:PORT\n" +
	"       eval-meter -role e2-stub -listen ADDR -upstream http://METER:PORT -log-dir PRIVATE_DIR\n" +
	"       eval-meter -role probe < PLAN_JSON\n" +
	"       eval-meter -role statfs < {\"paths\":[\"/MOUNT/POINT\"]}  (inside the PID namespace of the measured container)\n"

const (
	invalidInput        = "eval_meter_input_invalid"
	unsupportedPlatform = "eval_meter_platform_unsupported"
	privateDirRequired  = "eval_meter_private_directory_required"
	logExists           = "eval_meter_log_exists"
	storageUnavailable  = "eval_meter_storage_unavailable"
	listenUnavailable   = "eval_meter_listen_unavailable"
	serveFailed         = "eval_meter_serve_failed"
	probeOutputFailed   = "eval_meter_probe_output_unavailable"
	keyNotAllowed       = "eval_meter_key_not_allowed"
	statfsFailed        = "eval_meter_statfs_failed"
	statfsNotTmpfs      = "eval_meter_statfs_not_tmpfs"
	statfsNotMounted    = "eval_meter_statfs_not_mounted"
	statfsMountMismatch = "eval_meter_statfs_mount_mismatch"
)

const (
	roleEngine   = "engine"
	roleObserver = "observer"
	roleProbe    = "probe"
	roleGateway  = "llm-gateway"
	roleAPIFront = "api-front"
	roleStatfs   = "statfs"
	roleE2Stub   = "e2-stub"
)

// Bounds on the gateway's verification so a run cannot wait on it forever.
const (
	maxVerifyAttempts = 100
	maxVerifyInterval = 10 * time.Minute
	maxKeyLength      = 512
)

// Upper bound on how long the engine front waits after seal before cancelling
// open requests; the runner's own drain timeout must be longer.
const maxDrainGrace = 10 * time.Minute

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
	role := flags.String("role", "", "engine, observer, probe, llm-gateway, api-front, e2-stub or statfs")
	listen := flags.String("listen", "", "listen address")
	upstream := flags.String("upstream", "", "target origin http://HOST:PORT")
	logDir := flags.String("log-dir", "", "existing private 0700 directory for evidence")
	allowPath := flags.String("allow-path", "", "observer: the single registered GET path; llm-gateway: the chat completions path")
	backend := flags.String("backend", "", "llm-gateway: api or local")
	provider := flags.String("provider", "", "llm-gateway api: full provider slug to pin")
	sampling := flags.String("sampling", "", "llm-gateway api: JSON object of sampling parameters to pin")
	generationPath := flags.String("generation-path", "", "llm-gateway api: generation stats path")
	verifyAttempts := flags.Int("verify-attempts", 0, "llm-gateway api: lookups per generation id")
	verifyInterval := flags.Duration("verify-interval", 0, "llm-gateway api: wait between lookups")
	drainGrace := flags.Duration("drain-grace", 0, "engine: after seal, cancel requests still open this long (0: wait)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fail(stderr, invalidInput)
	}
	key := os.Getenv(llmKeyEnv)
	gatewayFlags := *backend != "" || *provider != "" || *sampling != "" || *generationPath != "" ||
		*verifyAttempts != 0 || *verifyInterval != 0
	if *role != roleGateway && key != "" {
		// A key in a meter, observer or probe container means the compose wiring is wrong.
		return fail(stderr, keyNotAllowed)
	}
	if *role != roleGateway && gatewayFlags {
		return fail(stderr, invalidInput)
	}
	if (*role != roleEngine && *drainGrace != 0) || *drainGrace < 0 || *drainGrace > maxDrainGrace {
		return fail(stderr, invalidInput)
	}
	var gatewayCfg gatewayConfig
	switch *role {
	case roleProbe:
		if *listen != "" || *upstream != "" || *logDir != "" || *allowPath != "" {
			return fail(stderr, invalidInput)
		}
		return runProbe(stdin, stdout, stderr)
	case roleStatfs:
		if *listen != "" || *upstream != "" || *logDir != "" || *allowPath != "" {
			return fail(stderr, invalidInput)
		}
		return runStatfs(stdin, stdout, stderr)
	case roleAPIFront:
		engine, ok := parseUpstream(*upstream)
		if !ok || *listen == "" || *logDir != "" || *allowPath != "" {
			return fail(stderr, invalidInput)
		}
		return serve(*listen, newAPIFront(engine), nil, nil, func() error { return nil }, stderr)
	case roleEngine, roleObserver, roleE2Stub:
	case roleGateway:
		cfg, ok := gatewayFromFlags(*backend, *upstream, *allowPath, *provider, *sampling, *generationPath,
			*verifyAttempts, *verifyInterval, key)
		if !ok || *listen == "" || *logDir == "" {
			return fail(stderr, invalidInput)
		}
		gatewayCfg = cfg
	default:
		return fail(stderr, invalidInput)
	}
	target, ok := parseUpstream(*upstream)
	if *role == roleGateway {
		target, ok = gatewayCfg.upstream, true
	}
	// Only the observer and the gateway take an allow-path; the engine meter and the stub forward everything.
	needsAllowPath := *role == roleObserver || *role == roleGateway
	if !ok || *listen == "" || *logDir == "" || needsAllowPath == (*allowPath == "") {
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
		m, code := newEngineMeter(dir, target, time.Now, *drainGrace)
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
	case roleGateway:
		g, code := newLLMGateway(dir, gatewayCfg, defaultUpstreamTransport(), time.Now, time.Sleep)
		if code != "" {
			return fail(stderr, code)
		}
		handler, seal, finish = g, g.seal, g.close
	case roleE2Stub:
		stub, code := newE2Stub(dir, target, rand.Reader, time.Now)
		if code != "" {
			return fail(stderr, code)
		}
		handler, finish = stub, stub.close
	}
	return serve(*listen, handler, seal, terminate, finish, stderr)
}

// listening is told the bound address once a front accepts connections. Tests start fronts on port 0 and
// learn the address here instead of picking a free port, closing it and hoping nothing else takes it.
var listening = func(net.Addr) {}

// serve runs one front until SIGTERM/interrupt; the seal signal only seals.
func serve(listen string, handler http.Handler, seal func(), terminate func(string), finish func() error,
	stderr io.Writer) int {
	// Handlers go in before the port opens: a seal or stop sent as soon as the front is reachable must be
	// recorded, not hit the default action.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, append([]os.Signal{syscall.SIGTERM, os.Interrupt}, sealSignals...)...)
	defer signal.Stop(signals)
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		if err := finish(); err != nil {
			fmt.Fprintln(stderr, storageUnavailable)
		}
		return fail(stderr, listenUnavailable)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: readHeaderTimeout}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	listening(listener.Addr())
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

// gatewayFromFlags checks the per-backend rules: api talks https to the
// provider and must pin the provider and sampling and verify; local talks http
// to a fixed address on the host and must carry no key, so the API key can
// never reach a local server. Sampling is not pinned for local yet, so local
// refuses the flag rather than pretending to apply it.
func gatewayFromFlags(backend, upstream, allowPath, provider, sampling, generationPath string, attempts int,
	interval time.Duration, key string) (gatewayConfig, bool) {
	cfg := gatewayConfig{backend: backend, allowPath: allowPath, provider: provider, generationPath: generationPath,
		key: key, verifyAttempts: attempts, verifyInterval: interval}
	if !validAllowPath(allowPath) {
		return cfg, false
	}
	switch backend {
	case backendAPI:
		cfg.upstream = parseGatewayUpstream(upstream, "https")
		params, pinned := parseSampling(sampling)
		cfg.sampling = params
		return cfg, cfg.upstream != nil && providerSlugRe.MatchString(provider) && pinned && validAllowPath(generationPath) &&
			attempts >= 1 && attempts <= maxVerifyAttempts && interval > 0 && interval <= maxVerifyInterval &&
			validKey(key)
	case backendLocal:
		cfg.upstream = parseGatewayUpstream(upstream, "http")
		return cfg, cfg.upstream != nil && provider == "" && sampling == "" && generationPath == "" && attempts == 0 &&
			interval == 0 && key == ""
	}
	return cfg, false
}

// parseGatewayUpstream accepts a bare origin; the gateway forwards the
// registered path unchanged, so the engine's base URL path must match it.
func parseGatewayUpstream(raw, scheme string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != scheme || u.Hostname() == "" || u.User != nil || u.Opaque != "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}
}

// validKey keeps the key usable as a header value without echoing it anywhere.
func validKey(key string) bool {
	if key == "" || len(key) > maxKeyLength {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return false
		}
	}
	return true
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
