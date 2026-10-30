package agent

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// ControlledHTTPToolName is the only tool exposed by the controlled-read
// session. The server must bind a sanitized, already-authorized tool instance
// to the execution before placing it in a worker context.
const ControlledHTTPToolName = "controlled_http_get"

// ControlledReadExecution is the immutable server binding for one restricted
// read. Tool output is sanitized by the server-side tool before it reaches the
// model; this package does not load assets, transcripts, or credentials.
type ControlledReadExecution struct {
	TaskID   int64
	IntentID int64
	RunRef   string
	Tool     actool.CoreTool
}

type controlledReadExecutionKey struct{}

// WithControlledReadExecution marks a Worker execution as restricted. A
// malformed binding is rejected by Worker.execute and never falls back to the
// ordinary worker tool set.
func WithControlledReadExecution(ctx context.Context, execution ControlledReadExecution) context.Context {
	return context.WithValue(ctx, controlledReadExecutionKey{}, execution)
}

func controlledReadExecutionFrom(ctx context.Context) (ControlledReadExecution, bool) {
	execution, ok := ctx.Value(controlledReadExecutionKey{}).(ControlledReadExecution)
	return execution, ok
}

func intentIDOf(intent *db.Node) int64 {
	if intent == nil {
		return 0
	}
	return intent.ID
}

var controlledRunRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func validateControlledRead(execution ControlledReadExecution, taskID, intentID int64, intent *db.Node, message string) error {
	if execution.TaskID <= 0 || execution.TaskID != taskID {
		return fmt.Errorf("controlled read task binding mismatch")
	}
	if execution.IntentID <= 0 || intent == nil || execution.IntentID != intentID || intent.ID != execution.IntentID {
		return fmt.Errorf("controlled read intent binding mismatch")
	}
	if !controlledRunRef.MatchString(execution.RunRef) {
		return fmt.Errorf("controlled read run reference is invalid")
	}
	if execution.Tool == nil || isNilCoreTool(execution.Tool) || execution.Tool.Name() != ControlledHTTPToolName {
		return fmt.Errorf("controlled read tool binding is invalid")
	}
	if strings.TrimSpace(message) != "" {
		return fmt.Errorf("controlled read does not accept human messages")
	}
	return nil
}

func isNilCoreTool(tool actool.CoreTool) bool {
	v := reflect.ValueOf(tool)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

const controlledReadSystemPrompt = `You execute a restricted registered read and describe execution evidence only.
Use only the registered controlled_http_get tool supplied in this session.
Use registered references from the tool schema; never invent URLs, credentials,
headers, cookies, tokens, or arbitrary commands. Do not call or discuss any
other tool. Report only the sanitized execution receipt and captured evidence.
Do not classify vulnerabilities, access success, or absence of vulnerabilities.
Vulnerability judgment is UNDECIDABLE here: E3 evaluation is pending.
This session cannot create findings or perform state-changing requests.`

func (w *Worker) executeControlledRead(ctx context.Context, name string, taskID int64, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), execution ControlledReadExecution) (harness.TerminalReason, WriteCounts, error) {
	if err := validateControlledRead(execution, taskID, intentIDOf(intent), intent, ""); err != nil {
		return harness.ReasonModelError, WriteCounts{}, err
	}

	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, IntentID: intent.ID})
	maxDur, _ := clampMaxDuration(taskClockFrom(ctx).DeadlineUnix, w.runTimeout)
	opts := agentcore.Options{
		Provider:               w.prov,
		SystemPrompt:           []string{controlledReadSystemPrompt},
		Tools:                  []actool.CoreTool{execution.Tool},
		SessionID:              "controlled-read-" + execution.RunRef,
		PermissionMode:         permission.ModeBypass,
		DisableBackgroundTasks: true,
		MaxTurns:               w.maxTurns,
		MaxDuration:            maxDur,
		WorkingDir:             "",
		NonStreaming:           w.nonStreaming(),
		MaxTokens:              w.maxTokens(),
	}
	if hooks != nil {
		opts.Hooks = hooks
	}
	s := agentcore.NewSession(opts)
	defer s.Close()

	// RunRef is an opaque safe session identity only; no ordinary transcript is
	// resumed or written, and no prior worker message is injected.
	if s.SessionID() == "" {
		return harness.ReasonModelError, WriteCounts{}, fmt.Errorf("controlled read session was not created")
	}
	input := "Execute the registered controlled read and return its sanitized result."
	_, reason, err := captureRunSession(ctx, s, input, func(r db.Activity) {
		if emit != nil {
			r.NodeID = &intent.ID
			r.Worker = name
			emit(r)
		}
	})
	return reason, WriteCounts{}, err
}
