// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"testing"
)

func TestUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), nil, &stdout, &stderr); code != 64 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatal(code, stdout.String(), stderr.String())
	}
}
