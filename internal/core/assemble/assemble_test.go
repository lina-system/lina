// SPDX-License-Identifier: Apache-2.0
package assemble_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/kit/provider"
)

func input() assemble.Input {
	return assemble.Input{
		Core: "core rules", AgentID: "agent-a", PersonaRevision: "revision-1",
		Persona:     agents.Layers{Core: "persona", DefaultVoice: "default", UserVoice: "voice", GrowthSnapshot: "growth"},
		Catalog:     []assemble.Skill{{ID: "skill", Description: "catalog"}},
		AlwaysTools: []provider.ToolDecl{json.RawMessage(`{ "type":"function","name":"always" }`)},
		LoadedTools: []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"loaded"}`)},
		Summary:     json.RawMessage(`{"role":"developer","content":"summary"}`),
		History:     []provider.Item{json.RawMessage(`{ "type":"reasoning", "encrypted_content":"opaque" }`), json.RawMessage(`{"role":"assistant","content":"earlier"}`)},
		Packet: assemble.ContextPacket{
			{ID: "external", Kind: assemble.External, Text: "outside", Origin: "source", Rank: 1},
			{ID: "material", Kind: assemble.Material, Text: "passage", Revision: "v2"},
			{ID: "memory", Kind: assemble.Memory, Text: "fact", Evidence: []assemble.Ref{{ID: "event-1"}}},
			{ID: "skill", Kind: assemble.SkillBody, Text: "skill body", Revision: "v1"},
			{ID: "waiting", Kind: assemble.Waiting, Text: "pending"},
			{ID: "commitments", Kind: assemble.Commitments, Text: "reply budget"},
			{ID: "clock", Kind: assemble.Clock, Text: "clock and emotion"},
		},
		Current: json.RawMessage(`{ "role":"user", "content":"current" }`),
		Model:   "model", Effort: "medium", Fields: provider.FieldList{"model", "input"}, MaxTokens: 100,
	}
}
func count(r provider.Request) int { return len(r.Items) + len(r.Tools) }
func TestLayoutAndDeterministicBytes(t *testing.T) {
	in := input()
	before, _ := json.Marshal(in)
	r, report := assemble.Assemble(in, count)
	if report.Failure != "" {
		t.Fatal(report)
	}
	for i := 0; i < 20; i++ {
		again, other := assemble.Assemble(in, count)
		if !reflect.DeepEqual(r, again) || !reflect.DeepEqual(report, other) {
			t.Fatal("nondeterministic assembly")
		}
	}
	var texts []string
	for _, raw := range r.Items {
		var item struct{ Role, Content, Type string }
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
		texts = append(texts, item.Content)
	}
	want := []string{"core rules", "persona", "default", "voice", "growth", "catalog", "summary", "", "earlier", "current", "clock and emotion", "reply budget", "pending", "skill body", "fact", "passage", "outside"}
	if len(texts) != len(want) {
		t.Fatalf("layout: %q", texts)
	}
	for i, text := range want {
		if !strings.Contains(texts[i], text) {
			t.Fatalf("item %d = %q, want %q", i, texts[i], text)
		}
	}
	if !bytes.Equal(r.Items[7], in.History[0]) || !bytes.Equal(r.Items[9], in.Current) {
		t.Fatal("opaque or user bytes changed")
	}
	if !reflect.DeepEqual(r.Tools, append(append([]provider.ToolDecl{}, in.AlwaysTools...), in.LoadedTools...)) {
		t.Fatal("tool order changed")
	}
	for i, raw := range r.Items {
		if i == 7 || i == 8 {
			continue
		}
		var item struct{ Role string }
		json.Unmarshal(raw, &item)
		wantRole := "developer"
		if i == 9 || i >= 14 && i <= 16 {
			wantRole = "user"
		}
		if item.Role != wantRole {
			t.Fatalf("item %d role %q", i, item.Role)
		}
	}
	if !strings.Contains(texts[16], "untrusted") || !strings.Contains(texts[15], "v2") || !strings.Contains(texts[14], "event-1") {
		t.Fatal("missing provenance")
	}
	encoded, _ := json.Marshal(report)
	for _, body := range []string{"core rules", "clock and emotion", "passage", "outside"} {
		if bytes.Contains(encoded, []byte(body)) {
			t.Fatal("report contains content")
		}
	}
	if report.Hash != assemble.Digest(r) || len(report.Hash) != 64 {
		t.Fatal("bad request digest")
	}
	after, _ := json.Marshal(in)
	if !bytes.Equal(before, after) {
		t.Fatal("mutated input")
	}
	r.Items[7][0] = '!'
	r.Tools[0][0] = '!'
	after, _ = json.Marshal(in)
	if !bytes.Equal(before, after) {
		t.Fatal("output aliases input")
	}
}

func TestPacketBudgetDropsOptionalByGroupThenRank(t *testing.T) {
	in := input()
	in.Packet = append(in.Packet,
		assemble.ContextItem{ID: "profile", Kind: assemble.Profile, Text: "profile", Rank: -100},
		assemble.ContextItem{ID: "brief", Kind: assemble.PersonBrief, Text: "brief", Rank: -200},
		assemble.ContextItem{ID: "memory-low", Kind: assemble.Memory, Text: "low", Rank: -1})
	full, _ := assemble.Assemble(in, count)
	want := []string{"external", "material", "memory-low", "memory", "brief", "profile"}
	for n := 1; n <= len(want); n++ {
		in.MaxTokens = count(full) - n
		_, report := assemble.Assemble(in, count)
		var dropped []string
		for _, d := range report.Dropped {
			dropped = append(dropped, d.ID)
		}
		if !reflect.DeepEqual(dropped, want[:n]) || report.Failure != "" {
			t.Fatalf("n=%d: %+v", n, report)
		}
	}
	in.MaxTokens--
	r, report := assemble.Assemble(in, count)
	if report.Failure != assemble.TooLarge || len(r.Items) != 0 {
		t.Fatal("mandatory packet was dropped or sent oversized")
	}
}

func TestScopeRetractionDuplicatesAndSupersession(t *testing.T) {
	in := input()
	in.Packet = append(in.Packet,
		assemble.ContextItem{ID: "foreign", Kind: assemble.Memory, AgentID: "agent-b", Text: "foreign secret"},
		assemble.ContextItem{ID: "removed", Kind: assemble.Memory, Retracted: true, Text: "forgotten"},
		assemble.ContextItem{ID: "sensitive", Kind: assemble.Memory, Secret: true, Text: "sensitive"},
		assemble.ContextItem{ID: "new", Kind: assemble.Memory, Text: "replacement", Supersedes: []string{"memory"}},
		assemble.ContextItem{ID: "new", Kind: assemble.Memory, Text: "replacement", Supersedes: []string{"memory"}})
	r, report := assemble.Assemble(in, count)
	if report.Failure != "" {
		t.Fatal(report)
	}
	b, _ := json.Marshal(r)
	for _, forbidden := range []string{"foreign secret", "forgotten", "sensitive", "fact"} {
		if bytes.Contains(b, []byte(forbidden)) {
			t.Fatalf("included %s", forbidden)
		}
	}
	if bytes.Count(b, []byte("replacement")) != 1 {
		t.Fatal("duplicate context")
	}
}

func TestInvalidAssemblyFailsClosed(t *testing.T) {
	for _, change := range []func(*assemble.Input){
		func(in *assemble.Input) { in.Current = json.RawMessage(`bad`) },
		func(in *assemble.Input) { in.Packet[0].Origin = "" },
		func(in *assemble.Input) { in.Packet[0].Kind = 99 },
		func(in *assemble.Input) { in.Packet[1].Revision = "" },
		func(in *assemble.Input) { in.Packet[3].Revision = "" },
		func(in *assemble.Input) { in.AgentID = "" },
		func(in *assemble.Input) { in.Packet[0].Supersedes = []string{"clock"} },
		func(in *assemble.Input) { in.Packet[0].ID = "clock" },
	} {
		in := input()
		change(&in)
		r, report := assemble.Assemble(in, count)
		if report.Failure == "" || len(r.Items) != 0 {
			t.Fatal("invalid assembly accepted")
		}
	}
}

func TestDigestCoversRequestAndOpaqueBytes(t *testing.T) {
	r, _ := assemble.Assemble(input(), count)
	hash := assemble.Digest(r)
	for _, change := range []func(*provider.Request){
		func(r *provider.Request) { r.Model = "other" },
		func(r *provider.Request) { r.Effort = "high" },
		func(r *provider.Request) { r.Instructions = "changed" },
		func(r *provider.Request) { r.Purpose = provider.Background },
		func(r *provider.Request) { r.Fields = append(r.Fields, "other") },
		func(r *provider.Request) {
			r.Items[7] = json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)
		},
		func(r *provider.Request) { r.Tools = r.Tools[1:] },
	} {
		r, _ := assemble.Assemble(input(), count)
		change(&r)
		if assemble.Digest(r) == hash {
			t.Fatal("request change not hashed")
		}
	}
}

func TestStandingProfileStaysInPacketAndCountsAgainstBudget(t *testing.T) {
	in := input()
	in.Profile = []provider.Item{json.RawMessage(`{"role":"user","content":"Remembered statement (preference): brief replies"}`)}
	r, report := assemble.Assemble(in, count)
	if report.Failure != "" {
		t.Fatal(report)
	}
	if !bytes.Equal(r.Items[14], in.Profile[0]) || !bytes.Equal(r.Items[9], in.Current) {
		t.Fatalf("profile or input moved: %s", r.Items)
	}
	r.Items[14][0] = '!'
	if !json.Valid(in.Profile[0]) {
		t.Fatal("profile aliases caller snapshot")
	}
	in.Packet = nil
	in.MaxTokens = 12 // Fixed/history/current and tools fit exactly; the profile exceeds the limit.
	if _, report = assemble.Assemble(in, count); report.Failure != assemble.TooLarge {
		t.Fatalf("profile/input budget ignored: %+v", report)
	}
	profile := in.Profile
	in.Profile = nil
	if _, report = assemble.Assemble(in, count); report.Failure != "" {
		t.Fatalf("fixture without profile should fit: %+v", report)
	}
	in.Profile = profile
	in.Profile[0] = json.RawMessage(`invalid`)
	if _, report = assemble.Assemble(in, count); report.Failure != assemble.Invalid {
		t.Fatalf("invalid profile accepted: %+v", report)
	}
}
