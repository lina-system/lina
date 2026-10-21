// SPDX-License-Identifier: Apache-2.0
package loop_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/responses"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestResponsesWireLoop(t *testing.T) {
	for _, cut := range []bool{false, true} {
		t.Run(fmt.Sprint("cutoff=", cut), func(t *testing.T) {
			requests, runs, deltas := 0, 0, 0
			client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
				requests++
				var body struct {
					Input  []json.RawMessage
					Store  bool
					Stream bool
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Store || !body.Stream {
					t.Fatal("incorrect request flags")
				}
				var data string
				if requests == 1 {
					data = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"working\",\"output_index\":0}\n\n" +
						"data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"a\",\"name\":\"edit\",\"arguments\":\"{}\"}}\n\n"
					if cut {
						data += "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n\n"
					}
				} else {
					if len(body.Input) != 2 {
						t.Errorf("expected assistant and tool result, got %s", body.Input)
					}
				}
				if !cut {
					data += "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(data)), Request: r}, nil
			})}
			adapter, err := responses.New(responses.Config{SDKModule: responses.SDKModule, SDKVersion: responses.SDKVersion, RequestFields: provider.FieldList{"model", "input", "store", "stream", "tools"}}, client)
			if err != nil {
				t.Fatal(err)
			}
			l := lease.New(func() {})
			defer l.Release()
			cfg := config(func(ctx context.Context, r provider.Request) (provider.Stream, error) {
				return adapter.Stream(ctx, l, r)
			}, func(_ context.Context, c provider.ToolCall) loop.Result { runs++; return success(c) })
			cfg.Request.Credential = provider.NewCredential("fixture", provider.LoopbackOAuth, "synthetic-token")
			cfg.Request.Model = "fixture"
			cfg.Request.Purpose = provider.Turn
			cfg.Observe = func(_ context.Context, e provider.Event) error {
				if e.Kind == provider.Delta {
					deltas++
				}
				return nil
			}
			out, err := loop.Run(t.Context(), cfg)
			if cut {
				if err == nil || runs != 0 || requests != 1 || len(out.Results) != 1 || out.Results[0].Code != loop.Incomplete {
					t.Fatalf("%+v %v runs=%d requests=%d", out, err, runs, requests)
				}
			} else if err != nil || runs != 1 || requests != 2 {
				t.Fatalf("%+v %v runs=%d requests=%d", out, err, runs, requests)
			}
			if deltas != 1 {
				t.Fatal(deltas)
			}
		})
	}
}
