package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ayush/cos-lite/internal/workspace"
)

type ImageViewer struct{ WS *workspace.Workspace }

func (v *ImageViewer) Definition() Tool {
	return Tool{Definition: Definition{
		Name: "view_image", Title: "View image",
		Description: "Read a PNG, JPEG, GIF, or WebP image from approved projects (" + v.WS.RootSummary() + ") and return native MCP image content.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}},
		Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
	}, Handler: v.Handle}
}

func (v *ImageViewer) Handle(_ context.Context, args map[string]any) (Result, error) {
	p, err := requireString(args, "path")
	if err != nil {
		return Error(err.Error()), nil
	}
	resolved, err := v.WS.Resolve(p)
	if err != nil {
		return Error(err.Error()), nil
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return Error(err.Error()), nil
	}
	if st.Size() > 12*1024*1024 {
		return Error("image exceeds 12 MiB limit"), nil
	}
	mimeType := ""
	switch strings.ToLower(filepath.Ext(resolved)) {
	case ".png":
		mimeType = "image/png"
	case ".jpg", ".jpeg":
		mimeType = "image/jpeg"
	case ".gif":
		mimeType = "image/gif"
	case ".webp":
		mimeType = "image/webp"
	default:
		return Error("unsupported image type; expected PNG/JPEG/GIF/WebP"), nil
	}
	b, err := os.ReadFile(resolved)
	if err != nil {
		return Error(err.Error()), nil
	}
	return Result{Content: []Content{{"type": "text", "text": fmt.Sprintf("%s (%d bytes)", v.WS.Display(resolved), len(b))}, {"type": "image", "mimeType": mimeType, "data": base64.StdEncoding.EncodeToString(b)}}}, nil
}
