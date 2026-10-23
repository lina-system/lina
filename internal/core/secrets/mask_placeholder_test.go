// SPDX-License-Identifier: Apache-2.0
package secrets_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

func TestMaskPlaceholderValueSuffix(t *testing.T) {
	for _, suffix := range []string{"livecredential", "0", "_value", "-value", "+value", "/value", "=value", "[value", "{value", "한글"} {
		t.Run(suffix, func(t *testing.T) {
			masked, err := secrets.Mask("token=" + secret.Placeholder + suffix)
			if err != nil {
				t.Fatal(err)
			}
			if want := "token=" + secret.Placeholder; masked.Text() != want {
				t.Fatalf("masked text = %q, want %q", masked.Text(), want)
			}
			wantSpans := []secrets.Span{{Start: len("token="), End: len("token=") + len(secret.Placeholder), Sensitivity: "secret"}}
			if !reflect.DeepEqual(masked.Spans(), wantSpans) || masked.DerivedText() != "token=" {
				t.Fatal("credential was not excluded from derived text")
			}
			assertMaskedRoundTrip(t, masked)
		})
	}
}

func TestMaskPlaceholderBoundaries(t *testing.T) {
	for _, boundary := range []string{"", " ", "\t", "\n", "\r", "\f", ",", ";", `"`, "'", "<", ">", "}", "]", ")", "."} {
		t.Run(boundary, func(t *testing.T) {
			input := "token=" + secret.Placeholder + boundary
			masked, err := secrets.Mask(input)
			if err != nil || masked.Text() != input {
				t.Fatalf("placeholder boundary changed: %q, %v", masked.Text(), err)
			}
			assertMaskedRoundTrip(t, masked)
			if boundary == "" {
				return
			}
			// Consuming the boundary must not hide the next assignment.
			masked, err = secrets.Mask(input + "token=" + secret.Placeholder + "livecredential")
			if err != nil || masked.Text() != input+"token="+secret.Placeholder {
				t.Fatalf("adjacent credential survived: %q, %v", masked.Text(), err)
			}
			assertMaskedRoundTrip(t, masked)
		})
	}
}

func TestMaskedRecordRejectsPlaceholderValueSuffix(t *testing.T) {
	masked, err := secrets.Mask("password=syntheticcredential")
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(masked)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []string{
		`{"text":"token=[REDACTED]livecredential","spans":[]}`,
		`{"text":"token=[REDACTED]livecredential","spans":[{"start":6,"end":16,"sensitivity":"secret"}]}`,
	} {
		if err := json.Unmarshal([]byte(record), &masked); !errors.Is(err, secrets.ErrSpan) {
			t.Errorf("unsafe stored record accepted: %v", err)
		}
		after, err := json.Marshal(masked)
		if err != nil || string(after) != string(before) {
			t.Fatal("invalid input changed the receiver")
		}
	}
}
