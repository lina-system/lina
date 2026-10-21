// SPDX-License-Identifier: Apache-2.0
package rpc

import (
	"io"
	"strings"
	"testing"
)

func TestLineBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		frames      int
		fail        bool
	}{
		{"two frames", "{}\n{}\n", 2, false},
		{"unterminated", "{}", 0, true},
		{"maximum", strings.Repeat(" ", MaxFrameBytes-3) + "{}\n", 1, false},
		{"too large", strings.Repeat(" ", MaxFrameBytes-2) + "{}\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := NewReader(strings.NewReader(tc.input))
			for i := 0; i < tc.frames; i++ {
				if _, err := reader.Read(); err != nil {
					t.Fatal(err)
				}
			}
			data, err := reader.Read()
			if err == nil {
				t.Fatal("accepted incomplete or oversized frame")
			}
			if !tc.fail && (err != io.EOF || len(data) != 0) {
				t.Fatalf("trailing data: %d, %v", len(data), err)
			}
		})
	}
}
