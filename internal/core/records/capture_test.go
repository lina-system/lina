// SPDX-License-Identifier: Apache-2.0
package records

import "testing"

func TestCaptureKeepsSentenceAndParsesExplicitTokens(t *testing.T) {
	t.Parallel()
	for _, sentence := range []string{"책을 반납한다", `Say "hello" to Sam`, "Call 123-456-7890"} {
		got, err := ParseCapture(sentence)
		if err != nil || got.Title != sentence || got.Todo.When != "" {
			t.Fatal(sentence, got, err)
		}
	}
	got, err := ParseCapture(`Return book #"Library trip" %errands ! 2026-10-25 {2026-10-26}`)
	if err != nil || got.Title != "Return book" || len(got.Projects) != 1 || got.Projects[0] != "Library trip" || got.Todo.When != "2026-10-25" || got.Todo.Deadline != "2026-10-26" || got.Todo.DeadlineSource == "" {
		t.Fatal(got, err)
	}
}
