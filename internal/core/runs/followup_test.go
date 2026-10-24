// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestFollowUpPartialMarksTheQuestionOpen(t *testing.T) {
	t.Parallel()
	var phase int
	s, _, store := setup(t, func(context.Context, provider.Request) (provider.Stream, error) {
		phase++
		if phase == 1 {
			return answer(runs.Artifact{Body: "Notes so far.", Done: false, Missing: []string{"the fee"}}), nil
		}
		return answer(runs.Artifact{Body: "Notes so far.", Done: false, Missing: []string{"the fee"}, OpenReason: "The sources do not state it."}), nil
	})
	id := start(t, s)
	release(t, s, id)
	first := wait(t, s, id)
	if first.Outcome != runs.Partial {
		t.Fatalf("first landing: %+v", first)
	}
	follow, ok := runs.FollowUpBrief(first)
	if !ok || follow.Scope != "the fee" || follow.FollowOf != first.ID || len(follow.Refs) != 1 || follow.Refs[0].ID != first.Material.ID || follow.NotTouch != first.Brief.NotTouch {
		t.Fatalf("follow-up brief: ok=%v %+v", ok, follow)
	}
	second, _, err := s.Start(t.Context(), origin(), follow)
	if err != nil {
		t.Fatal(err)
	}
	release(t, s, second)
	landed := wait(t, s, second)
	if landed.Outcome != runs.Partial || len(landed.Artifact.Open) != 1 || landed.Artifact.Open[0].Question != "the fee" || landed.Artifact.Open[0].Reason != "The sources do not state it." {
		t.Fatalf("question was not marked open: %+v", landed.Artifact)
	}
	if !strings.Contains(landed.Artifact.Body, "the fee") || !strings.Contains(landed.Artifact.Body, "The sources do not state it.") {
		t.Fatalf("result document omitted the open question: %q", landed.Artifact.Body)
	}
	data, err := store.Read(t.Context(), landed.Material.ID, landed.Material.Revision)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct{ Artifact runs.Artifact }
	if err = json.Unmarshal(data, &saved); err != nil || len(saved.Artifact.Open) != 1 {
		t.Fatalf("saved document: %s %v", data, err)
	}
}
