// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/result"
)

type mockBuildInput struct {
	ResultID     string   `json:"result_id"`
	Revision     int      `json:"revision"`
	Target       string   `json:"target"`
	Repositories []string `json:"repositories"`
}

func decodeMockBuild(call provider.ToolCall) (mockBuildInput, error) {
	var p mockBuildInput
	if call.Name != "build_mock" || call.ID == "" || call.Custom || len(call.Arguments) > 16<<10 {
		return p, result.ErrMalformed
	}
	if err := result.Decode([]byte(call.Arguments), &p); err != nil {
		return p, err
	}
	if p.ResultID == "" || p.Revision < 1 || (p.Target != "screen" && p.Target != "mini_app") || len(p.Repositories) == 0 || len(p.Repositories) > 16 {
		return p, result.ErrMalformed
	}
	return p, nil
}
func (r *conversationRuntime) buildMockTool(turnID string) loop.Tool {
	return loop.Tool{Validate: func(call provider.ToolCall) (string, error) {
		p, err := decodeMockBuild(call)
		return "mock-build:" + p.ResultID, err
	}, Execute: func(ctx context.Context, call provider.ToolCall) loop.Result {
		p, err := decodeMockBuild(call)
		if err != nil {
			return loop.Result{ID: call.ID, Code: loop.Invalid}
		}
		key := turnID + ":mock-build:" + call.ID
		answer, err := r.mockOperation(ctx, clientsrv.Session{PersonID: r.person}, protocol.Envelope{MessageID: call.ID, IdempotencyKey: &key, Payload: protocol.MockRequest{Action: "build", ResultID: p.ResultID, Revision: json.Number(strconv.Itoa(p.Revision)), ConversationID: r.conversation, Target: &p.Target, Repositories: &p.Repositories}})
		if err != nil {
			code := "unavailable"
			var refusal *clientsrv.OperationError
			if errors.As(err, &refusal) {
				code = string(refusal.Code)
			}
			raw, _ := json.Marshal(map[string]string{"code": code})
			return loop.Result{ID: call.ID, Code: loop.Denied, Output: raw}
		}
		raw, _ := json.Marshal(answer.Payload)
		return loop.Result{ID: call.ID, Code: loop.OK, Output: raw}
	}}
}
func buildMockDeclaration() provider.ToolDecl {
	return provider.ToolDecl(`{"type":"function","name":"build_mock","description":"Hand the current checked mock and its complete checklist to the work engine when the person asks to build it. Choose screen or mini_app and the repository scope. Retains pinned page/PNG references and normal coding-owner authority. Does not grant deployment approval. Read the current result after a stale revision.","parameters":{"type":"object","properties":{"result_id":{"type":"string"},"revision":{"type":"integer","minimum":1},"target":{"type":"string","enum":["screen","mini_app"]},"repositories":{"type":"array","minItems":1,"maxItems":16,"items":{"type":"string"}}},"required":["result_id","revision","target","repositories"],"additionalProperties":false}}`)
}
