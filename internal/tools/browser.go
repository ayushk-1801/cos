package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ayush/cos-lite/internal/browser"
)

type BrowserTools struct{ Browser *browser.Manager }

func (b *BrowserTools) Definitions() []Tool {
	return []Tool{
		{Definition: Definition{Name: "browser_tabs", Title: "Browser tabs", Description: "List, open, or close tabs in a dedicated Chromium instance controlled directly through CDP. No browser extension is used.", InputSchema: objSchema(map[string]any{"action": enumSchema("list", "new", "close"), "tab_id": stringSchema("Tab id for close."), "url": stringSchema("Initial URL for new tab.")}, []string{"action"}), Annotations: readWriteAnnotations()}, Handler: b.tabs},
		{Definition: Definition{Name: "browser_snapshot", Title: "Browser snapshot", Description: "Return visible page text plus up to 300 interactive elements and stable-enough CSS selectors for the current page.", InputSchema: tabSchema(), Annotations: readOnlyAnnotations()}, Handler: b.snapshot},
		{Definition: Definition{Name: "browser_screenshot", Title: "Browser screenshot", Description: "Capture the current viewport as PNG through Chromium CDP.", InputSchema: tabSchema(), Annotations: readOnlyAnnotations()}, Handler: b.screenshot},
		{Definition: Definition{Name: "browser_console", Title: "Browser console", Description: "Return console/log/exception events observed since cos attached to the tab. Set clear=true to clear retained events after reading.", InputSchema: objSchema(map[string]any{"tab_id": stringSchema("Tab id; optional only when one page tab exists."), "clear": map[string]any{"type": "boolean", "default": false}}, nil), Annotations: readOnlyAnnotations()}, Handler: b.console},
		{Definition: Definition{Name: "browser_network", Title: "Browser network", Description: "Return recent request/response/failure CDP events observed since cos attached to the tab. Set clear=true to clear retained events after reading.", InputSchema: objSchema(map[string]any{"tab_id": stringSchema("Tab id; optional only when one page tab exists."), "clear": map[string]any{"type": "boolean", "default": false}}, nil), Annotations: readOnlyAnnotations()}, Handler: b.network},
		{Definition: Definition{Name: "browser_navigate", Title: "Navigate browser", Description: "Navigate a Chromium page tab to a URL and wait for the DOM to become interactive/complete.", InputSchema: objSchema(map[string]any{"tab_id": stringSchema("Tab id; optional only when one page tab exists."), "url": stringSchema("Destination URL.")}, []string{"url"}), Annotations: readWriteAnnotations()}, Handler: b.navigate},
		{Definition: Definition{Name: "browser_action", Title: "Interact with page", Description: "Interact with a page using CSS selectors returned by browser_snapshot. Actions: click, fill, type, focus, press.", InputSchema: objSchema(map[string]any{"tab_id": stringSchema("Tab id; optional only when one page tab exists."), "action": enumSchema("click", "fill", "type", "focus", "press"), "selector": stringSchema("CSS selector for element actions."), "value": stringSchema("Value/text for fill/type, or key fallback for press."), "key": stringSchema("Key name for press, e.g. Enter.")}, []string{"action"}), Annotations: readWriteAnnotations()}, Handler: b.action},
		{Definition: Definition{Name: "browser_evaluate", Title: "Evaluate JavaScript", Description: "Evaluate JavaScript in the selected Chromium page and return its JSON-serializable value. Runs in the page, not on the host filesystem.", InputSchema: objSchema(map[string]any{"tab_id": stringSchema("Tab id; optional only when one page tab exists."), "expression": stringSchema("JavaScript expression or IIFE.")}, []string{"expression"}), Annotations: readWriteAnnotations()}, Handler: b.evaluate},
	}
}

func objSchema(props map[string]any, required []string) map[string]any {
	s := map[string]any{"type": "object", "additionalProperties": false, "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}
func stringSchema(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func enumSchema(v ...string) map[string]any { return map[string]any{"type": "string", "enum": v} }
func tabSchema() map[string]any {
	return objSchema(map[string]any{"tab_id": stringSchema("Tab id; optional only when one page tab exists.")}, nil)
}
func readOnlyAnnotations() map[string]any {
	return map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true}
}
func readWriteAnnotations() map[string]any {
	return map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": true}
}

func (b *BrowserTools) tabs(ctx context.Context, a map[string]any) (Result, error) {
	action := strArg(a, "action")
	switch action {
	case "list":
		tabs, err := b.Browser.Tabs(ctx)
		if err != nil {
			return Error(err.Error()), nil
		}
		x, _ := json.MarshalIndent(tabs, "", "  ")
		return Result{Content: []Content{{"type": "text", "text": string(x)}}, Structured: tabs}, nil
	case "new":
		t, err := b.Browser.NewTab(ctx, strArg(a, "url"))
		if err != nil {
			return Error(err.Error()), nil
		}
		x, _ := json.MarshalIndent(t, "", "  ")
		return Result{Content: []Content{{"type": "text", "text": string(x)}}, Structured: t}, nil
	case "close":
		id, err := requireString(a, "tab_id")
		if err != nil {
			return Error(err.Error()), nil
		}
		if err := b.Browser.CloseTab(ctx, id); err != nil {
			return Error(err.Error()), nil
		}
		return Text("closed tab " + id), nil
	default:
		return Error("action must be list, new, or close"), nil
	}
}
func (b *BrowserTools) snapshot(ctx context.Context, a map[string]any) (Result, error) {
	v, err := b.Browser.Snapshot(ctx, strArg(a, "tab_id"))
	if err != nil {
		return Error(err.Error()), nil
	}
	x, _ := json.MarshalIndent(v, "", "  ")
	return Result{Content: []Content{{"type": "text", "text": string(x)}}, Structured: v}, nil
}
func (b *BrowserTools) screenshot(ctx context.Context, a map[string]any) (Result, error) {
	data, err := b.Browser.Screenshot(ctx, strArg(a, "tab_id"))
	if err != nil {
		return Error(err.Error()), nil
	}
	return Result{Content: []Content{{"type": "image", "mimeType": "image/png", "data": data}, {"type": "text", "text": "Captured Chromium viewport."}}}, nil
}
func (b *BrowserTools) console(ctx context.Context, a map[string]any) (Result, error) {
	v, err := b.Browser.Console(ctx, strArg(a, "tab_id"), boolArg(a, "clear", false))
	if err != nil {
		return Error(err.Error()), nil
	}
	x, _ := json.MarshalIndent(v, "", "  ")
	return Result{Content: []Content{{"type": "text", "text": string(x)}}, Structured: v}, nil
}
func (b *BrowserTools) network(ctx context.Context, a map[string]any) (Result, error) {
	v, err := b.Browser.Network(ctx, strArg(a, "tab_id"), boolArg(a, "clear", false))
	if err != nil {
		return Error(err.Error()), nil
	}
	x, _ := json.MarshalIndent(v, "", "  ")
	return Result{Content: []Content{{"type": "text", "text": string(x)}}, Structured: v}, nil
}
func (b *BrowserTools) navigate(ctx context.Context, a map[string]any) (Result, error) {
	u, err := requireString(a, "url")
	if err != nil {
		return Error(err.Error()), nil
	}
	if err := b.Browser.Navigate(ctx, strArg(a, "tab_id"), u); err != nil {
		return Error(err.Error()), nil
	}
	return Text("navigated to " + u), nil
}
func (b *BrowserTools) action(ctx context.Context, a map[string]any) (Result, error) {
	action, err := requireString(a, "action")
	if err != nil {
		return Error(err.Error()), nil
	}
	if action != "press" && strings.TrimSpace(strArg(a, "selector")) == "" {
		return Error("selector is required for this action"), nil
	}
	v, err := b.Browser.Action(ctx, strArg(a, "tab_id"), action, strArg(a, "selector"), strArg(a, "value"), strArg(a, "key"))
	if err != nil {
		return Error(err.Error()), nil
	}
	return Result{Content: []Content{{"type": "text", "text": fmt.Sprintf("%v", v)}}, Structured: v}, nil
}
func (b *BrowserTools) evaluate(ctx context.Context, a map[string]any) (Result, error) {
	expr, err := requireString(a, "expression")
	if err != nil {
		return Error(err.Error()), nil
	}
	v, err := b.Browser.Evaluate(ctx, strArg(a, "tab_id"), expr)
	if err != nil {
		return Error(err.Error()), nil
	}
	x, _ := json.MarshalIndent(v, "", "  ")
	return Result{Content: []Content{{"type": "text", "text": string(x)}}, Structured: v}, nil
}
