// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/protocol/conformance"
)

func TestM1AgentLifecycleConformance(t *testing.T) {
	raw, err := os.ReadFile("m1/conformance-agents.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Corpus string `json:"corpus"`
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	cases, err := conformance.LoadApplications(os.DirFS(scenario.Corpus))
	if err != nil {
		t.Fatal(err)
	}
	binary := buildLina(t)
	bundle := makeInstallBundle(t, t.TempDir(), binary, buildCommand(t, "lina-node"))
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Service != "client" {
				t.Fatalf("unhandled service %q", c.Service)
			}
			var setup struct {
				Agent string `json:"agent"`
				Bound bool   `json:"bound"`
			}
			if err := json.Unmarshal(c.Setup, &setup); err != nil {
				t.Fatal(err)
			}
			h, start := installConversationBundle(t, newHarness(t, binary), bundle, "http://127.0.0.1:1")
			target := seedConformanceAgents(t, h, setup.Agent, setup.Bound)
			before := readConformanceAgentState(t, h, target)
			// A configured route installs conversation admission. No valid input may
			// reach it: these cases must fail on agent identity before model execution.
			core := start(true)
			conn := h.connect(core)
			client := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
			hello := clientHello([]string{"1.0"})
			hello.Capabilities = []protocol.Capability{{Name: "agents", Version: "1.0"}, {Name: "conversation", Version: "1.0"}, {Name: "channels", Version: "1.0"}}
			answer := client.call(t, protocol.KindHello, hello)
			welcome, ok := answer.Payload.(protocol.Welcome)
			if !ok || answer.Error != nil {
				t.Fatalf("welcome: %+v", answer)
			}
			client.welcome = welcome
			if !reflect.DeepEqual(welcome.Capabilities, hello.Capabilities) {
				// Capability order is chosen by Core; require every offered capability.
				for _, offered := range hello.Capabilities {
					found := false
					for _, granted := range welcome.Capabilities {
						found = found || offered == granted
					}
					if !found {
						t.Fatalf("capability %s not granted", offered.Name)
					}
				}
			}
			err = conformance.RunApplication(c, conformance.Application{
				Execute: func(raw json.RawMessage) (json.RawMessage, error) {
					// Only the fixture's symbolic target is substituted. Session identity,
					// epoch and correlation are supplied and checked by the socket client.
					source := strings.ReplaceAll(string(raw), `"agent-example"`, fmt.Sprintf("%q", target))
					source = strings.ReplaceAll(source, `"main:agent-example"`, fmt.Sprintf("%q", "main:"+target))
					var request protocol.Envelope
					if err := json.Unmarshal([]byte(source), &request); err != nil {
						return nil, err
					}
					reply := client.call(t, request.Kind, request.Payload)
					var code protocol.ErrorCode
					if reply.Error != nil {
						code = reply.Error.Code
					}
					return json.Marshal(map[string]any{"kind": reply.Kind, "effect_state": reply.EffectState, "code": code})
				},
				Observe: func() (json.RawMessage, error) {
					after := readConformanceAgentState(t, h, target)
					return json.Marshal(map[string]any{
						"state_unchanged": reflect.DeepEqual(before, after),
						"target_records":  after.TargetRecords, "target_active": after.TargetActive,
						"target_bindings": after.TargetBindings, "bindings": after.Bindings,
					})
				},
			})
			conn.Close()
			h.stopCore(core)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Seed only prerequisites through the production stores while Core is stopped.
// Channel pairing and an existing binding make refusals distinguish a deleted
// target from a missing bot, missing pairing or an untested scope replacement.
func seedConformanceAgents(t *testing.T, h *harness, state string, bound bool) string {
	t.Helper()
	ctl, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	raw, err := ctl.Read("main.json")
	if err != nil {
		t.Fatal(err)
	}
	var main struct {
		ID string `json:"main_id"`
	}
	if err = json.Unmarshal(raw, &main); err != nil {
		t.Fatal(err)
	}
	epoch, err := ctl.MintEpoch(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon", "canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: boot.ApplicationMigrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	store, err := agents.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	person := "person:" + main.ID
	first, err := store.FirstRun(h.ctx, person, true)
	if err != nil {
		t.Fatal(err)
	}
	target := "missing-agent"
	switch state {
	case "deleted":
		a, err := store.Create(h.ctx, agents.Create{Name: "Fixture target", PersonID: person, Preset: "lina"})
		if err != nil {
			t.Fatal(err)
		}
		target = a.ID
		if err := store.Delete(h.ctx, target, person, time.Now()); err != nil {
			t.Fatal(err)
		}
	case "missing":
	default:
		t.Fatalf("unknown agent setup %q", state)
	}
	channel := channels.New(db)
	bot := channels.Bot{Platform: "telegram", Account: "fixture-bot"}
	if err := channel.Connect(h.ctx, bot, person); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	code, err := channel.PairingCode(h.ctx, bot, person, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.Pair(h.ctx, bot, code, "42", "42", now); err != nil {
		t.Fatal(err)
	}
	if bound {
		if err := channel.Bind(h.ctx, channels.Binding{Address: channels.Address{Bot: bot, Chat: "42"}, Agent: first.Agent.ID, Person: person, Conversation: "fixture-channel", Scope: channels.PrivateScope()}); err != nil {
			t.Fatal(err)
		}
	}
	return target
}

type conformanceAgentState struct {
	TargetRecords, TargetActive, TargetBindings, Bindings int
	Rows                                                  []string
}

func readConformanceAgentState(t *testing.T, h *harness, target string) conformanceAgentState {
	t.Helper()
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state conformanceAgentState
	err = db.QueryRow(`SELECT (SELECT count(*) FROM agents WHERE id=?),
 (SELECT count(*) FROM agents WHERE id=? AND trashed_at=0),
 (SELECT count(*) FROM agent_bindings WHERE agent_id=?),
 (SELECT count(*) FROM agent_bindings)`, target, target, target).Scan(&state.TargetRecords, &state.TargetActive, &state.TargetBindings, &state.Bindings)
	if err != nil {
		t.Fatal(err)
	}
	// Read complete relevant rows, including previous channel scope and agent
	// revisions. A refusal must neither append events nor change existing data.
	for _, table := range []string{"agents", "agent_revisions", "agent_actions", "agent_bindings", "channel_bindings", "channel_actions", "conv_event", "turn_runs"} {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			data, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			state.Rows = append(state.Rows, table+":"+string(data))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return state
}
