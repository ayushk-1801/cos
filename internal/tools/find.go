package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ayush/cos-lite/internal/workspace"
)

type Finder struct{ WS *workspace.Workspace }

func (f *Finder) Definition() Tool {
	return Tool{Definition: Definition{
		Name: "find", Title: "Search workspace",
		Description: "Search file contents with ripgrep inside approved projects. Project roots are " + f.WS.RootSummary() + ". With multiple projects, path must start with /<project>/.... Returns path:line:column matches with bounded results.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"query": map[string]any{"type": "string"}, "path": map[string]any{"type": "string", "default": "."}, "glob": map[string]any{"type": "string"}, "case_sensitive": map[string]any{"type": "boolean", "default": false}, "fixed_strings": map[string]any{"type": "boolean", "default": false}, "max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "default": 100},
		}, "required": []string{"query"}},
		Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
	}, Handler: f.Handle}
}

func (f *Finder) Handle(ctx context.Context, args map[string]any) (Result, error) {
	query, err := requireString(args, "query")
	if err != nil {
		return Error(err.Error()), nil
	}
	path := strArg(args, "path")
	if path == "" {
		path = "."
	}
	resolved, err := f.WS.Resolve(path)
	if err != nil {
		return Error(err.Error()), nil
	}
	max := intArg(args, "max_results", 100)
	if max < 1 {
		max = 1
	}
	if max > 500 {
		max = 500
	}
	if _, look := exec.LookPath("rg"); look != nil {
		return Error("ripgrep (rg) is required; install it with: sudo apt install ripgrep"), nil
	}
	argv := []string{"--line-number", "--column", "--no-heading", "--color=never"}
	if !boolArg(args, "case_sensitive", false) {
		argv = append(argv, "--ignore-case")
	}
	if boolArg(args, "fixed_strings", false) {
		argv = append(argv, "--fixed-strings")
	}
	if g := strArg(args, "glob"); g != "" {
		argv = append(argv, "--glob", g)
	}
	argv = append(argv, "--", query, resolved)
	cmd := exec.CommandContext(ctx, "rg", argv...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Error("rg stdout: " + err.Error()), nil
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{W: &stderr, N: 32 * 1024}
	if err := cmd.Start(); err != nil {
		return Error("rg start: " + err.Error()), nil
	}

	// Stream instead of CombinedOutput so an extremely common query cannot allocate all
	// matches before the MCP result cap is applied. Once enough matches are collected,
	// terminate rg immediately.
	var b strings.Builder
	s := bufio.NewScanner(stdout)
	s.Buffer(make([]byte, 64*1024), 2*1024*1024)
	count := 0
	truncated := false
	for s.Scan() {
		line := s.Text()
		prefix := resolved + string(filepath.Separator)
		line = strings.Replace(line, prefix, "", 1)
		if line == resolved || strings.HasPrefix(line, resolved+":") {
			line = strings.TrimPrefix(line, resolved)
			line = strings.TrimPrefix(line, ":")
		}
		b.WriteString(line)
		b.WriteByte('\n')
		count++
		if count >= max {
			truncated = true
			_ = cmd.Process.Kill()
			break
		}
	}
	scanErr := s.Err()
	waitErr := cmd.Wait()
	if scanErr != nil && !truncated {
		return Error("rg output: " + scanErr.Error()), nil
	}
	if waitErr != nil && !truncated {
		var ee *exec.ExitError
		if !(errors.As(waitErr, &ee) && ee.ExitCode() == 1 && count == 0) {
			return Error(fmt.Sprintf("rg failed: %v\n%s", waitErr, strings.TrimSpace(stderr.String()))), nil
		}
	}
	text := strings.TrimRight(b.String(), "\n")
	if text == "" {
		text = "No matches."
	}
	structured := map[string]any{"matches": count}
	if truncated {
		structured["truncated"] = true
		text += fmt.Sprintf("\n\n(stopped after %d matches)", count)
	}
	return Result{Content: []Content{{"type": "text", "text": text}}, Structured: structured}, nil
}

// limitedWriter bounds diagnostics from child processes; writes beyond the cap are
// intentionally reported as accepted so the producer never retries the same bytes.
type limitedWriter struct {
	W *bytes.Buffer
	N int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	orig := len(p)
	if w.N > 0 {
		if len(p) > w.N {
			p = p[:w.N]
		}
		_, _ = w.W.Write(p)
		w.N -= len(p)
	}
	return orig, nil
}
