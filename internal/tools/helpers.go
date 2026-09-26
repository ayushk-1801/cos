package tools

import (
	"encoding/json"
	"fmt"
	"math"
)

func strArg(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func boolArg(m map[string]any, key string, def bool) bool {
	v, ok := m[key].(bool)
	if !ok {
		return def
	}
	return v
}

func intArg(m map[string]any, key string, def int) int {
	v, ok := m[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		if math.Trunc(n) == n {
			return int(n)
		}
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return def
}

func stringSliceArg(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok {
		return nil
	}
	if s, ok := v.(string); ok {
		return []string{s}
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func requireString(m map[string]any, key string) (string, error) {
	s := strArg(m, key)
	if s == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return s, nil
}
