// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIMemoryAgentsInstalledCore(t *testing.T) {
	t.Parallel()

	// One immutable build serves this scenario group. Every subtest still installs
	// it into fresh state and owns its own Core, provider and terminal processes.
	binary := buildLina(t)
	bundle := makeInstallBundle(t, t.TempDir(), binary, buildCommand(t, "lina-node"))
	raw, err := os.ReadFile("tui-memory-agents.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Commands                 []string
		Memory, Corrected, Agent string
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, command := range scenario.Commands {
		t.Run(command, func(t *testing.T) {
			called := make(chan struct{}, 1)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case called <- struct{}{}:
				default:
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
			}))
			defer provider.Close()
			h, start := installConversationBundle(t, newHarness(t, binary), bundle, provider.URL)
			core := start(true)
			waitCoreReady(t, h)
			tap := newAccountTap(t, h, h.socket)
			h.socket = tap.socket
			terminal := h.startScreenTUI("TERM=xterm-256color")
			waitComposerReady(t, h, terminal)
			h.keys(terminal, command+"\r")
			wanted := protocol.KindMemoryResult
			if command == "/agents" {
				wanted = protocol.KindAgentResult
			}
			for {
				select {
				case <-called:
					t.Fatalf("management command %s reached the conversation provider", command)
				case e := <-tap.replies:
					if e.Error != nil {
						t.Fatalf("management command refused: %s", e.Error.Code)
					}
					if e.Kind != wanted {
						continue
					}
				case <-h.ctx.Done():
					t.Fatal("management command produced no result")
				}
				break
			}
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
			h.socket = core.command.Args[len(core.command.Args)-1]
			h.stopCore(core)
		})
	}
	t.Run("round trip", func(t *testing.T) { memoryAgentRoundTrip(t, binary, bundle) })
	t.Run("paged memory", func(t *testing.T) { memoryAgentPages(t, binary, bundle) })
	t.Run("without model or default agent", func(t *testing.T) { managementWithoutModelOrDefaultAgent(t, binary, bundle) })
}

func memoryAgentRoundTrip(t *testing.T, binary, bundle string) {
	raw, err := os.ReadFile("tui-memory-agents.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct{ Memory, Corrected, Agent string }
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	requests := make(chan []byte, 16)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Fixture answer\"}]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer provider.Close()
	h, start := installConversationBundle(t, newHarness(t, binary), bundle, provider.URL)
	core := start(true)
	waitCoreReady(t, h)
	socket := h.socket
	tap := newAccountTap(t, h, socket, true)
	h.socket = tap.socket
	terminal := h.startScreenTUI("TERM=xterm-256color")
	waitComposerReady(t, h, terminal)
	opened := false
	command := func(text string, kind protocol.Kind) protocol.Envelope {
		t.Helper()
		if opened {
			h.keys(terminal, "\x1b")
			waitComposerReady(t, h, terminal)
		}
		h.keys(terminal, text+"\r")
		for {
			e := tap.next(t, h)
			if e.Error != nil {
				t.Fatalf("%s refused: %s", text, e.Error.Code)
			}
			if e.Kind == kind {
				// The response must be rendered before Esc and the next command.
				marker := "/memory forget"
				if kind == protocol.KindAgentResult {
					marker = "/agents delete"
				}
				if p, ok := e.Payload.(protocol.MemoryResult); ok && p.ItemID != nil {
					marker = *p.ItemID
				}
				if p, ok := e.Payload.(protocol.AgentResult); ok && p.AgentID != nil && *p.Operation != "delete" {
					marker = *p.AgentID
				}
				operation := ""
				if p, ok := e.Payload.(protocol.MemoryResult); ok {
					operation = *p.Operation
				}
				if p, ok := e.Payload.(protocol.AgentResult); ok {
					operation = *p.Operation
				}
				if _, err := terminal.screen.Wait(h.ctx, func(snapshot tuiterm.Snapshot) bool {
					text := screenText(snapshot)
					return strings.HasPrefix(text, design.Text(design.English, operation)+" ") && strings.Contains(text, marker)
				}); err != nil {
					t.Fatal("management result not rendered", err)
				}
				opened = true
				return e
			}
		}
	}
	memoryCommand := func(text string) protocol.MemoryResult {
		t.Helper()
		return command(text, protocol.KindMemoryResult).Payload.(protocol.MemoryResult)
	}
	agentCommand := func(text string) protocol.AgentResult {
		t.Helper()
		return command(text, protocol.KindAgentResult).Payload.(protocol.AgentResult)
	}
	chat := func(prompt, have, absent string) {
		t.Helper()
		if opened {
			h.keys(terminal, "\x1b")
			waitComposerReady(t, h, terminal)
			opened = false
		}
		h.keys(terminal, prompt+"\r")
		select {
		case body := <-requests:
			if have != "" && !bytes.Contains(body, []byte(have)) {
				t.Fatal("next model request omitted the remembered item")
			}
			if absent != "" && bytes.Contains(body, []byte(absent)) {
				t.Fatal("next model request retained corrected/forgotten content")
			}
		case <-h.ctx.Done():
			t.Fatalf("next model request missing; screen=%s", screenText(terminal.screen.Snapshot()))
		}
		for {
			e := tap.next(t, h)
			if e.Error != nil {
				t.Fatal(e.Error.Code)
			}
			if p, ok := e.Payload.(protocol.TurnState); ok && p.Active != nil && !*p.Active {
				break
			}
		}
		waitScreen(t, h, terminal, "Fixture answer")
	}
	first := agentCommand("/agents")
	if first.Agents == nil || len(*first.Agents) != 1 {
		t.Fatal("initial agent missing")
	}
	original := (*first.Agents)[0].AgentID
	saved := memoryCommand("/memory remember " + scenario.Memory)
	if saved.ItemID == nil {
		t.Fatal("remember had no committed identity")
	}
	listed := memoryCommand("/memory list orchard")
	if listed.Items == nil || len(*listed.Items) != 1 || (*listed.Items)[0].ItemID != *saved.ItemID || len((*listed.Items)[0].Evidence) == 0 {
		t.Fatal("memory identity/evidence not listed")
	}
	chat("fixture one", scenario.Memory, "")
	corrected := memoryCommand("/memory correct " + *saved.ItemID + " " + scenario.Corrected)
	if corrected.ItemID == nil || *corrected.ItemID == *saved.ItemID {
		t.Fatal("correction did not supersede by identity")
	}
	chat("fixture two", scenario.Corrected, scenario.Memory)
	memoryCommand("/memory forget " + *corrected.ItemID)
	listed = memoryCommand("/memory list preference")
	if listed.Items == nil || len(*listed.Items) != 0 {
		t.Fatal("forget did not give zero hits on next request")
	}
	chat("fixture three", "", scenario.Corrected)
	private := memoryCommand("/memory remember --holder=agent --scope=private Synthetic private orchard")
	locked := memoryCommand("/memory remember --scope=never-leaves Synthetic sealed orchard")
	if private.ItemID == nil || locked.ItemID == nil {
		t.Fatal("scoped remember failed")
	}
	chat("fixture four", "Synthetic private orchard", "Synthetic sealed orchard")
	created := agentCommand("/agents create " + scenario.Agent)
	if created.AgentID == nil {
		t.Fatal("create returned no id")
	}
	second := *created.AgentID
	chosen := agentCommand("/agents choose " + second)
	if chosen.AgentID == nil || *chosen.AgentID != second {
		t.Fatal("choose changed identity")
	}
	// Wait for the new subscription before querying or sending in this lane.
	for {
		e := tap.next(t, h)
		if p, ok := e.Payload.(protocol.Snapshot); ok && p.Topic == "conversation:main:"+second {
			break
		}
	}
	listed = memoryCommand("/memory list private")
	if listed.Items == nil || len(*listed.Items) != 0 {
		t.Fatal("another agent read private holder memory")
	}
	renamed := agentCommand("/agents rename " + second + " Fixture renamed")
	if renamed.AgentID == nil || *renamed.AgentID != second {
		t.Fatal("rename changed identity")
	}
	h.keys(terminal, "\x1b")
	waitComposerReady(t, h, terminal)
	waitScreen(t, h, terminal, "Fixture renamed")
	opened = false
	chat("second agent fixture", "", "Synthetic private orchard")
	deleted := agentCommand("/agents delete " + second)
	if deleted.AgentID == nil || *deleted.AgentID != second || deleted.Agents == nil || len(*deleted.Agents) != 1 || (*deleted.Agents)[0].AgentID != original {
		t.Fatal("delete did not remove the selected id")
	}
	watcher, err := clientapi.DialWithCapabilities(h.ctx, socket, "test", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	topic := "conversation:main:" + second
	if _, err = watcher.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	refused, err := watcher.Receive(h.ctx)
	if err != nil || refused.Error == nil {
		t.Fatal("deleted agent history remained subscribable", err)
	}
	watcher.Close()
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.socket = socket
	h.stopCore(core)
	core = start(true)
	client, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "agents", Version: "1.0"}, {Name: "memory", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	op := "list"
	query := "preference"
	if _, err = client.Memory(h.ctx, protocol.MemoryRequest{Operation: &op, AgentID: &original, Text: &query}); err != nil {
		t.Fatal(err)
	}
	e, err := client.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	after, ok := e.Payload.(protocol.MemoryResult)
	if !ok || after.Items == nil || len(*after.Items) != 0 {
		t.Fatal("restart restored forgotten memory")
	}
	if _, err = client.Agent(h.ctx, protocol.AgentRequest{Operation: &op}); err != nil {
		t.Fatal(err)
	}
	e, err = client.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	agents, ok := e.Payload.(protocol.AgentResult)
	if !ok || agents.Agents == nil || len(*agents.Agents) != 1 || (*agents.Agents)[0].AgentID != original {
		t.Fatal("restart changed agent deletion")
	}
	client.Close()
	h.stopCore(core)
	select {
	case <-requests:
		t.Fatal("management command leaked to provider")
	default:
	}
}

func managementWithoutModelOrDefaultAgent(t *testing.T, binary, bundle string) {
	h, start := installConversationBundle(t, newHarness(t, binary), bundle, "")
	core := start(false)
	waitCoreReady(t, h)
	caps := []protocol.Capability{{Name: "agents", Version: "1.0"}, {Name: "memory", Version: "1.0"}}
	client, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", caps)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.Welcome.Agents) != 1 {
		t.Fatal("initial agent absent")
	}
	id := client.Welcome.Agents[0].AgentID
	op := "delete"
	if _, err = client.Agent(h.ctx, protocol.AgentRequest{Operation: &op, AgentID: &id}); err != nil {
		t.Fatal(err)
	}
	e, err := client.Receive(h.ctx)
	if err != nil || e.Error != nil {
		t.Fatal("delete default failed", err)
	}
	client.Close()
	h.stopCore(core)
	core = start(false)
	waitCoreReady(t, h)
	socket := h.socket
	tap := newAccountTap(t, h, socket)
	h.socket = tap.socket
	terminal := h.startScreenTUI("TERM=xterm-256color")
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "/agents create Fixture replacement\r")
	for {
		e := tap.next(t, h)
		if e.Error != nil {
			t.Fatal(e.Error.Code)
		}
		if p, ok := e.Payload.(protocol.AgentResult); ok {
			if p.AgentID == nil || *p.AgentID == id || p.Agents == nil || len(*p.Agents) != 1 {
				t.Fatal("default recreated or replacement missing")
			}
			waitScreen(t, h, terminal, *p.AgentID)
			break
		}
	}
	h.keys(terminal, "\x1b")
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "/memory remember Fixture offline statement\r")
	for {
		e := tap.next(t, h)
		if e.Error != nil {
			t.Fatal(e.Error.Code)
		}
		if p, ok := e.Payload.(protocol.MemoryResult); ok {
			if p.ItemID == nil {
				t.Fatal("offline remember failed")
			}
			waitScreen(t, h, terminal, *p.ItemID)
			break
		}
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.socket = socket
	h.stopCore(core)
}

func memoryAgentPages(t *testing.T, binary, bundle string) {
	h, start := installConversationBundle(t, newHarness(t, binary), bundle, "")
	core := start(false)
	waitCoreReady(t, h)
	socket := h.socket
	client, err := clientapi.DialWithCapabilities(h.ctx, socket, "test", []protocol.Capability{{Name: "memory", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	agent := client.Welcome.Agents[0].AgentID
	raw, err := os.ReadFile("tui-memory-agents.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		PageItems int `json:"page_items"`
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < scenario.PageItems; i++ {
		op, text := "remember", fmt.Sprintf("paged %d ", i)+strings.Repeat("<", 16000)
		if _, err = client.Memory(h.ctx, protocol.MemoryRequest{Operation: &op, AgentID: &agent, Text: &text}); err != nil {
			t.Fatal(err)
		}
		e, err := client.Receive(h.ctx)
		if err != nil || e.Error != nil {
			t.Fatalf("page setup %d refused: transport=%v protocol=%+v", i, err, e.Error)
		}
	}
	client.Close()
	tap := newAccountTap(t, h, socket)
	h.socket = tap.socket
	terminal := h.startScreenTUI("TERM=xterm-256color")
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "/memory list paged\r")
	seen := map[string]bool{}
	for page := 0; page < scenario.PageItems; page++ {
		var result protocol.MemoryResult
		for {
			e := tap.next(t, h)
			if e.Error != nil {
				t.Fatal(e.Error.Code)
			}
			if p, ok := e.Payload.(protocol.MemoryResult); ok {
				result = p
				break
			}
		}
		if result.Items == nil || len(*result.Items) == 0 {
			t.Fatal("empty memory page")
		}
		for _, item := range *result.Items {
			if seen[item.ItemID] {
				t.Fatal("page repeated an item")
			}
			seen[item.ItemID] = true
		}
		waitScreen(t, h, terminal, (*result.Items)[0].ItemID)
		if result.Next == nil {
			break
		}
		h.keys(terminal, "\x1b")
		waitComposerReady(t, h, terminal)
		h.keys(terminal, "/memory next\r")
	}
	if len(seen) != scenario.PageItems {
		t.Fatal("TUI pagination lost memory")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.socket = socket
	h.stopCore(core)
}
