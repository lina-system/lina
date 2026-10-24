// SPDX-License-Identifier: Apache-2.0
package results

import "testing"

func TestRejectPersonLineAcceptsPlainSpeech(t *testing.T) {
	if err := RejectPersonLine("Starting: prepare the trip. Done means the calendar is ready.", nil); err != nil {
		t.Fatal(err)
	}
}

func TestRejectPersonLineBlocksTechnicalTokens(t *testing.T) {
	for _, text := range []string{
		"See issue #500",
		"See issue 500",
		"Open PR 12",
		"Open pull request 12",
		"Read docs/design/work.md",
		"Write result.txt",
		"Use branch: dev",
		"Use id: abc",
		"Run merge.read",
		"Head abcdef0",
		"Ask about LN-500",
	} {
		if err := RejectPersonLine(text, nil); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	if err := RejectPersonLine("Run the acceptance check", []string{"acceptance"}); err == nil {
		t.Fatal("accepted a check name")
	}
}
