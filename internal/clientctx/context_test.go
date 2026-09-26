package clientctx

import (
	"context"
	"strings"
	"testing"
)

func TestFromMetaStableAndExtensions(t *testing.T) {
	meta := map[string]any{
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "ChatGPT", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{"extensions": map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}}},
	}
	a := FromMeta(meta)
	b := FromMeta(meta)
	if a.Key == "" || a.Key != b.Key || a.Name != "ChatGPT" {
		t.Fatalf("bad identity: %#v %#v", a, b)
	}
	ctx := With(context.Background(), a)
	if !SupportsExtension(ctx, "io.modelcontextprotocol/tasks") {
		t.Fatal("tasks extension not detected")
	}
}

func TestOpenAISessionCreatesOwnerNamespace(t *testing.T) {
	meta := map[string]any{
		"io.modelcontextprotocol/clientInfo": map[string]any{"name": "ChatGPT", "version": "1"},
		"openai/session":                     "conversation-a",
	}
	info := FromMeta(meta)
	if !info.SessionScoped || !strings.Contains(info.Key, "session-") {
		t.Fatalf("unexpected session info: %#v", info)
	}
	ctx := With(context.Background(), info)
	if OwnerKey(ctx) != info.Key {
		t.Fatalf("owner key mismatch: %q != %q", OwnerKey(ctx), info.Key)
	}
	generic := With(context.Background(), FromMeta(map[string]any{"io.modelcontextprotocol/clientInfo": map[string]any{"name": "generic", "version": "1"}}))
	if OwnerKey(generic) != "" {
		t.Fatalf("generic clientInfo must not become auth owner: %q", OwnerKey(generic))
	}
}

func TestOpenAISessionSeparatesChatsWithSameClientInfo(t *testing.T) {
	base := map[string]any{
		"io.modelcontextprotocol/clientInfo": map[string]any{"name": "ChatGPT", "version": "1"},
	}
	a := map[string]any{}
	b := map[string]any{}
	for k, v := range base {
		a[k] = v
		b[k] = v
	}
	a["openai/session"] = "conversation-a"
	b["openai/session"] = "conversation-b"
	ai := FromMeta(a)
	bi := FromMeta(b)
	if ai.Key == bi.Key || !strings.Contains(ai.Key, "session-") || !strings.Contains(bi.Key, "session-") {
		t.Fatalf("chat sessions were not isolated: a=%q b=%q", ai.Key, bi.Key)
	}
	if strings.Contains(ai.Key, "conversation-a") || strings.Contains(bi.Key, "conversation-b") {
		t.Fatal("raw OpenAI session id leaked in client key")
	}
}
