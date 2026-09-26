package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ayush/cos-lite/internal/tools"
)

type Manager struct {
	clients []*Client
}

func LoadConfig(path string) ([]Config, error) {
	if path == "" || path == "none" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg []Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse plugin config %s: %w", path, err)
	}
	return cfg, nil
}

func (m *Manager) Register(ctx context.Context, reg *tools.Registry, cfgs []Config) error {
	for _, cfg := range cfgs {
		c, err := Start(ctx, cfg)
		if err != nil {
			return fmt.Errorf("start plugin %s: %w", cfg.Name, err)
		}
		defs, err := c.Discover(ctx)
		if err != nil {
			c.Close()
			return fmt.Errorf("discover plugin %s: %w", cfg.Name, err)
		}
		m.clients = append(m.clients, c)

		for _, raw := range defs {
			orig, _ := raw["name"].(string)
			if orig == "" {
				continue
			}
			pub := c.Prefix() + orig
			desc, _ := raw["description"].(string)
			if desc == "" {
				desc = "Tool from plugin " + cfg.Name
			}
			schema, _ := raw["inputSchema"].(map[string]any)
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}

			client := c
			original := orig
			t := tools.Tool{
				Definition: tools.Definition{
					Name:        pub,
					Title:       fmt.Sprintf("%s: %s", cfg.Name, orig),
					Description: desc,
					InputSchema: schema,
					Annotations: map[string]any{"openWorldHint": true},
				},
				Handler: func(ctx context.Context, args map[string]any) (tools.Result, error) {
					res, err := client.Call(ctx, original, args)
					if err != nil {
						return tools.Error(err.Error()), nil
					}
					content := []tools.Content{}
					if arr, ok := res["content"].([]any); ok {
						for _, v := range arr {
							if cm, ok := v.(map[string]any); ok {
								content = append(content, tools.Content(cm))
							}
						}
					}
					if len(content) == 0 {
						b, _ := json.MarshalIndent(res, "", "  ")
						content = []tools.Content{{"type": "text", "text": string(b)}}
					}
					structured := res["structuredContent"]
					isErr, _ := res["isError"].(bool)
					return tools.Result{Content: content, Structured: structured, IsError: isErr}, nil
				},
			}
			if err := reg.Register(t); err != nil {
				return fmt.Errorf("plugin %s tool %s: %w", cfg.Name, orig, err)
			}
		}
	}
	return nil
}

func (m *Manager) Close() {
	for _, c := range m.clients {
		c.Close()
	}
}

func DefaultConfigPath() string {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return strings.TrimRight(cfg, "/") + "/cos-lite/plugins.json"
}
