package tools

import "context"

type Content map[string]any

type Result struct {
	Content    []Content `json:"content"`
	Structured any       `json:"structuredContent,omitempty"`
	IsError    bool      `json:"isError,omitempty"`
}

type Definition struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

type Handler func(context.Context, map[string]any) (Result, error)

type Tool struct {
	Definition Definition
	Handler    Handler
}

func Text(text string) Result {
	return Result{Content: []Content{{"type": "text", "text": text}}}
}

func Error(text string) Result {
	return Result{Content: []Content{{"type": "text", "text": text}}, IsError: true}
}
