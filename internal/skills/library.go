package skills

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ayush/cos-lite/internal/workspace"
)

const (
	maxSkillBytes   = 128 * 1024
	maxPackageBytes = 2 * 1024 * 1024
	maxPackageFiles = 128
)

var skillIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}[A-Za-z0-9]$|^[A-Za-z0-9]$`)

type Skill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Scope       string `json:"scope"`
	Project     string `json:"project,omitempty"`
	filePath    string
}

type Library struct {
	WS     *workspace.Workspace
	Global bool
	Repo   bool
}

func GlobalDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agents", "skills"), nil
}

func (l *Library) List() ([]Skill, []string) {
	var out []Skill
	var errs []string
	if l.Global {
		if root, err := GlobalDir(); err != nil {
			errs = append(errs, err.Error())
		} else {
			skills, e := discoverRoot(root, "global", "")
			out = append(out, skills...)
			errs = append(errs, e...)
		}
	}
	if l.Repo && l.WS != nil {
		for _, root := range l.WS.Roots {
			skills, e := discoverRoot(filepath.Join(root.Path, ".agents", "skills"), "repo", root.Name)
			out = append(out, skills...)
			errs = append(errs, e...)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].ID < out[j].ID
	})
	return out, errs
}

func (l *Library) CatalogText() string {
	list, errs := l.List()
	if len(list) == 0 && len(errs) == 0 {
		return "No skills are installed."
	}
	var b strings.Builder
	if len(list) > 0 {
		b.WriteString("Available skills (load one only when its workflow is relevant):\n")
		for _, s := range list {
			fmt.Fprintf(&b, "- %s: %s\n", s.ID, s.Description)
		}
	}
	if len(errs) > 0 {
		b.WriteString("Skill discovery warnings:\n")
		for _, e := range errs {
			fmt.Fprintf(&b, "- %s\n", e)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (l *Library) Read(id string) (Skill, string, error) {
	return l.ReadFile(id, "SKILL.md")
}

func (l *Library) ReadFile(id, rel string) (Skill, string, error) {
	sk, err := l.resolve(id)
	if err != nil {
		return Skill{}, "", err
	}
	if strings.TrimSpace(rel) == "" {
		rel = "SKILL.md"
	}
	if filepath.IsAbs(rel) {
		return Skill{}, "", errors.New("skill file must be relative")
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return Skill{}, "", errors.New("skill file escapes skill directory")
	}
	root := filepath.Dir(sk.filePath)
	target := filepath.Join(root, clean)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Skill{}, "", err
	}
	real, err := filepath.EvalSymlinks(target)
	if err != nil {
		return Skill{}, "", err
	}
	relCheck, err := filepath.Rel(realRoot, real)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return Skill{}, "", errors.New("skill file escapes skill directory")
	}
	st, err := os.Stat(real)
	if err != nil {
		return Skill{}, "", err
	}
	if !st.Mode().IsRegular() {
		return Skill{}, "", errors.New("skill file is not a regular file")
	}
	limit := int64(maxSkillBytes)
	if clean != "SKILL.md" {
		limit = 512 * 1024
	}
	if st.Size() > limit {
		return Skill{}, "", fmt.Errorf("skill file exceeds %d bytes", limit)
	}
	b, err := os.ReadFile(real)
	if err != nil {
		return Skill{}, "", err
	}
	return sk, string(b), nil
}

func (l *Library) resolve(id string) (Skill, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Skill{}, errors.New("skill id is required")
	}
	list, _ := l.List()
	var matches []Skill
	for _, s := range list {
		if s.ID == id || bareID(s.ID) == id {
			matches = append(matches, s)
		}
	}
	if len(matches) == 0 {
		return Skill{}, fmt.Errorf("skill %q not found", id)
	}
	if len(matches) > 1 {
		ids := make([]string, len(matches))
		for i, s := range matches {
			ids[i] = s.ID
		}
		return Skill{}, fmt.Errorf("skill %q is ambiguous; use one of: %s", id, strings.Join(ids, ", "))
	}
	return matches[0], nil
}

func discoverRoot(root, scope, project string) ([]Skill, []string) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, []string{fmt.Sprintf("%s: %v", root, err)}
	}
	var out []Skill
	var errs []string
	for _, e := range entries {
		if !e.IsDir() || !skillIDRE.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if st, err := os.Lstat(dir); err != nil || st.Mode()&os.ModeSymlink != 0 {
			continue
		}
		p := filepath.Join(dir, "SKILL.md")
		st, err := os.Lstat(p)
		if err != nil || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if st.Size() > maxSkillBytes {
			errs = append(errs, fmt.Sprintf("%s exceeds %d bytes", p, maxSkillBytes))
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p, err))
			continue
		}
		name, desc, err := parseFrontmatter(string(b))
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p, err))
			continue
		}
		id := e.Name()
		if scope == "global" {
			id = "global/" + id
		} else {
			id = project + "/" + id
		}
		virtual := "~/.agents/skills/" + e.Name() + "/SKILL.md"
		if scope == "repo" {
			virtual = "/" + project + "/.agents/skills/" + e.Name() + "/SKILL.md"
		}
		out = append(out, Skill{ID: id, Name: name, Description: desc, Path: virtual, Scope: scope, Project: project, filePath: p})
	}
	return out, errs
}

func parseFrontmatter(text string) (string, string, error) {
	r := bufio.NewReader(strings.NewReader(text))
	first, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", "", err
	}
	if strings.TrimSpace(strings.TrimPrefix(first, "\ufeff")) != "---" {
		return "", "", errors.New("SKILL.md needs YAML frontmatter starting with ---")
	}
	values := map[string]string{}
	for {
		line, e := r.ReadString('\n')
		if e != nil && e != io.EOF {
			return "", "", e
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" || trimmed == "..." {
			break
		}
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			if k, v, ok := strings.Cut(trimmed, ":"); ok {
				key := strings.TrimSpace(k)
				if key == "name" || key == "description" {
					values[key] = unquote(strings.TrimSpace(v))
				}
			}
		}
		if e == io.EOF {
			return "", "", errors.New("unterminated YAML frontmatter")
		}
	}
	name := strings.TrimSpace(values["name"])
	desc := strings.TrimSpace(values["description"])
	if name == "" || desc == "" {
		return "", "", errors.New("frontmatter must contain name and description")
	}
	if len(name) > 160 || len(desc) > 1024 {
		return "", "", errors.New("skill metadata is too long")
	}
	return name, desc, nil
}

func unquote(v string) string {
	if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
		return v[1 : len(v)-1]
	}
	return v
}
func bareID(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

func Import(path, id string) (Skill, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Skill{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return Skill{}, err
	}
	srcDir := filepath.Dir(abs)
	skillFile := abs
	if st.IsDir() {
		srcDir = abs
		skillFile = filepath.Join(abs, "SKILL.md")
	}
	b, err := os.ReadFile(skillFile)
	if err != nil {
		return Skill{}, err
	}
	if len(b) > maxSkillBytes {
		return Skill{}, fmt.Errorf("skill exceeds %d bytes", maxSkillBytes)
	}
	name, desc, err := parseFrontmatter(string(b))
	if err != nil {
		return Skill{}, err
	}
	if id == "" {
		id = filepath.Base(srcDir)
	}
	if !skillIDRE.MatchString(id) {
		return Skill{}, fmt.Errorf("invalid skill id %q", id)
	}
	root, err := GlobalDir()
	if err != nil {
		return Skill{}, err
	}
	dstDir := filepath.Join(root, id)
	if _, err := os.Stat(dstDir); err == nil {
		return Skill{}, fmt.Errorf("skill %q already exists", id)
	}
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return Skill{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(dstDir)
		}
	}()
	if st.IsDir() {
		if err := copyPackage(srcDir, dstDir); err != nil {
			return Skill{}, err
		}
	} else {
		if err := os.WriteFile(filepath.Join(dstDir, "SKILL.md"), b, 0o600); err != nil {
			return Skill{}, err
		}
	}
	cleanup = false
	return Skill{ID: "global/" + id, Name: name, Description: desc, Path: "~/.agents/skills/" + id + "/SKILL.md", Scope: "global", filePath: filepath.Join(dstDir, "SKILL.md")}, nil
}

func copyPackage(src, dst string) error {
	files := 0
	bytesTotal := int64(0)
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill packages may not contain symlinks: %s", rel)
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported skill package entry: %s", rel)
		}
		files++
		bytesTotal += info.Size()
		if files > maxPackageFiles || bytesTotal > maxPackageBytes {
			return fmt.Errorf("skill package exceeds %d files or %d bytes", maxPackageFiles, maxPackageBytes)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
}

func RemoveGlobal(id string) error {
	id = strings.TrimPrefix(strings.TrimSpace(id), "global/")
	if !skillIDRE.MatchString(id) {
		return fmt.Errorf("invalid global skill id %q", id)
	}
	root, err := GlobalDir()
	if err != nil {
		return err
	}
	target := filepath.Join(root, id)
	rel, err := filepath.Rel(root, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		return errors.New("invalid skill path")
	}
	if _, err := os.Stat(target); err != nil {
		return err
	}
	return os.RemoveAll(target)
}
