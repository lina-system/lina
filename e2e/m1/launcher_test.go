// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

// TestM3LauncherInstalledCore proves the launcher capability through installed
// Core: a line lands once in the last or named agent's main conversation, the
// visible short form streams on the launcher topic, and its message locator
// finds the reply in that conversation.
func TestM3LauncherInstalledCore(t *testing.T) {
	t.Parallel()

	const (
		line      = "Launcher line: summarize the morning."
		namedLine = "Launcher line for the named agent."
		short     = "Short answer."
	)
	full := short + " " + strings.Repeat("More detail ", 40)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(string(mustJSON(t, body.Input)), "Launcher line") {
			t.Error("launcher line did not reach the model")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, delta := range []string{short + " ", strings.Repeat("More detail ", 40)} {
			fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%q}\n\n", delta)
		}
		fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}}\n\n", full)
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer provider.Close()

	h, start := installedConversationHarness(t, provider.URL)
	start(true)

	setup, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "agents", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close()
	receiveSetup := launcherCorrelated(t, h, setup)
	listOperation := "list"
	list := receiveSetup(setup.Agent(h.ctx, protocol.AgentRequest{Operation: &listOperation})).Payload.(protocol.AgentResult)
	if list.Agents == nil || len(*list.Agents) < 2 {
		t.Fatalf("launcher needs at least two agents: %+v", list.Agents)
	}
	last, named := (*list.Agents)[1].AgentID, (*list.Agents)[0].AgentID
	choose := "choose"
	receiveSetup(setup.Agent(h.ctx, protocol.AgentRequest{Operation: &choose, AgentID: &last}))

	launcher, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "launcher", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer launcher.Close()
	if len(launcher.Welcome.Capabilities) != 1 || launcher.Welcome.Capabilities[0].Name != "launcher" {
		t.Fatalf("launcher capability not granted: %+v", launcher.Welcome.Capabilities)
	}
	receiveLauncher := launcherCorrelated(t, h, launcher)
	topic := "launcher"
	receiveLauncher(launcher.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}))

	requestID := "launcher-request-1"
	replies, result := launcherTurn(t, h, launcher, protocol.LauncherRequest{RequestID: requestID, Text: line})
	if result.AgentID != last || result.ConversationID != "main:"+last {
		t.Fatalf("line did not land in the last agent: %+v", result)
	}
	if result.RequestID != requestID {
		t.Fatalf("wrong request correlation: %+v", result)
	}
	if len(replies) < 2 {
		t.Fatalf("short reply did not stream: %+v", replies)
	}
	for i, reply := range replies {
		if reply.RequestID != requestID || reply.ConversationID != result.ConversationID || reply.AgentID != last {
			t.Fatalf("reply %d lost its locator or attribution: %+v", i, reply)
		}
	}
	final := replies[len(replies)-1]
	if !final.Done || final.Text != short {
		t.Fatalf("final short form = %q done=%v, want %q", final.Text, final.Done, short)
	}
	if len(replies) > 1 && replies[0].Text == final.Text {
		t.Fatal("no intermediate short form streamed")
	}
	if final.MessageID == nil || *final.MessageID == "" {
		t.Fatal("missing message locator")
	}
	if strings.Contains(final.Text, "More detail") {
		t.Fatal("launcher received the full reply instead of its short form")
	}

	// The same line admits once: a repeated request id returns the original
	// admission and starts no second turn.
	receiveLauncher(launcher.Launcher(h.ctx, protocol.LauncherRequest{RequestID: requestID, Text: line}))
	if got := calls.Load(); got != 1 {
		t.Fatalf("duplicate launcher line started %d model calls", got)
	}

	open := launcherOpenConversation(t, h, protocol.HistoryGet{})
	if open.ConversationID == nil || *open.ConversationID != "main:"+last {
		t.Fatalf("last conversation did not open: %+v", open)
	}
	items := launcherSnapshot(t, h, open.ConversationID, line, *final.MessageID)
	if items != 1 {
		t.Fatalf("launcher line appears %d times in the main conversation", items)
	}

	// A named agent receives its own line in its own main conversation.
	namedRequest := "launcher-request-2"
	namedReplies, namedResult := launcherTurn(t, h, launcher, protocol.LauncherRequest{RequestID: namedRequest, Text: namedLine, AgentID: &named})
	if namedResult.AgentID != named || namedResult.ConversationID != "main:"+named {
		t.Fatalf("named line did not land in that agent: %+v", namedResult)
	}
	namedFinal := namedReplies[len(namedReplies)-1]
	if !namedFinal.Done || namedFinal.MessageID == nil {
		t.Fatalf("named reply did not finish: %+v", namedFinal)
	}
	namedOpen := launcherOpenConversation(t, h, protocol.HistoryGet{AgentID: &named})
	if namedOpen.ConversationID == nil || *namedOpen.ConversationID != "main:"+named {
		t.Fatalf("named conversation did not open: %+v", namedOpen)
	}
	if got := launcherSnapshot(t, h, namedOpen.ConversationID, namedLine, *namedFinal.MessageID); got != 1 {
		t.Fatalf("named launcher line appears %d times", got)
	}
}

func launcherCorrelated(t *testing.T, h *harness, c *clientapi.Client) func(string, error) protocol.Envelope {
	t.Helper()
	return func(id string, err error) protocol.Envelope {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		for {
			frame, err := c.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if frame.ReplyTo != nil && *frame.ReplyTo == id {
				if frame.Error != nil {
					t.Fatalf("request refused: %s", frame.Error.Code)
				}
				return frame
			}
		}
	}
}

// launcherTurn sends one line and collects its short reply until done.
func launcherTurn(t *testing.T, h *harness, c *clientapi.Client, request protocol.LauncherRequest) ([]protocol.LauncherReply, protocol.LauncherResult) {
	t.Helper()
	id, err := c.Launcher(h.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var (
		replies []protocol.LauncherReply
		result  *protocol.LauncherResult
		done    bool
	)
	for result == nil || !done {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch p := frame.Payload.(type) {
		case protocol.LauncherResult:
			if frame.ReplyTo != nil && *frame.ReplyTo == id {
				if frame.Error != nil {
					t.Fatalf("launcher request refused: %s", frame.Error.Code)
				}
				copy := p
				result = &copy
			}
		case protocol.LauncherReply:
			if p.RequestID == request.RequestID {
				replies = append(replies, p)
				done = done || p.Done
			}
		}
	}
	return replies, *result
}

func launcherOpenConversation(t *testing.T, h *harness, request protocol.HistoryGet) protocol.History {
	t.Helper()
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return launcherCorrelated(t, h, c)(c.History(h.ctx, request)).Payload.(protocol.History)
}

// launcherSnapshot opens the conversation at the locator and returns how often
// the person's line appears. It fails when the locator names no conversation
// message.
func launcherSnapshot(t *testing.T, h *harness, conversation *string, line, messageID string) int {
	t.Helper()
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	topic := "conversation:" + *conversation
	snapshot := launcherCorrelated(t, h, c)(c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic})).Payload.(protocol.Snapshot)
	count := 0
	located := false
	for _, item := range snapshot.Items {
		if item.Key == messageID {
			located = true
			if item.Kind != protocol.KindItemDone && item.Kind != protocol.KindResult {
				t.Fatalf("locator names %s, not a conversation message", item.Kind)
			}
		}
		if item.Kind != protocol.KindItemDone {
			continue
		}
		var done protocol.ItemDone
		if err := json.Unmarshal(item.Payload, &done); err != nil {
			t.Fatal(err)
		}
		if done.Text != nil && *done.Text == line && done.Speaker != nil && *done.Speaker == protocol.ConversationSpeakerPerson {
			count++
		}
	}
	if !located {
		t.Fatalf("locator %q opens no message in %s", messageID, *conversation)
	}
	return count
}
