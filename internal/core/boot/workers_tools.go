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
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	wire "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

func workerWorkTools() []worker.Tool {
	schema := func(properties, required string) json.RawMessage {
		return json.RawMessage(`{"type":"object","properties":{` + properties + `},"required":[` + required + `],"additionalProperties":false}`)
	}
	return []worker.Tool{
		{Namespace: "lina", Name: "read_projection", Description: "Read this assignment's task packet and revision. No other conversation, material or memory is authorized by this session.", InputSchema: schema("", "")},
		{Namespace: "lina", Name: "ask", Description: "Ask the originating person a question and wait for their answer.", InputSchema: schema(`"question":{"type":"string"}`, `"question"`)},
		{Namespace: "lina", Name: "coordinate", Description: "Send a coordination request to LINA Core in the originating conversation. This acknowledges receipt, not completion of the requested action.", InputSchema: schema(`"request":{"type":"string"}`, `"request"`)},
		{Namespace: "lina", Name: "submit_receipt", Description: "Submit a version 1 completion claim using the existing work receipt shape. Read the projection for assignment identity. The receipt is retained as worker evidence; it cannot verify or deliver a task.", InputSchema: schema(`"receipt":`+workerReceiptSchema, `"receipt"`)},
	}
}
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
func toolReply(body any, success bool) json.RawMessage {
	text, _ := json.Marshal(body)
	raw, _ := json.Marshal(wire.DynamicToolCallResponse{Success: success, ContentItems: []wire.V2DynamicToolCallOutputContentItem{{InputText: &wire.V2DynamicToolCallOutputContentItemInputText{Text: string(text)}}}})
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
	if err := e.adapter.Answer(ctx, event.Thread, worker.Answer{RequestID: event.ToolCall.ID, Result: toolReply(body, success)}); err != nil {
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
		return map[string]any{"assignment_id": e.thread.TaskID, "execution_generation": 1, "attempt_id": e.thread.AttemptID, "thread_id": event.Thread.ID, "turn_id": event.Thread.TurnID, "task_packet": e.packet, "workdir": e.workdir, "node_id": e.origin.main, "revision": fmt.Sprintf("%x", sha256.Sum256([]byte(e.packet))), "materials": []string{}, "memory": []string{}}, nil
	case "coordinate":
		var args struct {
			Request string `json:"request"`
		}
		if err := decodeWorkerArgs(call.Arguments, &args); err != nil {
			return nil, err
		}
		if strings.TrimSpace(args.Request) == "" {
			return nil, errors.New("empty coordination request")
		}
		id := e.requestKey(call.ID)
		if err := e.origin.workerNotice(ctx, e.input, id, "Codex coordination request: "+args.Request); err != nil {
			return nil, err
		}
		return map[string]any{"accepted": true, "event_id": id}, nil
	case "submit_receipt":
		var args struct {
			Receipt work.Receipt `json:"receipt"`
		}
		if err := decodeWorkerArgs(call.Arguments, &args); err != nil {
			return nil, err
		}
		card := work.Card{Task: work.Task{ID: e.thread.TaskID, ArtifactRoots: []string{e.workdir}}, Run: &work.Run{ID: e.thread.AttemptID, TaskID: e.thread.TaskID, Generation: 1, ThreadID: event.Thread.ID, TurnID: event.Thread.TurnID, NodeID: e.origin.main}}
		if err := work.ValidateWorkerReceipt(card, args.Receipt); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(args.Receipt)
		if err != nil {
			return nil, err
		}
		id := e.requestKey("receipt:" + args.Receipt.EventID)
		if err := e.origin.workerNotice(ctx, e.input, id, "Codex completion claim (unverified): "+string(raw)); err != nil {
			return nil, err
		}
		return map[string]any{"accepted": true, "verified": false, "event_id": id}, nil
	default:
		return nil, errors.New("unknown LINA work tool")
	}
}

// The worker gets the existing ledger receipt fields rather than an opaque
// object it would have to guess. Core still validates every field on ingress.
const workerReceiptSchema = `{
 "type":"object","additionalProperties":false,
 "required":["schema_version","eventId","relationshipId","executionGeneration","attempt","revisionHash","outcome","producer","turnRef","emittedAt","manifest","provenance"],
 "properties":{
  "schema_version":{"type":"integer","enum":[1]},
  "eventId":{"type":"string","description":"First 32 hex characters of SHA256 of relationshipId|executionGeneration|revisionHash|outcome for ready_for_review; otherwise relationshipId|executionGeneration|outcome|turnRef.turnId|attempt."},
  "relationshipId":{"type":"string","description":"assignment_id from read_projection"},
  "executionGeneration":{"type":"integer","enum":[1]},
  "attempt":{"type":"integer","minimum":1},
  "revisionHash":{"type":"string","description":"SHA256 of sorted path:sha256 manifest entries joined with newline; 64 zeros when empty."},
  "outcome":{"type":"string","enum":["ready_for_review","failed","interrupted","blocked_needs_input"]},
  "producer":{"type":"string","enum":["child"]},
  "turnRef":{"type":"object","additionalProperties":false,"required":["threadId","turnId","turnStatus"],"properties":{"threadId":{"type":"string"},"turnId":{"type":"string"},"turnStatus":{"type":"string","enum":["completed","failed","interrupted"]}}},
  "emittedAt":{"type":"string","description":"RFC3339 timestamp with explicit zone"},
  "manifest":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["path","sha256"],"properties":{"path":{"type":"string"},"sha256":{"type":"string"},"bytes":{"type":"integer","minimum":0}}}},
  "provenance":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["artifact_id","task_id","node_id","path","sha256","observed_at"],"properties":{"artifact_id":{"type":"string"},"task_id":{"type":"string"},"node_id":{"type":"string"},"path":{"type":"string"},"sha256":{"type":"string"},"observed_at":{"type":"string"}}}}
 }
}`
