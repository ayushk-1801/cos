package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ayush/cos-lite/internal/tools"
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
