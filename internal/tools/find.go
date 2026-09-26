package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ayush/cos-lite/internal/workspace"
)

type Finder struct{ WS *workspace.Workspace }

func (f *Finder) Definition() Tool {
	return Tool{Definition: Definition{
		Name: "find", Title: "Search workspace",
		Description: "Search file contents inside approved projects. Uses ripgrep when available and a bounded built-in search otherwise. Project roots are " + f.WS.RootSummary() + ". With multiple projects, path must start with /<project>/.... Returns path:line:column matches with bounded results.",
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
		return f.handleBuiltin(ctx, resolved, query, strArg(args, "glob"), boolArg(args, "case_sensitive", false), boolArg(args, "fixed_strings", false), max), nil
	}
	return f.handleRipgrep(ctx, resolved, query, strArg(args, "glob"), boolArg(args, "case_sensitive", false), boolArg(args, "fixed_strings", false), max), nil
}

func (f *Finder) handleRipgrep(ctx context.Context, resolved, query, glob string, caseSensitive, fixedStrings bool, max int) Result {
	argv := []string{"--line-number", "--column", "--no-heading", "--color=never"}
	if !caseSensitive {
		argv = append(argv, "--ignore-case")
	}
	if fixedStrings {
		argv = append(argv, "--fixed-strings")
	}
	if glob != "" {
		argv = append(argv, "--glob", glob)
	}
	argv = append(argv, "--", query, resolved)
	cmd := exec.CommandContext(ctx, "rg", argv...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Error("rg stdout: " + err.Error())
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{W: &stderr, N: 32 * 1024}
	if err := cmd.Start(); err != nil {
		return Error("rg start: " + err.Error())
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
		return Error("rg output: " + scanErr.Error())
	}
	if waitErr != nil && !truncated {
		var ee *exec.ExitError
		if !(errors.As(waitErr, &ee) && ee.ExitCode() == 1 && count == 0) {
			return Error(fmt.Sprintf("rg failed: %v\n%s", waitErr, strings.TrimSpace(stderr.String())))
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
	return Result{Content: []Content{{"type": "text", "text": text}}, Structured: structured}
}

const builtinFindMaxFile = 4 * 1024 * 1024

func (f *Finder) handleBuiltin(ctx context.Context, resolved, query, glob string, caseSensitive, fixedStrings bool, max int) Result {
	var re *regexp.Regexp
	needle := query
	if fixedStrings {
		if !caseSensitive {
			needle = strings.ToLower(needle)
		}
	} else {
		pattern := query
		if !caseSensitive {
			pattern = "(?i)" + pattern
		}
		var err error
		re, err = regexp.Compile(pattern)
		if err != nil {
			return Error("invalid search regex: " + err.Error())
		}
	}

	rootInfo, err := os.Stat(resolved)
	if err != nil {
		return Error(err.Error())
	}
	base := resolved
	if !rootInfo.IsDir() {
		base = filepath.Dir(resolved)
	}

	var out strings.Builder
	count := 0
	truncated := false
	stop := errors.New("find result limit reached")
	searchFile := func(path string, info fs.FileInfo) error {
		if info.Size() > builtinFindMaxFile {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			rel = filepath.Base(path)
		}
		rel = filepath.ToSlash(rel)
		if glob != "" && !matchSimpleGlob(glob, rel) {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		s := bufio.NewScanner(file)
		s.Buffer(make([]byte, 64*1024), 2*1024*1024)
		lineNo := 0
		for s.Scan() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			lineNo++
			line := s.Text()
			if strings.IndexByte(line, 0) >= 0 {
				return nil
			}
			column := -1
			if fixedStrings {
				hay := line
				if !caseSensitive {
					hay = strings.ToLower(hay)
				}
				column = strings.Index(hay, needle)
			} else if loc := re.FindStringIndex(line); loc != nil {
				column = loc[0]
			}
			if column < 0 {
				continue
			}
			name := rel
			if name == "." {
				name = filepath.Base(path)
			}
			fmt.Fprintf(&out, "%s:%d:%d:%s\n", name, lineNo, column+1, line)
			count++
			if count >= max {
				truncated = true
				return stop
			}
		}
		return nil
	}

	if rootInfo.IsDir() {
		err = filepath.Walk(resolved, func(path string, info fs.FileInfo, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if path != resolved && info.IsDir() {
				name := info.Name()
				if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "target" {
					return filepath.SkipDir
				}
				return nil
			}
			if info.Mode().IsRegular() {
				return searchFile(path, info)
			}
			return nil
		})
	} else {
		err = searchFile(resolved, rootInfo)
	}
	if err != nil && !errors.Is(err, stop) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return Error("built-in search failed: " + err.Error())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Error(err.Error())
	}
	text := strings.TrimRight(out.String(), "\n")
	if text == "" {
		text = "No matches."
	}
	structured := map[string]any{"matches": count, "engine": "builtin"}
	if truncated {
		structured["truncated"] = true
		text += fmt.Sprintf("\n\n(stopped after %d matches)", count)
	}
	return Result{Content: []Content{{"type": "text", "text": text}}, Structured: structured}
}

func matchSimpleGlob(pattern, rel string) bool {
	pattern = filepath.ToSlash(pattern)
	rel = filepath.ToSlash(rel)
	if ok, _ := filepath.Match(pattern, rel); ok {
		return true
	}
	if ok, _ := filepath.Match(pattern, filepath.Base(rel)); ok {
		return true
	}
	if strings.HasPrefix(pattern, "**/") {
		trimmed := strings.TrimPrefix(pattern, "**/")
		if ok, _ := filepath.Match(trimmed, rel); ok {
			return true
		}
		if ok, _ := filepath.Match(trimmed, filepath.Base(rel)); ok {
			return true
		}
	}
	return false
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
