// SPDX-License-Identifier: Apache-2.0
package modelpath

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/claudeproc"
)

// A model gateway is a Claude API-key account: the route answers at the
// confirmed base URL with the confirmed key and nothing from the vendor
// command's own gateway setup.
func TestGatewayIsAConfirmedAPIKeyRoute(t *testing.T) {
	var got []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path+" "+r.Header.Get("x-api-key")+" "+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	values := map[string]string{
		"LINA_CLAUDE_API_BASE_URL": server.URL + "/gateway/anthropic/v1",
		"LINA_CLAUDE_API_MODEL":    "gateway-model",
		"LINA_CLAUDE_API_KEY":      "confirmed-gateway-key",
		// The vendor command's own gateway login is present in Core's
		// environment and must be neither adopted nor forwarded.
		"ANTHROPIC_BASE_URL":      "https://unconfirmed.invalid/v1",
		"ANTHROPIC_API_KEY":       "command-key",
		"ANTHROPIC_AUTH_TOKEN":    "command-token",
		"CLAUDE_CODE_USE_BEDROCK": "1",
	}
	get := func(key string) string { return values[key] }
	cfg, err := FromEnv(get)
	if err != nil || cfg == nil {
		t.Fatalf("gateway configuration: %v", err)
	}
	if cfg.Route != "claude_api_key" || cfg.Adapter.Class() != provider.ModelAPI || cfg.Adapter.Provider() != provider.Claude {
		t.Fatalf("gateway is not the API-key route: %q %v %v", cfg.Route, cfg.Adapter.Class(), cfg.Adapter.Provider())
	}
	if _, ok := cfg.Adapter.(*claudeproc.Adapter); ok {
		t.Fatal("gateway selected the subscription adapter")
	}
	for _, rendered := range []string{fmt.Sprint(cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg)} {
		if strings.Contains(rendered, "unconfirmed.invalid") || strings.Contains(rendered, "confirmed-gateway-key") {
			t.Fatal("configuration formatting leaked a gateway value")
		}
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
	want := "/gateway/anthropic/v1/messages confirmed-gateway-key "
	if len(got) != 1 || got[0] != want {
		t.Fatalf("gateway request %v, want %q", got, want)
	}
}

// The vendor command's gateway setup never selects a route on its own: LINA
// serves a gateway only through the confirmed base URL and key, and the
// subscription adapter never inherits those values.
func TestGatewaySetupDoesNotConfigureOrEnterTheAdapter(t *testing.T) {
	gateway := map[string]string{
		"ANTHROPIC_BASE_URL":      "https://unconfirmed.invalid/v1",
		"ANTHROPIC_API_KEY":       "command-key",
		"ANTHROPIC_AUTH_TOKEN":    "command-token",
		"CLAUDE_CODE_USE_BEDROCK": "1",
	}
	get := func(key string) string { return gateway[key] }
	if cfg, err := FromEnv(get); cfg != nil || err != nil {
		t.Fatalf("unconfirmed gateway selected a route: %+v %v", cfg, err)
	}

	values := map[string]string{
		"LINA_CLAUDE_NODE":        "/tools/node",
		"LINA_CLAUDE_ADAPTER":     "/adapter/main.ts",
		"LINA_CLAUDE_MODEL":       "fixture-model",
		"PATH":                    "/tools/bin",
		"CLAUDE_CODE_OAUTH_TOKEN": "subscription-token",
	}
	for key, value := range gateway {
		values[key] = value
	}
	cfg, err := FromEnv(func(key string) string { return values[key] })
	if err != nil || cfg == nil || cfg.Route != "claude" {
		t.Fatalf("subscription route: %+v %v", cfg, err)
	}
	adapter, ok := cfg.Adapter.(*claudeproc.Adapter)
	if !ok {
		t.Fatalf("subscription route adapter %T", cfg.Adapter)
	}
	if strings.Join(adapter.Environment, " ") != "PATH=/tools/bin" {
		t.Fatalf("adapter environment %v", adapter.Environment)
	}
	if adapter.Token.Ready() != true {
		t.Fatal("subscription token not passed to the adapter")
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", cfg, adapter), "subscription-token") {
		t.Fatal("credential exposed by formatting")
	}
	if cfg, err := cfg.Account(); err != nil || cfg.Kind != provider.ObservedCLILogin {
		t.Fatalf("runtime account %+v %v", cfg, err)
	}
}
