package tools

import (
	"context"
	"fmt"
	"strings"

	planpkg "github.com/ayush/cos-lite/internal/plan"
)

type Planner struct{ Store *planpkg.Store }

func (p *Planner) Definition() Tool {
	return Tool{Definition: Definition{
		Name: "update_plan", Title: "Update task plan", Description: "Store and display a compact task plan for the current daemon. Status values are pending, in_progress, or completed.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"explanation": map[string]any{"type": "string"}, "plan": map[string]any{"type": "array", "minItems": 1, "maxItems": 50, "items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"step": map[string]any{"type": "string"}, "status": map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}}}, "required": []string{"step", "status"}}},
		}, "required": []string{"plan"}}, Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
	}, Handler: p.Handle}
}

func (p *Planner) Handle(_ context.Context, args map[string]any) (Result, error) {
	raw, ok := args["plan"].([]any)
	if !ok || len(raw) == 0 {
		return Error("plan must be a non-empty array"), nil
	}
	state := planpkg.State{Explanation: strArg(args, "explanation")}
	for i, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			return Error(fmt.Sprintf("plan[%d] must be an object", i)), nil
		}
		step := strArg(m, "step")
		status := strArg(m, "status")
		if step == "" {
			return Error(fmt.Sprintf("plan[%d].step is required", i)), nil
		}
		if status != "pending" && status != "in_progress" && status != "completed" {
			return Error(fmt.Sprintf("plan[%d].status is invalid", i)), nil
		}
		state.Plan = append(state.Plan, planpkg.Item{Step: step, Status: status})
	}
	state = p.Store.Update(state)
	var b strings.Builder
	if state.Explanation != "" {
		b.WriteString(state.Explanation)
		b.WriteString("\n\n")
	}
	for _, it := range state.Plan {
		mark := "○"
		if it.Status == "completed" {
			mark = "✓"
		} else if it.Status == "in_progress" {
			mark = "→"
		}
		fmt.Fprintf(&b, "%s %s\n", mark, it.Step)
	}
	return Result{Content: []Content{{"type": "text", "text": strings.TrimRight(b.String(), "\n")}}, Structured: state}, nil
}
