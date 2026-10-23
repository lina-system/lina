// SPDX-License-Identifier: Apache-2.0
// Package doing reads a worker's existing title and plan without prompting it.
package doing

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/work/observation"
)

type Snapshot = observation.Snapshot

func Clean(s string) string { return observation.Clean(s) }

type Reader struct {
	title, step string
	busy        bool
	since       time.Time
}

func (r *Reader) Busy(busy bool, at time.Time) {
	if r.since.IsZero() || r.busy != busy {
		r.busy, r.since = busy, at
	}
}
func (r *Reader) Title(title string) { r.title = Clean(title) }
func (r *Reader) Snapshot() Snapshot {
	line := r.title
	if r.step != "" && !strings.EqualFold(line, r.step) {
		if line != "" {
			line += " · "
		}
		line += r.step
	}
	return Snapshot{Line: Clean(line), Busy: r.busy, Since: r.since}
}

type object map[string]json.RawMessage

func obj(raw json.RawMessage) object { var v object; _ = json.Unmarshal(raw, &v); return v }
func str(raw json.RawMessage) string { var s string; _ = json.Unmarshal(raw, &s); return s }
func items(raw json.RawMessage) []json.RawMessage {
	var v []json.RawMessage
	_ = json.Unmarshal(raw, &v)
	return v
}

func (r *Reader) plan(raw json.RawMessage) {
	if len(raw) == 0 || raw[0] != '[' {
		return
	}
	var list []object
	if json.Unmarshal(raw, &list) != nil {
		return
	}
	r.step = ""
	for _, v := range list {
		switch str(v["status"]) {
		case "in_progress", "in-progress", "inProgress", "active":
		default:
			continue
		}
		for _, key := range []string{"activeForm", "content", "step", "description", "subject", "title"} {
			if s := str(v[key]); s != "" {
				r.step = Clean(s)
				return
			}
		}
	}
}
func (r *Reader) tool(v object) {
	name := str(v["name"])
	if name == "" {
		name = str(v["toolName"])
	}
	if name == "" {
		name = str(v["tool"])
	}
	switch name {
	case "TodoWrite", "update_plan", "write_todos", "todo_write", "todowrite":
	default:
		return
	}
	raw := v["input"]
	if len(raw) == 0 {
		raw = v["args"]
	}
	if len(raw) == 0 {
		raw = v["arguments"]
	}
	if len(raw) > 0 && raw[0] == '"' {
		raw = []byte(str(raw))
	}
	if len(raw) == 0 {
		raw = obj(v["state"])["input"]
	}
	args := obj(raw)
	if name == "update_plan" {
		r.plan(args["plan"])
	} else {
		r.plan(args["todos"])
	}
}

// Record accepts only known metadata and tool envelopes. It never recursively
// interprets arbitrary assistant prose, tool output or quoted JSON as a plan.
func (r *Reader) Record(kind string, raw []byte, at time.Time) {
	if len(raw) > 2<<20 {
		return
	}
	v := obj(raw)
	if v == nil {
		return
	}
	switch kind {
	case "acp":
		if str(v["method"]) == "session/update" {
			v = obj(obj(v["params"])["update"])
		}
		switch str(v["sessionUpdate"]) {
		case "session_info_update":
			if title, ok := v["title"]; ok {
				r.Title(str(title))
			}
		case "plan":
			r.plan(v["entries"])
		}
	case "codex":
		if title, ok := v["thread_name"]; ok {
			r.Title(str(title))
		}
		switch str(v["method"]) {
		case "thread/name/updated":
			r.Title(str(obj(v["params"])["threadName"]))
		case "turn/plan/updated":
			r.plan(obj(v["params"])["plan"])
		}
		if str(v["type"]) == "response_item" {
			r.tool(obj(v["payload"]))
		}
		if str(v["type"]) == "event_msg" {
			switch str(obj(v["payload"])["type"]) {
			case "task_started":
				r.Busy(true, at)
			case "task_complete", "turn_aborted":
				r.Busy(false, at)
			}
		}
	case "claude":
		if str(v["type"]) == "ai-title" {
			r.Title(str(v["aiTitle"]))
		}
		if str(v["type"]) == "assistant" {
			for _, block := range items(obj(v["message"])["content"]) {
				b := obj(block)
				if str(b["type"]) == "tool_use" {
					r.tool(b)
				}
			}
		}
	case "gemini":
		if title, ok := v["title"]; ok {
			r.Title(str(title))
		}
		for _, message := range items(v["messages"]) {
			for _, call := range items(obj(message)["toolCalls"]) {
				r.tool(obj(call))
			}
		}
	case "cursor":
		if title, ok := v["title"]; ok {
			r.Title(str(title))
		}
		for _, block := range items(v["content"]) {
			r.tool(obj(block))
		}
		for _, call := range items(v["tool_calls"]) {
			r.tool(obj(obj(call)["function"]))
		}
	case "opencode":
		p := obj(v["properties"])
		switch str(v["type"]) {
		case "session.updated":
			r.Title(str(obj(p["info"])["title"]))
		case "todo.updated":
			r.plan(p["todos"])
		case "message.part.updated":
			r.tool(obj(p["part"]))
		}
	}
}
