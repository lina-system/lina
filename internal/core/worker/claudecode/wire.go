// SPDX-License-Identifier: Apache-2.0
package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"time"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/doing"
	"github.com/thisisjun786/lina/kit/provider"
)

type PermissionDenial struct {
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
}

// Result retains the final worker report, not a delivery or acceptance verdict.
// Tool inputs are deliberately excluded from denial evidence.
type Result struct {
	Subtype           string             `json:"subtype"`
	SessionID         string             `json:"session_id"`
	IsError           *bool              `json:"is_error"`
	Result            string             `json:"result,omitempty"`
	Errors            []string           `json:"errors,omitempty"`
	PermissionDenials []PermissionDenial `json:"permission_denials,omitempty"`
	Usage             *Usage             `json:"usage,omitempty"`
}
type Usage struct {
	Input  *int64 `json:"input_tokens"`
	Output *int64 `json:"output_tokens"`
	Cached *int64 `json:"cache_read_input_tokens"`
}
type frame struct {
	Result
	Type    string          `json:"type"`
	CWD     string          `json:"cwd"`
	Model   string          `json:"model"`
	UUID    string          `json:"uuid"`
	Message json.RawMessage `json:"message"`
}
type assistantMessage struct {
	ID      string `json:"id"`
	Content []struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Text string `json:"text"`
		Name string `json:"name"`
	} `json:"content"`
}

func (a *Adapter) read(ctx context.Context, r io.Reader, ready chan<- error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	initialized, terminal := false, false
	code := "missing_result"
	defer func() {
		if !initialized {
			ready <- errors.New("Claude Code did not initialize")
			a.cancel()
			return
		}
		if !terminal {
			a.emit(ctx, worker.Event{Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.ConnectionFailure, Code: code}})
			a.cancel()
		}
	}()
	for scanner.Scan() {
		var f frame
		if json.Unmarshal(scanner.Bytes(), &f) != nil || f.Type == "" {
			code = "invalid_message"
			return
		}
		if initialized && f.SessionID != "" && f.SessionID != a.thread.ID {
			code = "session_mismatch"
			return
		}
		if initialized {
			a.mu.Lock()
			a.doing.Record("claude", scanner.Bytes(), time.Now())
			a.mu.Unlock()
		}
		switch f.Type {
		case "system":
			// Hooks and status messages may precede initialization. They cannot
			// authorize a thread or complete a turn.
			if f.Subtype != "init" {
				a.mu.Lock()
				a.effectSeen = true
				a.mu.Unlock()
				continue
			}
			if initialized || f.SessionID == "" || filepath.Clean(f.CWD) != filepath.Clean(a.spec.Workdir) || a.spec.Model != "" && f.Model != string(a.spec.Model) {
				code = "invalid_init"
				return
			}
			a.mu.Lock()
			a.thread = worker.Thread{ID: f.SessionID, TurnID: a.spec.AttemptID, TaskID: a.spec.TaskID, AttemptID: a.spec.AttemptID, ProcessGeneration: 1, BindingEpoch: a.cfg.Epoch}

			a.files = doing.ClaudeFiles{Root: doing.ClaudeRoot(a.cfg.Environment), Session: f.SessionID}
			a.doing.Busy(true, time.Now())
			a.mu.Unlock()
			initialized = true
			ready <- nil
		case "assistant":
			var message assistantMessage
			if !initialized || len(f.Message) == 0 || json.Unmarshal(f.Message, &message) != nil {
				code = "invalid_message"
				return
			}
			for _, block := range message.Content {
				text, id := block.Text, message.ID
				if block.Type == "tool_use" {
					a.mu.Lock()
					a.effectSeen = true
					a.mu.Unlock()
					text, id = block.Name, block.ID
				}
				if block.Type != "text" && block.Type != "tool_use" {
					continue
				}
				if id == "" {
					id = f.UUID
				}
				if id == "" {
					code = "invalid_message"
					return
				}
				if !a.emit(ctx, worker.Event{Kind: worker.ProgressEvent, Progress: &worker.Progress{ItemID: id, Phase: "completed", Text: text}}) {
					return
				}
			}
		case "result":
			if !initialized || f.SessionID != a.thread.ID || f.Subtype == "" || f.IsError == nil {
				code = "invalid_result"
				return
			}
			result := f.Result
			finish := worker.Finish{Status: worker.Done}
			if len(result.PermissionDenials) > 0 {
				finish = worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure, Code: "permission_denials"}
			} else if *result.IsError || result.Subtype != "success" || len(result.Errors) > 0 {
				finish = worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure, Code: "result_error"}
			}
			if result.Usage != nil {
				usage := provider.Usage{Input: result.Usage.Input, Output: result.Usage.Output, Cached: result.Usage.Cached}
				if !usage.Valid() {
					code = "invalid_usage"
					return
				}
				if !a.emit(ctx, worker.Event{Kind: worker.UsageEvent, Usage: &usage}) {
					return
				}
			}
			a.mu.Lock()
			a.result, a.state = result, worker.Idle
			a.finish = &finish
			a.mu.Unlock()
			terminal = true
			a.emit(ctx, worker.Event{Kind: worker.FinishEvent, Finish: &finish})
			// Completion reaches Core before exit, but the owner retains the
			// process lease until Close has reaped all descendants.
			_, _ = io.Copy(io.Discard, r)
			return
		case "ai-title":
			if initialized && !a.emit(ctx, worker.Event{Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: "doing"}}) {
				return
			}
		case "user":
			// Tool results are evidence of a prior call, even if its start was absent.
			a.mu.Lock()
			a.effectSeen = true
			a.mu.Unlock()
		default:
			a.mu.Lock()
			a.effectSeen = true
			a.mu.Unlock()
			// New informational events can coexist with the final result.
			// They neither finish a turn nor grant an unsupported capability.
		}
	}
}
