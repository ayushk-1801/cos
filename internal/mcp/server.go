package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/ayush/cos-lite/internal/clientctx"
	proc "github.com/ayush/cos-lite/internal/process"
	resourcepkg "github.com/ayush/cos-lite/internal/resources"
	taskpkg "github.com/ayush/cos-lite/internal/tasks"
	"github.com/ayush/cos-lite/internal/telemetry"
	"github.com/ayush/cos-lite/internal/tools"
)

const (
	VersionCurrent    = "2026-07-28"
	VersionLegacy     = "2025-11-25"
	VersionLegacy0618 = "2025-06-18"
	VersionLegacy0326 = "2025-03-26"
	VersionLegacy1105 = "2024-11-05"
)

var supportedVersions = []string{VersionCurrent, VersionLegacy, VersionLegacy0618, VersionLegacy0326, VersionLegacy1105}

type Server struct {
	mu                   sync.RWMutex
	Registry             *tools.Registry
	Name                 string
	Version              string
	Instructions         string
	InstructionsProvider func(context.Context) string
	Resources            *resourcepkg.Provider
	Tasks                *taskpkg.Manager
	Telemetry            *telemetry.Recorder
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func New(reg *tools.Registry) *Server {
	return &Server{Registry: reg, Name: "cos-lite", Version: "0.6.1", Instructions: "Ubuntu-first local coding tools: read/search/edit files, apply exact patches, run and interact with terminal commands, maintain a task plan, and optionally control a dedicated Chromium instance through CDP. No Electron app or browser extension is required."}
}

func (s *Server) Update(reg *tools.Registry, resources *resourcepkg.Provider, instructions func(context.Context) string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if reg != nil {
		s.Registry = reg
	}
	s.Resources = resources
	s.InstructionsProvider = instructions
}

func (s *Server) snapshot(ctx context.Context) (*tools.Registry, *resourcepkg.Provider, *taskpkg.Manager, *telemetry.Recorder, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	instructions := s.Instructions
	if s.InstructionsProvider != nil {
		instructions = s.InstructionsProvider(ctx)
	}
	return s.Registry, s.Resources, s.Tasks, s.Telemetry, instructions
}

func (s *Server) Handle(ctx context.Context, raw []byte, headerVersion string) (out []byte, respond bool) {
	var req request
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		return mustJSON(response{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "Parse error"}}), true
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32600, Message: "Invalid Request"}}), true
	}
	ctx = clientctx.With(ctx, clientctx.Merge(clientctx.From(ctx), clientctx.FromMeta(metaFromParams(req.Params))))
	reg, resourceProvider, taskManager, recorder, instructions := s.snapshot(ctx)
	var span telemetry.Span
	if recorder != nil {
		ctx, span = recorder.Begin(ctx)
		defer func() {
			status := "ok"
			if hasAnyRPCError(out) {
				status = "error"
			}
			recorder.EndWithPreview(ctx, span, req.Method, requestTarget(req.Method, req.Params), status, responseActivityPreview(req.Method, out))
		}()
	}
	if len(req.ID) == 0 { // notification
		return nil, false
	}
	protocol := headerVersion
	if protocol == "" {
		protocol = protocolFromParams(req.Params)
	}
	current := protocol == VersionCurrent || req.Method == "server/discover"
	serverMeta := map[string]any{"io.modelcontextprotocol/serverInfo": map[string]any{"name": s.Name, "version": s.Version}}

	var result any
	switch req.Method {
	case "server/discover":
		caps := map[string]any{"tools": map[string]any{}}
		if resourceProvider != nil {
			caps["resources"] = map[string]any{}
		}
		if taskManager != nil {
			caps["extensions"] = map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}}
		}
		result = map[string]any{"resultType": "complete", "supportedVersions": append([]string(nil), supportedVersions...), "capabilities": caps, "instructions": instructions, "ttlMs": 5000, "cacheScope": "private", "_meta": serverMeta}
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = decodeParams(req.Params, &p)
		if p.ProtocolVersion == VersionCurrent || protocol == VersionCurrent {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "Method not found: initialize was removed in MCP 2026-07-28; use server/discover or send a stateless request"}}), true
		}
		v := chooseLegacyVersion(p.ProtocolVersion)
		caps := map[string]any{"tools": map[string]any{}}
		if resourceProvider != nil {
			caps["resources"] = map[string]any{}
		}
		result = map[string]any{"protocolVersion": v, "capabilities": caps, "serverInfo": map[string]any{"name": s.Name, "version": s.Version}, "instructions": instructions}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		defs := reg.Definitions()
		if current {
			result = map[string]any{"resultType": "complete", "tools": defs, "ttlMs": 5000, "cacheScope": "private", "_meta": serverMeta}
		} else {
			result = map[string]any{"tools": defs}
		}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := decodeParams(req.Params, &p); err != nil {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "Invalid params", Data: err.Error()}}), true
		}
		if p.Name == "" {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "tool name is required"}}), true
		}
		if p.Arguments == nil {
			p.Arguments = map[string]any{}
		}
		tr, err := reg.Call(ctx, p.Name, p.Arguments)
		if err != nil {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: err.Error()}}), true
		}
		if !tr.IsError && resourceProvider != nil {
			appendApplicableInstructions(resourceProvider, p.Name, p.Arguments, &tr)
		}
		if current && p.Name == "exec_command" && taskManager != nil && clientctx.SupportsExtension(ctx, "io.modelcontextprotocol/tasks") && !boolFromMap(p.Arguments, "tty") {
			if pr, ok := tr.Structured.(proc.Result); ok && pr.Running && pr.SessionID != nil {
				task, err := taskManager.CreateProcess(*pr.SessionID)
				if err == nil {
					m := structToMap(task)
					m["resultType"] = "task"
					m["_meta"] = serverMeta
					result = m
					break
				}
			}
		}
		m := map[string]any{"content": tr.Content}
		if tr.Structured != nil {
			m["structuredContent"] = tr.Structured
		}
		if tr.IsError {
			m["isError"] = true
		}
		if current {
			m["resultType"] = "complete"
			m["_meta"] = serverMeta
		}
		result = m
	case "resources/list":
		if resourceProvider == nil {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "Method not found"}}), true
		}
		m := map[string]any{"resources": resourceProvider.List()}
		if current {
			m["resultType"] = "complete"
			m["ttlMs"] = int64(5000)
			m["cacheScope"] = "private"
			m["_meta"] = serverMeta
		}
		result = m
	case "resources/templates/list":
		if resourceProvider == nil {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "Method not found"}}), true
		}
		m := map[string]any{"resourceTemplates": resourceProvider.Templates()}
		if current {
			m["resultType"] = "complete"
			m["ttlMs"] = int64(5000)
			m["cacheScope"] = "private"
			m["_meta"] = serverMeta
		}
		result = m
	case "resources/read":
		if resourceProvider == nil {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "Method not found"}}), true
		}
		var p struct {
			URI string `json:"uri"`
		}
		if err := decodeParams(req.Params, &p); err != nil || strings.TrimSpace(p.URI) == "" {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "resource uri is required"}}), true
		}
		contents, ttl, scope, err := resourceProvider.Read(ctx, p.URI)
		if err != nil {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: err.Error()}}), true
		}
		m := map[string]any{"contents": contents}
		if current {
			m["resultType"] = "complete"
			m["ttlMs"] = ttl
			m["cacheScope"] = scope
			m["_meta"] = serverMeta
		}
		result = m
	case "tasks/get":
		if !current || taskManager == nil {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "Method not found"}}), true
		}
		if !clientctx.SupportsExtension(ctx, "io.modelcontextprotocol/tasks") {
			return missingTasksCapability(req.ID), true
		}
		var p struct {
			TaskID string `json:"taskId"`
		}
		if err := decodeParams(req.Params, &p); err != nil || p.TaskID == "" {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "taskId is required"}}), true
		}
		task, err := taskManager.Get(p.TaskID)
		if err != nil {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: err.Error()}}), true
		}
		m := structToMap(task)
		m["resultType"] = "complete"
		m["_meta"] = serverMeta
		result = m
	case "tasks/update":
		if !current || taskManager == nil {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "Method not found"}}), true
		}
		if !clientctx.SupportsExtension(ctx, "io.modelcontextprotocol/tasks") {
			return missingTasksCapability(req.ID), true
		}
		var p struct {
			TaskID         string         `json:"taskId"`
			InputResponses map[string]any `json:"inputResponses"`
		}
		if err := decodeParams(req.Params, &p); err != nil || p.TaskID == "" {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "taskId is required"}}), true
		}
		if err := taskManager.Update(p.TaskID, p.InputResponses); err != nil {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: err.Error()}}), true
		}
		result = map[string]any{"resultType": "complete", "_meta": serverMeta}
	case "tasks/cancel":
		if !current || taskManager == nil {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "Method not found"}}), true
		}
		if !clientctx.SupportsExtension(ctx, "io.modelcontextprotocol/tasks") {
			return missingTasksCapability(req.ID), true
		}
		var p struct {
			TaskID string `json:"taskId"`
		}
		if err := decodeParams(req.Params, &p); err != nil || p.TaskID == "" {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "taskId is required"}}), true
		}
		if err := taskManager.Cancel(p.TaskID); err != nil {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: err.Error()}}), true
		}
		result = map[string]any{"resultType": "complete", "_meta": serverMeta}
	default:
		return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "Method not found"}}), true
	}
	return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Result: result}), true
}

func metaFromParams(raw json.RawMessage) map[string]any {
	var p map[string]any
	if decodeParams(raw, &p) != nil {
		return nil
	}
	meta, _ := p["_meta"].(map[string]any)
	return meta
}

func requestTarget(method string, raw json.RawMessage) string {
	var p map[string]any
	_ = decodeParams(raw, &p)
	switch method {
	case "tools/call":
		v, _ := p["name"].(string)
		return v
	case "resources/read":
		v, _ := p["uri"].(string)
		return v
	case "tasks/get", "tasks/update", "tasks/cancel":
		// taskId is an opaque bearer capability. Never persist it in audit/trace
		// attributes; the method already says this is a task operation.
		return "task"
	default:
		return ""
	}
}

func structToMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func boolFromMap(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

func hasAnyRPCError(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var v struct {
		Error *rpcError `json:"error"`
	}
	return json.Unmarshal(body, &v) == nil && v.Error != nil
}

var (
	activityBearerRE = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+)[^\s]+`)
	activitySecretRE = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|token|secret|password)(\s*[:=]\s*)[^\s,;]+`)
	activitySKRE     = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}\b`)
	activityMCPRE    = regexp.MustCompile(`/mcp/[A-Za-z0-9_-]{16,}`)
	activityHandleRE = regexp.MustCompile(`\b(sess|plan|browser|task)_[0-9a-f]{32}\b`)
	activityANSIRE   = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
)

func responseActivityPreview(method string, body []byte) string {
	switch method {
	case "tools/call", "resources/read", "tasks/get":
	default:
		return ""
	}
	if len(body) == 0 {
		return ""
	}
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	if rpcErr, ok := envelope["error"].(map[string]any); ok {
		if message, _ := rpcErr["message"].(string); message != "" {
			return redactActivityPreview(message)
		}
	}
	result := envelope["result"]
	var textParts []string
	collectActivityText(result, &textParts, 0)
	if len(textParts) == 0 {
		return ""
	}
	preview := strings.TrimSpace(strings.Join(textParts, "\n"))
	if len([]rune(preview)) > 1200 {
		r := []rune(preview)
		preview = string(r[:1199]) + "…"
	}
	return redactActivityPreview(preview)
}

func collectActivityText(v any, out *[]string, depth int) {
	if depth > 6 || len(*out) >= 12 || v == nil {
		return
	}
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			collectActivityText(item, out, depth+1)
			if len(*out) >= 12 {
				return
			}
		}
	case map[string]any:
		if typ, _ := x["type"].(string); typ == "image" || typ == "audio" {
			return
		}
		if text, _ := x["text"].(string); strings.TrimSpace(text) != "" {
			*out = append(*out, text)
			return
		}
		if msg, _ := x["statusMessage"].(string); strings.TrimSpace(msg) != "" {
			*out = append(*out, msg)
		}
		for _, key := range []string{"content", "contents", "result"} {
			if child, ok := x[key]; ok {
				collectActivityText(child, out, depth+1)
			}
		}
	}
}

func redactActivityPreview(s string) string {
	s = activityANSIRE.ReplaceAllString(s, "")
	var clean strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' || r >= 32 {
			clean.WriteRune(r)
		}
	}
	s = clean.String()
	s = activityBearerRE.ReplaceAllString(s, `${1}********`)
	s = activitySecretRE.ReplaceAllString(s, `${1}${2}********`)
	s = activitySKRE.ReplaceAllString(s, "sk-********")
	s = activityMCPRE.ReplaceAllString(s, "/mcp/********")
	s = activityHandleRE.ReplaceAllString(s, `${1}_********`)
	return s
}

func appendApplicableInstructions(provider *resourcepkg.Provider, toolName string, args map[string]any, result *tools.Result) {
	if provider == nil || provider.Instructions == nil || provider.WS == nil || result == nil {
		return
	}
	paths := instructionPaths(toolName, args)
	if len(paths) == 0 {
		return
	}
	seen := map[string]bool{}
	var b strings.Builder
	const maxBytes = 64 * 1024
	for _, path := range paths {
		docs, err := provider.Instructions.ForPath(path)
		if err != nil {
			continue
		}
		for _, doc := range docs {
			if seen[doc.Path] || isConfiguredRootAgents(provider, doc.Path) {
				continue
			}
			seen[doc.Path] = true
			section := fmt.Sprintf("\n\n## %s\n%s", doc.Path, strings.TrimSpace(doc.Text))
			if b.Len()+len(section) > maxBytes {
				b.WriteString("\n\n[Additional AGENTS.md instructions truncated; use the project instructions Resource for the full hierarchy.]")
				break
			}
			b.WriteString(section)
		}
	}
	if b.Len() == 0 {
		return
	}
	text := "Applicable nested AGENTS.md instructions for this operation:" + b.String()
	result.Content = append(result.Content, tools.Content{"type": "text", "text": text})
}

func isConfiguredRootAgents(provider *resourcepkg.Provider, virtualPath string) bool {
	for _, root := range provider.WS.Roots {
		if virtualPath == "/"+root.Name+"/AGENTS.md" {
			return true
		}
	}
	return false
}

func instructionPaths(toolName string, args map[string]any) []string {
	add := func(out []string, value any) []string {
		s, ok := value.(string)
		if ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
		return out
	}
	var out []string
	switch toolName {
	case "read":
		out = add(out, args["path"])
		if raw, ok := args["paths"].([]any); ok {
			for _, v := range raw {
				out = add(out, v)
			}
		} else if raw, ok := args["paths"].([]string); ok {
			for _, v := range raw {
				out = add(out, v)
			}
		}
	case "view_image", "find", "code_intel":
		out = add(out, args["path"])
	case "exec_command":
		out = add(out, args["workdir"])
	case "apply_patch":
		patch, _ := args["patch"].(string)
		for _, line := range strings.Split(patch, "\n") {
			for _, prefix := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: ", "*** Move to: "} {
				if strings.HasPrefix(line, prefix) {
					out = append(out, strings.TrimSpace(strings.TrimPrefix(line, prefix)))
					break
				}
			}
		}
	}
	return out
}

func missingTasksCapability(id json.RawMessage) []byte {
	return mustJSON(response{JSONRPC: "2.0", ID: id, Error: &rpcError{
		Code: -32021, Message: "Missing required client capability",
		Data: map[string]any{"requiredCapabilities": map[string]any{"extensions": map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}}}},
	}})
}

func chooseLegacyVersion(requested string) string {
	switch requested {
	case VersionLegacy, VersionLegacy0618, VersionLegacy0326, VersionLegacy1105:
		return requested
	default:
		return VersionLegacy
	}
}

func supportedVersion(v string) bool {
	for _, supported := range supportedVersions {
		if v == supported {
			return true
		}
	}
	return false
}

func decodeParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	return dec.Decode(v)
}
func protocolFromParams(raw json.RawMessage) string {
	var p map[string]any
	if decodeParams(raw, &p) != nil {
		return ""
	}
	meta, _ := p["_meta"].(map[string]any)
	v, _ := meta["io.modelcontextprotocol/protocolVersion"].(string)
	return v
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 64*1024), 16*1024*1024)
	w := bufio.NewWriter(out)
	defer w.Flush()
	for scan.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := scan.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		resp, ok := s.Handle(ctx, line, "")
		if !ok {
			continue
		}
		if _, err := w.Write(resp); err != nil {
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			return err
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	return scan.Err()
}

func (s *Server) HTTPHandler(pathToken string) http.Handler {
	base := "/mcp"
	if pathToken != "" {
		base += "/" + pathToken
	}
	mux := http.NewServeMux()
	mux.HandleFunc(base, func(w http.ResponseWriter, r *http.Request) {
		if !validOrigin(r.Header.Get("Origin")) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write(mustJSON(response{JSONRPC: "2.0", Error: &rpcError{Code: -32000, Message: "forbidden Origin"}}))
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()
		body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024*1024+1))
		if err != nil {
			http.Error(w, "read request", 400)
			return
		}
		if len(body) > 16*1024*1024 {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		version := r.Header.Get("MCP-Protocol-Version")
		status, validation := validateModernHTTP(body, r)
		if validation != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(validation)
			return
		}
		requestCtx := r.Context()
		if tp := strings.TrimSpace(r.Header.Get("traceparent")); tp != "" {
			requestCtx = clientctx.With(requestCtx, clientctx.Info{
				Key: "anonymous", Name: "anonymous", TraceParent: tp,
				TraceState: r.Header.Get("tracestate"), Baggage: r.Header.Get("baggage"),
			})
		}
		resp, ok := s.Handle(requestCtx, body, version)
		if !ok {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if version == VersionCurrent && hasRPCErrorCode(resp, -32601) {
			status = http.StatusNotFound
		} else {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_, _ = w.Write(resp)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"name":%q,"version":%q}`, s.Name, s.Version)
	})
	return mux
}

func validOrigin(origin string) bool {
	if strings.TrimSpace(origin) == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func validateModernHTTP(body []byte, r *http.Request) (int, []byte) {
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		return http.StatusOK, nil // normal JSON-RPC parse handling owns this case
	}
	headerVersion := r.Header.Get("MCP-Protocol-Version")
	bodyVersion := protocolFromParams(req.Params)
	if headerVersion != "" && !supportedVersion(headerVersion) {
		return http.StatusBadRequest, mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32022, Message: "Unsupported protocol version", Data: map[string]any{"requested": headerVersion, "supported": append([]string(nil), supportedVersions...)}}})
	}
	modern := headerVersion == VersionCurrent || bodyVersion == VersionCurrent
	if !modern {
		return http.StatusOK, nil
	}
	id := req.ID
	mismatch := func(message string) (int, []byte) {
		return http.StatusBadRequest, mustJSON(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32020, Message: message}})
	}
	if headerVersion == "" {
		return mismatch("Header mismatch: MCP-Protocol-Version is required for 2026-07-28")
	}
	if headerVersion != VersionCurrent {
		return http.StatusBadRequest, mustJSON(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32022, Message: "Unsupported protocol version", Data: map[string]any{"requested": headerVersion, "supported": append([]string(nil), supportedVersions...)}}})
	}
	if bodyVersion != VersionCurrent {
		return mismatch("Header mismatch: MCP-Protocol-Version does not match request _meta protocol version")
	}
	if r.Header.Get("Mcp-Method") != req.Method {
		return mismatch("Header mismatch: Mcp-Method does not match JSON-RPC method")
	}
	if req.Method == "tools/call" {
		var p struct {
			Name string `json:"name"`
		}
		_ = decodeParams(req.Params, &p)
		if p.Name == "" || r.Header.Get("Mcp-Name") != p.Name {
			return mismatch("Header mismatch: Mcp-Name does not match tools/call name")
		}
	} else if req.Method == "resources/read" {
		var p struct {
			URI string `json:"uri"`
		}
		_ = decodeParams(req.Params, &p)
		if p.URI == "" || r.Header.Get("Mcp-Name") != p.URI {
			return mismatch("Header mismatch: Mcp-Name does not match resources/read uri")
		}
	} else if req.Method == "tasks/get" || req.Method == "tasks/update" || req.Method == "tasks/cancel" {
		var p struct {
			TaskID string `json:"taskId"`
		}
		_ = decodeParams(req.Params, &p)
		if p.TaskID == "" || r.Header.Get("Mcp-Name") != p.TaskID {
			return mismatch("Header mismatch: Mcp-Name does not match taskId")
		}
	}
	return http.StatusOK, nil
}

func hasRPCErrorCode(body []byte, code int) bool {
	var v struct {
		Error *rpcError `json:"error"`
	}
	return json.Unmarshal(body, &v) == nil && v.Error != nil && v.Error.Code == code
}
