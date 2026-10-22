// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/protocol"
)

type cardScenario struct {
	Race struct {
		ID, Scope string
		Outcomes  []protocol.DecisionOutcome
	}
	ChangedGroup struct {
		ID, Scope string
		AddedID   string `json:"added_id"`
		Outcome   protocol.DecisionOutcome
	} `json:"changed_group"`
}

func readCardScenario(t *testing.T) cardScenario {
	t.Helper()
	data, err := os.ReadFile("tui-cards.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario cardScenario
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	return scenario
}

// The helper is the actual Core listener in a separate process. Its parent
// owns the fake clients, so the production ancestry/decider gate is exercised
// without a test bypass. Both processes use #34's private harness root.
func TestDecisionCoreHelper(t *testing.T) {
	root := os.Getenv("LINA_TEST_DECISION_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	scenario := readCardScenario(t)
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	auth, err := authority.Open(t.Context(), db, authority.Config{Epoch: 1, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
		return grant.Grant{}, grant.Environment{}, errors.New("unused")
	}})
	if err != nil {
		t.Fatal(err)
	}
	seed := func(id, scope string) {
		a := authority.Approval{ID: id, EffectKey: id, ScopeKey: scope, Epoch: 1, Person: "person", Agent: "agent", State: authority.Pending, What: "Send document", Where: "recipient@example.invalid", Target: "document", Arguments: json.RawMessage(`{"body":"document"}`), Choices: []authority.Choice{authority.Once, authority.Deny}, Outward: true}
		data, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.Write(t.Context(), func(tx *canon.Tx) error {
			_, err := tx.ExecContext(t.Context(), "INSERT INTO authority_approvals(approval_id,effect_key,fingerprint,record) VALUES(?,?,?,?)", id, id, id, data)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed(scenario.Race.ID, scenario.Race.Scope)
	seed(scenario.ChangedGroup.ID, scenario.ChangedGroup.Scope)
	hub, err := clientsrv.NewTopics("1", clientsrv.TopicOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	service := &clientsrv.Decisions{Authority: auth, Topics: hub, Person: "person", AgentName: func(string) string { return "Agent" }}
	if err := service.PublishApprovals(t.Context()); err != nil {
		t.Fatal(err)
	}
	listener, err := clientsrv.Listen(filepath.Join(root, "run", "client.sock"))
	if err != nil {
		t.Fatal(err)
	}
	rows := clientsrv.LocalCombinations()
	for i := range rows {
		rows[i].Capabilities = []protocol.Capability{clientsrv.DecisionsCapability}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- clientsrv.Serve(ctx, listener, clientsrv.Identity{MainID: "main-cards", PersonID: "person", Epoch: "1", Supported: rows, Operations: service.Operations(), Topics: hub, TopicCapabilities: map[string]protocol.Capability{"waiting": clientsrv.DecisionsCapability}})
	}()
	fmt.Println("ready")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		switch scanner.Text() {
		case "add":
			seed(scenario.ChangedGroup.AddedID, scenario.ChangedGroup.Scope)
			if err := service.PublishApprovals(ctx); err != nil {
				t.Fatal(err)
			}
			fmt.Println("added")
		case "quit":
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		default:
			t.Fatal("unknown test control")
		}
	}
	cancel()
	<-done
}

func TestM1TUICards(t *testing.T) {
	t.Parallel()

	scenario := readCardScenario(t)
	h := newHarness(t, "")
	child := exec.CommandContext(h.ctx, os.Args[0], "-test.run=^TestDecisionCoreHelper$")
	child.Env = []string{"LINA_TEST_DECISION_ROOT=" + h.root}
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	joined := false
	t.Cleanup(func() {
		input.Close()
		if !joined {
			child.Process.Kill()
			child.Wait()
		}
	})
	scanner := bufio.NewScanner(output)
	observe := func(want string) {
		t.Helper()
		if !scanner.Scan() || scanner.Text() != want {
			t.Fatalf("helper control=%q err=%v", scanner.Text(), scanner.Err())
		}
	}
	observe("ready")
	var clients []*clientapi.Client
	for range 2 {
		c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{clientsrv.DecisionsCapability})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		clients = append(clients, c)
	}
	topic := "waiting"
	if _, err = clients[0].Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	frame, err := clients[0].Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := frame.Payload.(protocol.Snapshot)
	if !ok {
		t.Fatalf("snapshot=%+v", frame)
	}
	cards := map[string]protocol.DecisionCard{}
	for _, item := range snapshot.Items {
		var approval protocol.ApprovalRequest
		if err = json.Unmarshal(item.Payload, &approval); err != nil {
			t.Fatal(err)
		}
		if approval.Card != nil {
			cards[approval.Card.ID] = *approval.Card
		}
	}
	race, exists := cards[scenario.Race.ID]
	if !exists {
		t.Fatal("race card missing")
	}
	// Close the subscription before racing; only correlated replies are consumed.
	inactive := false
	if _, err = clients[0].Subscribe(h.ctx, protocol.Subscribe{Topic: &topic, Active: &inactive}); err != nil {
		t.Fatal(err)
	}
	if _, err = clients[0].Receive(h.ctx); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	outcomes := make(chan protocol.DecisionOutcome, 2)
	failures := make(chan error, 2)
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Go(func() {
			<-start
			_, err := c.AnswerApproval(h.ctx, protocol.ApprovalAnswer{CardID: &race.ID, Revision: &race.Revision, Choice: protocol.ApprovalChoiceOnce})
			if err != nil {
				failures <- err
				return
			}
			reply, err := c.Receive(h.ctx)
			if err != nil {
				failures <- err
				return
			}
			answer, ok := reply.Payload.(protocol.ApprovalAnswer)
			if !ok || answer.Outcome == nil {
				failures <- fmt.Errorf("decision reply: %+v", reply)
				return
			}
			outcomes <- *answer.Outcome
		})
	}
	close(start)
	wg.Wait()
	close(outcomes)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var got []protocol.DecisionOutcome
	for outcome := range outcomes {
		got = append(got, outcome)
	}
	slices.Sort(got)
	slices.Sort(scenario.Race.Outcomes)
	if !slices.Equal(got, scenario.Race.Outcomes) {
		t.Fatalf("race outcomes=%v", got)
	}
	group, exists := cards[scenario.ChangedGroup.ID]
	if !exists {
		t.Fatal("group card missing")
	}
	fmt.Fprintln(input, "add")
	observe("added")
	if _, err = clients[0].AnswerApproval(h.ctx, protocol.ApprovalAnswer{CardID: &group.ID, Revision: &group.Revision, Choice: protocol.ApprovalChoiceOnce}); err != nil {
		t.Fatal(err)
	}
	reply, err := clients[0].Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	answer, ok := reply.Payload.(protocol.ApprovalAnswer)
	if !ok || answer.Outcome == nil || *answer.Outcome != scenario.ChangedGroup.Outcome {
		t.Fatalf("changed group=%+v", reply)
	}
	for _, c := range clients {
		c.Close()
	}
	fmt.Fprintln(input, "quit")
	input.Close()
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	joined = true
}
