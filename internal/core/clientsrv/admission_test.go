// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestCoreDescendantCannotDecide(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "client.sock")
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener, Identity{MainID: "main-test", Epoch: "18446744073709551615"}) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	for _, depth := range []string{"child", "grandchild"} {
		child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestDeciderChild$")
		child.Env = []string{"LINA_TEST_CLIENT_SOCKET=" + path, "LINA_TEST_DEPTH=" + depth}
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", depth, err, output)
		}
	}
}

func TestDeciderChild(t *testing.T) {
	path := os.Getenv("LINA_TEST_CLIENT_SOCKET")
	if path == "" {
		t.Skip("subprocess helper")
	}
	if os.Getenv("LINA_TEST_DEPTH") == "grandchild" {
		child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestDeciderChild$")
		child.Env = []string{"LINA_TEST_CLIENT_SOCKET=" + path}
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("grandchild: %v\n%s", err, output)
		}
		return
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(conn)
	hello := testHello(t)
	welcome := exchange(t, conn, reader, hello)
	if welcome.Kind != protocol.KindWelcome {
		t.Fatalf("hello: %+v", welcome)
	}
	for _, payload := range []protocol.Payload{
		protocol.ApprovalAnswer{Choice: protocol.ApprovalChoiceOnce},
		protocol.QuestionAnswer{Answer: protocol.QuestionAnswerActionDecline},
		protocol.WaitingAnswer{},
	} {
		req := hello
		req.Epoch = welcome.Epoch
		req.Payload = payload
		switch payload.(type) {
		case protocol.ApprovalAnswer:
			req.Kind = protocol.KindApprovalAnswer
		case protocol.QuestionAnswer:
			req.Kind = protocol.KindQuestionAnswer
		default:
			req.Kind = protocol.KindWaitingAnswer
		}
		answer := exchange(t, conn, reader, req)
		if answer.Error == nil || answer.Error.Code != protocol.ErrorNotAuthorized {
			t.Fatalf("%s from Core descendant = %+v, want not_authorized", req.Kind, answer.Error)
		}
	}
}

func testHello(t *testing.T) protocol.Envelope {
	t.Helper()
	data, err := os.ReadFile("../../../protocol/fixtures/client/1.0/hello.json")
	if err != nil {
		t.Fatal(err)
	}
	var hello protocol.Envelope
	if err := json.Unmarshal(data, &hello); err != nil {
		t.Fatal(err)
	}
	payload := hello.Payload.(protocol.Hello)
	platform := runtime.GOOS + "/" + runtime.GOARCH
	payload.Platform = &platform
	hello.Payload = payload
	return hello
}

func exchange(t *testing.T, conn net.Conn, reader *bufio.Reader, envelope protocol.Envelope) protocol.Envelope {
	t.Helper()
	if err := json.NewEncoder(conn).Encode(map[string]any{"jsonrpc": "2.0", "id": "call", "method": envelope.Kind, "params": envelope}); err != nil {
		t.Fatal(err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var answer response
	if err := json.Unmarshal(line, &answer); err != nil {
		t.Fatal(err)
	}
	if answer.JSONRPC != "2.0" || string(answer.ID) != `"call"` || answer.Result.ReplyTo == nil || *answer.Result.ReplyTo != envelope.MessageID {
		t.Fatalf("uncorrelated response: %s", line)
	}
	return answer.Result
}
