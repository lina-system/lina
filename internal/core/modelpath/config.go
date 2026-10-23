// SPDX-License-Identifier: Apache-2.0

// Package modelpath configures the model route owned by Core.
package modelpath

import (
	"errors"
	"strings"

	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/responses"
)

// Config is an explicit model route, independent of stored account
// credentials. The caller constructs it once per route; it is never written to canon.
type Config struct {
	Route      string
	Adapter    provider.Adapter
	Model      provider.ModelRef
	credential provider.Credential
}

type ResponsesConfig = Config

func (*Config) String() string   { return "[model configuration]" }
func (*Config) GoString() string { return "[model configuration]" }

// ResponsesFromEnv returns nil when the route is unconfigured. Partial settings
// fail closed, with no credential discovery or fallback to a different model.
func ResponsesFromEnv(getenv func(string) string) (*ResponsesConfig, error) {
	base := getenv("LINA_RESPONSES_BASE_URL")
	model := getenv("LINA_RESPONSES_MODEL")
	key := getenv("LINA_RESPONSES_API_KEY")
	if base == "" && model == "" && key == "" {
		return nil, nil
	}
	if base == "" || model == "" || key == "" {
		return nil, errors.New("Responses requires LINA_RESPONSES_BASE_URL, LINA_RESPONSES_MODEL and LINA_RESPONSES_API_KEY")
	}
	if strings.TrimSpace(model) != model || strings.ContainsAny(model, "\r\n\x00") || strings.TrimSpace(key) != key || strings.ContainsAny(key, "\r\n\x00") {
		return nil, errors.New("invalid Responses model or API key")
	}
	policy, err := responses.BuildConfig()
	if err != nil {
		return nil, err
	}
	adapter, err := responses.NewWithBaseURL(policy, nil, base)
	if err != nil {
		return nil, err
	}
	return &ResponsesConfig{Route: "responses_api_key", Adapter: adapter, Model: provider.ModelRef(model), credential: provider.NewCredential("configured-responses", provider.APIKey, key)}, nil
}

func (c *Config) Request(items []provider.Item) provider.Request {
	return provider.Request{Credential: c.credential, Model: c.Model, Purpose: provider.Turn, Items: items}
}

// Capabilities returns the model declaration and the build's route admission.
// Core's composition root applies its selection policy before recording a call.
func (c *Config) Capabilities(model provider.ModelRef) (provider.CapabilitySet, provider.CapabilitySet) {
	route := c.Route
	if route == "" {
		route = string(c.Adapter.Provider())
	}
	admitted := provider.Admitted(route)
	if configured, ok := c.Adapter.(provider.CapabilityAdmission); ok {
		narrowing := configured.AdmittedCapabilities(model)
		var intersection provider.CapabilitySet
		for _, capability := range admitted {
			if narrowing.Has(capability) {
				intersection = append(intersection, capability)
			}
		}
		admitted = intersection
	}
	return c.Adapter.Capabilities(model), admitted
}
