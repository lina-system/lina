// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestM1ReplayExportInstalledCore(t *testing.T) {
	data, err := os.ReadFile("m1/replay-export.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct{ Input, Reply string }
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var mu sync.Mutex
	var instructions []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var request struct{ Instructions string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		mu.Lock()
		instructions = append(instructions, request.Instructions)
		mu.Unlock()
		reply := scenario.Reply
		if call == 1 {
			reply += "\n" + strings.Repeat("Synthetic detail.\n", 100)
		}
		emitResponse(w, reply, true)
	}))
	defer provider.Close()
	h, _ := installedConversationHarness(t, provider.URL)
	core := h.command("core", "--state-root", h.root, "--socket", h.socket)
	core.command.Env = append(core.command.Env, "PATH="+os.Getenv("PATH"), "LINA_RESPONSES_BASE_URL="+provider.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
	core.start(t)
	h.connect(core).Close()
	ready, conversation := conversationClient(t, h)
	ready.Close()
	terminal := h.startConversationTUI([]string{"TERM=xterm-256color"}, 80, 24, conversation)
	waitScreen(t, h, terminal, design.Text(design.English, "live"))
	h.keys(terminal, scenario.Input+"\r")
	waitScreen(t, h, terminal, scenario.Reply)
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	for _, command := range []string{"replay", "export"} {
		t.Run(command, func(t *testing.T) {
			p := h.command(command, "--help")
			p.start(t)
			p.wait(t, 0)
		})
	}

	client, conversation := conversationClient(t, h)
	agent := client.Welcome.Agents[0].AgentID
	// Cross the native ledger's material boundary, then replay this exact turn.
	large := strings.Repeat("기록", 3000)
	key := "large-replay-source"
	if _, err = client.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &large}); err != nil {
		t.Fatal(err)
	}
	echo, active := false, false
	for {
		frame, err := client.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Error != nil {
			t.Fatalf("large input refused: %s", frame.Error.Code)
		}
		if item, ok := frame.Payload.(protocol.ItemDone); ok && item.EchoKey != nil && *item.EchoKey == key {
			echo = true
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && echo {
			if *state.Active {
				active = true
			} else if active {
				break
			}
		}
	}
	client.Close()
	digest := func() [32]byte {
		var content []byte
		for _, suffix := range []string{"", "-wal"} {
			b, err := os.ReadFile(filepath.Join(h.root, "canon/canon.sqlite") + suffix)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			content = append(content, b...)
		}
		return sha256.Sum256(content)
	}
	before := digest()
	export := func(args ...string) protocol.ExportResult {
		p := h.command(append([]string{"export", "--socket", h.socket}, args...)...)
		p.start(t)
		p.wait(t, 0)
		var result protocol.ExportResult
		if err := json.Unmarshal(p.stdout.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	empty := export()
	var manifest replay.Manifest
	if err := json.Unmarshal([]byte(empty.Manifest), &manifest); err != nil || manifest.Selected != 0 {
		t.Fatalf("default selected turns: %+v %v", manifest, err)
	}
	chosen := export("--agent", agent, "--conversation", conversation, "--since", "2020-01-01T00:00:00Z", "--until", "2100-01-01T00:00:00Z")
	if err := json.Unmarshal([]byte(chosen.Manifest), &manifest); err != nil || manifest.Exported != 2 {
		t.Fatalf("export: %+v %v", manifest, err)
	}
	file := filepath.Join(chosen.Directory, "episodes.jsonl")
	data, err = os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var ep replay.Episode
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var next replay.Episode
		err = decoder.Decode(&next)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(next.Reply) != 1 || len(next.ReplyRefs) != 1 {
			t.Errorf("export duplicated assistant reply: %d items, %d refs", len(next.Reply), len(next.ReplyRefs))
		}
		for _, event := range next.History {
			if strings.HasPrefix(event.ID, "result:") {
				t.Error("card metadata entered exported history")
			}
		}
		ep = next
	}
	if ep.Input.Text != large {
		t.Fatal("large material-backed input lost")
	}
	for _, ref := range ep.Evidence.Assembly.History {
		if strings.HasPrefix(ref.ID, "result:") {
			t.Fatal("generated result card entered request assembly evidence")
		}
	}
	if !strings.Contains(string(data), scenario.Input) || ep.Input.ID == "" || len(ep.Evidence.Assembly.History) == 0 {
		t.Fatal("export lost original evidence")
	}
	candidateDir := filepath.Join(h.root, "candidate")
	if err = os.Mkdir(candidateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(candidateDir, "core.md"), []byte("synthetic candidate guidance"), 0600); err != nil {
		t.Fatal(err)
	}
	for i, source := range []string{ep.TurnID, file} {
		out := filepath.Join(h.root, []string{"replayed-turn", "replayed-episode"}[i])
		args := []string{"replay", source, "--socket", h.socket, "--out", out}
		if i == 1 {
			args = append(args, "--turn", ep.TurnID, "--candidates", candidateDir)
		}
		p := h.command(args...)
		p.start(t)
		p.wait(t, 0)
		reply, err := os.ReadFile(filepath.Join(out, "reply.json"))
		if err != nil || strings.Count(string(reply), scenario.Reply) != 1 {
			t.Fatalf("replay reply: %s %v", reply, err)
		}
		for _, name := range []string{"usage.json", "tool-results.json", "manifest.json"} {
			if _, err := os.Stat(filepath.Join(out, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("model calls=%d; export must not call model", calls.Load())
	}
	mu.Lock()
	candidateApplied := strings.Contains(instructions[3], "synthetic candidate guidance")
	mu.Unlock()
	if !candidateApplied {
		t.Fatal("candidate did not replace recorded layer")
	}
	if after := digest(); after != before {
		t.Fatal("export/replay changed canon or its WAL")
	}
	h.stopCore(core)
}
