// SPDX-License-Identifier: Apache-2.0
// Package config reads Responses build settings independently of the HTTP/SSE transport.
package config

import (
	"encoding/json"
	"errors"
	"io"
	"slices"

	"github.com/thisisjun786/lina/kit/provider"
)

const Adapter = "responses"

type Config struct {
	Capabilities  provider.CapabilitySet `json:"capabilities,omitempty"`
	Adapter       string                 `json:"adapter"`
	RetryCount    int                    `json:"retryCount"`
	RequestFields provider.FieldList     `json:"requestFields"`
}

// ReadConfig reads the ChatGPT portion of the build manifest. Missing settings
// fail closed; a caller cannot accidentally enable implicit defaults or retries.
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
	if err := json.Unmarshal(m.Providers["chatgpt"], &c); err != nil {
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
		return errors.New("invalid Responses capabilities")
	}
	for _, capability := range c.Capabilities {
		if !provider.Admitted("chatgpt").Has(capability) {
			return errors.New("unadmitted Responses capability")
		}
	}
	if c.Adapter != Adapter || c.RetryCount != 0 {
		return errors.New("unsupported Responses adapter configuration")
	}
	seen := map[string]bool{}
	for _, field := range c.RequestFields {
		switch field {
		case "model", "input", "store", "stream", "instructions", "tools", "reasoning", "include", "parallel_tool_calls", "service_tier":
		default:
			return errors.New("unsupported Responses request field")
		}
		if seen[field] {
			return errors.New("duplicate Responses request field")
		}
		seen[field] = true
	}
	for _, field := range []string{"model", "input", "store", "stream"} {
		if !slices.Contains(c.RequestFields, field) {
			return errors.New("missing required Responses request field")
		}
	}
	return nil
}
