package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ayush/cos-lite/internal/workspace"
)

type Patcher struct{ WS *workspace.Workspace }

type patchFile struct {
	kind   string
	path   string
	moveTo string
	add    []string
	hunks  []patchHunk
}

type patchHunk struct{ old, new []string }

func (p *Patcher) Definition() Tool {
	return Tool{Definition: Definition{
		Name: "apply_patch", Title: "Apply patch",
		Description: "Apply a Codex-style patch inside approved projects. Project roots are " + p.WS.RootSummary() + ". With multiple projects, every patch path must start with /<project>/.... Supports Add File, Update File, Delete File, multiple hunks, and Move to. Updates require exact context, preventing accidental fuzzy edits.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"patch": map[string]any{"type": "string", "description": "Patch beginning with *** Begin Patch and ending with *** End Patch."}}, "required": []string{"patch"}},
		Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": false},
	}, Handler: p.Handle}
}

func (p *Patcher) Handle(_ context.Context, args map[string]any) (Result, error) {
	raw := strArg(args, "patch")
	if raw == "" {
		return Error("patch is required"), nil
	}
	files, err := parsePatch(raw)
	if err != nil {
		return Error(err.Error()), nil
	}
	var changed []string
	for _, pf := range files {
		name, err := p.applyOne(pf)
		if err != nil {
			return Error(fmt.Sprintf("%s: %v", pf.path, err)), nil
		}
		changed = append(changed, name)
	}
	return Result{Content: []Content{{"type": "text", "text": "Applied patch successfully:\n- " + strings.Join(changed, "\n- ")}}, Structured: map[string]any{"changed": changed}}, nil
}

func parsePatch(raw string) ([]patchFile, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(raw, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "*** Begin Patch" {
		return nil, errors.New("patch must start with *** Begin Patch")
	}
	if strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) < 2 || strings.TrimSpace(lines[len(lines)-1]) != "*** End Patch" {
		return nil, errors.New("patch must end with *** End Patch")
	}
	var out []patchFile
	for i := 1; i < len(lines)-1; {
		line := lines[i]
		if strings.HasPrefix(line, "*** Add File: ") {
			pf := patchFile{kind: "add", path: strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))}
			i++
			for i < len(lines)-1 && !strings.HasPrefix(lines[i], "*** ") {
				if !strings.HasPrefix(lines[i], "+") {
					return nil, fmt.Errorf("add file %s: every content line must start with +", pf.path)
				}
				pf.add = append(pf.add, strings.TrimPrefix(lines[i], "+"))
				i++
			}
			out = append(out, pf)
			continue
		}
		if strings.HasPrefix(line, "*** Delete File: ") {
			out = append(out, patchFile{kind: "delete", path: strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))})
			i++
			continue
		}
		if strings.HasPrefix(line, "*** Update File: ") {
			pf := patchFile{kind: "update", path: strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))}
			i++
			if i < len(lines)-1 && strings.HasPrefix(lines[i], "*** Move to: ") {
				pf.moveTo = strings.TrimSpace(strings.TrimPrefix(lines[i], "*** Move to: "))
				i++
			}
			for i < len(lines)-1 && !strings.HasPrefix(lines[i], "*** Add File: ") && !strings.HasPrefix(lines[i], "*** Delete File: ") && !strings.HasPrefix(lines[i], "*** Update File: ") {
				if strings.HasPrefix(lines[i], "*** Move to: ") {
					pf.moveTo = strings.TrimSpace(strings.TrimPrefix(lines[i], "*** Move to: "))
					i++
					continue
				}
				if !strings.HasPrefix(lines[i], "@@") {
					return nil, fmt.Errorf("update file %s: expected @@ hunk header, got %q", pf.path, lines[i])
				}
				i++
				h := patchHunk{}
				for i < len(lines)-1 && !strings.HasPrefix(lines[i], "@@") && !strings.HasPrefix(lines[i], "*** ") {
					l := lines[i]
					if l == "\\ No newline at end of file" {
						i++
						continue
					}
					if l == "" {
						return nil, fmt.Errorf("update file %s: hunk lines require prefix space, +, or -", pf.path)
					}
					switch l[0] {
					case ' ':
						h.old = append(h.old, l[1:])
						h.new = append(h.new, l[1:])
					case '-':
						h.old = append(h.old, l[1:])
					case '+':
						h.new = append(h.new, l[1:])
					default:
						return nil, fmt.Errorf("update file %s: invalid hunk line %q", pf.path, l)
					}
					i++
				}
				if len(h.old) == 0 && len(h.new) == 0 {
					return nil, fmt.Errorf("update file %s: empty hunk", pf.path)
				}
				pf.hunks = append(pf.hunks, h)
			}
			if len(pf.hunks) == 0 && pf.moveTo == "" {
				return nil, fmt.Errorf("update file %s has no hunks", pf.path)
			}
			out = append(out, pf)
			continue
		}
		return nil, fmt.Errorf("unexpected patch line %q", line)
	}
	if len(out) == 0 {
		return nil, errors.New("patch contains no file operations")
	}
	return out, nil
}

func (p *Patcher) applyOne(pf patchFile) (string, error) {
	switch pf.kind {
	case "add":
		dst, err := p.WS.ResolveForCreate(pf.path)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(dst); err == nil {
			return "", errors.New("file already exists")
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		content := strings.Join(pf.add, "\n")
		if len(pf.add) > 0 {
			content += "\n"
		}
		if err := atomicWrite(dst, []byte(content), 0o644); err != nil {
			return "", err
		}
		return pf.path, nil
	case "delete":
		dst, err := p.WS.ResolveForMutation(pf.path)
		if err != nil {
			return "", err
		}
		st, err := os.Stat(dst)
		if err != nil {
			return "", err
		}
		if st.IsDir() {
			return "", errors.New("refusing to delete directory")
		}
		if err := os.Remove(dst); err != nil {
			return "", err
		}
		return pf.path, nil
	case "update":
		src, err := p.WS.ResolveForMutation(pf.path)
		if err != nil {
			return "", err
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return "", err
		}
		st, err := os.Stat(src)
		if err != nil {
			return "", err
		}
		text := strings.ReplaceAll(string(b), "\r\n", "\n")
		hadNL := strings.HasSuffix(text, "\n")
		if hadNL {
			text = strings.TrimSuffix(text, "\n")
		}
		lines := []string{}
		if text != "" {
			lines = strings.Split(text, "\n")
		}
		cursor := 0
		for hi, h := range pf.hunks {
			idx := findBlock(lines, h.old, cursor)
			if idx < 0 {
				idx = findBlock(lines, h.old, 0)
			}
			if idx < 0 {
				return "", fmt.Errorf("hunk %d context not found", hi+1)
			}
			newLines := make([]string, 0, len(lines)-len(h.old)+len(h.new))
			newLines = append(newLines, lines[:idx]...)
			newLines = append(newLines, h.new...)
			newLines = append(newLines, lines[idx+len(h.old):]...)
			lines = newLines
			cursor = idx + len(h.new)
		}
		out := strings.Join(lines, "\n")
		if hadNL {
			out += "\n"
		}
		target := src
		display := pf.path
		if pf.moveTo != "" {
			target, err = p.WS.ResolveForCreate(pf.moveTo)
			if err != nil {
				return "", err
			}
			if _, e := os.Lstat(target); e == nil && target != src {
				return "", fmt.Errorf("move target already exists: %s", pf.moveTo)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", err
			}
			display = pf.path + " -> " + pf.moveTo
		}
		if err := atomicWrite(target, []byte(out), st.Mode().Perm()); err != nil {
			return "", err
		}
		if target != src {
			if err := os.Remove(src); err != nil {
				return "", err
			}
		}
		return display, nil
	default:
		return "", fmt.Errorf("unknown patch operation %q", pf.kind)
	}
}

func findBlock(lines, block []string, start int) int {
	if len(block) == 0 {
		return start
	}
	if start < 0 {
		start = 0
	}
	for i := start; i+len(block) <= len(lines); i++ {
		ok := true
		for j := range block {
			if lines[i+j] != block[j] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".cos-patch-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
