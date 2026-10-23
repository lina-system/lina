// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestDefinitionRefusedBeforeAdmission(t *testing.T) {
	for _, name := range []string{"done", "write_area", "blank_write_area", "missing_file", "missing_section", "empty_section", "working_tree_only", "fenced_example", "indented_example", "missing_base"} {
		t.Run(name, func(t *testing.T) {
			d := definition(t)
			switch name {
			case "done":
				d.Done = " \n"
			case "write_area":
				d.WriteArea = nil
			case "blank_write_area":
				d.WriteArea = []string{"  "}
			case "missing_file":
				d.DesignRefs = []string{"absent.md#result"}
			case "missing_section":
				d.DesignRefs = []string{"design.md#absent"}
			case "empty_section":
				d.DesignRefs = []string{"design.md#"}
			case "working_tree_only":
				d.DesignRefs = []string{"design.md#uncommitted"}
				if err := os.WriteFile(filepath.Join(d.Repository, "design.md"), []byte("# Uncommitted\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "fenced_example", "indented_example":
				body := "```md\n# Result\n```\n"
				if name == "indented_example" {
					body = "    # Result\n"
				}
				if err := os.WriteFile(filepath.Join(d.Repository, "design.md"), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				gitFixture(t, d.Repository, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-am", "Example")
			case "missing_base":
				d.Base = "absent"
			}
			managed := 0
			s, err := openService(t, Config{DB: store(t), Epoch: 1, Manage: func(_ context.Context, r Record) (Record, error) { managed++; return r, nil }})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close(context.Background())
			tool := s.Tool(Origin{Person: "person", Agent: "agent", Conversation: "main:agent", Turn: "turn:input"})
			call := provider.ToolCall{ID: "call", Name: "dispatch_work", Arguments: string(mustJSON(d))}
			if result := tool.Execute(t.Context(), call); result.Code != loop.Invalid {
				t.Errorf("invalid definition admitted: %+v", result)
			}
			records, err := s.records(t.Context())
			if err != nil || len(records) != 0 || managed != 0 {
				t.Fatalf("invalid definition reached manager/queue: managed=%d records=%+v err=%v", managed, records, err)
			}
		})
	}
}

func TestDefinitionPinsBaseAndRendersPacket(t *testing.T) {
	d := definition(t)
	base := gitFixture(t, d.Repository, "rev-parse", "HEAD")
	s, err := openService(t, Config{DB: store(t), Epoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	tool := s.Tool(Origin{Person: "person", Agent: "agent", Conversation: "main:agent", Turn: "turn:input"})
	call := provider.ToolCall{ID: "call", Name: "dispatch_work", Arguments: string(mustJSON(d))}
	first := tool.Execute(t.Context(), call)
	if first.Code != loop.OK {
		t.Fatal(first)
	}
	// A retry must keep the admitted base even after the named branch changes.
	if err := os.WriteFile(filepath.Join(d.Repository, "design.md"), []byte("# Changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, d.Repository, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-am", "Move base")
	if err = s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err = openService(t, Config{DB: s.cfg.DB, Epoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	tool = s.Tool(Origin{Person: "person", Agent: "agent", Conversation: "main:agent", Turn: "turn:input"})
	if again := tool.Execute(t.Context(), call); again.Code != loop.OK || string(again.Output) != string(first.Output) {
		t.Fatalf("replay changed: %+v", again)
	}
	records, err := s.records(t.Context())
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	r := records[0]
	if r.BaseCommit != base {
		t.Errorf("admission did not pin base: got %q want %q", r.BaseCommit, base)
	}
	packet, err := taskPacket(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(packet, r.ID) || !strings.Contains(packet, base) {
		t.Errorf("packet lost assignment or base: %s", packet)
	}
	var rendered Definition
	if err := json.Unmarshal([]byte(packet[strings.IndexByte(packet, '{'):]), &rendered); err != nil || !reflect.DeepEqual(rendered, r.TaskDefinition()) {
		t.Fatalf("packet changed definition: %+v %v", rendered, err)
	}
}

func TestDesignReferenceUsesMarkdownSections(t *testing.T) {
	for _, tc := range []struct {
		body, anchor string
		want         bool
	}{
		{"# Result\n", "result", true},
		{"```md\n# Result\n```\n", "result", false},
		{"    # Result\n", "result", false},
		{"#Result\n", "result", false},
		{"Result\n======\n", "result", true},
		{"## **Work** and `tools`! ##\n", "work-and-tools", true},
		{"# Result\n# Result\n", "result-1", true},
		{"# 결과 확인\n", "결과-확인", true},
		{"# <em>Result</em> &amp; checks\n", "result--checks", true},
		{"# Result-1\n# Result\n# Result\n", "result-2", true},
		{"# Result\n", "missing", false},
	} {
		t.Run(tc.anchor+tc.body, func(t *testing.T) {
			if got := hasHeading(tc.body, tc.anchor); got != tc.want {
				t.Errorf("hasHeading(%q,%q)=%v want %v", tc.body, tc.anchor, got, tc.want)
			}
		})
	}
}
