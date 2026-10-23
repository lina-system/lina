// SPDX-License-Identifier: Apache-2.0
package mocks

import (
	"github.com/thisisjun786/lina/kit/result"
	"testing"
)

func TestPageRejectsUnauthorizedEffectsAndStyles(t *testing.T) {
	list := []result.MockItem{{ID: "title", Description: "Tasks", Action: "none"}, {ID: "filter", Description: "Today", Action: "toggle", Target: "tasks"}}
	valid := `<main class="stack"><h1 data-check="title">Tasks</h1><button data-check="filter" data-action="toggle" data-target="tasks">Today</button><section id="tasks">One task</section></main>`
	if _, checks := CheckFragment(valid, list); !checks["elements"] || !checks["controls"] || !checks["tokens"] || !checks["network"] {
		t.Fatalf("valid page failed: %+v", checks)
	}
	for _, tc := range []struct{ html, dimension string }{
		{`<h1 data-check="title">Other title</h1>`, "elements"},
		{valid + `<button>Unasked</button>`, "controls"},
		{valid + `<script>fetch('/core')</script>`, "network"},
		{valid + `<img src="https://example.com/image.png"/>`, "network"},
		{valid + `<div style="color:red">Text</div>`, "tokens"},
		{valid + `<svg onload="fetch('/')"></svg>`, "network"},
		{valid + `<a href="javascript:alert(1)">Go</a>`, "network"},
	} {
		t.Run(tc.dimension+tc.html[:4], func(t *testing.T) {
			safe, checks := CheckFragment(tc.html, list)
			if checks[tc.dimension] || safe != "" {
				t.Fatalf("accepted unsafe fragment: %+v", checks)
			}
		})
	}
}
