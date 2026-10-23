// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/provider"
)

func TestReadConfig(t *testing.T) {
	t.Parallel()
	valid := `{"providers":{"chatgpt":{"adapter":"responses","retryCount":0,"capabilities":["reasoning_state"],"requestFields":["model","input","store","stream","instructions","tools","reasoning","include"]}}}`
	want := Config{
		Capabilities:  provider.CapabilitySet{provider.ReasoningStateCapability},
		Adapter:       Adapter,
		RequestFields: provider.FieldList{"model", "input", "store", "stream", "instructions", "tools", "reasoning", "include"},
	}
	tests := []struct {
		name, input, wantErr string
	}{
		{name: "valid manifest", input: valid},
		{name: "trailing data", input: valid + ` {"extra":true}`, wantErr: "invalid manifest suffix"},
		{name: "missing providers.chatgpt", input: `{"providers":{}}`, wantErr: "unexpected end of JSON input"},
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
			if got.Adapter != want.Adapter || got.RetryCount != want.RetryCount || !slices.Equal(got.Capabilities, want.Capabilities) || !slices.Equal(got.RequestFields, want.RequestFields) {
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
			input:   `{"adapter":"responses","requestFields":["model","input","store","stream"]}`,
			wantErr: "manifest requires retryCount",
		},
		{
			name:    "non-zero retryCount",
			input:   `{"adapter":"responses","retryCount":1,"requestFields":["model","input","store","stream"]}`,
			wantErr: "unsupported Responses adapter configuration",
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
	fields := provider.FieldList{"model", "input", "store", "stream"}
	if err := (Config{Adapter: Adapter, RequestFields: fields}).Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "wrong adapter",
			cfg:  Config{Adapter: "completions", RequestFields: fields},
			want: "unsupported Responses adapter configuration",
		},
		{
			name: "unsupported request field",
			cfg: Config{
				Adapter:       Adapter,
				RequestFields: provider.FieldList{"model", "input", "store", "stream", "temperature"},
			},
			want: "unsupported Responses request field",
		},
		{
			name: "duplicate request field",
			cfg: Config{
				Adapter:       Adapter,
				RequestFields: provider.FieldList{"model", "input", "store", "stream", "model"},
			},
			want: "duplicate Responses request field",
		},
		{
			name: "missing model",
			cfg:  Config{Adapter: Adapter, RequestFields: provider.FieldList{"input", "store", "stream"}},
			want: "missing required Responses request field",
		},
		{
			name: "missing input",
			cfg:  Config{Adapter: Adapter, RequestFields: provider.FieldList{"model", "store", "stream"}},
			want: "missing required Responses request field",
		},
		{
			name: "missing store",
			cfg:  Config{Adapter: Adapter, RequestFields: provider.FieldList{"model", "input", "stream"}},
			want: "missing required Responses request field",
		},
		{
			name: "missing stream",
			cfg:  Config{Adapter: Adapter, RequestFields: provider.FieldList{"model", "input", "store"}},
			want: "missing required Responses request field",
		},
		{
			name: "unadmitted capability",
			cfg: Config{
				Adapter:       Adapter,
				Capabilities:  provider.CapabilitySet{provider.ServerStateCapability},
				RequestFields: fields,
			},
			want: "unadmitted Responses capability",
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
