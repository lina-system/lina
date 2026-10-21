// SPDX-License-Identifier: Apache-2.0
package materials_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/materials"
)

type source string

func (s source) OpenRead(context.Context, string, int64) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(s))), nil
}
func TestCitationsVerifyRevisionBytesAndSpan(t *testing.T) {
	body := source("a cited passage")
	anchor := materials.Anchor{AssetID: "asset", Revision: 1, Start: 2, End: 7, Hash: fmt.Sprintf("%x", sha256.Sum256([]byte(body)))}
	for _, tc := range []struct {
		name     string
		change   func(*materials.Anchor)
		resolved bool
	}{
		{"valid", func(*materials.Anchor) {}, true},
		{"wrong hash", func(a *materials.Anchor) { a.Hash = strings.Repeat("0", 64) }, false},
		{"outside passage", func(a *materials.Anchor) { a.End = 100 }, false},
		{"empty span", func(a *materials.Anchor) { a.End = a.Start }, false},
		{"missing revision", func(a *materials.Anchor) { a.Revision = 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := anchor
			tc.change(&a)
			got, err := materials.Check(t.Context(), body, []materials.Anchor{a})
			if err != nil || len(got) != 1 || got[0].Resolved != tc.resolved {
				t.Fatalf("checks=%+v error=%v", got, err)
			}
		})
	}
}
