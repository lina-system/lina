// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/authority"
)

func TestApprovalButtonsCarryOnlyIDAndChoice(t *testing.T) {
	markup, err := approvalButtons("request_123")
	if err != nil {
		t.Fatal(err)
	}
	if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 2 {
		t.Fatal("expected once and deny only")
	}
	for i, want := range []authority.Choice{authority.Once, authority.Deny} {
		button := markup.InlineKeyboard[0][i]
		id, choice, ok := parseButton(button.CallbackData)
		if !ok || id != "request_123" || choice != want || len(button.CallbackData) > 64 {
			t.Fatalf("button %d invalid", i)
		}
	}
	for _, invalid := range []string{"", "id:a", "id:always", "id:o:owner", " id:o", "id\n:o", ":o", strings.Repeat("x", 63) + ":o"} {
		if _, _, ok := parseButton(invalid); ok {
			t.Errorf("accepted malformed button %q", invalid)
		}
	}
	if _, err := approvalButtons(strings.Repeat("x", 63)); err == nil {
		t.Fatal("oversized callback accepted")
	}
}
