// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/protocol"
)

type questionScenario struct {
	Question protocol.Question
	Values   protocol.FormValues
	Outcome  protocol.DecisionOutcome
}

func readQuestionScenario(t *testing.T) questionScenario {
	t.Helper()
	data, err := os.ReadFile("tui-questions.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario questionScenario
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	return scenario
}

// This is the scenario's fake question owner, not a product decision store.
// Its lock models the source's atomic revision/answer operation.
type scenarioQuestionSource struct {
	mu       sync.Mutex
	scenario questionScenario
	calls    int
	answered bool
}

func (s *scenarioQuestionSource) Question(_ context.Context, person, id string) (protocol.Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if person != "person" || id != s.scenario.Question.Card.ID {
		return protocol.Question{}, &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	}
	q := s.scenario.Question
	card := *q.Card
	q.Card = &card
	if s.answered {
		outcome := s.scenario.Outcome
		q.Card.Outcome = &outcome
	}
	return q, nil
}
func (s *scenarioQuestionSource) AnswerQuestion(_ context.Context, person string, answer protocol.QuestionAnswer) (protocol.DecisionOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if person != "person" || answer.CardID == nil || *answer.CardID != s.scenario.Question.Card.ID {
		return "", &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	}
	if answer.Revision == nil || *answer.Revision != s.scenario.Question.Card.Revision {
		return protocol.DecisionOutcomeStaleGroup, nil
	}
	if s.answered {
		return protocol.DecisionOutcomeAlreadyResolved, nil
	}
	if answer.Answer != protocol.QuestionAnswerActionAccept || answer.Values == nil || !reflect.DeepEqual(*answer.Values, s.scenario.Values) {
		return "", &clientsrv.OperationError{Code: protocol.ErrorMalformed}
	}
	s.calls++
	s.answered = true
	return s.scenario.Outcome, nil
}
func TestQuestionCoreHelper(t *testing.T) {
	root := os.Getenv("LINA_TEST_QUESTION_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	source := &scenarioQuestionSource{scenario: readQuestionScenario(t)}
	hub, err := clientsrv.NewTopics("1", clientsrv.TopicOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	service := &clientsrv.Decisions{Questions: source, Topics: hub, Person: "person"}
	if err := service.PublishQuestion(t.Context(), source.scenario.Question.Card.ID); err != nil {
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
		done <- clientsrv.Serve(ctx, listener, clientsrv.Identity{MainID: "main-questions", PersonID: "person", Epoch: "1", Supported: rows, Operations: service.Operations(), Topics: hub, TopicCapabilities: map[string]protocol.Capability{"waiting": clientsrv.DecisionsCapability}})
	}()
	fmt.Println("ready")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if scanner.Text() == "quit" {
			break
		}
		t.Fatal("unknown control")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.calls != 1 {
		t.Fatalf("source calls=%d", source.calls)
	}
}
func TestM1TUIQuestions(t *testing.T) {
	scenario := readQuestionScenario(t)
	h := newHarness(t, "")
	child := exec.CommandContext(h.ctx, os.Args[0], "-test.run=^TestQuestionCoreHelper$")
	child.Env = []string{"LINA_TEST_QUESTION_ROOT=" + h.root}
	child.Stderr = os.Stderr
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
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
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatalf("helper=%q err=%v", scanner.Text(), scanner.Err())
	}
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{clientsrv.DecisionsCapability})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	receive := func(id string) protocol.Envelope {
		t.Helper()
		for range 10 {
			frame, err := c.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if frame.ReplyTo != nil && *frame.ReplyTo == id {
				return frame
			}
		}
		t.Fatal("response missing")
		return protocol.Envelope{}
	}
	topic := "waiting"
	id, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := receive(id).Payload.(protocol.Snapshot)
	if len(snapshot.Items) != 1 {
		t.Fatal("question missing")
	}
	var shown protocol.Question
	if err = json.Unmarshal(snapshot.Items[0].Payload, &shown); err != nil {
		t.Fatal(err)
	}
	if shown.Card == nil || !reflect.DeepEqual(shown.Form, scenario.Question.Form) || shown.Card.Outcome != nil {
		t.Fatal("form projection mismatch")
	}
	answer := protocol.QuestionAnswer{CardID: &shown.Card.ID, Revision: &shown.Card.Revision, Answer: protocol.QuestionAnswerActionAccept, Values: &scenario.Values}
	id, err = c.AnswerQuestion(h.ctx, answer)
	if err != nil {
		t.Fatal(err)
	}
	reply := receive(id)
	got, ok := reply.Payload.(protocol.QuestionAnswer)
	if !ok || got.Outcome == nil || *got.Outcome != scenario.Outcome {
		t.Fatalf("answer=%+v", reply)
	}
	id, err = c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic})
	if err != nil {
		t.Fatal(err)
	}
	snapshot = receive(id).Payload.(protocol.Snapshot)
	if len(snapshot.Items) != 1 {
		t.Fatal("terminal card missing")
	}
	if err = json.Unmarshal(snapshot.Items[0].Payload, &shown); err != nil {
		t.Fatal(err)
	}
	if shown.Card.Outcome == nil || *shown.Card.Outcome != scenario.Outcome {
		t.Fatal("answered question remains pending in the subscribed view")
	}
	c.Close()
	fmt.Fprintln(input, "quit")
	input.Close()
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	joined = true
}
