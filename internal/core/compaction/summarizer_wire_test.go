// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/responses"
)

type summaryTransport func(*http.Request) (*http.Response, error)

func (f summaryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAuxiliaryInstructionsReachResponsesWire(t *testing.T) {
	cfg, err := responses.BuildConfig()
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Input []struct{ Role, Content string }
		Tools []json.RawMessage
	}
	client := &http.Client{Transport: summaryTransport(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Fatal(err)
		}
		item := summaryItem(t)
		body := "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":" + string(item) + "}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"summary-response\",\"status\":\"completed\"}}\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	adapter, err := responses.New(cfg, client)
	if err != nil {
		t.Fatal(err)
	}
	l := lease.New(func() {})
	defer l.Release()
	auxiliary := Auxiliary{Request: provider.Request{Model: "model", Credential: provider.NewCredential("account", provider.APIKey, "synthetic-token")}, Stream: func(ctx context.Context, r provider.Request) (provider.Stream, error) {
		return adapter.Stream(ctx, l, r)
	}}
	got, err := auxiliary.Summarize(t.Context(), PassSegment{Records: []Record{{ID: "fact", Item: message("value 42")}}})
	if err != nil || got.Values != "42" {
		t.Fatalf("summary: %+v %v", got, err)
	}
	if len(wire.Input) != 2 || wire.Input[0].Role != "developer" || wire.Input[1].Role != "user" || len(wire.Tools) != 0 {
		t.Fatalf("auxiliary wire omitted the developer instructions: %+v", wire)
	}
	for _, key := range []string{"unanswered_requests", "topics_and_conclusions", "completed_actions", "specific_values", "omitted_detail"} {
		if !strings.Contains(wire.Input[0].Content, key) {
			t.Fatalf("required output field %s absent from wire instructions", key)
		}
	}
	if strings.Contains(wire.Input[0].Content, "value 42") || !strings.Contains(wire.Input[1].Content, "fact") {
		t.Fatal("source data escaped its separate user envelope")
	}
}

func TestCompactionRetainsConversationPrefixOnResponsesWire(t *testing.T) {
	cfg, err := responses.BuildConfig()
	if err != nil {
		t.Fatal(err)
	}
	tool := json.RawMessage(`{"type":"function","name":"read_file","parameters":{"type":"object"}}`)
	prefix := message("fixed catalog")
	source := message("receipt 42")
	client := &http.Client{Transport: summaryTransport(func(r *http.Request) (*http.Response, error) {
		var wire struct {
			Instructions string
			Input, Tools []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Fatal(err)
		}
		if wire.Instructions != "fixed persona" || len(wire.Tools) != 1 || !bytes.Equal(wire.Tools[0], tool) || len(wire.Input) != 3 || !bytes.Equal(wire.Input[0], prefix) || !bytes.Equal(wire.Input[1], source) {
			t.Fatalf("compaction changed the cached prefix: %+v", wire)
		}
		if !strings.Contains(string(wire.Input[2]), "unanswered_requests") {
			t.Fatal("summary instruction did not follow the segment")
		}
		body := "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":" + string(summaryItem(t)) + "}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	adapter, err := responses.New(cfg, client)
	if err != nil {
		t.Fatal(err)
	}
	l := lease.New(func() {})
	defer l.Release()
	aux := Auxiliary{RetainPrefix: true, Request: provider.Request{Model: "model", Credential: provider.NewCredential("account", provider.APIKey, "synthetic-token"), Instructions: "fixed persona", Items: []provider.Item{prefix}, Tools: []provider.ToolDecl{tool}}, Stream: func(ctx context.Context, r provider.Request) (provider.Stream, error) {
		if r.Purpose != provider.Auxiliary || r.Bridge != nil {
			t.Fatal("compaction acquired tool execution")
		}
		return adapter.Stream(ctx, l, r)
	}}
	if _, err := aux.Summarize(t.Context(), PassSegment{Records: []Record{{ID: "source", Item: source}}}); err != nil {
		t.Fatal(err)
	}
}
