// SPDX-License-Identifier: Apache-2.0
package responses

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestConfiguredEndpoint(t *testing.T) {
	for _, suffix := range []string{"/proxy/v1", "/proxy/v1/"} {
		t.Run(suffix, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer synthetic-key" {
					t.Error("wrong credential")
				}
				switch r.URL.Path {
				case "/proxy/v1/models":
					w.Write([]byte(`{"data":[{"id":"configured-model"}]}`))
				case "/proxy/v1/responses":
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["model"] != "configured-model" || body["store"] != false || body["stream"] != true {
						t.Error("configuration did not reach the stateless streaming request")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					sse(w, "response.output_text.delta", `"output_index":0,"delta":"hello"`)
					sse(w, "response.output_item.done", `"output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}`)
					completed(w)
				default:
					t.Error("base path was not preserved")
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			a, err := NewWithBaseURL(config(t), server.Client(), server.URL+suffix)
			if err != nil {
				t.Fatal(err)
			}
			r := request()
			r.Model = "configured-model"
			r.Credential = provider.NewCredential("configured-account", provider.APIKey, "synthetic-key")
			events, _, err := run(t, a, r)
			if err != nil || len(events) != 3 || events[0].Kind != provider.Delta || events[1].Kind != provider.ItemDone || events[2].Kind != provider.Terminal {
				t.Fatalf("stream: %v, events=%d", err, len(events))
			}
			l := lease.New(func() {})
			defer l.Release()
			models, err := a.Models(t.Context(), l, r.Credential)
			if err != nil || len(models.Models) != 1 || models.Models[0] != r.Model || calls.Load() != 2 {
				t.Fatalf("models: %v; requests=%d", err, calls.Load())
			}
			_, _, err = run(t, a, request())
			failure(t, err, provider.NotAccepted)
			if calls.Load() != 2 {
				t.Fatal("sent an OAuth token to a configured API-key endpoint")
			}
		})
	}
}

func TestConfiguredEndpointValidation(t *testing.T) {
	for _, base := range []string{"", "relative/v1", "ftp://example.invalid/v1", "https://user:secret@example.invalid/v1", "https://example.invalid/v1?key=secret", "https://example.invalid/v1#secret", "https:///v1", "https://example.invalid/%zz"} {
		_, err := NewWithBaseURL(config(t), nil, base)
		if err == nil || strings.Contains(err.Error(), "example.invalid") || strings.Contains(err.Error(), "secret") {
			t.Error("invalid endpoint accepted or included in diagnostic")
		}
	}
}

func TestConfiguredEndpointDoesNotRedirect(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/redirected")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	a, err := NewWithBaseURL(config(t), server.Client(), server.URL+"/v1")
	if err != nil {
		t.Fatal(err)
	}
	r := request()
	r.Credential = provider.NewCredential("configured-account", provider.APIKey, "synthetic-key")
	_, _, err = run(t, a, r)
	if err == nil || calls.Load() != 1 {
		t.Fatalf("redirected or retried: calls=%d error=%v", calls.Load(), err)
	}
}
