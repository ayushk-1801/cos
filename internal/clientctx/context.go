package clientctx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

type contextKey struct{}

type Info struct {
	Key           string
	Name          string
	Version       string
	SessionScoped bool
	TraceParent   string
	TraceState    string
	Baggage       string
	Capabilities  map[string]any
}

func Anonymous() Info {
	return Info{Key: "anonymous", Name: "anonymous", Capabilities: map[string]any{}}
}

func With(ctx context.Context, info Info) context.Context {
	if strings.TrimSpace(info.Key) == "" {
		info.Key = deriveKey(info.Name, info.Version, nil)
	}
	if info.Capabilities == nil {
		info.Capabilities = map[string]any{}
	}
	return context.WithValue(ctx, contextKey{}, info)
}

func From(ctx context.Context) Info {
	if ctx == nil {
		return Anonymous()
	}
	if v, ok := ctx.Value(contextKey{}).(Info); ok {
		if v.Key == "" {
			v.Key = "anonymous"
		}
		return v
	}
	return Anonymous()
}

func Key(ctx context.Context) string { return From(ctx).Key }

// OwnerKey returns a stable per-conversation namespace when the connector
// supplies one. It is for telemetry/UI grouping only; capability handles are
// the authorization boundary for transient state.
func OwnerKey(ctx context.Context) string {
	info := From(ctx)
	if !info.SessionScoped {
		return ""
	}
	return info.Key
}

func SupportsExtension(ctx context.Context, id string) bool {
	info := From(ctx)
	ext, _ := info.Capabilities["extensions"].(map[string]any)
	if ext == nil {
		return false
	}
	_, ok := ext[id]
	return ok
}

func FromMeta(meta map[string]any) Info {
	if meta == nil {
		return Anonymous()
	}
	info := Anonymous()
	if raw, ok := meta["io.modelcontextprotocol/clientInfo"].(map[string]any); ok {
		info.Name, _ = raw["name"].(string)
		info.Version, _ = raw["version"].(string)
		if strings.TrimSpace(info.Name) == "" {
			info.Name = "anonymous"
		}
		info.Key = deriveKey(info.Name, info.Version, raw)
	}
	// ChatGPT includes an anonymized per-conversation identifier on connector
	// requests. Prefer it for transient-state isolation so multiple chats using
	// the same connector/clientInfo do not share terminal, plan, browser or Task
	// state. Hash it before use so the raw conversation identifier is never
	// persisted in audit/log state.
	if session, ok := meta["openai/session"].(string); ok && strings.TrimSpace(session) != "" {
		info.Key = deriveSessionKey(info.Name, session)
		info.SessionScoped = true
	}
	if caps, ok := meta["io.modelcontextprotocol/clientCapabilities"].(map[string]any); ok {
		info.Capabilities = caps
	}
	info.TraceParent, _ = meta["traceparent"].(string)
	info.TraceState, _ = meta["tracestate"].(string)
	info.Baggage, _ = meta["baggage"].(string)
	return info
}

func Merge(base, override Info) Info {
	out := override
	if !override.SessionScoped && base.SessionScoped {
		out.Key = base.Key
		out.SessionScoped = true
	}
	if out.Key == "" || out.Key == "anonymous" {
		out.Key = base.Key
	}
	if out.Name == "" || out.Name == "anonymous" {
		out.Name = base.Name
	}
	if out.Version == "" {
		out.Version = base.Version
	}
	if out.TraceParent == "" {
		out.TraceParent = base.TraceParent
	}
	if out.TraceState == "" {
		out.TraceState = base.TraceState
	}
	if out.Baggage == "" {
		out.Baggage = base.Baggage
	}
	if out.Capabilities == nil || len(out.Capabilities) == 0 {
		out.Capabilities = base.Capabilities
	}
	if out.Capabilities == nil {
		out.Capabilities = map[string]any{}
	}
	return out
}

func deriveKey(name, version string, raw map[string]any) string {
	if strings.TrimSpace(name) == "" {
		name = "anonymous"
	}
	var material []byte
	if raw != nil {
		material, _ = json.Marshal(raw)
	}
	if len(material) == 0 {
		material = []byte(name + "\x00" + version)
	}
	sum := sha256.Sum256(material)
	return sanitize(name) + "-" + hex.EncodeToString(sum[:6])
}

func deriveSessionKey(name, session string) string {
	if strings.TrimSpace(name) == "" {
		name = "chatgpt"
	}
	sum := sha256.Sum256([]byte("openai/session\x00" + strings.TrimSpace(session)))
	return sanitize(name) + "-session-" + hex.EncodeToString(sum[:8])
}

func sanitize(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "anonymous"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" {
		return "client"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}
