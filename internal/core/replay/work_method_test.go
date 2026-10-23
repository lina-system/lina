// SPDX-License-Identifier: Apache-2.0
package replay_test

import (
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/provider"
	"testing"
)

func TestRequestKeepsPinnedWorkMethodWithCandidateVoice(t *testing.T) {
	ep := replay.Episode{WorkMethod: `{"schema":1,"reply":{"candidates":2}}`, Persona: agents.Layers{Core: "core", UserVoice: "before"}}
	ep.Evidence.Assembly.InstructionFormat = "persona-tui-work-method-v1"
	ep.Evidence.Assembly.AgentID = "agent"
	ep.Evidence.Assembly.AgentName = "Fixture"
	ep.Evidence.Assembly.OwnerPersonID = "person"
	got, err := replay.Request(ep, map[string]string{"user-voice.md": "after"}, "", "", func(_ string, p agents.Preparation, _ []provider.ToolDecl) (string, error) {
		if p.WorkMethod != ep.WorkMethod || p.Layers.UserVoice != "after" {
			t.Fatalf("request lost pinned record: %+v", p)
		}
		return p.WorkMethod, nil
	})
	if err != nil || got.Instructions != ep.WorkMethod {
		t.Fatalf("request=%+v error=%v", got, err)
	}
}
