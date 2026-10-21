// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"errors"
	"testing"
)

func TestPatchRejectsStaleContextBeforeAnyChange(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: a\n@@\n-old\n+new\n*** Update File: b\n@@\n-stale\n+replacement\n*** End Patch\n"
	files := map[string][]byte{"a": []byte("old\n"), "b": []byte("current\n")}
	changes, err := PlanPatch(patch, files)
	if !errors.Is(err, ErrPatchContext) || len(changes) != 0 {
		t.Fatalf("stale patch: changes=%+v error=%v", changes, err)
	}
	if string(files["a"]) != "old\n" || string(files["b"]) != "current\n" {
		t.Fatal("planning modified input")
	}
}

func TestPatchGrammar(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: added\n+one\n+two\n*** Update File: old\n*** Move to: renamed\n@@ section\n keep\n-before\n+after\n@@\n-tail\n+end\n*** End of File\n*** Delete File: removed\n*** End Patch\n"
	files := map[string][]byte{"old": []byte("section\nkeep\nbefore\nmiddle\ntail\n"), "removed": {0, 1, 255}}
	changes, err := PlanPatch(patch, files)
	if err != nil || len(changes) != 4 {
		t.Fatalf("plan: %+v %v", changes, err)
	}
	want := map[string]string{"added": "one\ntwo\n", "renamed": "section\nkeep\nafter\nmiddle\nend\n"}
	for _, c := range changes {
		if text, ok := want[c.Path]; ok {
			if c.Delete || string(c.Content) != text {
				t.Fatalf("change: %+v", c)
			}
		} else if (c.Path != "old" && c.Path != "removed") || !c.Delete {
			t.Fatalf("unexpected change: %+v", c)
		}
	}
}

func TestPatchExactContextAndEOF(t *testing.T) {
	for _, current := range []string{"a \nb\n", "a\nb\nextra\n", "A\nb\n"} {
		patch := "*** Begin Patch\n*** Update File: file\n@@\n a\n-b\n+c\n*** End of File\n*** End Patch"
		if _, err := PlanPatch(patch, map[string][]byte{"file": []byte(current)}); !errors.Is(err, ErrPatchContext) {
			t.Fatalf("accepted stale context %q: %v", current, err)
		}
	}
	changes, err := PlanPatch("*** Begin Patch\n*** Update File: file\n@@\n-old\n+new\n*** End Patch", map[string][]byte{"file": []byte("old")})
	if err != nil || string(changes[0].Content) != "new\n" {
		t.Fatalf("unterminated input: %+v %v", changes, err)
	}
}

func TestPatchRefusesUnsafeOrConflictingPathsAndMalformedGrammar(t *testing.T) {
	for _, patch := range []string{
		"*** Begin Patch\n*** Add File: ../escape\n+x\n*** End Patch",
		"*** Begin Patch\n*** Add File: /absolute\n+x\n*** End Patch",
		"*** Begin Patch\n*** Add File: .git/config\n+x\n*** End Patch",
		"*** Begin Patch\n*** Add File: dir/.git/config\n+x\n*** End Patch",
		"*** Begin Patch\n*** Add File: file\n+x\n*** End Patch",
		"*** Begin Patch\n*** Delete File: absent\n*** End Patch",
		"*** Begin Patch\n*** Update File: file\n*** Move to: occupied\n@@\n-old\n+new\n*** End Patch",
		"*** Begin Patch\n*** Delete File: file\n*** Add File: file\n+x\n*** End Patch",
		"*** Begin Patch\n*** Update File: file\n@@\ninvalid\n*** End Patch",
		"*** Begin Patch\n*** Update File: file\n@@\n-old\n+new",
		"*** Begin Patch\n*** End Patch\nignored",
	} {
		if changes, err := PlanPatch(patch, map[string][]byte{"file": []byte("old\n"), "occupied": []byte("keep\n")}); err == nil || len(changes) != 0 {
			t.Fatalf("accepted %q: %+v %v", patch, changes, err)
		}
	}
}
