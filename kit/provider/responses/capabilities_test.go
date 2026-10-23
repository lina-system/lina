// SPDX-License-Identifier: Apache-2.0
package responses

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/thisisjun786/lina/kit/provider"
)

func TestCapabilitiesRefusedBeforeSending(t *testing.T) {
	c, err := BuildConfig()
	if err != nil {
		t.Fatal(err)
	}
	a, calls := fixture(t, c, func(http.ResponseWriter, *http.Request) { t.Error("unadmitted request reached transport") })
	for _, use := range []provider.CapabilityUse{{CacheIdentity: "bad"}, {ServerState: "handle"}, {CacheMarks: []provider.CacheMark{{}}}, {Flex: true}, {Batch: "id"}, {ProviderCompaction: true}} {
		r := request()
		r.Capabilities = use
		_, _, err := run(t, a, r)
		var failure *provider.Failure
		if !errors.As(err, &failure) || failure.Kind != provider.NotAccepted {
			t.Fatalf("%+v: %v", use, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("transport started")
	}
	if !a.Capabilities("model").Has(provider.ReasoningStateCapability) || len(a.Capabilities("")) != 0 {
		t.Fatal("capability declaration")
	}
	// A manifest may narrow acceptance even for a supported optimization.
	c.Capabilities = nil
	a, _ = fixture(t, c, func(http.ResponseWriter, *http.Request) { t.Error("narrowed capability sent") })
	r := request()
	r.Capabilities.ReasoningState = true
	if _, _, err := run(t, a, r); err == nil {
		t.Fatal("manifest ignored")
	}
}

// The wire option that returns encrypted reasoning is the application of the
// reasoning-state capability, so it follows Core's selection: a narrowed route
// that leaves the capability unselected must not send it.
func TestReasoningWireOptionFollowsSelectedCapability(t *testing.T) {
	for _, selected := range []bool{true, false} {
		c, err := BuildConfig()
		if err != nil {
			t.Fatal(err)
		}
		if !selected {
			c.Capabilities = nil
		}
		var wire map[string]json.RawMessage
		a, _ := fixture(t, c, func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
				t.Error(err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			completed(w)
		})
		r := request()
		r.Capabilities.ReasoningState = selected
		if _, _, err := run(t, a, r); err != nil {
			t.Fatalf("selected=%v: %v", selected, err)
		}
		include, sent := wire["include"]
		if sent != selected {
			t.Fatalf("selected=%v include=%s", selected, include)
		}
		if selected && string(include) != `["reasoning.encrypted_content"]` {
			t.Fatalf("include=%s", include)
		}
	}
}
