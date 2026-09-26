package tools

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func requireJSSandbox(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("unshare unavailable")
	}
	if err := exec.Command(unshare, "-Urn", "true").Run(); err != nil {
		t.Skipf("unprivileged user/network namespaces unavailable: %v", err)
	}
}

func echoRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	if err := r.Register(Tool{Definition: Definition{Name: "echo", Description: "echo", InputSchema: map[string]any{"type": "object"}}, Handler: func(_ context.Context, a map[string]any) (Result, error) {
		return Result{Content: []Content{{"type": "text", "text": strArg(a, "text")}}, Structured: map[string]any{"text": strArg(a, "text")}}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestExecModeJavaScript(t *testing.T) {
	requireJSSandbox(t)
	e := &ExecMode{Registry: echoRegistry(t)}
	res, err := e.Handle(context.Background(), map[string]any{"code": `const r=await tools.echo({text:"hello"}); console.log(r.structuredContent.text); return {ok:r.structuredContent.text};`})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("error result: %+v", res)
	}
	text := res.Content[0]["text"].(string)
	if !strings.Contains(text, "hello") {
		t.Fatalf("%q", text)
	}
}

func TestExecModeJavaScriptCannotReadHostFiles(t *testing.T) {
	requireJSSandbox(t)
	e := &ExecMode{Registry: echoRegistry(t)}
	// Deliberately use the classic vm host-function constructor escape. The Node
	// permission layer must still refuse the filesystem read even if the script
	// obtains require/process through a host callback.
	res, err := e.Handle(context.Background(), map[string]any{"code": `const req=tools.echo.constructor("return require")(); return req("node:fs").readFileSync("/etc/passwd","utf8")`})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("host read unexpectedly succeeded: %+v", res)
	}
	text, _ := res.Content[0]["text"].(string)
	if !strings.Contains(text, "restricted") && !strings.Contains(strings.ToLower(text), "permission") {
		t.Fatalf("unexpected refusal: %q", text)
	}
}
