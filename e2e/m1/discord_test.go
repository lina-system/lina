// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2DiscordCore(t *testing.T) {
	runM2DiscordCore(t, nil)
}

func runM2DiscordCore(t *testing.T, closeCodes []websocket.StatusCode) {
	t.Helper()
	var scenario struct {
		Platform, Account, Owner, Chat, Input string
		Paragraphs                            []int
		Blocked                               int `json:"blocked_code"`
	}
	data, err := os.ReadFile("../m2/discord.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	reply := strings.Repeat("a", scenario.Paragraphs[0]) + "\n\n" + strings.Repeat("b", scenario.Paragraphs[1])
	var modelCalls, sendCalls, foreground, background atomic.Int32
	var backgroundEvidence atomic.Bool
	turnEntered := make(chan struct{}, 1)
	releaseTurn := make(chan struct{})
	const steeringText = "CHANNEL-STEERING-CORRECTION"
	const appSteering = "APP-STEERING-BARRIER"
	const groupInput = "INDEPENDENT-GROUP-WORK"
	const groupReply = "Independent group completed."
	groupSent := make(chan string, 1)
	const workResult = "Verified background skill inventory."
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		var request struct {
			Instructions string
			Input, Tools []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(string(mustJSON(t, request.Input)), groupInput) {
			if len(request.Tools) != 0 || strings.Contains(string(mustJSON(t, request.Input)), steeringText) {
				t.Error("group scope leaked private context or tools")
			}
			emitResponse(w, groupReply, true)
			return
		}
		if strings.Contains(request.Instructions, "self-contained background brief") {
			if background.Add(1) == 1 {
				emitPersonaCall(w, "worker-read", "skill_index", map[string]any{})
				return
			}
			if !strings.Contains(string(mustJSON(t, request.Input)), "function_call_output") {
				t.Error("worker did not execute the read tool")
			}
			emitResponse(w, string(mustJSON(t, runs.Artifact{Body: workResult, Done: true})), true)
			return
		}
		switch foreground.Add(1) {
		case 1:
			turnEntered <- struct{}{}
			select {
			case <-releaseTurn:
			case <-r.Context().Done():
				return
			}
			emitPersonaCall(w, "channel-work", "start_run", runs.Brief{Goal: "Read the skill inventory", Scope: "Read instruction metadata", NotTouch: "No writes", Done: "Inventory returned", Checks: []runs.Check{{Name: "skill_index", Kind: runs.ToolCalled}}, Result: "summary", Allow: []string{"skill_index"}})
		case 2:
			if !strings.Contains(string(mustJSON(t, request.Input)), steeringText) || !strings.Contains(string(mustJSON(t, request.Input)), appSteering) {
				t.Error("active turn missed cross-surface steering")
			}
			found := false
			for _, raw := range request.Input {
				var item struct{ Type, Output string }
				_ = json.Unmarshal(raw, &item)
				if item.Type == "function_call_output" {
					found = strings.Contains(item.Output, `"code":"ok"`)
					if !found {
						t.Errorf("start_run refused: %s", item.Output)
					}
				}
			}
			if !found {
				t.Error("missing start_run receipt")
			}
			emitResponse(w, reply, true)
		default:
			for _, raw := range request.Input {
				var item struct{ Role, Content string }
				if json.Unmarshal(raw, &item) != nil || !strings.Contains(item.Content, workResult) {
					continue
				}
				if item.Role != "user" || !strings.Contains(item.Content, "<external_evidence origin=") || !strings.Contains(item.Content, "</external_evidence>") {
					t.Errorf("background result became trusted history: %s", raw)
				} else {
					backgroundEvidence.Store(true)
				}
			}
			if !backgroundEvidence.Load() {
				t.Error("next model request lost untrusted background result")
			}
			if len(request.Tools) != 0 {
				t.Error("tools-disabled binding still offered tools")
			}
			emitResponse(w, reply, true)
		}
	}))
	defer provider.Close()
	presented := make(chan string, 1)
	edits := make(chan struct{}, 4)
	var buttonAnswers atomic.Int32
	frames := make(chan any, 16)
	sent := make(chan string, 16)
	type gatewayAuth struct {
		Op int `json:"op"`
		D  struct {
			Token   string `json:"token"`
			Session string `json:"session_id"`
			Seq     int64  `json:"seq"`
		} `json:"d"`
	}
	identified := make(chan gatewayAuth, 4)
	var sessions atomic.Int32
	var blocked atomic.Bool
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway" {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			if wsjson.Write(ctx, conn, map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 1000}}) != nil {
				return
			}
			var identify gatewayAuth
			if wsjson.Read(ctx, conn, &identify) != nil {
				return
			}
			if identify.D.Token != "synthetic.discord.token" {
				t.Error("missing identify credential")
				return
			}
			n := sessions.Add(1)
			ready := map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{"session_id": fmt.Sprintf("session-%d", n), "user": map[string]any{"id": scenario.Account, "bot": true}}}
			if identify.Op == 6 {
				ready = map[string]any{"op": 0, "t": "RESUMED", "s": identify.D.Seq, "d": map[string]any{}}
			}
			if wsjson.Write(ctx, conn, ready) != nil {
				return
			}
			identified <- identify
			joined := make(chan struct{})
			go func() {
				defer close(joined)
				for {
					var heartbeat struct {
						Op int `json:"op"`
					}
					if wsjson.Read(ctx, conn, &heartbeat) != nil {
						cancel()
						return
					}
					if heartbeat.Op == 1 {
						if wsjson.Write(ctx, conn, map[string]any{"op": 11, "d": nil}) != nil {
							cancel()
							return
						}
					}
				}
			}()
			defer func() { cancel(); conn.CloseNow(); <-joined }()
			for {
				select {
				case <-ctx.Done():
					return
				case frame := <-frames:
					if code, ok := frame.(websocket.StatusCode); ok {
						_ = conn.Close(code, "synthetic invalid session")
						return
					}
					if wsjson.Write(ctx, conn, frame) != nil {
						return
					}
				}
			}
		}
		if r.URL.Path == "/api/v10/applications/123456/commands" {
			fmt.Fprint(w, `{"id":"700"}`)
			return
		}
		if r.URL.Path == "/api/v10/interactions/51/button-secret/callback" {
			buttonAnswers.Add(1)
			w.WriteHeader(204)
			return
		}
		if r.Method == http.MethodPatch && r.URL.Path == "/api/v10/channels/100/messages/400" {
			edits <- struct{}{}
			fmt.Fprint(w, `{"id":"400"}`)
			return
		}
		if r.URL.Path == "/api/v10/interactions/12/interaction-secret/callback" {
			w.WriteHeader(204)
			return
		}
		if r.Header.Get("Authorization") != "Bot synthetic.discord.token" {
			t.Error("missing REST credential")
		}
		if r.URL.Path == "/api/v10/users/@me" {
			fmt.Fprintf(w, `{"id":%q,"bot":true}`, scenario.Account)
			return
		}
		if r.URL.Path == "/api/v10/channels/101" {
			fmt.Fprint(w, `{"id":"101","type":0}`)
			return
		}
		if r.Method == http.MethodPost && (r.URL.Path == "/api/v10/channels/100/messages" || r.URL.Path == "/api/v10/channels/101/messages") {
			var body struct {
				Components []struct {
					Components []struct {
						ID string `json:"custom_id"`
					} `json:"components"`
				} `json:"components"`
				Content  string `json:"content"`
				Mentions struct {
					Parse []string `json:"parse"`
				} `json:"allowed_mentions"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("bad send body")
			}
			if len(body.Mentions.Parse) != 0 {
				t.Error("mentions enabled")
			}
			if r.URL.Path == "/api/v10/channels/101/messages" {
				fmt.Fprint(w, `{"id":"900"}`)
				groupSent <- body.Content
				return
			}
			if len(body.Components) > 0 {
				fmt.Fprint(w, `{"id":"400"}`)
				presented <- body.Components[0].Components[0].ID
				return
			}
			count := sendCalls.Add(1)
			if blocked.Load() {
				w.WriteHeader(403)
				fmt.Fprintf(w, `{"code":%d}`, scenario.Blocked)
			} else {
				fmt.Fprintf(w, `{"id":%q}`, fmt.Sprint(300+count))
			}
			sent <- body.Content
			return
		}
		t.Errorf("unexpected Discord request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(500)
	}))
	defer platform.Close()
	lina, node := buildLina(t), buildCommand(t, "lina-node")
	h := newHarness(t, lina)
	bundle := makeInstallBundle(t, h.root, os.Args[0], node)
	h, _ = installConversationBundle(t, h, bundle, provider.URL)
	// Installation/build work is outside the bounded protocol scenario. This
	// journey includes ten bounded control/work/restart phases; unlike a single
	// conversation exchange it must not share one three-operation deadline.
	scenarioCtx, scenarioCancel := context.WithTimeout(t.Context(), 10*operationTimeout)
	t.Cleanup(scenarioCancel)
	h.ctx = scenarioCtx
	start := func() *process {
		p := h.command("-test.run=^TestDiscordBootHelper$")
		p.command.Env = append(p.command.Env, "LINA_TEST_DISCORD_ROOT="+h.root, "LINA_TEST_DISCORD_SOCKET="+h.socket, "LINA_TEST_DISCORD_HTTP="+platform.URL, "LINA_TEST_DISCORD_PROVIDER="+provider.URL)
		p.start(t)
		h.connect(p).Close()
		waitCoreReady(t, h)
		return p
	}
	core := start()
	defer func() {
		select {
		case <-core.done:
		default:
			h.stopCore(core)
		}
	}()
	connect := func() *client {
		conn := h.connect(core)
		c := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
		hello := clientHello([]string{"1.0"})
		hello.Capabilities = []protocol.Capability{{Name: "channels", Version: "1.0"}, {Name: "conversation", Version: "1.0"}}
		answer := c.call(t, protocol.KindHello, hello)
		if answer.Error != nil {
			t.Fatal(answer.Error)
		}
		c.welcome = answer.Payload.(protocol.Welcome)
		return c
	}
	c := connect()
	defer c.conn.Close()
	control := func(p protocol.ChannelRequest) protocol.ChannelResult {
		t.Helper()
		answer := c.call(t, protocol.KindChannelRequest, p)
		if answer.Error != nil {
			t.Fatalf("channel %s: %+v", *p.Operation, answer.Error)
		}
		return answer.Payload.(protocol.ChannelResult)
	}
	str := func(s string) *string { return &s }
	waitAuth := func(op int, session string, seq int64) {
		t.Helper()
		select {
		case auth := <-identified:
			if auth.Op != op || auth.D.Session != session || auth.D.Seq != seq {
				t.Fatalf("Gateway auth: op=%d session=%q seq=%d, want %d %q %d", auth.Op, auth.D.Session, auth.D.Seq, op, session, seq)
			}
		case <-h.ctx.Done():
			t.Fatal("Gateway not identified")
		}
	}
	control(protocol.ChannelRequest{Operation: str("connect"), Platform: &scenario.Platform, Token: str("synthetic.discord.token")})
	waitAuth(2, "", 0)
	pair := control(protocol.ChannelRequest{Operation: str("pair"), Platform: &scenario.Platform, Account: &scenario.Account})
	if pair.PairingCode == nil {
		t.Fatal("no pairing code")
	}
	seq := int64(1)
	message := func(id, owner, text string) {
		seq++
		frames <- map[string]any{"op": 0, "s": seq, "t": "MESSAGE_CREATE", "d": map[string]any{"id": id, "channel_id": scenario.Chat, "content": text, "author": map[string]any{"id": owner}}}
	}
	message("9", scenario.Owner, "UNPAIRED-MUST-NOT-DISPATCH")
	message("10", scenario.Owner, *pair.PairingCode)
	waitConnection := func(predicate func(protocol.ChannelConnection) bool) {
		t.Helper()
		for {
			result := control(protocol.ChannelRequest{Operation: str("list")})
			for _, row := range *result.Connections {
				if predicate(row) {
					return
				}
			}
			select {
			case <-h.ctx.Done():
				t.Fatal("channel state did not arrive")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	waitConnection(func(row protocol.ChannelConnection) bool { return row.PrivateChat == scenario.Chat })
	if len(c.welcome.Agents) == 0 {
		t.Fatal("no agent")
	}
	agent := c.welcome.Agents[0].AgentID
	control(protocol.ChannelRequest{Operation: str("bind"), Platform: &scenario.Platform, Account: &scenario.Account, AgentID: &agent, Scope: &protocol.ChannelScope{Reads: "dm_only", Writes: "reply_only", Tools: true, Memory: true}})

	var custom string
	select {
	case custom = <-presented:
	case <-h.ctx.Done():
		t.Fatal("Core did not present canonical approval")
	}
	reader, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	barrier := func(id string) {
		t.Helper()
		message(id, "999", "BARRIER")
		for {
			var n int
			err := reader.Read(h.ctx, func(tx *canon.Tx) error {
				return tx.QueryRowContext(h.ctx, "SELECT count(*) FROM channel_receipts WHERE native_id=?", id).Scan(&n)
			})
			if err != nil {
				t.Fatal(err)
			}
			if n == 1 {
				return
			}
			select {
			case <-h.ctx.Done():
				t.Fatal("Gateway barrier not admitted")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	approvalState := func() authority.Approval {
		t.Helper()
		var raw []byte
		if err := reader.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, "SELECT record FROM authority_approvals WHERE approval_id='discord_approval'").Scan(&raw)
		}); err != nil {
			t.Fatal(err)
		}
		var a authority.Approval
		if json.Unmarshal(raw, &a) != nil {
			t.Fatal("invalid canonical approval")
		}
		return a
	}
	button := func(id, owner string) {
		seq++
		frames <- map[string]any{"op": 0, "s": seq, "t": "INTERACTION_CREATE", "d": map[string]any{"id": id, "application_id": scenario.Account, "type": 3, "token": "button-secret", "channel_id": scenario.Chat, "user": map[string]any{"id": owner}, "data": map[string]any{"custom_id": custom}, "message": map[string]any{"id": "400", "author": map[string]any{"id": scenario.Account, "bot": true}}}}
	}
	button("50", "999")
	barrier("52")
	if approvalState().State != authority.Pending || len(edits) != 0 || buttonAnswers.Load() != 0 {
		t.Fatal("non-owner press changed state")
	}
	button("51", scenario.Owner)
	button("51", scenario.Owner)
	barrier("53")
	if approvalState().State != authority.Approved || len(edits) != 1 || buttonAnswers.Load() != 1 {
		t.Fatal("owner interaction was not handled exactly once")
	}
	message("11", "999", "MUST-NOT-DISPATCH")
	interaction := func() {
		seq++
		frames <- map[string]any{"op": 0, "s": seq, "t": "INTERACTION_CREATE", "d": map[string]any{"id": "12", "application_id": scenario.Account, "type": 2, "token": "interaction-secret", "channel_id": scenario.Chat, "user": map[string]any{"id": scenario.Owner}, "data": map[string]any{"name": "lina", "options": []any{map[string]any{"name": "text", "value": scenario.Input}}}}}
	}
	interaction()
	interaction()
	select {
	case <-turnEntered:
	case <-h.ctx.Done():
		t.Fatal("initial channel turn did not start")
	}
	message("14", scenario.Owner, steeringText)
	for {
		var conversation string
		err := reader.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, "SELECT conversation_id FROM conv_event WHERE json_extract(origin,'$.locator')='14'").Scan(&conversation)
		})
		if err == nil {
			if conversation != "main:"+agent {
				t.Fatalf("private channel has isolated history: %s", conversation)
			}
			break
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		select {
		case <-h.ctx.Done():
			t.Fatal("active channel turn blocked steering admission")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Reject each established session while real Core work is pending. The
	// supervisor must Identify again without replaying admitted interactions.
	for i, code := range closeCodes {
		barrier(fmt.Sprint(60 + i))
		frames <- code
		waitAuth(2, "", 0)
		seq = 1
		interaction()
		button("51", scenario.Owner)
		barrier(fmt.Sprint(70 + i))
		if approvalState().State != authority.Approved || len(edits) != 1 || buttonAnswers.Load() != 1 {
			t.Fatal("session reset lost the native interaction receipt")
		}
	}

	control(protocol.ChannelRequest{Operation: str("bind"), Platform: &scenario.Platform, Account: &scenario.Account, Chat: str("101"), AgentID: &agent, Scope: &protocol.ChannelScope{Reads: "addressed", Writes: "reply_only"}})
	seq++
	frames <- map[string]any{"op": 0, "s": seq, "t": "MESSAGE_CREATE", "d": map[string]any{"id": "15", "channel_id": "101", "guild_id": "800", "content": groupInput, "author": map[string]any{"id": scenario.Owner}, "mentions": []any{map[string]any{"id": scenario.Account, "bot": true}}}}
	select {
	case text := <-groupSent:
		if text != groupReply {
			t.Fatal("group result changed")
		}
	case <-h.ctx.Done():
		t.Fatal("active private turn stalled another chat")
	}
	answer := c.call(t, protocol.KindInput, protocol.Input{ConversationID: str("main:" + agent), InputID: str("app-steering"), Text: str(appSteering)})
	if answer.Error != nil {
		t.Fatalf("main-conversation steering failed: %+v", answer.Error)
	}
	close(releaseTurn)

	var parts []string
	for len(parts) < 2 {
		select {
		case text := <-sent:
			parts = append(parts, text)
		case <-h.ctx.Done():
			t.Fatalf("no complete channel reply; calls=%d", modelCalls.Load())
		}
	}
	if strings.Join(parts, "") != reply || !strings.HasSuffix(parts[0], "\n\n") {
		t.Fatalf("reply split lost paragraph: lengths %d %d", len(parts[0]), len(parts[1]))
	}
	select {
	case text := <-sent:
		if !strings.Contains(text, workResult) {
			t.Fatalf("missing worker result: %s", text)
		}
	case <-h.ctx.Done():
		t.Fatal("background result did not reach Discord")
	}
	control(protocol.ChannelRequest{Operation: str("bind"), Platform: &scenario.Platform, Account: &scenario.Account, AgentID: &agent, Scope: &protocol.ChannelScope{Reads: "dm_only", Writes: "reply_only", Tools: false, Memory: false}})
	blocked.Store(true)
	message("13", scenario.Owner, "Return another result")
	select {
	case <-sent:
	case <-h.ctx.Done():
		t.Fatalf("blocked delivery not attempted: model=%d foreground=%d background=%d sends=%d", modelCalls.Load(), foreground.Load(), background.Load(), sendCalls.Load())
	}
	waitConnection(func(row protocol.ChannelConnection) bool {
		for _, binding := range row.Bindings {
			if binding.Chat == "100" && binding.Unreachable {
				return true
			}
		}
		return false
	})
	c.conn.Close()
	h.stopCore(core)
	if modelCalls.Load() != 6 || sendCalls.Load() != 4 {
		t.Fatalf("duplicate/external dispatch: model=%d sends=%d", modelCalls.Load(), sendCalls.Load())
	}
	core = start()
	waitAuth(6, fmt.Sprintf("session-%d", len(closeCodes)+1), seq)
	c = connect()
	defer c.conn.Close()
	waitConnection(func(row protocol.ChannelConnection) bool {
		for _, binding := range row.Bindings {
			if binding.Chat == "100" && binding.Unreachable {
				return true
			}
		}
		return false
	})
	c.conn.Close()
	h.stopCore(core)
	if modelCalls.Load() != 6 || sendCalls.Load() != 4 {
		t.Fatal("restart replayed work")
	}
}

func TestDiscordBootHelper(t *testing.T) {
	root := os.Getenv("LINA_TEST_DISCORD_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	route, err := modelpath.ResponsesFromEnv(func(k string) string {
		return map[string]string{"LINA_RESPONSES_BASE_URL": os.Getenv("LINA_TEST_DISCORD_PROVIDER") + "/v1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse(os.Getenv("LINA_TEST_DISCORD_HTTP"))
	if err != nil {
		t.Fatal(err)
	}
	var epoch atomic.Uint64
	transport := pairingTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/api/v10/applications/123456/commands" {
			seedDiscordApproval(t, root, epoch.Load())
		}
		wire := req.Clone(req.Context())
		wire.URL.Scheme = endpoint.Scheme
		wire.URL.Host = endpoint.Host
		if req.URL.Host == "gateway.discord.gg" {
			wire.URL.Path = "/gateway"
		}
		return http.DefaultTransport.RoundTrip(wire)
	})
	ctx, cancel := signal.NotifyContext(t.Context(), syscall.SIGTERM)
	defer cancel()
	if err = boot.Run(ctx, boot.Config{StateRoot: root, Socket: os.Getenv("LINA_TEST_DISCORD_SOCKET"), Conversation: route, ChannelTransport: transport, AfterStep: func(step boot.Step, s boot.Snapshot) error {
		if s.Epoch > 0 {
			epoch.Store(s.Epoch)
		}
		return nil
	}}); err != nil {
		fmt.Fprintln(os.Stderr, "Discord Core helper:", err)
		t.Fatal(err)
	}
}

// This synthetic producer inserts one canonical pending request after production
// authority startup. All presentation, socket and decision handling stays in
// Core; no test handler answers the request or bypasses the paired-owner gate.
func seedDiscordApproval(t *testing.T, root string, epoch uint64) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(root, "canon", "canon.sqlite")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var agent, person string
	if err = db.QueryRow("SELECT agent_id,person_id FROM agent_revisions LIMIT 1").Scan(&agent, &person); err != nil {
		t.Fatal(err)
	}
	a := authority.Approval{ID: "discord_approval", EffectKey: "discord_effect", Epoch: epoch, Person: person, Agent: agent, State: authority.Pending, Choices: []authority.Choice{authority.Once, authority.Deny}, What: "Synthetic approval", Where: "Synthetic destination", Arguments: json.RawMessage(`{}`)}
	data, _ := json.Marshal(a)
	if _, err = db.Exec("INSERT INTO authority_approvals(approval_id,effect_key,fingerprint,record) VALUES(?,?,?,?)", a.ID, a.EffectKey, "fixture", data); err != nil {
		t.Fatal(err)
	}
}
