package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	proc "github.com/ayush/cos-lite/internal/process"
	"github.com/ayush/cos-lite/internal/workspace"
)

type Executor struct {
	WS *workspace.Workspace
	PM *proc.Manager
}

func (e *Executor) ExecDefinition() Tool {
	return Tool{Definition: Definition{
		Name: "exec_command", Title: "Run command",
		Description: "Run one shell command (or a small sequential batch) in an approved Ubuntu project. Project roots are " + e.WS.RootSummary() + ". With multiple projects, workdir must start with /<project>/.... Long-running commands return a session_id that write_stdin can poll or interact with. tty=true allocates a real pseudo-terminal via util-linux script.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"cmd":     map[string]any{"type": "string", "description": "Shell command."},
			"cmds":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 20, "description": "Sequential commands; joined with set -e semantics."},
			"workdir": map[string]any{"type": "string", "default": "."}, "tty": map[string]any{"type": "boolean", "default": false},
			"yield_time_ms":    map[string]any{"type": "integer", "minimum": 0, "maximum": 30000, "default": 1000},
			"timeout_ms":       map[string]any{"type": "integer", "minimum": 100, "maximum": 7200000, "default": 1800000},
			"max_output_chars": map[string]any{"type": "integer", "minimum": 1024, "maximum": 8388608, "default": 1048576},
		}},
		Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": false},
	}, Handler: e.HandleExec}
}

func (e *Executor) StdinDefinition() Tool {
	return Tool{Definition: Definition{
		Name: "write_stdin", Title: "Interact with running command",
		Description: "Write characters to a running exec_command session or poll it for new output. Sending the single control character \\u0003 delivers SIGINT to the whole process group.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"session_id": map[string]any{"type": "integer", "minimum": 1}, "chars": map[string]any{"type": "string"}, "yield_time_ms": map[string]any{"type": "integer", "minimum": 0, "maximum": 30000, "default": 250},
		}, "required": []string{"session_id"}},
		Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": false},
	}, Handler: e.HandleStdin}
}

func (e *Executor) HandleExec(_ context.Context, args map[string]any) (Result, error) {
	cmd := strArg(args, "cmd")
	if arr := stringSliceArg(args, "cmds"); len(arr) > 0 {
		if cmd != "" {
			return Error("provide cmd or cmds, not both"), nil
		}
		for i := range arr {
			arr[i] = strings.TrimSpace(arr[i])
		}
		cmd = "set -e\n" + strings.Join(arr, "\n")
	}
	if strings.TrimSpace(cmd) == "" {
		return Error("cmd or cmds is required"), nil
	}
	wd := strArg(args, "workdir")
	if wd == "" {
		wd = "."
	}
	resolved, err := e.WS.Resolve(wd)
	if err != nil {
		return Error(err.Error()), nil
	}
	yield := time.Duration(intArg(args, "yield_time_ms", 1000)) * time.Millisecond
	timeout := time.Duration(intArg(args, "timeout_ms", int(proc.DefaultTimeout/time.Millisecond))) * time.Millisecond
	maxOut := intArg(args, "max_output_chars", proc.DefaultMaxOutput)
	res, err := e.PM.Start(proc.StartOptions{Command: cmd, Workdir: resolved, TTY: boolArg(args, "tty", false), Yield: yield, Timeout: timeout, MaxOutput: maxOut, Env: proc.SanitizedEnv()})
	if err != nil {
		return Error(err.Error()), nil
	}
	text := formatProcessResult(res)
	return Result{Content: []Content{{"type": "text", "text": text}}, Structured: res}, nil
}

func (e *Executor) HandleStdin(_ context.Context, args map[string]any) (Result, error) {
	id := intArg(args, "session_id", 0)
	if id <= 0 {
		return Error("valid session_id is required"), nil
	}
	yield := time.Duration(intArg(args, "yield_time_ms", 250)) * time.Millisecond
	chars, has := args["chars"]
	var res proc.Result
	var err error
	if has {
		s, ok := chars.(string)
		if !ok {
			return Error("chars must be a string"), nil
		}
		res, err = e.PM.Write(id, s, yield)
	} else {
		res, err = e.PM.Poll(id, yield)
	}
	if err != nil {
		return Error(err.Error()), nil
	}
	return Result{Content: []Content{{"type": "text", "text": formatProcessResult(res)}}, Structured: res}, nil
}

func formatProcessResult(r proc.Result) string {
	var b strings.Builder
	if r.SessionID != nil {
		fmt.Fprintf(&b, "session_id: %d\n", *r.SessionID)
	}
	if r.ExitCode != nil {
		fmt.Fprintf(&b, "exit_code: %d\n", *r.ExitCode)
	} else {
		b.WriteString("status: running\n")
	}
	if r.Truncated {
		b.WriteString("warning: retained output hit its configured cap\n")
	}
	if r.Output != "" {
		b.WriteString("\n")
		b.WriteString(r.Output)
	}
	return strings.TrimRight(b.String(), "\n")
}
