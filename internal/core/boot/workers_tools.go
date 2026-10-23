// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	wire "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

func workerWorkTools() []worker.Tool { return worker.WorkToolDeclarations() }

func decodeWorkerArgs(raw json.RawMessage, out any) error {
	if len(raw) > 1<<20 {
		return errors.New("worker arguments too large")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing worker arguments")
	}
	return nil
}
func framedToolReply(sender, kind, task, id string, body any, success bool) json.RawMessage {
	text, _ := json.Marshal(body)
	raw, _ := json.Marshal(wire.DynamicToolCallResponse{Success: success, ContentItems: []wire.V2DynamicToolCallOutputContentItem{{InputText: &wire.V2DynamicToolCallOutputContentItemInputText{Text: worker.FrameMessage(sender, kind, task, id, string(text))}}}})
	return raw
}
func (e *workerSession) workTool(event worker.Event) {
	call := event.ToolCall
	if call.Namespace != "lina" {
		e.replyTool(event, nil, errors.New("unknown work tool namespace"))
		return
	}
	if call.Name == "ask" {
		var args struct {
			Question string `json:"question"`
		}
		if err := decodeWorkerArgs(call.Arguments, &args); err != nil || strings.TrimSpace(args.Question) == "" {
			e.replyTool(event, nil, errors.New("invalid worker question"))
			return
		}
		e.askWorker(event, args.Question)
		return
	}
	body, err := e.executeWorkTool(e.origin.ctx, event)
	e.replyTool(event, body, err)
}
func (e *workerSession) replyTool(event worker.Event, body any, err error) {
	success := err == nil
	if err != nil {
		body = map[string]string{"error": err.Error()}
	}
	ctx, cancel := context.WithTimeout(e.origin.ctx, 10*time.Second)
	defer cancel()
	if err := e.adapter.Answer(ctx, event.Thread, worker.Answer{RequestID: event.ToolCall.ID, Result: framedToolReply("core", "tool", event.Thread.TaskID, e.requestKey(event.ToolCall.ID)+":reply", body, success)}); err != nil {
		e.bridgeFailure(err)
	}
}
func (e *workerSession) executeWorkTool(ctx context.Context, event worker.Event) (any, error) {
	call := event.ToolCall
	// Identity comes exclusively from the bound adapter event, never arguments.
	if event.Thread.ID != e.thread.ID || event.Thread.TaskID != e.thread.TaskID || event.Thread.AttemptID != e.thread.AttemptID || event.Thread.ProcessGeneration != e.thread.ProcessGeneration || event.Thread.BindingEpoch != e.thread.BindingEpoch {
		return nil, errors.New("foreign work tool caller")
	}
	switch call.Name {
	case "read_projection":
		if err := decodeWorkerArgs(call.Arguments, &struct{}{}); err != nil {
			return nil, err
		}
		return map[string]any{"assignment_id": e.thread.TaskID, "execution_generation": max(1, e.executionGeneration), "attempt_id": e.thread.AttemptID, "thread_id": event.Thread.ID, "turn_id": event.Thread.TurnID, "task_packet": e.packet, "workdir": e.workdir, "node_id": e.origin.main, "revision": fmt.Sprintf("%x", sha256.Sum256([]byte(e.packet))), "materials": []string{}, "memory": []string{}}, nil
	case "coordinate":
		var args struct {
			Request string   `json:"request"`
			Files   []string `json:"files,omitempty"`
		}
		if err := decodeWorkerArgs(call.Arguments, &args); err != nil {
			return nil, err
		}
		if strings.TrimSpace(args.Request) == "" || len(args.Request) > 64<<10 {
			return nil, errors.New("coordination request must contain 1 to 65536 bytes")
		}
		files, err := worker.ReadMessageFiles(e.workdir, args.Files)
		if err != nil {
			return nil, err
		}
		id := e.requestKey(call.ID)
		message, _ := json.Marshal(map[string]any{"request": args.Request, "files": files})
		if err := worker.AdmitMessage(ctx, e.origin.db, e.thread.TaskID, id, string(message)); err != nil {
			return nil, err
		}
		// Keep Core-named copies outside the assignment worktree.
		var copies []string
		if len(files) > 0 {
			dir := filepath.Join(e.origin.root, "work", "messages", fmt.Sprintf("%x", sha256.Sum256([]byte(e.thread.TaskID+"\x00"+id))))
			if err := os.MkdirAll(dir, 0700); err != nil {
				return nil, err
			}
			for i, f := range files {
				path := filepath.Join(dir, fmt.Sprintf("%02d.txt", i))
				if err := os.WriteFile(path, []byte(f.Text), 0600); err != nil {
					return nil, err
				}
				copies = append(copies, path)
			}
		}
		if err := e.origin.workerNotice(ctx, e.input, id, worker.FrameMessage(e.thread.ID, "tool", e.thread.TaskID, id, string(message))); err != nil {
			return nil, err
		}
		return map[string]any{"accepted": true, "event_id": id, "files": copies}, nil
	case "submit_receipt":
		var args struct {
			Receipt work.Receipt `json:"receipt"`
		}
		if err := decodeWorkerArgs(call.Arguments, &args); err != nil {
			return nil, err
		}
		card := work.Card{Task: work.Task{ID: e.thread.TaskID, ArtifactRoots: []string{e.workdir}}, Run: &work.Run{ID: e.thread.AttemptID, TaskID: e.thread.TaskID, Generation: max(1, e.executionGeneration), ThreadID: event.Thread.ID, TurnID: event.Thread.TurnID, NodeID: e.origin.main}}
		if err := work.ValidateWorkerReceipt(card, args.Receipt); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(args.Receipt)
		if err != nil {
			return nil, err
		}
		id := e.requestKey("receipt:" + args.Receipt.EventID)
		if err := e.origin.workerNotice(ctx, e.input, id, "Worker completion claim (unverified): "+string(raw)); err != nil {
			return nil, err
		}
		return map[string]any{"accepted": true, "verified": false, "event_id": id}, nil
	default:
		return nil, errors.New("unknown LINA work tool")
	}
}
