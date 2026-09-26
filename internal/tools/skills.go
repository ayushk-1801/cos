package tools

import (
	"context"
	"fmt"
	"strings"

	skillpkg "github.com/ayush/cos-lite/internal/skills"
)

type SkillsTool struct{ Library *skillpkg.Library }

func (s *SkillsTool) Definition() Tool {
	return Tool{Definition: Definition{
		Name: "skills", Title: "Discover and load skills",
		Description: "List reusable coding/workflow skills and load a SKILL.md only when its workflow is relevant. Skills provide instructions, not extra permissions. Repo-local skills come from <project>/.agents/skills and global skills from ~/.agents/skills, following the cross-client Agent Skills convention.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"list", "get"}, "default": "list"},
			"id":     map[string]any{"type": "string", "description": "Qualified skill id such as global/review or project/build. A bare id is accepted when unique."},
			"file":   map[string]any{"type": "string", "description": "Optional supporting file inside the skill package; defaults to SKILL.md."},
		}},
		Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
	}, Handler: s.Handle}
}
func (s *SkillsTool) Handle(_ context.Context, args map[string]any) (Result, error) {
	if s.Library == nil {
		return Error("skills library is unavailable"), nil
	}
	action := strArg(args, "action")
	if action == "" {
		action = "list"
	}
	switch action {
	case "list":
		list, errs := s.Library.List()
		var b strings.Builder
		if len(list) == 0 {
			b.WriteString("No skills installed.")
		} else {
			for _, sk := range list {
				fmt.Fprintf(&b, "%-28s %s — %s\n", sk.ID, sk.Name, sk.Description)
			}
		}
		if len(errs) > 0 {
			b.WriteString("\nWarnings:\n")
			for _, e := range errs {
				fmt.Fprintf(&b, "- %s\n", e)
			}
		}
		return Result{Content: []Content{{"type": "text", "text": strings.TrimRight(b.String(), "\n")}}, Structured: map[string]any{"skills": list, "warnings": errs}}, nil
	case "get":
		id, err := requireString(args, "id")
		if err != nil {
			return Error(err.Error()), nil
		}
		sk, body, err := s.Library.ReadFile(id, strArg(args, "file"))
		if err != nil {
			return Error(err.Error()), nil
		}
		return Result{Content: []Content{{"type": "text", "text": body}}, Structured: map[string]any{"skill": sk}}, nil
	default:
		return Error("action must be list or get"), nil
	}
}
