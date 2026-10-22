// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/rpc"
)

// The existing process harness runs installed Core. Only the physical dial
// address is Unix for this credential-free scenario; HTTP upgrade, WebSocket
// messages, Core dispatch, tools, provider continuation and canon are real.
type mobileClient struct {
	wire    *rpc.WebSocket
	welcome protocol.Welcome
	seq     int
}

func mobileDial(t *testing.T, h *harness, endpoint string) *mobileClient {
	t.Helper()
	transport := &http.Transport{}
	if endpoint == "" {
		endpoint = "ws://core/client"
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", h.socket)
		}
	}
	t.Cleanup(transport.CloseIdleConnections)
	conn, _, err := websocket.Dial(h.ctx, endpoint, &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	c := &mobileClient{wire: rpc.NewWebSocket(conn)}
	t.Cleanup(func() { c.wire.Close() })
	hello := clientHello([]string{"1.0"})
	hello.Capabilities = []protocol.Capability{{Name: "conversation", Version: "1.0"}}
	request := c.send(t, h, protocol.KindHello, hello)
	frame := c.receive(t, h)
	if frame.Kind != protocol.KindWelcome || frame.ReplyTo == nil || *frame.ReplyTo != request {
		t.Fatal("WebSocket hello not welcomed")
	}
	c.welcome = frame.Payload.(protocol.Welcome)
	if len(c.welcome.Capabilities) != 1 {
		t.Fatal("conversation capability absent")
	}
	return c
}

func (c *mobileClient) send(t *testing.T, h *harness, kind protocol.Kind, payload protocol.Payload) string {
	t.Helper()
	c.seq++
	id := fmt.Sprintf("mobile-%s-%d", c.welcome.Epoch, c.seq)
	epoch := c.welcome.Epoch
	if epoch == "" {
		epoch = "0"
	}
	e := protocol.Envelope{Protocol: protocol.Protocol{Name: "client", Version: "1.0"}, Kind: kind, MessageID: id, AttemptID: id,
		Sender: protocol.Sender{Party: protocol.PartyClient, ID: "client-e2e"}, Epoch: epoch, SentAt: time.Now().UTC().Format(time.RFC3339Nano),
		EffectState: protocol.EffectNone, SourceRefs: []protocol.SourceRef{}, Payload: payload}
	if err := c.wire.Write(h.ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": kind, "params": e}); err != nil {
		t.Fatal(err)
	}
	return id
}

func (c *mobileClient) receive(t *testing.T, h *harness) protocol.Envelope {
	t.Helper()
	data, err := c.wire.Read(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		JSONRPC string             `json:"jsonrpc"`
		ID      *string            `json:"id"`
		Method  protocol.Kind      `json:"method"`
		Result  *protocol.Envelope `json:"result"`
		Params  *protocol.Envelope `json:"params"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.JSONRPC != "2.0" {
		t.Fatal("invalid JSON-RPC")
	}
	var e protocol.Envelope
	if frame.Result != nil && frame.Params == nil && frame.ID != nil {
		e = *frame.Result
		if e.ReplyTo == nil || *e.ReplyTo != *frame.ID {
			t.Fatal("uncorrelated response")
		}
	} else if frame.Params != nil && frame.Result == nil && frame.ID == nil {
		e = *frame.Params
		if frame.Method != e.Kind || e.ReplyTo != nil {
			t.Fatal("invalid notification")
		}
	} else {
		t.Fatal("invalid message shape")
	}
	if e.Error != nil {
		t.Fatalf("Core refused: %s", e.Error.Code)
	}
	if c.welcome.Epoch != "" && (e.Epoch != c.welcome.Epoch || e.Sender.ID != c.welcome.MainID) {
		t.Fatal("Core identity changed")
	}
	return e
}

func (c *mobileClient) history(t *testing.T, h *harness) protocol.History {
	id := c.send(t, h, protocol.KindHistoryGet, protocol.HistoryGet{})
	frame := c.receive(t, h)
	history, ok := frame.Payload.(protocol.History)
	if !ok || frame.ReplyTo == nil || *frame.ReplyTo != id || history.ConversationID == nil {
		t.Fatal("missing history")
	}
	topic := "conversation:" + *history.ConversationID
	id = c.send(t, h, protocol.KindSubscribe, protocol.Subscribe{Topic: &topic})
	frame = c.receive(t, h)
	snapshot, ok := frame.Payload.(protocol.Snapshot)
	if !ok || frame.ReplyTo == nil || *frame.ReplyTo != id {
		t.Fatal("missing conversation snapshot")
	}
	history.Snapshot = &snapshot

	return history
}

func mobileHistoryRows(t *testing.T, history protocol.History) []partialHistoryRow {
	t.Helper()
	var rows []partialHistoryRow
	for _, item := range history.Snapshot.Items {
		if item.Kind != protocol.KindItemDone {
			continue
		}
		var payload protocol.ItemDone
		data, err := json.Marshal(item.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Text == nil || payload.Speaker == nil {
			t.Fatal("incomplete history item")
		}
		rows = append(rows, partialHistoryRow{Key: item.Key, Text: *payload.Text, Speaker: *payload.Speaker})
	}
	return rows
}

func TestM3WebSocketInstalledCore(t *testing.T) { websocketConversation(t, false) }

func TestM3WebSocketTailnetCore(t *testing.T) { websocketConversation(t, true) }

func websocketConversation(t *testing.T, tailnet bool) {
	var scenario struct {
		Input, Quote, Voice, Reply string
		FollowUp                   string `json:"follow_up"`
		RestoredReply              string `json:"restored_reply"`
	}
	data, err := os.ReadFile("../m3/websocket.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	deltaSeen := make(chan struct{})
	var once sync.Once
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input, Tools []json.RawMessage
			Instructions string
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		n := calls.Add(1)
		switch n {
		case 1:
			if !strings.Contains(string(mustJSON(t, req.Input)), scenario.Input) {
				t.Error("mobile input missing")
			}
			if !strings.Contains(string(mustJSON(t, req.Tools)), `"change_persona"`) {
				t.Error("tool not offered")
			}
			emitPersonaCall(w, "mobile-change", "change_persona", map[string]string{"quote": scenario.Quote, "user_voice": scenario.Voice})
		case 2:
			var output struct{ Type, Output string }
			if len(req.Input) == 0 {
				t.Error("missing tool result")
				return
			}
			if err := json.Unmarshal(req.Input[len(req.Input)-1], &output); err != nil {
				t.Error(err)
			}
			if output.Type != "function_call_output" || !strings.Contains(output.Output, `"code":"ok"`) {
				t.Error("tool did not execute")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%q}\n\n", scenario.Reply)
			w.(http.Flusher).Flush()
			select {
			case <-deltaSeen:
			case <-r.Context().Done():
				return
			}
			emitResponse(w, scenario.Reply, true)
		case 3:
			var persona struct {
				Layers struct {
					UserVoice string `json:"user_voice"`
				} `json:"layers"`
			}
			if err := json.Unmarshal(instructionBlock(req.Instructions, "persona"), &persona); err != nil {
				t.Error(err)
			}
			if persona.Layers.UserVoice != scenario.Voice {
				t.Error("tool effect not restored")
			}
			emitResponse(w, scenario.RestoredReply, true)
		default:
			t.Error("unexpected model replay")
			w.WriteHeader(http.StatusConflict)
		}
	}))
	t.Cleanup(provider.Close)
	var h *harness
	var start func(bool) *process
	if tailnet {
		// The manifest verifies the executable actually hosting boot.Run.
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		h = newHarness(t, buildLina(t))
		bundle := makeInstallBundle(t, h.root, executable, buildCommand(t, "lina-node"))
		h, start = installConversationBundle(t, h, bundle, provider.URL)
	} else {
		h, start = installedConversationHarness(t, provider.URL)
	}
	endpoint := ""
	launch := func() func() {
		core := start(true)
		return func() { h.stopCore(core) }
	}
	if tailnet {
		launch = mobileTailnetLauncher(t, h, provider.URL, &endpoint)
	}
	stopCore := launch()
	c := mobileDial(t, h, endpoint)
	history := c.history(t, h)
	conversation := *history.ConversationID
	turn := func(input, reply string, onDelta func()) {
		id := fmt.Sprintf("input-%s-%d", c.welcome.Epoch, c.seq)
		request := c.send(t, h, protocol.KindInput, protocol.Input{ConversationID: &conversation, InputID: &id, Text: &input})
		ack, complete, terminal := false, false, false
		for !ack || !terminal {
			frame := c.receive(t, h)
			if frame.ReplyTo != nil && *frame.ReplyTo == request {
				if frame.Kind != protocol.KindItemDone || frame.EffectState != protocol.EffectApplied {
					t.Fatal("input not committed")
				}
				ack = true
			}
			if delta, ok := frame.Payload.(protocol.OutputDelta); ok && delta.Text != nil && *delta.Text != "" && onDelta != nil {
				onDelta()
			}
			if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerAgent && item.Text != nil && *item.Text == reply {
				complete = true
			}
			if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
				terminal = true
			}
		}
		if !complete {
			t.Fatal("missing completed result")
		}
	}
	turn(scenario.Input, scenario.Reply, func() { once.Do(func() { close(deltaSeen) }) })
	before := c.history(t, h)
	rows := mobileHistoryRows(t, before)
	seen := map[string]bool{}
	inputSaved, replySaved := false, false
	for _, row := range rows {
		if row.Key == "" || seen[row.Key] {
			t.Fatal("history has missing or duplicate identities")
		}
		seen[row.Key] = true
		inputSaved = inputSaved || row.Text == scenario.Input && row.Speaker == protocol.ConversationSpeakerPerson
		replySaved = replySaved || row.Text == scenario.Reply && row.Speaker == protocol.ConversationSpeakerAgent
	}
	if !inputSaved || !replySaved {
		t.Fatal("committed input and result missing from history")
	}
	epoch := c.welcome.Epoch
	c.wire.Close()
	stopCore()
	stopCore = launch()
	c = mobileDial(t, h, endpoint)
	after := c.history(t, h)
	if *after.ConversationID != conversation || c.welcome.Epoch == epoch || !reflect.DeepEqual(mobileHistoryRows(t, before), mobileHistoryRows(t, after)) {
		t.Fatal("restart lost conversation results")
	}
	turn(scenario.FollowUp, scenario.RestoredReply, nil)
	c.wire.Close()
	stopCore()
	if calls.Load() != 3 {
		t.Fatalf("provider requests=%d", calls.Load())
	}
	t.Log("WebSocket: input committed, tool executed, live delta and result received, canon and tool effect restored without replay")
}
