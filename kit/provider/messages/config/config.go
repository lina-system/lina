// SPDX-License-Identifier: Apache-2.0
// Package config reads Messages build settings independently of the HTTP/SSE transport.
package config

import (
	"encoding/json"
	"errors"
	"io"
	"slices"

	"github.com/thisisjun786/lina/kit/provider"
)

const Adapter = "messages"

// Config is the Messages adapter's build settings. The request field list and
// retry policy come from the release manifest, never from a runtime setting.
type Config struct {
	Capabilities    provider.CapabilitySet `json:"capabilities,omitempty"`
	Adapter         string                 `json:"adapter"`
	RequestFields   provider.FieldList     `json:"requestFields"`
	MaxOutputTokens int64                  `json:"maxOutputTokens"`
	RetryCount      int                    `json:"retryCount"`
}

// ReadConfig reads the Claude API-key portion of the build manifest. Missing
// settings fail closed: a caller cannot enable implicit defaults or retries.
func ReadConfig(r io.Reader) (Config, error) {
	var m struct {
		Providers map[string]json.RawMessage `json:"providers"`
	}
	d := json.NewDecoder(r)
	if err := d.Decode(&m); err != nil {
		return Config{}, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return Config{}, errors.New("invalid manifest suffix")
	}
	var c Config
	if err := json.Unmarshal(m.Providers["claude_api_key"], &c); err != nil {
		return Config{}, err
	}
	return c, c.Validate()
}

func (c *Config) UnmarshalJSON(b []byte) error {
	type plain Config
	var v struct {
		plain
		RetryCount *int `json:"retryCount"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	if v.RetryCount == nil {
		return errors.New("manifest requires retryCount")
	}
	*c = Config(v.plain)
	c.RetryCount = *v.RetryCount
	return c.Validate()
}

func (c Config) Validate() error {
	if !c.Capabilities.Valid() {
		return errors.New("invalid Messages capabilities")
	}
	for _, capability := range c.Capabilities {
		if !provider.Admitted("claude_api_key").Has(capability) {
			return errors.New("unadmitted Messages capability")
		}
	}
	if c.Adapter != Adapter || c.RetryCount != 0 {
		return errors.New("unsupported Messages adapter configuration")
	}
	if c.MaxOutputTokens <= 0 || c.MaxOutputTokens > 1<<20 {
		return errors.New("invalid Messages output limit")
	}
	seen := map[string]bool{}
	for _, field := range c.RequestFields {
		switch field {
		case "model", "max_tokens", "stream", "system", "messages", "tools", "thinking", "metadata":
		default:
			return errors.New("unsupported Messages request field")
		}
		if seen[field] {
			return errors.New("duplicate Messages request field")
		}
		seen[field] = true
	}
	for _, field := range []string{"model", "max_tokens", "stream", "messages"} {
		if !slices.Contains(c.RequestFields, field) {
			return errors.New("missing required Messages request field")
		}
	}
	return nil
}
