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

// v1ProbeCacheIdentity is a fixed probe-only value in the shape a conversation's
// cache identity takes. It is never a conversation's identity.
var v1ProbeCacheIdentity = provider.RunCacheIdentity("v1-probe-identity")

// The current adapter accepts opaque additional_tools input, the tool form
// supported by the sign-in surface. These tools return synthetic results only;
// acceptance never executes model-supplied code or writes a proposed patch.
const v1Tools = `{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"probe_echo","description":"Return a synthetic echo result for protocol validation.","strict":true,"parameters":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":false}},{"type":"function","name":"apply_patch","description":"Validate a synthetic one-string patch call; no files are changed.","strict":true,"parameters":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":false}}]}`

// v1Endpoint names where the probe sends its requests. The zero value is the
// fixed API origin LINA's own route uses; a base URL selects an explicit
// Responses-compatible endpoint.
type v1Endpoint struct {
	transport http.RoundTripper
	baseURL   string
}

func probeV1Responses(ctx context.Context, credential provider.Credential, model provider.ModelRef, endpoint v1Endpoint) (v1ResponsesReport, error) {
	report := v1ResponsesReport{Fields: make(map[string]bool)}
	input := []provider.Item{json.RawMessage(v1Tools), json.RawMessage(`{"role":"user","content":"For a transport check, call probe_echo with input echo and apply_patch with input patch in parallel in the same response. Both tools are synthetic and change nothing. After both tool outputs arrive, answer OK without further tool calls."}`)}
	request := provider.Request{Credential: credential, Model: model, Effort: "medium", Purpose: provider.Turn, Items: input}
	// The probe measures the surface's acceptance of the selected reasoning-state
	// capability, so it asks for the encrypted round trip the route selects.
	request.Capabilities.ReasoningState = true
	// The isolated field probes run first: they are separate requests, so a route
	// that cannot show the reasoning round trip still records what it accepted.
	var probed []string
	for _, probe := range []struct {
		field string
		value any
	}{{"parallel_tool_calls", true}, {"service_tier", "auto"}, {"prompt_cache_key", v1ProbeCacheIdentity}} {
		accepted, err := probeV1Field(ctx, credential, model, probe.field, probe.value, endpoint)
		if err == nil || errors.Is(err, errV1Rejected) {
			report.Fields[probe.field] = accepted
			if accepted {
				probed = append(probed, probe.field)
			}
		}
		if err != nil && !errors.Is(err, errV1Rejected) {
			return report, err
		}
	}
	// The turn loop records each observation as it arrives. A turn that fails, or
	// a required item that is absent, leaves the recorded facts in place and ends
	// the run without acceptance.
	missing, encrypted, resent := false, false, false
	// Four rounds at most: the route may answer the tool round before it returns
	// any reasoning item, and the round trip needs a later request to carry it.
	for round := 0; round < 4 && !missing; round++ {
		carrying := encrypted
		items, calls, err := v1Turn(ctx, request, endpoint)
		if err != nil {
			report.EncryptedRoundTrip = resent
			return report, err
		}
		// The round trip counts only once a request that carried the encrypted
		// item completed; a refusal of that request is a failed round trip.
		if carrying {
			resent = true
		}
		report.CompletedTurns++
		report.StoreFalseStreamTrue = true
		report.RequestFields = append([]string{"model", "input", "store", "stream", "include", "reasoning"}, probed...)
		report.Fields["include"] = true
		for _, item := range items {
			var metadata struct {
				Type      string `json:"type"`
				Encrypted string `json:"encrypted_content"`
			}
			if json.Unmarshal(item, &metadata) != nil {
				missing = true
				break
			}
			if metadata.Type == "reasoning" && metadata.Encrypted != "" {
				encrypted = true
			}
			request.Items = append(request.Items, item)
		}
		if missing {
			break
		}
		if round == 0 {
			if len(calls) < 2 {
				missing = true
				break
			}
			seen := map[string]bool{}
			for _, call := range calls {
				if call.Custom || call.ID == "" || call.Name != "probe_echo" && call.Name != "apply_patch" {
					missing = true
					break
				}
				var args struct {
					Input string `json:"input"`
				}
				if json.Unmarshal([]byte(call.Arguments), &args) != nil || args.Input == "" {
					missing = true
					break
				}
				seen[call.Name] = true
				output, _ := json.Marshal(map[string]string{"type": "function_call_output", "call_id": call.ID, "output": "synthetic result for " + call.Name})
				request.Items = append(request.Items, output)
			}
			// The follow-up input gives the later turns something to answer, so the
			// route returns its reasoning item where the probe can carry it back.
			request.Items = append(request.Items, json.RawMessage(`{"role":"user","content":"Name the two synthetic tools whose results you received. Do not call a tool."}`))
			if !missing {
				report.ParallelCalls = seen["probe_echo"] && seen["apply_patch"]
				report.ApplyPatch = seen["apply_patch"]
				report.ToolDeclarations = json.RawMessage(v1Tools)
				missing = !report.ParallelCalls
			}
		} else if len(calls) != 0 {
			missing = true
		}
		if !missing && encrypted && resent && report.CompletedTurns >= 3 {
			break
		}
	}
	report.EncryptedRoundTrip = resent
	if missing || report.CompletedTurns < 3 || !report.EncryptedRoundTrip {
		return report, errV1Incomplete
	}
	return report, nil
}

func v1Turn(ctx context.Context, request provider.Request, endpoint v1Endpoint) ([]provider.Item, []provider.ToolCall, error) {
	config := responses.Config{Adapter: "responses", RequestFields: v1RequestFields, Capabilities: provider.CapabilitySet{provider.ReasoningStateCapability}}
	client := &http.Client{Transport: endpoint.transport}
	var adapter *responses.Adapter
	var err error
	if endpoint.baseURL == "" {
		adapter, err = responses.New(config, client)
	} else {
		adapter, err = responses.NewWithBaseURL(config, client, endpoint.baseURL)
	}
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
func probeV1Field(ctx context.Context, credential provider.Credential, model provider.ModelRef, field string, value any, endpoint v1Endpoint) (bool, error) {
	if field != "parallel_tool_calls" && field != "service_tier" && field != "prompt_cache_key" {
		return false, errV1Incomplete
	}
	// A cache identity has one accepted shape; a probe that sent anything else
	// would measure the surface's rejection of a malformed value instead.
	if field == "prompt_cache_key" {
		identity, ok := value.(string)
		if !ok || !provider.ValidCacheIdentity(identity) {
			return false, errV1Incomplete
		}
	}
	transport := endpoint.transport
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
	_, _, err := v1Turn(ctx, provider.Request{Credential: credential, Model: model, Purpose: provider.Turn, Items: []provider.Item{json.RawMessage(`{"role":"user","content":"Respond with OK."}`)}}, v1Endpoint{transport: probe, baseURL: endpoint.baseURL})
	return err == nil, err
}
