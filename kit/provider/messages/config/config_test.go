// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/provider"
)

func validConfig() Config {
	return Config{
		Capabilities: provider.CapabilitySet{
			provider.PrefixCacheCapability,
			provider.CacheMarksCapability,
			provider.CacheLifetime1hCapability,
			provider.CacheLifetime5mCapability,
			provider.ReasoningStateCapability,
			provider.BatchCapability,
		},
		Adapter:         Adapter,
		RequestFields:   provider.FieldList{"model", "max_tokens", "stream", "system", "messages", "tools", "thinking"},
		MaxOutputTokens: 4096,
	}
}

func TestReadConfig(t *testing.T) {
	t.Parallel()
	want := validConfig()
	valid := `{"providers":{"claude_api_key":{"adapter":"messages","retryCount":0,"maxOutputTokens":4096,"requestFields":["model","max_tokens","stream","system","messages","tools","thinking"],"capabilities":["prefix_cache","cache_marks","cache_lifetime_1h","cache_lifetime_5m","reasoning_state","batch"]}}}`
	tests := []struct {
		name, input, wantErr string
	}{
		{name: "valid manifest", input: valid},
		{name: "trailing data", input: valid + ` {"extra":true}`, wantErr: "invalid manifest suffix"},
		{name: "missing providers.claude_api_key", input: `{"providers":{}}`, wantErr: "unexpected end of JSON input"},
		{name: "invalid JSON", input: `{`, wantErr: "unexpected EOF"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReadConfig(strings.NewReader(test.input))
			if test.wantErr != "" {
				if err == nil || err.Error() != test.wantErr {
					t.Fatalf("ReadConfig() error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Adapter != want.Adapter || got.RetryCount != want.RetryCount || got.MaxOutputTokens != want.MaxOutputTokens || !slices.Equal(got.Capabilities, want.Capabilities) || !slices.Equal(got.RequestFields, want.RequestFields) {
				t.Fatalf("ReadConfig() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestConfigUnmarshalJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, input, wantErr string
	}{
		{
			name:    "missing retryCount",
			input:   `{"adapter":"messages","maxOutputTokens":4096,"requestFields":["model","max_tokens","stream","messages"]}`,
			wantErr: "manifest requires retryCount",
		},
		{
			name:    "non-zero retryCount",
			input:   `{"adapter":"messages","retryCount":1,"maxOutputTokens":4096,"requestFields":["model","max_tokens","stream","messages"]}`,
			wantErr: "unsupported Messages adapter configuration",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var got Config
			err := json.Unmarshal([]byte(test.input), &got)
			if err == nil || err.Error() != test.wantErr {
				t.Fatalf("UnmarshalJSON() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()
	fields := provider.FieldList{"model", "max_tokens", "stream", "messages"}
	if err := (Config{Adapter: Adapter, MaxOutputTokens: 4096, RequestFields: fields}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Config{Adapter: Adapter, MaxOutputTokens: 1 << 20, RequestFields: fields}).Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "wrong adapter",
			cfg:  Config{Adapter: "completions", MaxOutputTokens: 4096, RequestFields: fields},
			want: "unsupported Messages adapter configuration",
		},
		{
			name: "invalid capabilities",
			cfg: Config{
				Adapter:         Adapter,
				Capabilities:    provider.CapabilitySet{"unknown"},
				MaxOutputTokens: 4096,
				RequestFields:   fields,
			},
			want: "invalid Messages capabilities",
		},
		{
			name: "unadmitted capability",
			cfg: Config{
				Adapter:         Adapter,
				Capabilities:    provider.CapabilitySet{provider.ServerStateCapability},
				MaxOutputTokens: 4096,
				RequestFields:   fields,
			},
			want: "unadmitted Messages capability",
		},
		{
			name: "maxOutputTokens zero",
			cfg:  Config{Adapter: Adapter, RequestFields: fields},
			want: "invalid Messages output limit",
		},
		{
			name: "maxOutputTokens above 1<<20",
			cfg:  Config{Adapter: Adapter, MaxOutputTokens: 1<<20 + 1, RequestFields: fields},
			want: "invalid Messages output limit",
		},
		{
			name: "unsupported request field",
			cfg: Config{
				Adapter:         Adapter,
				MaxOutputTokens: 4096,
				RequestFields:   provider.FieldList{"model", "max_tokens", "stream", "messages", "temperature"},
			},
			want: "unsupported Messages request field",
		},
		{
			name: "duplicate request field",
			cfg: Config{
				Adapter:         Adapter,
				MaxOutputTokens: 4096,
				RequestFields:   provider.FieldList{"model", "max_tokens", "stream", "messages", "model"},
			},
			want: "duplicate Messages request field",
		},
		{
			name: "missing model",
			cfg:  Config{Adapter: Adapter, MaxOutputTokens: 4096, RequestFields: provider.FieldList{"max_tokens", "stream", "messages"}},
			want: "missing required Messages request field",
		},
		{
			name: "missing max_tokens",
			cfg:  Config{Adapter: Adapter, MaxOutputTokens: 4096, RequestFields: provider.FieldList{"model", "stream", "messages"}},
			want: "missing required Messages request field",
		},
		{
			name: "missing stream",
			cfg:  Config{Adapter: Adapter, MaxOutputTokens: 4096, RequestFields: provider.FieldList{"model", "max_tokens", "messages"}},
			want: "missing required Messages request field",
		},
		{
			name: "missing messages",
			cfg:  Config{Adapter: Adapter, MaxOutputTokens: 4096, RequestFields: provider.FieldList{"model", "max_tokens", "stream"}},
			want: "missing required Messages request field",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.cfg.Validate()
			if err == nil || err.Error() != test.want {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}
