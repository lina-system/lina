// SPDX-License-Identifier: Apache-2.0
package modelpath

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/claudeproc"
	"github.com/thisisjun786/lina/kit/secret"
)

// FromEnv selects exactly one explicitly configured route. Automatic account
// setup and installation are separate from this external-program boundary.
func FromEnv(getenv func(string) string) (*Config, error) {
	responses, err := ResponsesFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	node, program, model := getenv("LINA_CLAUDE_NODE"), getenv("LINA_CLAUDE_ADAPTER"), getenv("LINA_CLAUDE_MODEL")
	if node == "" && program == "" && model == "" {
		return responses, nil
	}
	if responses != nil || !filepath.IsAbs(node) || !filepath.IsAbs(program) || strings.TrimSpace(model) == "" || strings.ContainsAny(model, "\r\n\x00") {
		return nil, errors.New("invalid Claude route")
	}
	adapter := &claudeproc.Adapter{Node: node, Program: program, Token: secret.New(getenv("CLAUDE_CODE_OAUTH_TOKEN"))}
	for _, key := range []string{"PATH", "HOME", "USERPROFILE", "SystemRoot", "TMPDIR", "TMP", "TEMP"} {
		if value := getenv(key); value != "" {
			adapter.Environment = append(adapter.Environment, key+"="+value)
		}
	}
	return &Config{Route: "claude", Adapter: adapter, Model: provider.ModelRef(model), credential: provider.NewCredential("configured-claude", provider.ObservedCLILogin, "")}, nil
}
