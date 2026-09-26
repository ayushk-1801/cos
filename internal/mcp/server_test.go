package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ayush/cos-lite/internal/clientctx"
	"github.com/ayush/cos-lite/internal/instructions"
	proc "github.com/ayush/cos-lite/internal/process"
	resourcepkg "github.com/ayush/cos-lite/internal/resources"
	taskpkg "github.com/ayush/cos-lite/internal/tasks"
	"github.com/ayush/cos-lite/internal/tools"
	"github.com/ayush/cos-lite/internal/workspace"
)

func TestDiscoverAndList(t *testing.T) {
	r := tools.NewRegistry()
	_ = r.Register(tools.Tool{Definition: tools.Definition{Name: "x", Description: "x", InputSchema: map[string]any{"type": "object"}}, Handler: func(context.Context, map[string]any) (tools.Result, error) { return tools.Text("ok"), nil }})
	s := New(r)
	b, ok := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`), "")
	if !ok {
		t.Fatal("missing response")
	}
	var v map[string]any
	if json.Unmarshal(b, &v) != nil {
		t.Fatal(string(b))
	}
	res := v["result"].(map[string]any)
	if res["resultType"] != "complete" {
		t.Fatalf("%s", b)
	}
	versions, _ := res["supportedVersions"].([]any)
	foundLegacy := false
	for _, version := range versions {
		if version == VersionLegacy {
			foundLegacy = true
		}
	}
	if !foundLegacy {
		t.Fatalf("discover did not advertise %s: %s", VersionLegacy, b)
	}
	b, _ = s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`), "")
	if !json.Valid(b) {
		t.Fatalf("%s", b)
	}
}

func TestHTTP2026HeaderValidation(t *testing.T) {
	r := tools.NewRegistry()
	_ = r.Register(tools.Tool{Definition: tools.Definition{Name: "x", Description: "x", InputSchema: map[string]any{"type": "object"}}, Handler: func(context.Context, map[string]any) (tools.Result, error) { return tools.Text("ok"), nil }})
	s := New(r)
	h := s.HTTPHandler("secret")
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`
	req := httptest.NewRequest("POST", "/mcp/secret", strings.NewReader(body))
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/list")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("POST", "/mcp/secret", strings.NewReader(body))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "-32020") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestLegacyInitializeNegotiates2025November(t *testing.T) {
	r := tools.NewRegistry()
	s := New(r)
	b, ok := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`), "")
	if !ok {
		t.Fatal("missing response")
	}
	var v struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if v.Result.ProtocolVersion != VersionLegacy {
		t.Fatalf("got %q: %s", v.Result.ProtocolVersion, b)
	}
}

func TestHTTPRejectsNonLoopbackOrigin(t *testing.T) {
	r := tools.NewRegistry()
	s := New(r)
	h := s.HTTPHandler("secret")
	body := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
	req := httptest.NewRequest("POST", "/mcp/secret", strings.NewReader(body))
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("MCP-Protocol-Version", VersionCurrent)
	req.Header.Set("Mcp-Method", "server/discover")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHTTPRejectsUnsupportedVersion(t *testing.T) {
	r := tools.NewRegistry()
	s := New(r)
	h := s.HTTPHandler("secret")
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req := httptest.NewRequest("POST", "/mcp/secret", strings.NewReader(body))
	req.Header.Set("MCP-Protocol-Version", "2099-01-01")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "-32022") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestResourcesModernShape(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("test instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, _ := workspace.NewRoots([]workspace.Root{{Name: "proj", Path: root}})
	r := tools.NewRegistry()
	s := New(r)
	s.Resources = &resourcepkg.Provider{WS: ws, Instructions: &instructions.Resolver{WS: ws}}
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"cos://projects/proj/instructions","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"client-a","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`)
	b, ok := s.Handle(context.Background(), body, VersionCurrent)
	if !ok {
		t.Fatal("missing response")
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	res := v["result"].(map[string]any)
	if res["resultType"] != "complete" || res["cacheScope"] != "private" || res["ttlMs"] == nil || !strings.Contains(string(b), "test instructions") {
		t.Fatalf("response=%s", b)
	}
}

func TestNestedAgentsAutomaticallySurfaceOnPathTool(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "src", "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root instruction"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "AGENTS.md"), []byte("nested instruction"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "x.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.NewRoots([]workspace.Root{{Name: "proj", Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry()
	if err := reg.Register((&tools.Reader{WS: ws}).Definition()); err != nil {
		t.Fatal(err)
	}
	s := New(reg)
	s.Resources = &resourcepkg.Provider{WS: ws, Instructions: &instructions.Resolver{WS: ws}}
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read","arguments":{"path":"/proj/src/pkg/x.go"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`)
	b, ok := s.Handle(context.Background(), body, VersionCurrent)
	if !ok {
		t.Fatal("missing response")
	}
	text := string(b)
	if !strings.Contains(text, "nested instruction") || !strings.Contains(text, "Applicable nested AGENTS.md") {
		t.Fatalf("nested instructions missing: %s", b)
	}
	if strings.Contains(text, "root instruction") {
		t.Fatalf("root instructions should stay in server instructions, not duplicate in tool result: %s", b)
	}
}

func TestExecTaskExtensionUsesOpaqueCapability(t *testing.T) {
	root := t.TempDir()
	ws, _ := workspace.NewRoots([]workspace.Root{{Name: "proj", Path: root}})
	pm := proc.NewManager()
	reg := tools.NewRegistry()
	ex := &tools.Executor{WS: ws, PM: pm}
	if err := reg.Register(ex.ExecDefinition()); err != nil {
		t.Fatal(err)
	}
	s := New(reg)
	s.Tasks = taskpkg.NewManager(pm)
	call := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"exec_command","arguments":{"cmd":"sleep 0.1; printf task-ok","workdir":"/proj","yield_time_ms":5},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"ChatGPT","version":"1"},"openai/session":"chat-a","io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`)
	b, _ := s.Handle(context.Background(), call, VersionCurrent)
	var envelope map[string]any
	_ = json.Unmarshal(b, &envelope)
	result := envelope["result"].(map[string]any)
	if result["resultType"] != "task" {
		t.Fatalf("expected task result: %s", b)
	}
	id, _ := result["taskId"].(string)
	if id == "" {
		t.Fatalf("missing task id: %s", b)
	}
	getBody := func(session string) []byte {
		return []byte(`{"jsonrpc":"2.0","id":2,"method":"tasks/get","params":{"taskId":"` + id + `","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"ChatGPT","version":"1"},"openai/session":"` + session + `","io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`)
	}
	unknownBody := []byte(`{"jsonrpc":"2.0","id":3,"method":"tasks/get","params":{"taskId":"task_00000000000000000000000000000000","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"ChatGPT","version":"1"},"openai/session":"chat-b","io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`)
	unknown, _ := s.Handle(context.Background(), unknownBody, VersionCurrent)
	if !strings.Contains(string(unknown), "unknown taskId") {
		t.Fatalf("unknown task capability was not rejected: %s", unknown)
	}
	// clientInfo/openai/session are diagnostic metadata, not an authorization
	// boundary. Possession of the unguessable task capability is sufficient.
	other, _ := s.Handle(context.Background(), getBody("chat-b"), VersionCurrent)
	if !strings.Contains(string(other), `"status":"working"`) && !strings.Contains(string(other), `"status":"completed"`) {
		t.Fatalf("valid task capability did not resolve across diagnostic client metadata: %s", other)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.Handle(context.Background(), getBody("chat-a"), VersionCurrent)
		if strings.Contains(string(got), `"status":"completed"`) {
			if !strings.Contains(string(got), "task-ok") {
				t.Fatalf("missing final output: %s", got)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task did not complete")
}

func TestHTTPModernRoutingNamesForResourceAndTask(t *testing.T) {
	r := tools.NewRegistry()
	s := New(r)
	h := s.HTTPHandler("secret")
	for _, tc := range []struct {
		method string
		name   string
		params string
	}{
		{"resources/read", "cos://status", `"uri":"cos://status"`},
		{"tasks/get", "task_abc", `"taskId":"task_abc"`},
	} {
		body := `{"jsonrpc":"2.0","id":1,"method":"` + tc.method + `","params":{` + tc.params + `,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`
		req := httptest.NewRequest("POST", "/mcp/secret", strings.NewReader(body))
		req.Header.Set("MCP-Protocol-Version", VersionCurrent)
		req.Header.Set("Mcp-Method", tc.method)
		req.Header.Set("Mcp-Name", tc.name+"-wrong")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "-32020") {
			t.Fatalf("%s status=%d body=%s", tc.method, w.Code, w.Body.String())
		}
	}
}

func TestAuditTargetRedactsTaskCapability(t *testing.T) {
	taskID := "task_0123456789abcdef0123456789abcdef"
	raw := json.RawMessage(`{"taskId":"` + taskID + `"}`)
	for _, method := range []string{"tasks/get", "tasks/update", "tasks/cancel"} {
		got := requestTarget(method, raw)
		if got != "task" || strings.Contains(got, taskID) {
			t.Fatalf("%s audit target leaked task capability: %q", method, got)
		}
	}
}

func TestHTTPTraceparentReachesToolContext(t *testing.T) {
	r := tools.NewRegistry()
	if err := r.Register(tools.Tool{
		Definition: tools.Definition{Name: "trace", Description: "trace", InputSchema: map[string]any{"type": "object"}},
		Handler: func(ctx context.Context, _ map[string]any) (tools.Result, error) {
			return tools.Text(clientctx.From(ctx).TraceParent), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	s := New(r)
	h := s.HTTPHandler("secret")
	tp := "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"trace","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"client","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	req := httptest.NewRequest("POST", "/mcp/secret", strings.NewReader(body))
	req.Header.Set("MCP-Protocol-Version", VersionCurrent)
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "trace")
	req.Header.Set("traceparent", tp)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), tp) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestActivityPreviewExtractsTextAndRedactsSecrets(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"image","data":"QUJDREVGRw=="},{"type":"text","text":"ok token=supersecret Authorization: Bearer abc123 /mcp/0123456789abcdef0123456789abcdef browser_0123456789abcdef0123456789abcdef"}]}}`)
	got := responseActivityPreview("tools/call", body)
	if strings.Contains(got, "QUJD") || strings.Contains(got, "supersecret") || strings.Contains(got, "abc123") || strings.Contains(got, "0123456789abcdef") {
		t.Fatalf("preview leaked sensitive/binary content: %q", got)
	}
	if !strings.Contains(got, "ok") || !strings.Contains(got, "********") {
		t.Fatalf("preview missing expected content/redaction: %q", got)
	}
	if got := responseActivityPreview("tools/list", body); got != "" {
		t.Fatalf("non-result-heavy method should not record output preview: %q", got)
	}
}
