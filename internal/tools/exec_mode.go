package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	proc "github.com/ayush/cos-lite/internal/process"
)

// ExecMode keeps the CoS-style composition tool while avoiding an embedded JS runtime.
// If code is supplied, it runs in Node's vm context inside a separate permission-restricted
// process. The sandbox receives only a tools proxy and a tiny console. A declarative calls
// array remains available when Node is not installed.
type ExecMode struct{ Registry *Registry }

func (e *ExecMode) Definition() Tool {
	return Tool{Definition: Definition{
		Name: "exec", Title: "Compose local tools",
		Description: "Compose local MCP tools in one request. Prefer code for CoS-style JavaScript: `const r = await tools.read({paths:[\"README.md\"]}); return r;`. JavaScript runs in a separate Node vm with Node's permission system enabled and no filesystem/child-process permissions. Alternatively provide a declarative calls array. Recursive exec calls are refused.",
		InputSchema: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"code":              map[string]any{"type": "string", "description": "JavaScript body executed as an async function with a `tools` proxy."},
				"calls":             map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"tool": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"}}, "required": []string{"tool"}}},
				"continue_on_error": map[string]any{"type": "boolean", "default": false},
				"timeout_ms":        map[string]any{"type": "integer", "minimum": 1000, "maximum": 120000, "default": 30000},
			},
		},
		Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": false},
	}, Handler: e.Handle}
}

func (e *ExecMode) Handle(ctx context.Context, args map[string]any) (Result, error) {
	code := strArg(args, "code")
	raw, hasCalls := args["calls"]
	if code != "" && hasCalls {
		return Error("provide code or calls, not both"), nil
	}
	if code != "" {
		timeout := time.Duration(intArg(args, "timeout_ms", 30000)) * time.Millisecond
		return e.handleJS(ctx, code, timeout)
	}
	calls, ok := raw.([]any)
	if !ok || len(calls) == 0 {
		return Error("code or a non-empty calls array is required"), nil
	}
	return e.handleCalls(ctx, calls, boolArg(args, "continue_on_error", false))
}

func (e *ExecMode) handleCalls(ctx context.Context, raw []any, cont bool) (Result, error) {
	if len(raw) > 20 {
		return Error("at most 20 calls are allowed"), nil
	}
	var out strings.Builder
	structured := make([]any, 0, len(raw))
	for i, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			return Error(fmt.Sprintf("calls[%d] must be an object", i)), nil
		}
		name := strArg(m, "tool")
		if name == "" || name == "exec" {
			return Error(fmt.Sprintf("calls[%d].tool must name a non-exec tool", i)), nil
		}
		a, _ := m["arguments"].(map[string]any)
		if a == nil {
			a = map[string]any{}
		}
		res, err := e.Registry.Call(ctx, name, a)
		if err != nil {
			return Error(err.Error()), nil
		}
		fmt.Fprintf(&out, "== %d: %s ==\n", i+1, name)
		for _, c := range res.Content {
			if c["type"] == "text" {
				fmt.Fprintln(&out, c["text"])
			} else {
				fmt.Fprintf(&out, "[%v content]\n", c["type"])
			}
		}
		structured = append(structured, map[string]any{"tool": name, "is_error": res.IsError, "result": res.Structured})
		if res.IsError && !cont {
			return Result{Content: []Content{{"type": "text", "text": strings.TrimRight(out.String(), "\n")}}, Structured: structured, IsError: true}, nil
		}
	}
	return Result{Content: []Content{{"type": "text", "text": strings.TrimRight(out.String(), "\n")}}, Structured: structured}, nil
}

const nodeHarness = `
const readline=require('node:readline');
const vm=require('node:vm');
const rl=readline.createInterface({input:process.stdin,crlfDelay:Infinity});
const pending=new Map(); let seq=0, started=false;
function emit(x){process.stdout.write(JSON.stringify(x)+'\n')}
function safe(v){try{JSON.stringify(v);return v}catch{return String(v)}}
function tool(name,args={}){return new Promise((resolve,reject)=>{const id=++seq;pending.set(id,{resolve,reject});emit({kind:'call',id,tool:name,args:args??{}})})}
const tools=new Proxy(Object.create(null),{get(_t,p){if(typeof p!=='string')return undefined;return (args)=>tool(p,args)}});
function onResult(m){const p=pending.get(m.id);if(!p)return;pending.delete(m.id);if(m.error)p.reject(new Error(m.error));else p.resolve(m.result)}
async function run(code){
 const sandbox=Object.create(null); sandbox.tools=tools; sandbox.JSON=JSON;
 sandbox.console=Object.freeze({log:(...a)=>emit({kind:'log',text:a.map(x=>typeof x==='string'?x:JSON.stringify(safe(x))).join(' ')})});
 const ctx=vm.createContext(sandbox,{name:'cos-exec',codeGeneration:{strings:false,wasm:false}});
 try{const script=new vm.Script('(async()=>{'+code+'\n})()',{filename:'cos-exec.js'});const value=await script.runInContext(ctx,{timeout:5000});emit({kind:'done',value:safe(value)});setImmediate(()=>process.exit(0))}
 catch(e){emit({kind:'error',error:String(e&&e.stack||e)});setImmediate(()=>process.exit(1))}
}
rl.on('line',line=>{let m;try{m=JSON.parse(line)}catch(e){emit({kind:'error',error:'bad parent message'});return}if(m.kind==='run'&&!started){started=true;run(String(m.code||''))}else if(m.kind==='result')onResult(m)});
`

func nodePermissionFlag(node string) (string, error) {
	out, err := exec.Command(node, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("cannot determine Node.js version: %w", err)
	}
	v := strings.TrimSpace(string(out))
	v = strings.TrimPrefix(v, "v")
	majorText := v
	if i := strings.IndexByte(majorText, '.'); i >= 0 {
		majorText = majorText[:i]
	}
	major, err := strconv.Atoi(majorText)
	if err != nil || major < 20 {
		return "", fmt.Errorf("exec code mode requires Node.js 20+; found %q", strings.TrimSpace(string(out)))
	}
	if major >= 22 {
		return "--permission", nil
	}
	return "--experimental-permission", nil
}

func (e *ExecMode) handleJS(parent context.Context, code string, timeout time.Duration) (Result, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		return Error("exec code mode requires Node.js 20+; use the calls array instead or install node"), nil
	}
	permissionFlag, err := nodePermissionFlag(node)
	if err != nil {
		return Error(err.Error()), nil
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		return Error("exec code mode requires util-linux unshare for its Linux network/PID sandbox; use the calls array instead"), nil
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	// Node's permission system denies filesystem, child-process, worker and native-addon
	// APIs, while a fresh Linux network + PID namespace closes the remaining host-network
	// and process-control escape hatches. This matters because node:vm by itself is not a
	// security boundary. If unprivileged user namespaces are disabled, code mode fails
	// closed and callers can still use the declarative calls form.
	cmd := exec.CommandContext(ctx, unshare, "-Urnpf", "--kill-child=SIGKILL", "--", node, permissionFlag, "--max-old-space-size=128", "-e", nodeHarness)
	cmd.Env = proc.SanitizedEnv()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Error(err.Error()), nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Error(err.Error()), nil
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Error(err.Error()), nil
	}
	if err := cmd.Start(); err != nil {
		return Error(err.Error()), nil
	}
	defer func() { _ = cmd.Process.Kill() }()
	enc := json.NewEncoder(stdin)
	if err := enc.Encode(map[string]any{"kind": "run", "code": code}); err != nil {
		return Error(err.Error()), nil
	}

	// Drain stderr to avoid blocking and preserve useful diagnostics if Node itself fails.
	var stderrText strings.Builder
	go func() {
		s := bufio.NewScanner(stderr)
		for s.Scan() {
			if stderrText.Len() < 32*1024 {
				stderrText.WriteString(s.Text())
				stderrText.WriteByte('\n')
			}
		}
	}()

	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var logs strings.Builder
	for scan.Scan() {
		var msg map[string]any
		dec := json.NewDecoder(strings.NewReader(scan.Text()))
		dec.UseNumber()
		if err := dec.Decode(&msg); err != nil {
			return Error("invalid exec runtime message: " + err.Error()), nil
		}
		switch msg["kind"] {
		case "log":
			if logs.Len() < 64*1024 {
				fmt.Fprintln(&logs, msg["text"])
			}
		case "call":
			id := msg["id"]
			name, _ := msg["tool"].(string)
			if name == "" || name == "exec" {
				_ = enc.Encode(map[string]any{"kind": "result", "id": id, "error": "recursive or empty tool call refused"})
				continue
			}
			a, _ := msg["args"].(map[string]any)
			if a == nil {
				a = map[string]any{}
			}
			res, err := e.Registry.Call(ctx, name, a)
			if err != nil {
				_ = enc.Encode(map[string]any{"kind": "result", "id": id, "error": err.Error()})
				continue
			}
			_ = enc.Encode(map[string]any{"kind": "result", "id": id, "result": map[string]any{"content": res.Content, "structuredContent": res.Structured, "isError": res.IsError}})
		case "done":
			_ = cmd.Wait()
			payload := msg["value"]
			pretty, _ := json.MarshalIndent(payload, "", "  ")
			text := strings.TrimSpace(logs.String())
			if len(pretty) > 0 && string(pretty) != "null" {
				if text != "" {
					text += "\n\n"
				}
				text += string(pretty)
			}
			if text == "" {
				text = "exec completed"
			}
			return Result{Content: []Content{{"type": "text", "text": text}}, Structured: payload}, nil
		case "error":
			_ = cmd.Wait()
			return Error(fmt.Sprint(msg["error"])), nil
		}
	}
	_ = cmd.Wait()
	if ctx.Err() != nil {
		return Error("exec code mode timed out or was cancelled: " + ctx.Err().Error()), nil
	}
	msg := strings.TrimSpace(stderrText.String())
	if msg == "" {
		msg = "exec runtime exited without a result"
	}
	return Error(msg), nil
}
