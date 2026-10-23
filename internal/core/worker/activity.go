// SPDX-License-Identifier: Apache-2.0
package worker

// Activity is a safe display class from observed events, never worker text,
// command arguments, reasoning or a claim that the work is complete.
func Activity(event Event) string {
	if event.Kind == ToolCallEvent {
		return "tool"
	}
	if event.Progress == nil {
		return ""
	}
	switch event.Progress.Phase {
	case "started", "item_completed":
		return "working"
	case "delta", "message":
		return "responding"
	case "command":
		return "executing"
	case "file_change":
		return "editing"
	case "tool":
		return "tool"
	}
	return ""
}
