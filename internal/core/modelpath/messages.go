// SPDX-License-Identifier: Apache-2.0
package modelpath

import (
	"errors"
	"strings"

	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/messages"
)

// MessagesFromEnv selects the Claude API-key route over the Messages API.
// The base URL and the effort are optional; the model is required. A missing
// key keeps the route configured but signed out, so a turn reports needs
// sign-in instead of falling back to another account or route.
func MessagesFromEnv(getenv func(string) string) (*Config, error) {
	base := getenv("LINA_CLAUDE_API_BASE_URL")
	model := getenv("LINA_CLAUDE_API_MODEL")
	key := getenv("LINA_CLAUDE_API_KEY")
	effort := provider.Effort(getenv("LINA_CLAUDE_API_EFFORT"))
	if base == "" && model == "" && key == "" && effort == "" {
		return nil, nil
	}
	if model == "" {
		return nil, errors.New("Messages requires LINA_CLAUDE_API_MODEL")
	}
	invalid := strings.TrimSpace(model) != model || strings.ContainsAny(model, "\r\n\x00") || strings.TrimSpace(key) != key || strings.ContainsAny(key, "\r\n\x00")
	for _, value := range []string{base, string(effort)} {
		invalid = invalid || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00")
	}
	if invalid {
		return nil, errors.New("invalid Messages model, key or route setting")
	}
	policy, err := messages.BuildConfig()
	if err != nil {
		return nil, err
	}
	var adapter *messages.Adapter
	if base == "" {
		adapter, err = messages.New(policy, nil)
	} else {
		adapter, err = messages.NewWithBaseURL(policy, nil, base)
	}
	if err != nil {
		return nil, err
	}
	return &Config{Route: "claude_api_key", Adapter: adapter, Model: provider.ModelRef(model), Effort: effort, credential: provider.NewCredential("configured-claude-api", provider.APIKey, key)}, nil
}
