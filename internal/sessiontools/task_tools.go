package sessiontools

const (
	TaskContext      = "wio_get_task_context"
	TaskMessage      = "wio_send_task_message"
	TaskReview       = "wio_request_task_review"
	TaskBlocker      = "wio_report_task_blocker"
	TaskPlanProposal = "wio_propose_task_plan"
)

func IsTaskTool(name string) bool {
	switch name {
	case TaskContext, TaskMessage, TaskReview, TaskBlocker, TaskPlanProposal:
		return true
	}
	return false
}

func taskDefinitions() []Tool {
	str := map[string]any{"type": "string", "minLength": 1}
	specs := []struct {
		name, description  string
		required, optional []string
	}{
		{TaskContext, "Read the authorized task plan, current step, team messages and original request status.", nil, []string{"request_id"}},
		{TaskMessage, "Send a bounded task message to another member of this plan.", []string{"request_id", "target_member_id", "reason"}, nil},
		{TaskReview, "Request a preauthorized downstream review of your development attempt or its selected artifact.", []string{"request_id", "target_step_id", "reason"}, []string{"artifact_id"}},
		{TaskBlocker, "Record a task blocker for the coordinator and the user.", []string{"request_id", "reason"}, nil},
		{TaskPlanProposal, "As coordinator, propose a revised explicit plan for user approval.", []string{"request_id", "reason", "plan"}, nil},
	}
	out := make([]Tool, 0, len(specs))
	for _, spec := range specs {
		props := map[string]any{}
		for _, field := range append(append([]string{}, spec.required...), spec.optional...) {
			props[field] = str
		}
		if spec.name == TaskPlanProposal {
			props["plan"] = map[string]any{"type": "object", "required": []string{"members", "steps"}, "additionalProperties": false,
				"properties": map[string]any{"members": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
					"steps": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}}}
		}
		required := spec.required
		if required == nil {
			required = []string{}
		}
		out = append(out, Tool{spec.name, spec.description, map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false},
			map[string]any{"readOnlyHint": spec.name == TaskContext, "destructiveHint": false, "openWorldHint": false}})
	}
	return out
}
