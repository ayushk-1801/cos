package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ayush/cos-lite/internal/instructions"
	"github.com/ayush/cos-lite/internal/skills"
	"github.com/ayush/cos-lite/internal/workspace"
)

type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

type Template struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

type Content struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
}

type Provider struct {
	WS           *workspace.Workspace
	Skills       *skills.Library
	Instructions *instructions.Resolver
	Status       func(context.Context) any
	Audit        func(context.Context) any
}

func (p *Provider) List() []Resource {
	out := []Resource{
		{URI: "cos://status", Name: "status", Title: "cos-lite status", Description: "Daemon, project, client and runtime status.", MIMEType: "application/json"},
		{URI: "cos://projects", Name: "projects", Title: "Exposed projects", Description: "Approved project roots exposed by cos-lite.", MIMEType: "application/json"},
		{URI: "cos://skills", Name: "skills", Title: "Skills catalog", Description: "Discovered global and repo-local Agent Skills.", MIMEType: "application/json"},
		{URI: "cos://audit/recent", Name: "audit-recent", Title: "Recent MCP audit events", Description: "Bounded recent multi-client audit events without tool arguments or secrets.", MIMEType: "application/json"},
	}
	if p != nil && p.WS != nil {
		for _, root := range p.WS.Roots {
			base := "cos://projects/" + url.PathEscape(root.Name)
			out = append(out,
				Resource{URI: base, Name: "project-" + root.Name, Title: root.Name, Description: "Project metadata.", MIMEType: "application/json"},
				Resource{URI: base + "/git", Name: "project-" + root.Name + "-git", Title: root.Name + " Git context", Description: "Branch, worktree status and recent commits.", MIMEType: "application/json"},
				Resource{URI: base + "/instructions", Name: "project-" + root.Name + "-instructions", Title: root.Name + " AGENTS.md", Description: "Applicable root AGENTS.md instructions.", MIMEType: "text/markdown"},
			)
		}
	}
	return out
}

func (p *Provider) Templates() []Template {
	return []Template{{
		URITemplate: "cos://projects/{project}/instructions{?path}",
		Name:        "project-instructions",
		Title:       "Applicable AGENTS.md instructions",
		Description: "Resolve hierarchical AGENTS.md files for a project-relative path.",
		MIMEType:    "text/markdown",
	}}
}

func (p *Provider) Read(ctx context.Context, rawURI string) ([]Content, int64, string, error) {
	u, err := url.Parse(rawURI)
	if err != nil || u.Scheme != "cos" {
		return nil, 0, "private", fmt.Errorf("invalid resource URI %q", rawURI)
	}
	switch u.Host {
	case "status":
		var v any = map[string]any{"ok": true}
		if p.Status != nil {
			v = p.Status(ctx)
		}
		return jsonContent(rawURI, v), 1000, "private", nil
	case "projects":
		parts := splitPath(u.Path)
		if len(parts) == 0 {
			return jsonContent(rawURI, p.projectList()), 5000, "private", nil
		}
		root, ok := p.root(parts[0])
		if !ok {
			return nil, 0, "private", fmt.Errorf("unknown project %q", parts[0])
		}
		if len(parts) == 1 {
			return jsonContent(rawURI, map[string]any{"name": root.Name, "path": "/" + root.Name}), 5000, "private", nil
		}
		switch parts[1] {
		case "git":
			git, err := gitContext(ctx, root.Path)
			if err != nil {
				return nil, 0, "private", err
			}
			return jsonContent(rawURI, git), 2000, "private", nil
		case "instructions":
			return p.readInstructions(rawURI, root, u.Query().Get("path"))
		default:
			return nil, 0, "private", fmt.Errorf("unknown project resource %q", parts[1])
		}
	case "skills":
		if p.Skills == nil {
			return jsonContent(rawURI, []any{}), 5000, "private", nil
		}
		list, warnings := p.Skills.List()
		return jsonContent(rawURI, map[string]any{"skills": list, "warnings": warnings}), 5000, "private", nil
	case "audit":
		if strings.Trim(u.Path, "/") != "recent" {
			return nil, 0, "private", fmt.Errorf("unknown audit resource")
		}
		var v any = []any{}
		if p.Audit != nil {
			v = p.Audit(ctx)
		}
		return jsonContent(rawURI, v), 0, "private", nil
	default:
		return nil, 0, "private", fmt.Errorf("resource not found: %s", rawURI)
	}
}

func (p *Provider) readInstructions(rawURI string, root workspace.Root, rel string) ([]Content, int64, string, error) {
	if p.Instructions == nil {
		return []Content{{URI: rawURI, MIMEType: "text/markdown", Text: "No AGENTS.md instructions found."}}, 5000, "private", nil
	}
	var docs []instructions.Document
	var err error
	if strings.TrimSpace(rel) == "" {
		docs, err = p.Instructions.ForProject(root.Name)
	} else {
		clean := filepath.Clean(strings.TrimPrefix(rel, "/"))
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, 0, "private", fmt.Errorf("instruction path escapes project")
		}
		docs, err = p.Instructions.ForPath("/" + root.Name + "/" + filepath.ToSlash(clean))
	}
	if err != nil {
		return nil, 0, "private", err
	}
	if len(docs) == 0 {
		return []Content{{URI: rawURI, MIMEType: "text/markdown", Text: "No applicable AGENTS.md instructions."}}, 5000, "private", nil
	}
	var b strings.Builder
	for _, d := range docs {
		fmt.Fprintf(&b, "# %s\n\n%s\n\n", d.Path, strings.TrimSpace(d.Text))
	}
	return []Content{{URI: rawURI, MIMEType: "text/markdown", Text: strings.TrimSpace(b.String())}}, 5000, "private", nil
}

func (p *Provider) projectList() []map[string]any {
	if p == nil || p.WS == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(p.WS.Roots))
	for _, root := range p.WS.Roots {
		out = append(out, map[string]any{"name": root.Name, "path": p.WS.Display(root.Path)})
	}
	return out
}

func (p *Provider) root(name string) (workspace.Root, bool) {
	if p == nil || p.WS == nil {
		return workspace.Root{}, false
	}
	for _, root := range p.WS.Roots {
		if root.Name == name {
			return root, true
		}
	}
	return workspace.Root{}, false
}

func gitContext(ctx context.Context, dir string) (map[string]any, error) {
	run := func(args ...string) string {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, "git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	inside := run("rev-parse", "--is-inside-work-tree")
	if inside != "true" {
		return map[string]any{"isGit": false}, nil
	}
	return map[string]any{
		"isGit":         true,
		"branch":        run("branch", "--show-current"),
		"status":        run("status", "--short", "--branch"),
		"head":          run("rev-parse", "HEAD"),
		"recentCommits": strings.Split(run("log", "-5", "--pretty=format:%h %s"), "\n"),
	}, nil
}

func jsonContent(uri string, v any) []Content {
	b, _ := json.MarshalIndent(v, "", "  ")
	return []Content{{URI: uri, MIMEType: "application/json", Text: string(b)}}
}

func splitPath(p string) []string {
	var out []string
	for _, part := range strings.Split(strings.Trim(p, "/"), "/") {
		if part != "" {
			if decoded, err := url.PathUnescape(part); err == nil {
				part = decoded
			}
			out = append(out, part)
		}
	}
	return out
}
