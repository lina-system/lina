// SPDX-License-Identifier: Apache-2.0
package modelpath

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestMessagesEnvironment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "synthetic-key" {
			t.Error("configured request did not reach endpoint")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	values := map[string]string{
		"LINA_CLAUDE_API_BASE_URL": server.URL + "/v1",
		"LINA_CLAUDE_API_MODEL":    "configured-model",
		"LINA_CLAUDE_API_KEY":      "synthetic-key",
	}
	get := func(name string) string { return values[name] }
	cfg, err := MessagesFromEnv(get)
	if err != nil || cfg == nil {
		t.Fatalf("read configuration: %v", err)
	}
	if cfg.Route != "claude_api_key" || cfg.Model != "configured-model" {
		t.Fatalf("route %q model %q", cfg.Route, cfg.Model)
	}
	if cfg.Adapter.Class() != provider.ModelAPI || cfg.Adapter.Provider() != provider.Claude {
		t.Fatal("wrong route class")
	}
	request := cfg.Request([]provider.Item{json.RawMessage(`{"role":"user","content":"hello"}`)})
	if request.Credential.Kind != provider.APIKey || request.Credential.Account == "" {
		t.Fatal("route does not hold its own API key")
	}
	for _, rendered := range []string{fmt.Sprint(cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg)} {
		if strings.Contains(rendered, server.URL) || strings.Contains(rendered, "synthetic-key") {
			t.Fatal("configuration formatting leaked endpoint or credential")
		}
	}
	supported, admitted := cfg.Capabilities(cfg.Model)
	for _, capability := range []provider.Capability{provider.CacheMarksCapability, provider.CacheLifetime1hCapability, provider.BatchCapability} {
		if !supported.Has(capability) || !admitted.Has(capability) {
			t.Fatalf("%s not admitted", capability)
		}
	}
	if admitted.Has(provider.CacheIdentityCapability) || admitted.Has(provider.ServerStateCapability) {
		t.Fatal("unadmitted capability offered")
	}
	l := lease.New(func() {})
	defer l.Release()
	s, err := cfg.Adapter.Stream(t.Context(), l, cfg.Request([]provider.Item{json.RawMessage(`{"role":"user","content":"hello"}`)}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Next(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, values := range []map[string]string{
		{"LINA_CLAUDE_API_BASE_URL": server.URL + "/v1"},
		{"LINA_CLAUDE_API_KEY": "synthetic-key"},
		{"LINA_CLAUDE_API_BASE_URL": server.URL + "/v1", "LINA_CLAUDE_API_KEY": "synthetic-key"},
	} {
		if _, err := MessagesFromEnv(func(name string) string { return values[name] }); err == nil {
			t.Errorf("incomplete configuration accepted: %v", values)
		}
	}
	// A model without a key stays configured but signed out: the turn reports
	// needs sign-in and no other account serves it.
	signedOut, err := MessagesFromEnv(func(name string) string {
		return map[string]string{"LINA_CLAUDE_API_MODEL": "configured-model"}[name]
	})
	if err != nil || signedOut == nil {
		t.Fatalf("signed-out route: %v", err)
	}
	if _, err := signedOut.Account(); err == nil {
		t.Fatal("signed-out route prepared a request")
	} else {
		var failure *provider.Failure
		if !errors.As(err, &failure) || failure.Kind != provider.NeedsSignIn {
			t.Fatalf("account failure: %v", err)
		}
	}
	if _, err := MessagesFromEnv(get); err != nil {
		t.Fatal("optional base URL refused")
	}
	values["LINA_CLAUDE_API_KEY"] = "synthetic-key\n"
	if _, err := MessagesFromEnv(get); err == nil || strings.Contains(err.Error(), "synthetic-key") {
		t.Fatal("invalid credential accepted or leaked")
	}
	clear(values)
	if cfg, err := MessagesFromEnv(get); cfg != nil || err != nil {
		t.Fatal("unconfigured Core must retain connection-only mode")
	}
}

func TestMessagesRouteIsExclusive(t *testing.T) {
	values := map[string]string{"LINA_CLAUDE_API_MODEL": "configured-model", "LINA_CLAUDE_API_KEY": "synthetic-key"}
	get := func(name string) string { return values[name] }
	if cfg, err := FromEnv(get); err != nil || cfg == nil || cfg.Route != "claude_api_key" {
		t.Fatalf("default endpoint route: %+v %v", cfg, err)
	}
	values["LINA_CLAUDE_NODE"], values["LINA_CLAUDE_ADAPTER"] = "/tools/node", "/adapter/main.ts"
	if _, err := FromEnv(get); err == nil {
		t.Fatal("ambiguous Claude routes accepted")
	}
	delete(values, "LINA_CLAUDE_NODE")
	delete(values, "LINA_CLAUDE_ADAPTER")
	values["LINA_RESPONSES_BASE_URL"], values["LINA_RESPONSES_MODEL"], values["LINA_RESPONSES_API_KEY"] = "https://example.invalid/v1", "fixture", "synthetic-key"
	if _, err := FromEnv(get); err == nil {
		t.Fatal("ambiguous model routes accepted")
	}
}
