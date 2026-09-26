package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ayush/cos-lite/internal/codeintel"
)

type CodeIntel struct{ Client *codeintel.Client }

func (c *CodeIntel) Definition() Tool {
	return Tool{Definition: Definition{
		Name: "code_intel", Title: "Semantic code intelligence",
		Description: "Query installed language servers for definitions, references, hover/type information, symbols, implementations, diagnostics, and rename previews. This is semantic code navigation, not text search. Language servers start lazily and are stopped after the request.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"action":              map[string]any{"type": "string", "enum": []string{"definition", "references", "hover", "document_symbols", "workspace_symbols", "implementations", "diagnostics", "rename"}},
			"path":                map[string]any{"type": "string", "description": "File path inside an exposed project. For workspace_symbols this may be the project root."},
			"language":            map[string]any{"type": "string", "enum": []string{"go", "c", "cpp", "rust", "python", "typescript", "javascript"}, "description": "Optional override when language cannot be inferred from the path."},
			"line":                map[string]any{"type": "integer", "minimum": 1, "default": 1},
			"column":              map[string]any{"type": "integer", "minimum": 1, "default": 1},
			"query":               map[string]any{"type": "string", "description": "Workspace-symbol query."},
			"new_name":            map[string]any{"type": "string", "description": "New symbol name for rename. Rename returns a preview WorkspaceEdit and does not write files."},
			"include_declaration": map[string]any{"type": "boolean", "default": true},
			"timeout_ms":          map[string]any{"type": "integer", "minimum": 1000, "maximum": 120000, "default": 20000},
		}, "required": []string{"action", "path"}},
		Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
	}, Handler: c.Handle}
}
func (c *CodeIntel) Handle(ctx context.Context, args map[string]any) (Result, error) {
	if c.Client == nil {
		return Error("code intelligence is unavailable"), nil
	}
	action, err := requireString(args, "action")
	if err != nil {
		return Error(err.Error()), nil
	}
	path, err := requireString(args, "path")
	if err != nil {
		return Error(err.Error()), nil
	}
	req := codeintel.Request{Action: action, Path: path, Language: strArg(args, "language"), Line: intArg(args, "line", 1), Column: intArg(args, "column", 1), Query: strArg(args, "query"), NewName: strArg(args, "new_name"), IncludeDeclaration: boolArg(args, "include_declaration", true), Timeout: time.Duration(intArg(args, "timeout_ms", 20000)) * time.Millisecond}
	res, err := c.Client.Run(ctx, req)
	if err != nil {
		return Error(err.Error()), nil
	}
	pretty, _ := json.MarshalIndent(res.Result, "", "  ")
	text := fmt.Sprintf("language: %s\nserver: %s\n", res.Language, res.Server)
	if len(pretty) > 0 {
		text += "\n" + string(pretty)
	} else {
		text += "\n(no result)"
	}
	if action == "rename" {
		text = "RENAME PREVIEW ONLY; no files were changed.\n" + text
	}
	return Result{Content: []Content{{"type": "text", "text": strings.TrimSpace(text)}}, Structured: res}, nil
}
