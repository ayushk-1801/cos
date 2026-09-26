package tools

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireJSSandbox(t *testing.T) {
	t.Helper()
	if _, err := JSSandboxStatus(); err != nil {
		t.Skipf("JavaScript sandbox unavailable: %v", err)
	}
}

func TestFindNodeRuntimeSkipsNodeNameShim(t *testing.T) {
	root := t.TempDir()
	badDir := filepath.Join(root, "bad")
	goodDir := filepath.Join(root, "good")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(goodDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(badDir, "node")
	good := filepath.Join(goodDir, "node")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo v24.0.0; else exit 1; fi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo v24.0.0; elif [ \"$1\" = -e ]; then printf node; else exit 1; fi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", badDir+string(os.PathListSeparator)+goodDir)
	t.Setenv("HOME", root)
	node, flag, err := findNodeRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if node != good || flag != "--permission" {
		t.Fatalf("node=%q flag=%q", node, flag)
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

func TestExecModeJavaScriptCannotReachHostNetwork(t *testing.T) {
	requireJSSandbox(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			_ = conn.Close()
			accepted <- struct{}{}
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	code := fmt.Sprintf(`
const req=tools.echo.constructor("return require")();
const http=req("node:http");
return await new Promise(resolve=>{
  const q=http.get("http://127.0.0.1:%d/",()=>resolve("NETWORK_REACHED"));
  q.on("error",e=>resolve("blocked:"+(e.code||e.message)));
  setTimeout(()=>resolve("blocked:timeout"),800);
});`, port)
	e := &ExecMode{Registry: echoRegistry(t)}
	res, err := e.Handle(context.Background(), map[string]any{"code": code, "timeout_ms": 5000})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-accepted:
		t.Fatal("sandboxed JavaScript reached a host TCP listener")
	case <-time.After(150 * time.Millisecond):
	}
	if !res.IsError {
		text, _ := res.Content[0]["text"].(string)
		if strings.Contains(text, "NETWORK_REACHED") {
			t.Fatalf("sandboxed JavaScript reached host network: %q", text)
		}
	}
}
