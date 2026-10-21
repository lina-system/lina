// SPDX-License-Identifier: Apache-2.0
package secrets_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

func TestMaskPunctuationRoundTrip(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"json", `{"api_key":"syntheticcredential"}`, `{"api_key":"` + secret.Placeholder + `"}`},
		{"json_fields", `{"api_key":"syntheticcredential","token":"anothercredential"}`, `{"api_key":"` + secret.Placeholder + `","token":"` + secret.Placeholder + `"}`},
		{"quoted_placeholder_prefix", `token="[REDACTED]syntheticcredential".`, "token=" + secret.Placeholder + "."},
	}
	for _, punctuation := range []string{".", ",", "}", ")", ";"} {
		for _, quote := range []string{`"`, `'`} {
			cases = append(cases, struct{ name, input, want string }{
				quote + punctuation,
				"token=" + quote + "syntheticcredential" + quote + punctuation,
				"token=" + secret.Placeholder + punctuation,
			})
		}
		// Unquoted real values retain the existing masking boundaries.
		want := "token=" + secret.Placeholder
		if punctuation == "," || punctuation == ";" {
			want += punctuation
		}
		cases = append(cases, struct{ name, input, want string }{
			"unquoted" + punctuation, "token=syntheticcredential" + punctuation, want,
		})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			masked, err := secrets.Mask(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if masked.Text() != tc.want {
				t.Fatalf("masked text = %q, want %q", masked.Text(), tc.want)
			}
			assertMaskedRoundTrip(t, masked)
		})
	}
}

func assertMaskedRoundTrip(t *testing.T, masked secrets.Masked) {
	t.Helper()
	again, err := secrets.Mask(masked.Text())
	if err != nil {
		t.Fatal(err)
	}
	if again.Text() != masked.Text() {
		t.Errorf("mask is not idempotent: %q became %q", masked.Text(), again.Text())
	}
	encoded, err := json.Marshal(masked)
	if err != nil {
		t.Fatal(err)
	}
	var restored secrets.Masked
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatalf("masked record cannot round-trip: %v", err)
	}
	if restored.Text() != masked.Text() || !reflect.DeepEqual(restored.Spans(), masked.Spans()) || restored.DerivedText() != masked.DerivedText() {
		t.Fatal("round-trip changed the masked text, spans or derived text")
	}
}
