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
	calls := 0
	transport := v1Script(func(r *http.Request) (*http.Response, error) {
		calls++
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var request map[string]json.RawMessage
		if json.Unmarshal(raw, &request) != nil || r.URL.String() != "https://api.openai.com/v1/responses" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Fatal("unexpected request")
		}
		if string(request["store"]) != "false" || string(request["stream"]) != "true" {
			t.Fatal("stateful or non-streaming request")
		}
		if calls > 1 && calls <= 3 && !bytes.Contains(raw, []byte(reasoning)) {
			t.Fatal("encrypted item was dropped or rewritten")
		}
		switch calls {
		case 1:
			if !bytes.Contains(raw, []byte(`"type":"additional_tools"`)) {
				t.Fatal("missing supported tool declaration")
			}
			return v1SSE(reasoning, `{"type":"function_call","name":"probe_echo","call_id":"call-one","arguments":"{\"input\":\"echo\"}"}`, `{"type":"function_call","name":"apply_patch","call_id":"call-two","arguments":"{\"input\":\"patch\"}"}`), nil
		case 2:
			if !bytes.Contains(raw, []byte(`"type":"function_call_output"`)) {
				t.Fatal("missing tool outputs")
			}
		case 4:
			if string(request["parallel_tool_calls"]) != "true" || request["service_tier"] != nil {
				t.Fatal("parallel-call field probe was not isolated")
			}
		case 5:
			if string(request["service_tier"]) != `"auto"` || request["parallel_tool_calls"] != nil || request["prompt_cache_key"] != nil {
				t.Fatal("service-tier field probe was not isolated")
			}
		case 6:
			if string(request["prompt_cache_key"]) != `"`+v1ProbeCacheIdentity+`"` || request["parallel_tool_calls"] != nil || request["service_tier"] != nil {
				t.Fatal("cache-identity field probe was not isolated")
			}
		}
		return v1SSE(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}`), nil
	})
	result, err := probeV1Responses(t.Context(), provider.NewCredential("synthetic", provider.LoopbackOAuth, "synthetic-token"), "synthetic-model", transport)
	if err != nil {
		t.Fatal(err)
	}
	if result.CompletedTurns != 3 || !result.EncryptedRoundTrip || !result.ParallelCalls || !result.ApplyPatch || calls != 6 {
		t.Fatalf("incomplete acceptance: %+v calls=%d", result, calls)
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
			calls := 0
			result, err := probeV1Responses(context.Background(), provider.NewCredential("synthetic", provider.LoopbackOAuth, "synthetic-token"), "synthetic-model", v1Script(func(*http.Request) (*http.Response, error) { calls++; return test.response(), nil }))
			if err == nil || calls != 1 || result.EncryptedRoundTrip {
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
			accepted, err := probeV1Field(t.Context(), provider.NewCredential("synthetic", provider.LoopbackOAuth, "synthetic-token"), "synthetic-model", "prompt_cache_key", v1ProbeCacheIdentity, v1Script(func(r *http.Request) (*http.Response, error) {
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
			}))
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

func TestV1ProbeRefusesToRecordAcceptanceForAMalformedCacheIdentity(t *testing.T) {
	if accepted, err := probeV1Field(t.Context(), provider.NewCredential("synthetic", provider.LoopbackOAuth, "synthetic-token"), "synthetic-model", "prompt_cache_key", "conversation-identity", v1Script(func(*http.Request) (*http.Response, error) {
		t.Fatal("malformed identity reached the wire")
		return nil, nil
	})); accepted || !errors.Is(err, errV1Incomplete) {
		t.Fatalf("malformed identity: %v %v", accepted, err)
	}
}

func TestV1FieldProbeRecordsRejectionWithoutClaimingAcceptance(t *testing.T) {
	field, err := probeV1Field(t.Context(), provider.NewCredential("synthetic", provider.LoopbackOAuth, "synthetic-token"), "synthetic-model", "service_tier", "auto", v1Script(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"code":"unsupported_parameter","message":"synthetic-token"}}`))}, nil
	}))
	if field || err == nil || strings.Contains(err.Error(), "synthetic-token") {
		t.Fatalf("rejected field: %v %v", field, err)
	}
	if !errors.Is(err, errV1Rejected) {
		t.Fatalf("unclassified surface rejection: %v", err)
	}
}
