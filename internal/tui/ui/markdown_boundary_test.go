// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"testing"

	"github.com/thisisjun786/lina/internal/tui/render"
)

func TestMarkdownEmptyCodeRowsRetainIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, source, text string
		rows               int
	}{
		{"boundary", "```\n\ncode\n\n```", "\ncode\n", 3},
		{"empty-only", "```\n\n\n```", "\n", 2},
		{"empty-fence", "```\n```", "", 1},
		{"indented", "    code\n\n    next", "code\n\nnext", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := render.NewStream("code", render.Config{Width: 40}, nil)
			stream.Seal(1, tc.source)
			rows := stream.Markdown(render.Config{Width: 40}, ">", parseMarkdown)
			if len(rows) != tc.rows || styledText(rows) != tc.text {
				t.Fatalf("literal code rows = %q (%d), want %q (%d)", styledText(rows), len(rows), tc.text, tc.rows)
			}
			for i, row := range rows {
				code := false
				for _, span := range row.Spans {
					code = code || span.Style.Code
				}
				if !code {
					t.Errorf("code row %d lost semantic identity: %+v", i, row)
				}
			}
		})
	}
}
