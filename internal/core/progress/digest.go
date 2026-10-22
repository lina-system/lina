// SPDX-License-Identifier: Apache-2.0
package progress

import "strings"

// Summary carries at most five display lines. Counts and navigation remain
// separate so they do not consume a work row or reveal execution identities.
type Summary struct {
	Lines       []string
	Total, More int
}

func Digest(waiting []string, entries []Entry) Summary {
	d := Summary{Lines: []string{}, Total: len(waiting) + len(entries)}
	add := func(s string) {
		if len(d.Lines) < 5 {
			d.Lines = append(d.Lines, strings.Join(strings.Fields(s), " "))
		}
	}
	for _, line := range waiting {
		add(line)
	}
	for _, e := range entries {
		v := e.Line
		add(v.Name + " · " + v.Stage + " · " + v.JustFinished + " → " + v.Next)
	}
	d.More = d.Total - len(d.Lines)
	return d
}
