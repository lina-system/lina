// SPDX-License-Identifier: Apache-2.0
package design

import "testing"

func TestConversationLanguage(t *testing.T) {
	for _, tc := range []struct {
		text           string
		previous, want Language
	}{
		{"한국어로 PATH를 설명해 줘", English, Korean},
		{"Explain the result", Korean, English},
		{"👍", Korean, Korean},
		{"42", English, English},
	} {
		if got := ConversationLanguage(tc.text, tc.previous); got != tc.want {
			t.Errorf("%q: %s, want %s", tc.text, got, tc.want)
		}
	}
}
