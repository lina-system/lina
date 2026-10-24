// SPDX-License-Identifier: Apache-2.0
package modelpath

import (
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/responses"
	"testing"
)

func TestCapabilitiesHonorAdapterAdmission(t *testing.T) {
	config, err := responses.BuildConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.Capabilities = nil
	adapter, err := responses.New(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	route := Config{Adapter: adapter, Model: "model", Route: "chatgpt"}
	supported, admitted := route.Capabilities(route.Model)
	if !supported.Has(provider.ReasoningStateCapability) {
		t.Fatal("support declaration lost")
	}
	if len(admitted) != 0 {
		t.Fatalf("adapter admission widened: %v", admitted)
	}
}
