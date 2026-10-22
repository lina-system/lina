// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2InstalledGrowthFollowups(t *testing.T) {
	var scenario struct {
		AgentName          string                        `json:"agent_name"`
		SnapshotCharacters int                           `json:"snapshot_characters"`
		Snapshots          []struct{ Name, Unit string } `json:"snapshots"`
		Notice             string                        `json:"notice"`
	}
	data, err := os.ReadFile("m2/persona-growth-followups.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	h, start := installedConversationHarness(t, "")
	core := start(false)
	caps := []protocol.Capability{{Name: "agents", Version: "1.0"}, {Name: "growth", Version: "1.0"}}
	connect := func() *clientapi.Client {
		t.Helper()
		c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", caps)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	c := connect()
	request := func(t *testing.T, p protocol.AgentRequest) protocol.AgentResult {
		t.Helper()
		if _, err := c.Agent(h.ctx, p); err != nil {
			t.Fatal(err)
		}
		e, err := c.Receive(h.ctx)
		if err != nil || e.Error != nil {
			t.Fatalf("operation %v failed: %+v %v", *p.Operation, e.Error, err)
		}
		result, ok := e.Payload.(protocol.AgentResult)
		if !ok {
			t.Fatalf("unexpected result: %+v", e)
		}
		return result
	}
	agent := c.Welcome.Agents[0].AgentID
	t.Run("near-limit-candidates", func(t *testing.T) {
		for i, sample := range scenario.Snapshots {
			t.Run(sample.Name, func(t *testing.T) {
				operation := "growth_adopt"
				candidate := &protocol.GrowthCandidate{ID: sample.Name, Expected: strconv.Itoa(i + 1), Snapshot: strings.Repeat(sample.Unit, scenario.SnapshotCharacters), Notice: scenario.Notice}
				result := request(t, protocol.AgentRequest{Operation: &operation, AgentID: &agent, Candidate: candidate})
				if result.Revision == nil || *result.Revision != strconv.Itoa(i+2) {
					t.Fatalf("revision=%+v", result)
				}
			})
		}
	})
	operation := "delete"
	request(t, protocol.AgentRequest{Operation: &operation, AgentID: &agent})
	c.Close()
	h.stopCore(core)
	core = start(false)
	c = connect()
	if len(c.Welcome.Agents) != 0 {
		t.Fatal("deleted agent returned")
	}
	if !slices.Contains(c.Welcome.Capabilities, caps[1]) {
		t.Fatal("empty-agent startup omitted growth capability")
	}
	operation = "create"
	created := request(t, protocol.AgentRequest{Operation: &operation, Name: &scenario.AgentName})
	if created.AgentID == nil {
		t.Fatal("no replacement agent")
	}
	agent = *created.AgentID
	operation = "growth_adopt"
	adopted := request(t, protocol.AgentRequest{Operation: &operation, AgentID: &agent, Candidate: &protocol.GrowthCandidate{ID: "replacement-step", Expected: "1", Snapshot: "replacement growth", Notice: scenario.Notice}})
	if adopted.Revision == nil || *adopted.Revision != "2" || adopted.Notices == nil || len(*adopted.Notices) != 1 {
		t.Fatalf("replacement adoption=%+v", adopted)
	}
	// The same dynamically bound service must also be reachable on the worker
	// socket. Replaying its committed proposal does not create a second revision.
	epoch, err := strconv.ParseUint(c.Welcome.Epoch, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(agents.GrowthCandidate{ID: "replacement-step", Expected: 1, Snapshot: "replacement growth", Notice: scenario.Notice})
	worker := jobapi.NewClient(filepath.Join(h.root, "run", "jobs.sock"))
	if applied, err := worker.Apply(h.ctx, jobapi.Proposal{Epoch: epoch, JobID: "growth:" + agent, From: 0, Through: 1, Output: raw}); err != nil || applied.Watermark != 1 {
		t.Fatalf("replacement worker service: %+v %v", applied, err)
	}
	// A second agent shares the socket but has its own controls and notices.
	operation = "create"
	second := request(t, protocol.AgentRequest{Operation: &operation, Name: &scenario.AgentName})
	operation = "growth_notices"
	separate := request(t, protocol.AgentRequest{Operation: &operation, AgentID: second.AgentID})
	if separate.Revision == nil || *separate.Revision != "1" || separate.Notices == nil || len(*separate.Notices) != 0 {
		t.Fatalf("growth leaked across agents: %+v", separate)
	}
	operation = "growth_undo"
	undo := (*adopted.Notices)[0].UndoRevision
	restored := request(t, protocol.AgentRequest{Operation: &operation, AgentID: &agent, Revision: &undo})
	if restored.Revision == nil || *restored.Revision != "3" {
		t.Fatalf("replacement undo=%+v", restored)
	}
	operation = "growth_adopt"
	request(t, protocol.AgentRequest{Operation: &operation, AgentID: &agent, Candidate: &protocol.GrowthCandidate{ID: "replacement-next", Expected: "3", Snapshot: "next growth", Notice: scenario.Notice}})
	operation = "growth_undo"
	replay := request(t, protocol.AgentRequest{Operation: &operation, AgentID: &agent, Revision: &undo})
	if replay.Revision == nil || *replay.Revision != "4" {
		t.Fatalf("repeated undo=%+v", replay)
	}
	c.Close()
	h.stopCore(core)
	core = start(false)
	c = connect()
	operation = "growth_notices"
	recovered := request(t, protocol.AgentRequest{Operation: &operation, AgentID: &agent})
	if recovered.Revision == nil || *recovered.Revision != "4" || recovered.Notices == nil || len(*recovered.Notices) != 1 {
		t.Fatalf("replacement restart=%+v", recovered)
	}
	c.Close()
	h.stopCore(core)
}
