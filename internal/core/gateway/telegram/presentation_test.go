// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"strings"
	"testing"
)

func TestShapeKeepsParagraphsAndUsesLongResultLink(t *testing.T) {
	paragraph := strings.Repeat("a", 3000)
	text := paragraph + "\n\n" + paragraph + "\n\n" + paragraph
	parts, err := Shape(text, "", "")
	if err != nil || len(parts) != 3 || strings.Join(parts, "") != text {
		t.Fatalf("parts=%d err=%v", len(parts), err)
	}
	for _, p := range parts {
		if !textFits(p, 4096) {
			t.Fatal("oversized part")
		}
	}
	parts, err = Shape(text+"\n\n"+paragraph, "Summary", "https://example.invalid/result")
	if err != nil || len(parts) != 1 || parts[0] != "Summary\nhttps://example.invalid/result" {
		t.Fatalf("long result=%v err=%v", parts, err)
	}
	if _, err := Shape(strings.Repeat("x", 4*4096), "", ""); err == nil {
		t.Fatal("long reply lost content without fallback")
	}
}
