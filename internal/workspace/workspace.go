package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var ErrOutsideRoot = errors.New("path escapes approved workspace root")
var ErrAmbiguousRoot = errors.New("path must name an approved project root")

type Root struct{ Name, Path string }

type Workspace struct {
	// Root remains for backwards compatibility with single-root callers/tests.
	Root  string
	Roots []Root
}

func New(root string) (*Workspace, error) {
	if root == "" {
		root = "."
	}
	name := filepath.Base(filepath.Clean(root))
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = "project"
	}
	return NewRoots([]Root{{Name: name, Path: root}})
}

func NewRoots(roots []Root) (*Workspace, error) {
	if len(roots) == 0 {
		return nil, errors.New("no enabled projects; add and enable at least one project")
	}
	seen := map[string]bool{}
	out := make([]Root, 0, len(roots))
	for _, r := range roots {
		r.Name = strings.TrimSpace(r.Name)
		if r.Name == "" {
			return nil, errors.New("project root name is empty")
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("duplicate project root %q", r.Name)
		}
		seen[r.Name] = true
		abs, err := filepath.Abs(r.Path)
		if err != nil {
			return nil, err
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace root %s: %w", r.Name, err)
		}
		st, err := os.Stat(real)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			return nil, fmt.Errorf("workspace root is not a directory: %s", real)
		}
		out = append(out, Root{Name: r.Name, Path: filepath.Clean(real)})
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	w := &Workspace{Roots: out}
	if len(out) == 1 {
		w.Root = out[0].Path
	}
	return w, nil
}

func (w *Workspace) VirtualRoots() []string {
	out := make([]string, len(w.Roots))
	for i, r := range w.Roots {
		out[i] = "/" + r.Name
	}
	return out
}
func (w *Workspace) RootSummary() string { return strings.Join(w.VirtualRoots(), ", ") }

func (w *Workspace) Resolve(path string) (string, error)          { return w.resolve(path, false) }
func (w *Workspace) ResolveForCreate(path string) (string, error) { return w.resolve(path, true) }
func (w *Workspace) ResolveForMutation(path string) (string, error) {
	root, candidate, err := w.candidate(path)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", errors.New("mutation path is empty")
	}
	parentReal, err := filepath.EvalSymlinks(filepath.Dir(candidate))
	if err != nil {
		return "", err
	}
	resolved := filepath.Join(parentReal, filepath.Base(candidate))
	if !inside(root.Path, resolved) {
		return "", fmt.Errorf("%w: %s", ErrOutsideRoot, path)
	}
	if st, err := os.Lstat(resolved); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing to mutate symlink: %s", path)
	}
	return resolved, nil
}

func (w *Workspace) resolve(path string, allowMissing bool) (string, error) {
	root, candidate, err := w.candidate(path)
	if err != nil {
		return "", err
	}
	var resolved string
	if allowMissing {
		resolved, err = resolveMissing(candidate)
	} else {
		resolved, err = filepath.EvalSymlinks(candidate)
	}
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	if !inside(root.Path, resolved) {
		return "", fmt.Errorf("%w: %s", ErrOutsideRoot, path)
	}
	return resolved, nil
}

func (w *Workspace) candidate(path string) (Root, string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "."
	}
	// Native absolute paths are accepted only when already inside a configured root.
	if filepath.IsAbs(path) {
		clean := filepath.Clean(path)
		for _, r := range w.Roots {
			if inside(r.Path, clean) {
				return r, clean, nil
			}
		}
		// Otherwise /name/... is a virtual project path.
		trimmed := strings.TrimPrefix(filepath.ToSlash(clean), "/")
		parts := strings.SplitN(trimmed, "/", 2)
		if r, ok := w.rootByName(parts[0]); ok {
			rest := "."
			if len(parts) == 2 {
				rest = parts[1]
			}
			return r, filepath.Join(r.Path, filepath.FromSlash(rest)), nil
		}
		return Root{}, "", fmt.Errorf("%w: %s; approved roots: %s", ErrOutsideRoot, path, w.RootSummary())
	}
	if len(w.Roots) == 1 {
		r := w.Roots[0]
		return r, filepath.Join(r.Path, path), nil
	}
	slash := filepath.ToSlash(path)
	parts := strings.SplitN(slash, "/", 2)
	if r, ok := w.rootByName(parts[0]); ok {
		rest := "."
		if len(parts) == 2 {
			rest = parts[1]
		}
		return r, filepath.Join(r.Path, filepath.FromSlash(rest)), nil
	}
	return Root{}, "", fmt.Errorf("%w: use /<project>/...; approved roots: %s", ErrAmbiguousRoot, w.RootSummary())
}

func (w *Workspace) rootByName(name string) (Root, bool) {
	for _, r := range w.Roots {
		if r.Name == name {
			return r, true
		}
	}
	return Root{}, false
}

func resolveMissing(path string) (string, error) {
	cur := filepath.Clean(path)
	var suffix []string
	for {
		_, err := os.Lstat(cur)
		if err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				real = filepath.Join(real, suffix[i])
			}
			return filepath.Clean(real), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		suffix = append(suffix, filepath.Base(cur))
		cur = parent
	}
}

func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (w *Workspace) Display(path string) string {
	clean := filepath.Clean(path)
	for _, r := range w.Roots {
		if inside(r.Path, clean) {
			rel, err := filepath.Rel(r.Path, clean)
			if err != nil {
				continue
			}
			if rel == "." {
				return "/" + r.Name
			}
			return "/" + r.Name + "/" + filepath.ToSlash(rel)
		}
	}
	return path
}
