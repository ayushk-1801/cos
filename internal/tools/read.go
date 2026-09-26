package tools

import (
	"bufio"
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ayush/cos-lite/internal/workspace"
)

const defaultReadBytes = 256 * 1024
const maxReadBytes = 2 * 1024 * 1024
const maxDirEntries = 200

type Reader struct{ WS *workspace.Workspace }

func (r *Reader) Definition() Tool {
	return Tool{Definition: Definition{
		Name: "read", Title: "Read files and folders",
		Description: "Read one or more files or list folders inside approved projects. Project roots are " + r.WS.RootSummary() + ". With multiple projects, paths must start with /<project>/.... Text is returned with 1-based line numbers; directories are listed one level deep. Supports per-call start_line/end_line and bounded output.",
		InputSchema: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"paths":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": 20, "description": "Paths inside approved project roots: " + r.WS.RootSummary() + ". With multiple projects use /<project>/..."},
				"path":       map[string]any{"type": "string", "description": "Convenience alias for a single path."},
				"start_line": map[string]any{"type": "integer", "minimum": 1},
				"end_line":   map[string]any{"type": "integer", "minimum": 1},
				"max_bytes":  map[string]any{"type": "integer", "minimum": 1, "maximum": maxReadBytes},
			},
		},
		Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
	}, Handler: r.Handle}
}

func (r *Reader) Handle(_ context.Context, args map[string]any) (Result, error) {
	paths := stringSliceArg(args, "paths")
	if p := strArg(args, "path"); p != "" {
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		return Error("paths or path is required"), nil
	}
	if len(paths) > 20 {
		return Error("at most 20 paths are allowed"), nil
	}
	start := intArg(args, "start_line", 1)
	end := intArg(args, "end_line", 0)
	if end > 0 && end < start {
		return Error("end_line must be >= start_line"), nil
	}
	maxBytes := intArg(args, "max_bytes", defaultReadBytes)
	if maxBytes <= 0 {
		maxBytes = defaultReadBytes
	}
	if maxBytes > maxReadBytes {
		maxBytes = maxReadBytes
	}

	sections := make([]string, 0, len(paths))
	for _, p := range paths {
		resolved, err := r.WS.Resolve(p)
		if err != nil {
			sections = append(sections, fmt.Sprintf("--- %s — ERROR ---\n%v", p, err))
			continue
		}
		st, err := os.Stat(resolved)
		if err != nil {
			sections = append(sections, fmt.Sprintf("--- %s — ERROR ---\n%v", p, err))
			continue
		}
		if st.IsDir() {
			sections = append(sections, r.readDir(resolved, st))
			continue
		}
		if !st.Mode().IsRegular() {
			sections = append(sections, metadataText(r.WS.Display(resolved), st)+"\n(non-regular file; content not decoded)")
			continue
		}
		if isLikelyBinary(resolved) {
			sections = append(sections, metadataText(r.WS.Display(resolved), st)+"\n(binary/image file; use view_image for supported images)")
			continue
		}
		text, err := readTextRange(resolved, start, end, maxBytes)
		if err != nil {
			sections = append(sections, fmt.Sprintf("--- %s — ERROR ---\n%v", p, err))
			continue
		}
		sections = append(sections, metadataText(r.WS.Display(resolved), st)+"\n"+text)
	}
	return Text(strings.Join(sections, "\n\n")), nil
}

func (r *Reader) readDir(path string, st os.FileInfo) string {
	entries, err := os.ReadDir(path)
	if err != nil {
		return metadataText(r.WS.Display(path), st) + "\nERROR: " + err.Error()
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var b strings.Builder
	b.WriteString(metadataText(r.WS.Display(path), st))
	b.WriteByte('\n')
	for i, e := range entries {
		if i >= maxDirEntries {
			fmt.Fprintf(&b, "... (%d more entries)\n", len(entries)-i)
			break
		}
		info, _ := e.Info()
		kind := "file"
		if e.IsDir() {
			kind = "dir"
		} else if e.Type()&os.ModeSymlink != 0 {
			kind = "symlink"
		}
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		fmt.Fprintf(&b, "%-8s %10d  %s\n", kind, size, e.Name())
	}
	return strings.TrimRight(b.String(), "\n")
}

func metadataText(path string, st os.FileInfo) string {
	return fmt.Sprintf("--- %s ---\nsize=%d mode=%s modified=%s", path, st.Size(), st.Mode().String(), st.ModTime().UTC().Format(time.RFC3339))
}

func readTextRange(path string, start, end, maxBytes int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	s.Buffer(buf, 2*1024*1024)
	var b strings.Builder
	line := 0
	used := 0
	truncated := false
	for s.Scan() {
		line++
		if line < start {
			continue
		}
		if end > 0 && line > end {
			break
		}
		row := fmt.Sprintf("%6d | %s\n", line, s.Text())
		if used+len(row) > maxBytes {
			truncated = true
			break
		}
		b.WriteString(row)
		used += len(row)
	}
	if err := s.Err(); err != nil {
		return "", err
	}
	if truncated {
		fmt.Fprintf(&b, "... output truncated at %d bytes; continue with start_line=%d\n", maxBytes, line)
	}
	if b.Len() == 0 {
		return "(no lines in requested range)", nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func isLikelyBinary(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if strings.HasPrefix(mime.TypeByExtension(ext), "image/") {
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	buf = buf[:n]
	for _, b := range buf {
		if b == 0 {
			return true
		}
	}
	return false
}
