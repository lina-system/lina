// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCompareMurmurCommandRejectsIncompleteScope(t *testing.T) {
	for _, args := range [][]string{nil, {"--person", "PRIVATE VALUE"}, {"--unknown=PRIVATE VALUE"}, {"--state-root", "PRIVATE VALUE", "--inbox", "PRIVATE VALUE", "--person", "p", "--account", "a", "--since", "2026-10-08T00:00:00Z", "--until", "2026-10-01T00:00:00Z"}} {
		var out, diagnostic bytes.Buffer
		code := compareMurmurCommand(context.Background(), args, &out, &diagnostic)
		if code != 64 || out.Len() != 0 || strings.Contains(diagnostic.String(), "PRIVATE") {
			t.Fatalf("code=%d stdout=%s stderr=%s", code, &out, &diagnostic)
		}
	}
}
