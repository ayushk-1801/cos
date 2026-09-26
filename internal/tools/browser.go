package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ayush/cos-lite/internal/browser"
	"github.com/ayush/cos-lite/internal/clientctx"
)

type BrowserTools struct {
	Browser *browser.Manager
	Pool    *browser.Pool
}

func (b *BrowserTools) manager(ctx context.Context, a map[string]any, allowCreate bool) (*browser.Manager, string, error) {
	if b.Pool == nil {
		return b.Browser, "shared", nil
	}
	id := strArg(a, "browser_context_id")
	if id != "" {
		m, err := b.Pool.Get(id)
		return m, id, err
	}
	if allowCreate && boolArg(a, "new_context", false) {
		ctxID, m, err := b.Pool.Create()
		return m, ctxID, err
	}
	ctxID, m, err := b.Pool.Default(clientctx.Key(ctx))
	return m, ctxID, err
}

func (b *BrowserTools) Definitions() []Tool {
	ctxSchema := map[string]any{"type": "string", "pattern": "^browser_[0-9a-f]{32}$", "description": "Opaque browser context handle returned by browser_tabs."}
	tabProps := map[string]any{"browser_context_id": ctxSchema, "tab_id": stringSchema("Tab id inside the browser context; optional only when one page tab exists in that context.")}
	return []Tool{
		{Definition: Definition{Name: "browser_tabs", Title: "Browser tabs", Description: "Create/list/close tabs or release an isolated Chromium context. Omitting browser_context_id uses a stable per-client default context for backward compatibility. Set new_context=true to mint an additional opaque browser context handle. Idle contexts are reclaimed automatically after 30 minutes.", InputSchema: objSchema(map[string]any{"action": enumSchema("list", "new", "close", "close_context"), "browser_context_id": ctxSchema, "new_context": map[string]any{"type": "boolean", "default": false, "description": "Mint a fresh isolated browser context when browser_context_id is omitted."}, "tab_id": stringSchema("Tab id for close."), "url": stringSchema("Initial URL for new tab.")}, []string{"action"}), Annotations: readWriteAnnotations()}, Handler: b.tabs},
		{Definition: Definition{Name: "browser_snapshot", Title: "Browser snapshot", Description: "Return visible page text plus up to 300 interactive elements and CSS selectors. Omit browser_context_id to use this client's default context.", InputSchema: objSchema(tabProps, nil), Annotations: readOnlyAnnotations()}, Handler: b.snapshot},
		{Definition: Definition{Name: "browser_screenshot", Title: "Browser screenshot", Description: "Capture the current viewport as PNG through Chromium CDP. Omit browser_context_id to use this client's default context.", InputSchema: objSchema(tabProps, nil), Annotations: readOnlyAnnotations()}, Handler: b.screenshot},
		{Definition: Definition{Name: "browser_console", Title: "Browser console", Description: "Return console/log/exception events observed in one isolated browser context. Omit browser_context_id to use this client's default context. Set clear=true to clear retained events after reading.", InputSchema: objSchema(map[string]any{"browser_context_id": ctxSchema, "tab_id": stringSchema("Tab id; optional only when one page tab exists."), "clear": map[string]any{"type": "boolean", "default": false}}, nil), Annotations: readOnlyAnnotations()}, Handler: b.console},
		{Definition: Definition{Name: "browser_network", Title: "Browser network", Description: "Return recent request/response/failure CDP events in one isolated browser context. Omit browser_context_id to use this client's default context. Set clear=true to clear retained events after reading.", InputSchema: objSchema(map[string]any{"browser_context_id": ctxSchema, "tab_id": stringSchema("Tab id; optional only when one page tab exists."), "clear": map[string]any{"type": "boolean", "default": false}}, nil), Annotations: readOnlyAnnotations()}, Handler: b.network},
		{Definition: Definition{Name: "browser_navigate", Title: "Navigate browser", Description: "Navigate a Chromium page tab and wait for the DOM to become interactive/complete. Omit browser_context_id to use this client's default context.", InputSchema: objSchema(map[string]any{"browser_context_id": ctxSchema, "tab_id": stringSchema("Tab id; optional only when one page tab exists."), "url": stringSchema("Destination URL.")}, []string{"url"}), Annotations: readWriteAnnotations()}, Handler: b.navigate},
		{Definition: Definition{Name: "browser_action", Title: "Interact with page", Description: "Interact with a page using CSS selectors returned by browser_snapshot. Omit browser_context_id to use this client's default context. Actions: click, fill, type, focus, press.", InputSchema: objSchema(map[string]any{"browser_context_id": ctxSchema, "tab_id": stringSchema("Tab id; optional only when one page tab exists."), "action": enumSchema("click", "fill", "type", "focus", "press"), "selector": stringSchema("CSS selector for element actions."), "value": stringSchema("Value/text for fill/type, or key fallback for press."), "key": stringSchema("Key name for press, e.g. Enter.")}, []string{"action"}), Annotations: readWriteAnnotations()}, Handler: b.action},
		{Definition: Definition{Name: "browser_evaluate", Title: "Evaluate JavaScript", Description: "Evaluate JavaScript in a selected Chromium page and return its JSON-serializable value. Omit browser_context_id to use this client's default context.", InputSchema: objSchema(map[string]any{"browser_context_id": ctxSchema, "tab_id": stringSchema("Tab id; optional only when one page tab exists."), "expression": stringSchema("JavaScript expression or IIFE.")}, []string{"expression"}), Annotations: readWriteAnnotations()}, Handler: b.evaluate},
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
		m, ctxID, err := b.manager(ctx, a, true)
		if err != nil {
			return Error(err.Error()), nil
		}
		tabs, err := m.Tabs(ctx)
		if err != nil {
			return Error(err.Error()), nil
		}
		v := map[string]any{"browser_context_id": ctxID, "tabs": tabs}
		x, _ := json.MarshalIndent(v, "", "  ")
		return Result{Content: []Content{{"type": "text", "text": string(x)}}, Structured: v}, nil
	case "new":
		m, ctxID, err := b.manager(ctx, a, true)
		if err != nil {
			return Error(err.Error()), nil
		}
		t, err := m.NewTab(ctx, strArg(a, "url"))
		if err != nil {
			return Error(err.Error()), nil
		}
		v := map[string]any{"browser_context_id": ctxID, "tab": t}
		x, _ := json.MarshalIndent(v, "", "  ")
		return Result{Content: []Content{{"type": "text", "text": string(x)}}, Structured: v}, nil
	case "close":
		id, err := requireString(a, "tab_id")
		if err != nil {
			return Error(err.Error()), nil
		}
		m, _, err := b.manager(ctx, a, false)
		if err != nil {
			return Error(err.Error()), nil
		}
		if err := m.CloseTab(ctx, id); err != nil {
			return Error(err.Error()), nil
		}
		return Text("closed tab " + id), nil
	case "close_context":
		if b.Pool == nil {
			return Error("browser context management is unavailable in shared-browser mode"), nil
		}
		ctxID, err := requireString(a, "browser_context_id")
		if err != nil {
			return Error(err.Error()), nil
		}
		if err := b.Pool.CloseContext(ctxID); err != nil {
			return Error(err.Error()), nil
		}
		return Text("closed browser context"), nil
	default:
		return Error("action must be list, new, close, or close_context"), nil
	}
}
func (b *BrowserTools) snapshot(ctx context.Context, a map[string]any) (Result, error) {
	m, _, err := b.manager(ctx, a, false)
	if err != nil {
		return Error(err.Error()), nil
	}
	v, err := m.Snapshot(ctx, strArg(a, "tab_id"))
	if err != nil {
		return Error(err.Error()), nil
	}
	x, _ := json.MarshalIndent(v, "", "  ")
	return Result{Content: []Content{{"type": "text", "text": string(x)}}, Structured: v}, nil
}
func (b *BrowserTools) screenshot(ctx context.Context, a map[string]any) (Result, error) {
	m, _, err := b.manager(ctx, a, false)
	if err != nil {
		return Error(err.Error()), nil
	}
	data, err := m.Screenshot(ctx, strArg(a, "tab_id"))
	if err != nil {
		return Error(err.Error()), nil
	}
	return Result{Content: []Content{{"type": "image", "mimeType": "image/png", "data": data}, {"type": "text", "text": "Captured Chromium viewport."}}}, nil
}
func (b *BrowserTools) console(ctx context.Context, a map[string]any) (Result, error) {
	m, _, err := b.manager(ctx, a, false)
	if err != nil {
		return Error(err.Error()), nil
	}
	v, err := m.Console(ctx, strArg(a, "tab_id"), boolArg(a, "clear", false))
	if err != nil {
		return Error(err.Error()), nil
	}
	x, _ := json.MarshalIndent(v, "", "  ")
	return Result{Content: []Content{{"type": "text", "text": string(x)}}, Structured: v}, nil
}
func (b *BrowserTools) network(ctx context.Context, a map[string]any) (Result, error) {
	m, _, err := b.manager(ctx, a, false)
	if err != nil {
		return Error(err.Error()), nil
	}
	v, err := m.Network(ctx, strArg(a, "tab_id"), boolArg(a, "clear", false))
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
	m, _, err := b.manager(ctx, a, false)
	if err != nil {
		return Error(err.Error()), nil
	}
	if err := m.Navigate(ctx, strArg(a, "tab_id"), u); err != nil {
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
	m, _, err := b.manager(ctx, a, false)
	if err != nil {
		return Error(err.Error()), nil
	}
	v, err := m.Action(ctx, strArg(a, "tab_id"), action, strArg(a, "selector"), strArg(a, "value"), strArg(a, "key"))
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
	m, _, err := b.manager(ctx, a, false)
	if err != nil {
		return Error(err.Error()), nil
	}
	v, err := m.Evaluate(ctx, strArg(a, "tab_id"), expr)
	if err != nil {
		return Error(err.Error()), nil
	}
	x, _ := json.MarshalIndent(v, "", "  ")
	return Result{Content: []Content{{"type": "text", "text": string(x)}}, Structured: v}, nil
}
