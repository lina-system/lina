// SPDX-License-Identifier: Apache-2.0

package accounts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/provider"
)

type v1Script func(*http.Request) (*http.Response, error)

func (f v1Script) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func v1SSE(items ...string) *http.Response {
	var body strings.Builder
	for i, item := range items {
		fmt.Fprintf(&body, "data: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":%s}\n\n", i, item)
	}
	body.WriteString("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-response\",\"status\":\"completed\"}}\n\n")
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body.String()))}
}

func TestV1ProbeRequiresCompletedTurnsAndPreservesReasoning(t *testing.T) {
	reasoning := `{"id":"reason-one","type":"reasoning","summary":[],"encrypted_content":"synthetic-encrypted-state"}`
	turns, probes := 0, 0
	transport := v1Script(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		var request map[string]json.RawMessage
		if json.Unmarshal(raw, &request) != nil || r.URL.String() != "https://api.openai.com/v1/responses" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
			return nil, errors.New("unexpected request")
		}
		if string(request["store"]) != "false" || string(request["stream"]) != "true" {
			return nil, errors.New("stateful or non-streaming request")
		}
		if bytes.Contains(raw, []byte(`"content":"Respond with OK."`)) {
			probes++
			present := 0
			for _, field := range []string{"parallel_tool_calls", "service_tier", "prompt_cache_key"} {
				if request[field] != nil {
					present++
				}
			}
			if present != 1 || request["include"] != nil || bytes.Contains(raw, []byte(`"additional_tools"`)) {
				return nil, errors.New("field probe was not isolated")
			}
			if string(request["prompt_cache_key"]) == `"conversation-identity"` {
				return nil, errors.New("field probe sent a malformed identity")
			}
			return v1SSE(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}`), nil
		}
		turns++
		if !bytes.Contains(raw, []byte(`"type":"additional_tools"`)) {
			return nil, errors.New("missing supported tool declaration")
		}
		if turns > 1 && !bytes.Contains(raw, []byte(reasoning)) {
			return nil, errors.New("encrypted item was dropped or rewritten")
		}
		switch turns {
		case 1:
			return v1SSE(reasoning, `{"type":"function_call","name":"probe_echo","call_id":"call-one","arguments":"{\"input\":\"echo\"}"}`, `{"type":"function_call","name":"apply_patch","call_id":"call-two","arguments":"{\"input\":\"patch\"}"}`), nil
		case 2:
			if !bytes.Contains(raw, []byte(`"type":"function_call_output"`)) {
				return nil, errors.New("missing tool outputs")
			}
		}
		return v1SSE(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}`), nil
	})
	result, err := probeV1Responses(t.Context(), provider.NewCredential("synthetic", provider.LoopbackOAuth, "synthetic-token"), "synthetic-model", v1Endpoint{transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if result.CompletedTurns != 3 || !result.EncryptedRoundTrip || !result.ParallelCalls || !result.ApplyPatch || turns != 3 || probes != 3 {
		t.Fatalf("incomplete acceptance: %+v turns=%d probes=%d", result, turns, probes)
	}
	if !reflect.DeepEqual(result.Fields, map[string]bool{"include": true, "parallel_tool_calls": true, "service_tier": true, "prompt_cache_key": true}) {
		t.Fatalf("field observations: %v", result.Fields)
	}
	if !reflect.DeepEqual(result.RequestFields, []string{"model", "input", "store", "stream", "include", "reasoning", "parallel_tool_calls", "service_tier", "prompt_cache_key"}) {
		t.Fatalf("accepted request fields: %v", result.RequestFields)
	}
	if strings.Contains(fmt.Sprintf("%+v", result), "synthetic-token") || strings.Contains(fmt.Sprintf("%+v", result), "synthetic-encrypted-state") {
		t.Fatal("acceptance record contains credentials or opaque state")
	}
}

func TestV1ProbeDoesNotTurnPartialEvidenceIntoAcceptance(t *testing.T) {
	for _, test := range []struct {
		name     string
		response func() *http.Response
	}{
		{"no encrypted reasoning", func() *http.Response {
			return v1SSE(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}`)
		}},
		{"truncated", func() *http.Response {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"unfinished\"}\n\n"))}
		}},
		{"rejected", func() *http.Response {
			return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"code":"unsupported_parameter"}}`))}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := probeV1Responses(context.Background(), provider.NewCredential("synthetic", provider.LoopbackOAuth, "synthetic-token"), "synthetic-model", v1Endpoint{transport: v1Script(func(*http.Request) (*http.Response, error) { return test.response(), nil })})
			if err == nil || result.EncryptedRoundTrip || result.ParallelCalls || result.ApplyPatch || result.CompletedTurns >= 3 {
				t.Fatalf("accepted incomplete result: %+v %v", result, err)
			}
		})
	}
}

func TestV1ProbeRecordsCacheIdentityAcceptance(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
	}{
		{"accepted", 200},
		{"rejected", 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			sent := ""
			accepted, err := probeV1Field(t.Context(), provider.NewCredential("synthetic", provider.LoopbackOAuth, "synthetic-token"), "synthetic-model", "prompt_cache_key", v1ProbeCacheIdentity, v1Endpoint{transport: v1Script(func(r *http.Request) (*http.Response, error) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]json.RawMessage
				if json.Unmarshal(raw, &body) != nil {
					t.Fatal("unreadable probe request")
				}
				_ = json.Unmarshal(body["prompt_cache_key"], &sent)
				if test.status == 400 {
					return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"code":"unsupported_parameter"}}`))}, nil
				}
				return v1SSE(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}`), nil
			})})
			if err != nil && !errors.Is(err, errV1Rejected) {
				t.Fatal(err)
			}
			if accepted != (test.status == 200) {
				t.Fatalf("accepted=%v for status %d", accepted, test.status)
			}
			if !provider.ValidCacheIdentity(sent) || sent != v1ProbeCacheIdentity {
				t.Fatalf("probe did not send its cache identity: %q", sent)
			}
		})
	}
}

func TestV1ProbeRecordsFieldObservationsWhenTheTurnLoopFails(t *testing.T) {
	transport := v1Script(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]json.RawMessage
		if json.Unmarshal(raw, &body) != nil {
			t.Fatal("unreadable probe request")
		}
		// The route answers a plain turn but returns no encrypted reasoning, so
		// the round trip cannot be shown. The isolated field probes still ran.
		return v1SSE(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}`), nil
	})
	result, err := probeV1Responses(t.Context(), provider.NewCredential("synthetic", provider.LoopbackOAuth, "synthetic-token"), "synthetic-model", v1Endpoint{transport: transport})
	if !errors.Is(err, errV1Incomplete) {
		t.Fatalf("incomplete turn loop: %v", err)
	}
	if !reflect.DeepEqual(result.Fields, map[string]bool{"include": true, "parallel_tool_calls": true, "service_tier": true, "prompt_cache_key": true}) {
		t.Fatalf("field observations lost with the failed turn loop: %v", result.Fields)
	}
	if result.EncryptedRoundTrip || result.ParallelCalls {
		t.Fatalf("failed turn loop claimed acceptance: %+v", result)
	}
}

func TestV1ProbeSendsToAnExplicitResponsesEndpoint(t *testing.T) {
	var path, authorization, model string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, authorization = r.URL.Path, r.Header.Get("Authorization")
		var body struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		model = body.Model
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"synthetic-response","status":"completed"}}`+"\n\n")
	}))
	defer server.Close()
	accepted, err := probeV1Field(t.Context(), provider.NewCredential("synthetic", provider.APIKey, "synthetic-token"), "synthetic-model", "service_tier", "auto", v1Endpoint{baseURL: server.URL + "/proxy/v1"})
	if err != nil || !accepted {
		t.Fatalf("explicit endpoint probe: %v %v", accepted, err)
	}
	if path != "/proxy/v1/responses" || authorization != "Bearer synthetic-token" || model != "synthetic-model" {
		t.Fatalf("request went to %q as %q for %q", path, authorization, model)
	}
}

func TestV1ProbeRefusesToRecordAcceptanceForAMalformedCacheIdentity(t *testing.T) {
	if accepted, err := probeV1Field(t.Context(), provider.NewCredential("synthetic", provider.LoopbackOAuth, "synthetic-token"), "synthetic-model", "prompt_cache_key", "conversation-identity", v1Endpoint{transport: v1Script(func(*http.Request) (*http.Response, error) {
		t.Fatal("malformed identity reached the wire")
		return nil, nil
	})}); accepted || !errors.Is(err, errV1Incomplete) {
		t.Fatalf("malformed identity: %v %v", accepted, err)
	}
}

func TestV1FieldProbeRecordsRejectionWithoutClaimingAcceptance(t *testing.T) {
	field, err := probeV1Field(t.Context(), provider.NewCredential("synthetic", provider.LoopbackOAuth, "synthetic-token"), "synthetic-model", "service_tier", "auto", v1Endpoint{transport: v1Script(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"code":"unsupported_parameter","message":"synthetic-token"}}`))}, nil
	})})
	if field || err == nil || strings.Contains(err.Error(), "synthetic-token") {
		t.Fatalf("rejected field: %v %v", field, err)
	}
	if !errors.Is(err, errV1Rejected) {
		t.Fatalf("unclassified surface rejection: %v", err)
	}
}
