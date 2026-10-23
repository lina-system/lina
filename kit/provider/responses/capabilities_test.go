// SPDX-License-Identifier: Apache-2.0
package responses

import (
	"errors"
	"github.com/thisisjun786/lina/kit/provider"
	"net/http"
	"testing"
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
