package codeintel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ayush/cos-lite/internal/managedproc"
	proc "github.com/ayush/cos-lite/internal/process"
	"github.com/ayush/cos-lite/internal/workspace"
)

type Client struct {
	WS        *workspace.Workspace
	Overrides map[string][]string
	Pool      *Pool
}

type Request struct {
	Action             string
	Path               string
	Language           string
	Line               int
	Column             int
	Query              string
	NewName            string
	IncludeDeclaration bool
	Timeout            time.Duration
}

type Response struct {
	Language string `json:"language"`
	Server   string `json:"server"`
	Result   any    `json:"result"`
}

type languageSpec struct {
	ID       string
	Commands [][]string
	Hint     string
}

func (c *Client) Run(ctx context.Context, req Request) (Response, error) {
	if c.WS == nil {
		return Response{}, errors.New("code intelligence workspace is not configured")
	}
	resolved, err := c.WS.Resolve(req.Path)
	if err != nil {
		return Response{}, err
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return Response{}, err
	}
	lang := strings.TrimSpace(req.Language)
	if lang == "" {
		lang = detectLanguage(resolved)
	}
	if lang == "" {
		return Response{}, fmt.Errorf("cannot detect language for %s; pass language explicitly", req.Path)
	}
	spec, ok := specs()[lang]
	if !ok {
		return Response{}, fmt.Errorf("unsupported code-intel language %q", lang)
	}
	root := c.rootFor(resolved, lang)
	if root.Path == "" {
		return Response{}, fmt.Errorf("path is outside approved projects")
	}
	cmdline, err := c.commandFor(spec)
	if err != nil {
		return Response{}, err
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var p *lspProc
	var lease *Lease
	if c.Pool != nil {
		lease, err = c.Pool.Acquire(runCtx, root.Path, cmdline)
		if err != nil {
			return Response{}, err
		}
		defer lease.Release()
		p = lease.Proc()
	} else {
		p, err = startLSP(runCtx, cmdline, root.Path)
		if err != nil {
			return Response{}, err
		}
		defer p.close()
		if err := p.initializeContext(runCtx, root.Path); err != nil {
			return Response{}, err
		}
	}

	uri := fileURI(resolved)
	if !st.IsDir() && needsDocument(req.Action) {
		b, err := os.ReadFile(resolved)
		if err != nil {
			return Response{}, err
		}
		if len(b) > 4*1024*1024 {
			return Response{}, fmt.Errorf("source file exceeds 4 MiB code-intel limit")
		}
		if err := p.syncDocument(uri, lang, string(b)); err != nil {
			return Response{}, err
		}
	}

	position := map[string]any{"line": max(req.Line-1, 0), "character": max(req.Column-1, 0)}
	doc := map[string]any{"uri": uri}
	var method string
	var params any
	switch req.Action {
	case "definition":
		method = "textDocument/definition"
		params = map[string]any{"textDocument": doc, "position": position}
	case "references":
		method = "textDocument/references"
		params = map[string]any{"textDocument": doc, "position": position, "context": map[string]any{"includeDeclaration": req.IncludeDeclaration}}
	case "hover":
		method = "textDocument/hover"
		params = map[string]any{"textDocument": doc, "position": position}
	case "document_symbols":
		method = "textDocument/documentSymbol"
		params = map[string]any{"textDocument": doc}
	case "workspace_symbols":
		method = "workspace/symbol"
		params = map[string]any{"query": req.Query}
	case "implementations":
		method = "textDocument/implementation"
		params = map[string]any{"textDocument": doc, "position": position}
	case "rename":
		if strings.TrimSpace(req.NewName) == "" {
			return Response{}, errors.New("new_name is required for rename")
		}
		method = "textDocument/rename"
		params = map[string]any{"textDocument": doc, "position": position, "newName": req.NewName}
	case "diagnostics":
		method = "textDocument/diagnostic"
		params = map[string]any{"textDocument": doc}
	default:
		return Response{}, fmt.Errorf("unsupported code_intel action %q", req.Action)
	}
	result, err := p.requestContext(runCtx, method, params)
	if err != nil && req.Action == "diagnostics" && len(p.diagnostics(uri)) > 0 {
		result = map[string]any{"kind": "full", "items": p.diagnostics(uri), "source": "textDocument/publishDiagnostics"}
		err = nil
	}
	if err != nil {
		return Response{}, err
	}
	if req.Action == "workspace_symbols" {
		result = c.filterWorkspaceSymbols(result)
	}
	result = c.virtualize(result)
	return Response{Language: lang, Server: strings.Join(cmdline, " "), Result: result}, nil
}

func (c *Client) filterWorkspaceSymbols(v any) any {
	items, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		loc, _ := m["location"].(map[string]any)
		uri, _ := loc["uri"].(string)
		if uri == "" || !strings.HasPrefix(uri, "file://") {
			out = append(out, item)
			continue
		}
		p, err := pathFromURI(uri)
		if err == nil && c.approvedPath(p) {
			out = append(out, item)
		}
	}
	return out
}

func (c *Client) approvedPath(path string) bool {
	clean := filepath.Clean(path)
	for _, r := range c.WS.Roots {
		rel, err := filepath.Rel(r.Path, clean)
		if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
			return true
		}
	}
	return false
}

func (c *Client) commandFor(spec languageSpec) ([]string, error) {
	if ov := c.Overrides[spec.ID]; len(ov) > 0 {
		if filepath.IsAbs(ov[0]) {
			if st, err := os.Stat(ov[0]); err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
				return nil, fmt.Errorf("configured %s server %q not executable", spec.ID, ov[0])
			}
			return append([]string(nil), ov...), nil
		}
		path, err := findExecutable(ov[0])
		if err != nil {
			return nil, fmt.Errorf("configured %s server %q not found", spec.ID, ov[0])
		}
		return append([]string{path}, ov[1:]...), nil
	}
	for _, candidate := range spec.Commands {
		if len(candidate) == 0 {
			continue
		}
		if path, err := findExecutable(candidate[0]); err == nil {
			out := append([]string{path}, candidate[1:]...)
			return out, nil
		}
	}
	return nil, fmt.Errorf("no %s language server found. %s", spec.ID, spec.Hint)
}
func (c *Client) rootFor(path, lang string) workspace.Root {
	clean := filepath.Clean(path)
	for _, r := range c.WS.Roots {
		rel, err := filepath.Rel(r.Path, clean)
		if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
			rootPath := nearestLanguageRoot(clean, r.Path, lang)
			return workspace.Root{Name: r.Name, Path: rootPath}
		}
	}
	return workspace.Root{}
}

func nearestLanguageRoot(path, boundary, lang string) string {
	start := path
	if st, err := os.Stat(start); err == nil && !st.IsDir() {
		start = filepath.Dir(start)
	}
	start = filepath.Clean(start)
	boundary = filepath.Clean(boundary)
	markers := languageRootMarkers(lang)
	gitFallback := ""
	for dir := start; ; dir = filepath.Dir(dir) {
		for _, marker := range markers {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir
			}
		}
		if gitFallback == "" {
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				gitFallback = dir
			}
		}
		if dir == boundary {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		rel, err := filepath.Rel(boundary, parent)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			break
		}
	}
	if gitFallback != "" {
		return gitFallback
	}
	// Broad approved roots commonly contain several unrelated repositories.
	// If no language marker or Git boundary exists, keep the LSP scoped to the
	// file's directory instead of indexing the entire exposed root.
	return start
}

func languageRootMarkers(lang string) []string {
	switch lang {
	case "go":
		return []string{"go.work", "go.mod"}
	case "rust":
		return []string{"Cargo.toml"}
	case "typescript", "javascript":
		return []string{"tsconfig.json", "jsconfig.json", "package.json"}
	case "python":
		return []string{"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt"}
	case "c", "cpp":
		return []string{"compile_commands.json", "CMakeLists.txt", "meson.build"}
	default:
		return nil
	}
}
func (c *Client) virtualize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = c.virtualize(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = c.virtualize(vv)
		}
		return out
	case string:
		if strings.HasPrefix(x, "file://") {
			if p, err := pathFromURI(x); err == nil {
				if c.approvedPath(p) {
					return c.WS.Display(p)
				}
				return "external://" + filepath.Base(p)
			}
		}
		return x
	default:
		return v
	}
}

func detectLanguage(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".cxx", ".hh", ".hpp", ".hxx":
		return "cpp"
	case ".rs":
		return "rust"
	case ".py", ".pyi":
		return "python"
	case ".ts", ".tsx", ".mts", ".cts":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	}
	return ""
}
func specs() map[string]languageSpec {
	return map[string]languageSpec{
		"go":         {ID: "go", Commands: [][]string{{"gopls"}}, Hint: "Install with: go install golang.org/x/tools/gopls@latest"},
		"c":          {ID: "c", Commands: [][]string{{"clangd"}}, Hint: "Install with: sudo apt install clangd"},
		"cpp":        {ID: "cpp", Commands: [][]string{{"clangd"}}, Hint: "Install with: sudo apt install clangd"},
		"rust":       {ID: "rust", Commands: [][]string{{"rust-analyzer"}}, Hint: "Install rust-analyzer through rustup or apt."},
		"python":     {ID: "python", Commands: [][]string{{"basedpyright-langserver", "--stdio"}, {"pyright-langserver", "--stdio"}}, Hint: "Install basedpyright or pyright (for example: pipx install basedpyright)."},
		"typescript": {ID: "typescript", Commands: [][]string{{"typescript-language-server", "--stdio"}}, Hint: "Install: npm i -g typescript typescript-language-server"},
		"javascript": {ID: "javascript", Commands: [][]string{{"typescript-language-server", "--stdio"}}, Hint: "Install: npm i -g typescript typescript-language-server"},
	}
}
func needsDocument(action string) bool { return action != "workspace_symbols" }

func fileURI(path string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	return u.String()
}
func pathFromURI(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "file" {
		return "", errors.New("not file URI")
	}
	return filepath.FromSlash(u.Path), nil
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type rpcMessage struct {
	JSONRPC string  `json:"jsonrpc"`
	ID      any     `json:"id,omitempty"`
	Method  string  `json:"method,omitempty"`
	Params  any     `json:"params,omitempty"`
	Result  any     `json:"result,omitempty"`
	Error   *rpcErr `json:"error,omitempty"`
}
type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type lspProc struct {
	cmd       *exec.Cmd
	in        io.WriteCloser
	rd        *bufio.Reader
	next      int
	mu        sync.Mutex
	published map[string][]any
	stderr    bytes.Buffer
	docs      map[string]int
	done      chan struct{}
	waitMu    sync.Mutex
	waitErr   error
}

func startLSP(ctx context.Context, argv []string, cwd string) (*lspProc, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty LSP command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = managedproc.ManagedEnv(proc.SanitizedEnv(), "lsp", filepath.Base(argv[0]), os.Getpid())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	lp := &lspProc{cmd: cmd, in: in, rd: bufio.NewReader(out), published: map[string][]any{}, docs: map[string]int{}, done: make(chan struct{})}
	cmd.Stderr = &limitedBuffer{buf: &lp.stderr, max: 64 * 1024}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if err := managedproc.Register(cmd.Process.Pid, os.Getpid(), "lsp", filepath.Base(argv[0])); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return nil, fmt.Errorf("register LSP process: %w", err)
	}
	go func() {
		err := cmd.Wait()
		managedproc.Unregister(cmd.Process.Pid)
		lp.waitMu.Lock()
		lp.waitErr = err
		lp.waitMu.Unlock()
		close(lp.done)
	}()
	return lp, nil
}

func (p *lspProc) pid() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil || p.exited() {
		return 0
	}
	return p.cmd.Process.Pid
}
func (p *lspProc) close() {
	if p.cmd == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	if p.exited() {
		return
	}
	shutdownDone := make(chan struct{})
	go func() { _, _ = p.request("shutdown", nil); _ = p.notify("exit", nil); close(shutdownDone) }()
	select {
	case <-shutdownDone:
	case <-ctx.Done():
	}
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
	select {
	case <-p.done:
	case <-time.After(300 * time.Millisecond):
	}
}
func (p *lspProc) initializeContext(ctx context.Context, root string) error {
	caps := map[string]any{"textDocument": map[string]any{"definition": map[string]any{}, "references": map[string]any{}, "hover": map[string]any{}, "documentSymbol": map[string]any{}, "implementation": map[string]any{}, "rename": map[string]any{}, "diagnostic": map[string]any{}}, "workspace": map[string]any{"symbol": map[string]any{}}}
	_, err := p.requestContext(ctx, "initialize", map[string]any{"processId": os.Getpid(), "rootUri": fileURI(root), "capabilities": caps, "clientInfo": map[string]any{"name": "cos-lite", "version": "0.7.0"}})
	if err != nil {
		return err
	}
	return p.notify("initialized", map[string]any{})
}

func (p *lspProc) requestContext(ctx context.Context, method string, params any) (any, error) {
	type outcome struct {
		value any
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		v, err := p.request(method, params)
		done <- outcome{value: v, err: err}
	}()
	select {
	case out := <-done:
		return out.value, out.err
	case <-ctx.Done():
		if p.cmd != nil && p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil, ctx.Err()
	}
}

func (p *lspProc) syncDocument(uri, languageID, text string) error {
	version := p.docs[uri]
	if version == 0 {
		version = 1
		if err := p.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": languageID, "version": version, "text": text}}); err != nil {
			return err
		}
	} else {
		version++
		if err := p.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": version}, "contentChanges": []any{map[string]any{"text": text}}}); err != nil {
			return err
		}
	}
	p.docs[uri] = version
	return nil
}

func (p *lspProc) exited() bool {
	if p == nil || p.done == nil {
		return true
	}
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}
func (p *lspProc) request(method string, params any) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	id := p.next
	if err := p.write(rpcMessage{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	for {
		m, err := p.read()
		if err != nil {
			return nil, p.wrapErr(err)
		}
		if m.Method != "" {
			if m.ID != nil {
				if err := p.handleServerRequest(m); err != nil {
					return nil, err
				}
			} else {
				p.handleNotification(m)
			}
			continue
		}
		mid, ok := numberID(m.ID)
		if !ok || mid != id {
			continue
		}
		if m.Error != nil {
			return nil, fmt.Errorf("LSP %s failed (%d): %s", method, m.Error.Code, m.Error.Message)
		}
		return m.Result, nil
	}
}
func (p *lspProc) notify(method string, params any) error {
	return p.write(rpcMessage{JSONRPC: "2.0", Method: method, Params: params})
}
func (p *lspProc) write(m rpcMessage) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(p.in, "Content-Length: %d\r\n\r\n", len(b))
	if err != nil {
		return err
	}
	_, err = p.in.Write(b)
	return err
}
func (p *lspProc) read() (rpcMessage, error) {
	length := -1
	for {
		line, err := p.rd.ReadString('\n')
		if err != nil {
			return rpcMessage{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			n, e := strconv.Atoi(strings.TrimSpace(v))
			if e != nil {
				return rpcMessage{}, e
			}
			length = n
		}
	}
	if length < 0 || length > 16*1024*1024 {
		return rpcMessage{}, fmt.Errorf("invalid LSP Content-Length %d", length)
	}
	b := make([]byte, length)
	if _, err := io.ReadFull(p.rd, b); err != nil {
		return rpcMessage{}, err
	}
	var m rpcMessage
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return rpcMessage{}, err
	}
	return m, nil
}

func (p *lspProc) handleServerRequest(m rpcMessage) error {
	var result any
	switch m.Method {
	case "workspace/configuration":
		obj, _ := m.Params.(map[string]any)
		items, _ := obj["items"].([]any)
		vals := make([]any, len(items))
		result = vals
	case "workspace/workspaceFolders":
		result = []any{}
	case "client/registerCapability", "client/unregisterCapability", "window/workDoneProgress/create", "workspace/applyEdit":
		if m.Method == "workspace/applyEdit" {
			result = map[string]any{"applied": false, "failureReason": "cos-lite code_intel is read-only"}
		} else {
			result = nil
		}
	default:
		result = nil
	}
	return p.write(rpcMessage{JSONRPC: "2.0", ID: m.ID, Result: result})
}
func (p *lspProc) handleNotification(m rpcMessage) {
	if m.Method != "textDocument/publishDiagnostics" {
		return
	}
	obj, _ := m.Params.(map[string]any)
	uri, _ := obj["uri"].(string)
	arr, _ := obj["diagnostics"].([]any)
	if uri != "" {
		p.published[uri] = arr
	}
}
func (p *lspProc) diagnostics(uri string) []any { return append([]any(nil), p.published[uri]...) }
func (p *lspProc) wrapErr(err error) error {
	s := strings.TrimSpace(p.stderr.String())
	if s != "" {
		return fmt.Errorf("LSP transport: %w: %s", err, s)
	}
	return fmt.Errorf("LSP transport: %w", err)
}
func numberID(v any) (int, bool) {
	switch n := v.(type) {
	case json.Number:
		i, e := n.Int64()
		return int(i), e == nil
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

type limitedBuffer struct {
	buf *bytes.Buffer
	max int
}

func (w *limitedBuffer) Write(b []byte) (int, error) {
	n := len(b)
	if w.max > 0 {
		c := b
		if len(c) > w.max {
			c = c[:w.max]
		}
		_, _ = w.buf.Write(c)
		w.max -= len(c)
	}
	return n, nil
}

func AvailableServers() map[string]string {
	out := map[string]string{}
	for id, s := range specs() {
		for _, c := range s.Commands {
			if p, err := findExecutable(c[0]); err == nil {
				out[id] = p
				break
			}
		}
	}
	return out
}

func findExecutable(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	if filepath.IsAbs(name) {
		return "", exec.ErrNotFound
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", exec.ErrNotFound
	}
	for _, dir := range []string{
		filepath.Join(home, "go", "bin"),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".cargo", "bin"),
	} {
		candidate := filepath.Join(dir, name)
		st, statErr := os.Stat(candidate)
		if statErr == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}
