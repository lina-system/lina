// SPDX-License-Identifier: Apache-2.0
package result_test

import (
	"github.com/thisisjun786/lina/kit/result"
	"testing"
)

func TestReplyAnnotationsRetainCompleteDraft(t *testing.T) {
	raw := []byte(`{"blocks":[{"kind":"paragraph","text":"Please sign.","audience":"recipient"},{"kind":"paragraph","text":"Private note.","audience":"person"}],"from_summary":null,"recipient":"Client"}`)
	body, err := result.Validate(result.DraftType, raw, result.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.PlainText(body, nil); got != "Please sign.\nPrivate note." {
		t.Fatalf("annotation rewrote body: %q", got)
	}
}
