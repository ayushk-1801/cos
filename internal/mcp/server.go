package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

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
	Registry     *tools.Registry
	Name         string
	Version      string
	Instructions string
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
	return &Server{Registry: reg, Name: "cos-lite", Version: "0.4.3", Instructions: "Ubuntu-first local coding tools: read/search/edit files, apply exact patches, run and interact with terminal commands, maintain a task plan, and optionally control a dedicated Chromium instance through CDP. No Electron app or browser extension is required."}
}

func (s *Server) Handle(ctx context.Context, raw []byte, headerVersion string) ([]byte, bool) {
	var req request
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		return mustJSON(response{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "Parse error"}}), true
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32600, Message: "Invalid Request"}}), true
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
		result = map[string]any{"resultType": "complete", "supportedVersions": append([]string(nil), supportedVersions...), "capabilities": map[string]any{"tools": map[string]any{}}, "instructions": s.Instructions, "ttlMs": 300000, "cacheScope": "public", "_meta": serverMeta}
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = decodeParams(req.Params, &p)
		if p.ProtocolVersion == VersionCurrent || protocol == VersionCurrent {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "Method not found: initialize was removed in MCP 2026-07-28; use server/discover or send a stateless request"}}), true
		}
		v := chooseLegacyVersion(p.ProtocolVersion)
		result = map[string]any{"protocolVersion": v, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": s.Name, "version": s.Version}, "instructions": s.Instructions}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		defs := s.Registry.Definitions()
		if current {
			result = map[string]any{"resultType": "complete", "tools": defs, "ttlMs": 300000, "cacheScope": "private", "_meta": serverMeta}
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
		tr, err := s.Registry.Call(ctx, p.Name, p.Arguments)
		if err != nil {
			return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: err.Error()}}), true
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
	default:
		return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "Method not found"}}), true
	}
	return mustJSON(response{JSONRPC: "2.0", ID: req.ID, Result: result}), true
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
		resp, ok := s.Handle(r.Context(), body, version)
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
	}
	return http.StatusOK, nil
}

func hasRPCErrorCode(body []byte, code int) bool {
	var v struct {
		Error *rpcError `json:"error"`
	}
	return json.Unmarshal(body, &v) == nil && v.Error != nil && v.Error.Code == code
}
