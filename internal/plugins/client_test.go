package plugins

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFindPluginExecutableSearchesVitePlusBin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	p := filepath.Join(home, ".vite-plus", "bin", "npx")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := findPluginExecutable("npx")
	if err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Fatalf("got %q want %q", got, p)
	}
}

func TestPluginDiscoveryAndCall(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	script := `import sys,json
for line in sys.stdin:
 m=json.loads(line); mid=m.get('id'); method=m.get('method')
 if mid is None: continue
 if method=='server/discover': r={'resultType':'complete','supportedVersions':['2026-07-28'],'capabilities':{'tools':{}},'ttlMs':1000,'cacheScope':'public'}
 elif method=='tools/list': r={'resultType':'complete','tools':[{'name':'echo','description':'echo','inputSchema':{'type':'object','properties':{'text':{'type':'string'}}}}],'ttlMs':1000,'cacheScope':'public'}
 elif method=='tools/call': r={'resultType':'complete','content':[{'type':'text','text':m['params']['arguments'].get('text','')} ]}
 else:
  print(json.dumps({'jsonrpc':'2.0','id':mid,'error':{'code':-32601,'message':'no'}}),flush=True); continue
 print(json.dumps({'jsonrpc':'2.0','id':mid,'result':r}),flush=True)
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Start(ctx, Config{Name: "fake", Command: []string{"python3", "-u", "-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	defs, err := c.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || fmt.Sprint(defs[0]["name"]) != "echo" {
		t.Fatalf("defs=%v", defs)
	}
	res, err := c.Call(ctx, "echo", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(res["content"]) == "" {
		t.Fatalf("res=%v", res)
	}
}
