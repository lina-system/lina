// SPDX-License-Identifier: Apache-2.0

package accounts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/responses"
)

var (
	errV1Rejected    = errors.New("v1_request_rejected")
	errV1Incomplete  = errors.New("v1_evidence_incomplete")
	errV1Unavailable = errors.New("v1_request_unavailable")
)

type v1ResponsesReport struct {
	CompletedTurns       int             `json:"completed_turns"`
	EncryptedRoundTrip   bool            `json:"encrypted_reasoning_round_trip"`
	ParallelCalls        bool            `json:"parallel_tool_calls"`
	ApplyPatch           bool            `json:"apply_patch_function"`
	Fields               map[string]bool `json:"accepted_fields"`
	RequestFields        []string        `json:"accepted_request_fields"`
	ToolDeclarations     json.RawMessage `json:"accepted_tool_declarations"`
	StoreFalseStreamTrue bool            `json:"store_false_stream_true"`
}

var v1RequestFields = provider.FieldList{"model", "input", "store", "stream", "instructions", "reasoning", "include"}

// The current adapter accepts opaque additional_tools input, the tool form
// supported by the sign-in surface. These tools return synthetic results only;
// acceptance never executes model-supplied code or writes a proposed patch.
const v1Tools = `{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"probe_echo","description":"Return a synthetic echo result for protocol validation.","strict":true,"parameters":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":false}},{"type":"function","name":"apply_patch","description":"Validate a synthetic one-string patch call; no files are changed.","strict":true,"parameters":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":false}}]}`

func probeV1Responses(ctx context.Context, credential provider.Credential, model provider.ModelRef, transport http.RoundTripper) (v1ResponsesReport, error) {
	report := v1ResponsesReport{Fields: make(map[string]bool)}
	input := []provider.Item{json.RawMessage(v1Tools), json.RawMessage(`{"role":"user","content":"For a transport check, call probe_echo with input echo and apply_patch with input patch in parallel in the same response. Both tools are synthetic and change nothing. After both tool outputs arrive, answer OK without further tool calls."}`)}
	request := provider.Request{Credential: credential, Model: model, Effort: "medium", Purpose: provider.Turn, Items: input}
	// The probe measures the surface's acceptance of the selected reasoning-state
	// capability, so it asks for the encrypted round trip the route selects.
	request.Capabilities.ReasoningState = true
	for round := 0; round < 3; round++ {
		items, calls, err := v1Turn(ctx, request, transport)
		if err != nil {
			return report, err
		}
		report.CompletedTurns++
		report.StoreFalseStreamTrue = true
		report.RequestFields = []string{"model", "input", "store", "stream", "include", "reasoning"}
		report.Fields["include"] = true
		encrypted := false
		for _, item := range items {
			var metadata struct {
				Type      string `json:"type"`
				Encrypted string `json:"encrypted_content"`
			}
			if json.Unmarshal(item, &metadata) != nil {
				return report, errV1Incomplete
			}
			if metadata.Type == "reasoning" && metadata.Encrypted != "" {
				encrypted = true
			}
			request.Items = append(request.Items, item)
		}
		if round == 0 {
			if !encrypted || len(calls) < 2 {
				return report, errV1Incomplete
			}
			seen := map[string]bool{}
			for _, call := range calls {
				if call.Custom || call.ID == "" || call.Name != "probe_echo" && call.Name != "apply_patch" {
					return report, errV1Incomplete
				}
				var args struct {
					Input string `json:"input"`
				}
				if json.Unmarshal([]byte(call.Arguments), &args) != nil || args.Input == "" {
					return report, errV1Incomplete
				}
				seen[call.Name] = true
				output, _ := json.Marshal(map[string]string{"type": "function_call_output", "call_id": call.ID, "output": "synthetic result for " + call.Name})
				request.Items = append(request.Items, output)
			}
			report.ParallelCalls = seen["probe_echo"] && seen["apply_patch"]
			report.ApplyPatch = seen["apply_patch"]
			report.ToolDeclarations = json.RawMessage(v1Tools)
			if !report.ParallelCalls {
				return report, errV1Incomplete
			}
		} else {
			if len(calls) != 0 {
				return report, errV1Incomplete
			}
			report.EncryptedRoundTrip = true
			if round == 1 {
				request.Items = append(request.Items, json.RawMessage(`{"role":"user","content":"Name the two synthetic tools whose results you received. Do not call a tool."}`))
			}
		}
	}
	for _, field := range []string{"parallel_tool_calls", "service_tier"} {
		var value any = true
		if field == "service_tier" {
			value = "auto"
		}
		accepted, err := probeV1Field(ctx, credential, model, field, value, transport)
		if err == nil || errors.Is(err, errV1Rejected) {
			report.Fields[field] = accepted
			if accepted {
				report.RequestFields = append(report.RequestFields, field)
			}
		}
		if err != nil && !errors.Is(err, errV1Rejected) {
			return report, err
		}
	}
	return report, nil
}

func v1Turn(ctx context.Context, request provider.Request, transport http.RoundTripper) ([]provider.Item, []provider.ToolCall, error) {
	adapter, err := responses.New(responses.Config{Adapter: "responses", RequestFields: v1RequestFields, Capabilities: provider.CapabilitySet{provider.ReasoningStateCapability}}, &http.Client{Transport: transport})
	if err != nil {
		return nil, nil, errV1Incomplete
	}
	activity := lease.New(func() {})
	defer activity.Release()
	stream, err := adapter.Stream(ctx, activity, request)
	if err != nil {
		return nil, nil, v1Failure(err)
	}
	defer stream.Close()
	var items []provider.Item
	var calls []provider.ToolCall
	completed := false
	for {
		event, err := stream.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, v1Failure(err)
		}
		switch event.Kind {
		case provider.Delta:
		case provider.ItemDone:
			items = append(items, bytes.Clone(event.Item))
		case provider.ToolCallReady:
			if event.Call == nil {
				return nil, nil, errV1Incomplete
			}
			calls = append(calls, *event.Call)
		case provider.Terminal:
			completed = true
		}
	}
	if !completed {
		return nil, nil, errV1Incomplete
	}
	return items, calls, nil
}

func v1Failure(err error) error {
	var failure *provider.Failure
	if errors.As(err, &failure) && failure.Kind == provider.NotAccepted {
		return errV1Rejected
	}
	return errV1Unavailable
}

// Open fields are isolated probes, not additions to the product manifest.
// The production adapter assembles and consumes the stream. Only this explicit
// acceptance probe inserts the one field being measured into the HTTP body.
func probeV1Field(ctx context.Context, credential provider.Credential, model provider.ModelRef, field string, value any, transport http.RoundTripper) (bool, error) {
	if field != "parallel_tool_calls" && field != "service_tier" {
		return false, errV1Incomplete
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	probe := v1Script(func(request *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(request.Body)
		request.Body.Close()
		if err != nil {
			return nil, errV1Incomplete
		}
		var body map[string]json.RawMessage
		if json.Unmarshal(raw, &body) != nil {
			return nil, errV1Incomplete
		}
		body[field], err = json.Marshal(value)
		if err != nil {
			return nil, errV1Incomplete
		}
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, errV1Incomplete
		}
		cloned := request.Clone(request.Context())
		cloned.Body = io.NopCloser(bytes.NewReader(raw))
		cloned.ContentLength = int64(len(raw))
		cloned.GetBody = nil
		return transport.RoundTrip(cloned)
	})
	_, _, err := v1Turn(ctx, provider.Request{Credential: credential, Model: model, Purpose: provider.Turn, Items: []provider.Item{json.RawMessage(`{"role":"user","content":"Respond with OK."}`)}}, probe)
	return err == nil, err
}
