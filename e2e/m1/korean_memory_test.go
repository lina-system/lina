// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1KoreanMemoryByID(t *testing.T) {
	var corpus struct {
		Documents []struct{ ID, Body string }
		Queries   []struct {
			Name, Text string
			IDs        []string
		}
	}
	data, err := os.ReadFile("../../testdata/korean/search.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	binary := buildLina(t)
	bundle := makeInstallBundle(t, t.TempDir(), binary, buildCommand(t, "lina-node"))
	h, start := installConversationBundle(t, newHarness(t, binary), bundle, "")
	core := start(false)
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "memory", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	agent := c.Welcome.Agents[0].AgentID
	requestMemory := func(request protocol.MemoryRequest) (protocol.MemoryResult, error) {
		if _, err := c.Memory(h.ctx, request); err != nil {
			return protocol.MemoryResult{}, err
		}
		answer, err := c.Receive(h.ctx)
		if err != nil {
			return protocol.MemoryResult{}, err
		}
		result, ok := answer.Payload.(protocol.MemoryResult)
		if !ok || answer.Error != nil {
			t.Fatalf("memory response: %+v", answer)
		}
		return result, nil
	}
	ids := map[string]string{}
	for _, doc := range corpus.Documents {
		op := "remember"
		result, err := requestMemory(protocol.MemoryRequest{Operation: &op, AgentID: &agent, Text: &doc.Body})
		if err != nil || result.ItemID == nil {
			t.Fatalf("remember %s: %+v %v", doc.ID, result, err)
		}
		ids[*result.ItemID] = doc.ID
	}
	for _, query := range corpus.Queries {
		t.Run(query.Name, func(t *testing.T) {
			op := "list"
			result, err := requestMemory(protocol.MemoryRequest{Operation: &op, AgentID: &agent, Text: &query.Text})
			if err != nil || result.Items == nil {
				t.Fatalf("query: %+v %v", result, err)
			}
			got := []string{}
			for _, item := range *result.Items {
				id, found := ids[item.ItemID]
				if !found || len(item.Evidence) == 0 {
					t.Fatalf("unidentified or unsourced hit: %+v", item)
				}
				got = append(got, id)
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, query.IDs) {
				t.Fatalf("ids=%v want=%v", got, query.IDs)
			}
		})
	}
	c.Close()
	h.stopCore(core)
}
