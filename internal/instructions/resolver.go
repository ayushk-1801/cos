package instructions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ayush/cos-lite/internal/workspace"
)

const (
	maxInstructionFile  = 128 * 1024
	maxInstructionTotal = 256 * 1024
	maxServerText       = 32 * 1024
)

type Document struct {
	Project string `json:"project"`
	Path    string `json:"path"`
	Text    string `json:"text"`
}

type Resolver struct {
	WS *workspace.Workspace

	mu       sync.Mutex
	cache    map[string]cachedHierarchy
	cacheTTL time.Duration
}

type cachedHierarchy struct {
	until time.Time
	docs  []Document
	err   string
}

func (r *Resolver) ForPath(path string) ([]Document, error) {
	if r == nil || r.WS == nil {
		return nil, errors.New("instructions resolver has no workspace")
	}
	resolved, err := r.WS.Resolve(path)
	if err != nil {
		return nil, err
	}
	root, ok := rootFor(r.WS, resolved)
	if !ok {
		return nil, fmt.Errorf("path is outside approved projects")
	}
	st, err := os.Stat(resolved)
	if err == nil && !st.IsDir() {
		resolved = filepath.Dir(resolved)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return r.cachedHierarchy(root, resolved)
}

func (r *Resolver) ForProject(name string) ([]Document, error) {
	if r == nil || r.WS == nil {
		return nil, errors.New("instructions resolver has no workspace")
	}
	for _, root := range r.WS.Roots {
		if root.Name == name {
			return r.cachedHierarchy(root, root.Path)
		}
	}
	return nil, fmt.Errorf("unknown project %q", name)
}

func (r *Resolver) ServerText() string {
	if r == nil || r.WS == nil {
		return ""
	}
	var b strings.Builder
	for _, root := range r.WS.Roots {
		docs, err := r.cachedHierarchy(root, root.Path)
		if err != nil || len(docs) == 0 {
			continue
		}
		for _, doc := range docs {
			section := fmt.Sprintf("\n\n## %s · %s\n%s", root.Name, doc.Path, doc.Text)
			if b.Len()+len(section) > maxServerText {
				remaining := maxServerText - b.Len()
				if remaining > 0 {
					b.WriteString(section[:remaining])
				}
				b.WriteString("\n\n[AGENTS.md context truncated; use cos://projects/<project>/instructions for full applicable instructions]")
				return strings.TrimSpace(b.String())
			}
			b.WriteString(section)
		}
	}
	return strings.TrimSpace(b.String())
}

func (r *Resolver) cachedHierarchy(root workspace.Root, targetDir string) ([]Document, error) {
	if r == nil {
		return nil, errors.New("instructions resolver is nil")
	}
	key := root.Name + "\x00" + filepath.Clean(targetDir)
	now := time.Now()
	r.mu.Lock()
	if r.cache != nil {
		if cached, ok := r.cache[key]; ok && now.Before(cached.until) {
			docs := append([]Document(nil), cached.docs...)
			errText := cached.err
			r.mu.Unlock()
			if errText != "" {
				return docs, errors.New(errText)
			}
			return docs, nil
		}
	}
	r.mu.Unlock()

	docs, err := r.hierarchy(root, targetDir)
	ttl := r.cacheTTL
	if ttl <= 0 {
		ttl = 2 * time.Second
	}
	entry := cachedHierarchy{until: time.Now().Add(ttl), docs: append([]Document(nil), docs...)}
	if err != nil {
		entry.err = err.Error()
	}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]cachedHierarchy{}
	}
	r.cache[key] = entry
	r.mu.Unlock()
	return docs, err
}

func (r *Resolver) hierarchy(root workspace.Root, targetDir string) ([]Document, error) {
	targetDir = filepath.Clean(targetDir)
	rel, err := filepath.Rel(root.Path, targetDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("target is outside project %s", root.Name)
	}
	dirs := []string{root.Path}
	if rel != "." {
		cur := root.Path
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			if part == "" || part == "." {
				continue
			}
			cur = filepath.Join(cur, part)
			dirs = append(dirs, cur)
		}
	}
	var out []Document
	total := 0
	for _, dir := range dirs {
		p := filepath.Join(dir, "AGENTS.md")
		st, err := os.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			continue
		}
		if st.Size() > maxInstructionFile {
			return nil, fmt.Errorf("%s exceeds %d bytes", p, maxInstructionFile)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		total += len(b)
		if total > maxInstructionTotal {
			return nil, fmt.Errorf("applicable AGENTS.md files exceed %d bytes", maxInstructionTotal)
		}
		out = append(out, Document{Project: root.Name, Path: r.WS.Display(p), Text: string(b)})
	}
	return out, nil
}

func rootFor(ws *workspace.Workspace, path string) (workspace.Root, bool) {
	clean := filepath.Clean(path)
	for _, root := range ws.Roots {
		rel, err := filepath.Rel(root.Path, clean)
		if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
			return root, true
		}
	}
	return workspace.Root{}, false
}
