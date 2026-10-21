// SPDX-License-Identifier: Apache-2.0
package modelpath

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
)

func TestResponsesEnvironment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Error("configured request did not reach endpoint")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()
	values := map[string]string{
		"LINA_RESPONSES_BASE_URL": server.URL + "/v1",
		"LINA_RESPONSES_MODEL":    "configured-model",
		"LINA_RESPONSES_API_KEY":  "synthetic-key",
	}
	get := func(name string) string { return values[name] }
	cfg, err := ResponsesFromEnv(get)
	if err != nil || cfg == nil {
		t.Fatalf("read configuration: %v", err)
	}
	if cfg.Model != "configured-model" {
		t.Fatal("model not configured")
	}
	for _, rendered := range []string{fmt.Sprint(cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg)} {
		if strings.Contains(rendered, server.URL) || strings.Contains(rendered, "synthetic-key") {
			t.Fatal("configuration formatting leaked endpoint or credential")
		}
	}
	l := lease.New(func() {})
	defer l.Release()
	s, err := cfg.Adapter.Stream(t.Context(), l, cfg.Request(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Next(t.Context()); err != nil {
		t.Fatal(err)
	}
	for name := range values {
		original := values[name]
		delete(values, name)
		if _, err := ResponsesFromEnv(get); err == nil {
			t.Errorf("partial configuration accepted without %s", name)
		}
		values[name] = original
	}
	values["LINA_RESPONSES_API_KEY"] = "synthetic-key\n"
	if _, err := ResponsesFromEnv(get); err == nil || strings.Contains(err.Error(), "synthetic-key") {
		t.Fatal("invalid credential accepted or leaked")
	}
	clear(values)
	if cfg, err := ResponsesFromEnv(get); cfg != nil || err != nil {
		t.Fatal("unconfigured Core must retain connection-only mode")
	}
}
