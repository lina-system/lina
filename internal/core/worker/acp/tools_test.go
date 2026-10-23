// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/worker"
)

func TestWorkToolsFenceSessionsTurnsAndAnswers(t *testing.T) {
	a, ctx, l, spec := fixture(t)
	spec.TaskPacket = "cancel"
	thread, err := a.Start(ctx, l, spec)
	if err != nil {
		t.Fatal(err)
	}
	b, _, bl, bs := fixture(t)
	bs.TaskPacket = "cancel"
	if _, err = b.Start(ctx, bl, bs); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(a.tools.dir, "core.sock")
	call := func(request clientapi.WorkerToolRequest) clientapi.WorkerToolResponse {
		t.Helper()
		response, err := clientapi.CallWorkerTool(ctx, socket, request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	request := clientapi.WorkerToolRequest{Token: a.tools.token, Method: "tools/list"}
	listed := call(request)
	want := worker.WorkToolDeclarations()
	if listed.Error != "" || len(listed.Tools) != len(want) {
		t.Fatal("tools unavailable")
	}
	for i, tool := range listed.Tools {
		var gotSchema, wantSchema any
		json.Unmarshal(tool.InputSchema, &gotSchema)
		json.Unmarshal(want[i].InputSchema, &wantSchema)
		if tool.Name != want[i].Name || !reflect.DeepEqual(gotSchema, wantSchema) {
			t.Fatal("tool schema drift")
		}
	}
	request.Method, request.Name, request.Arguments = "tools/call", "read_projection", json.RawMessage(`{}`)
	request.Token = b.tools.token
	if response := call(request); response.Error != "refused" {
		t.Fatal("another session's token accepted")
	}
	request.Token = a.tools.token
	response := make(chan clientapi.WorkerToolResponse, 1)
	go func() { r, _ := clientapi.CallWorkerTool(ctx, socket, request); response <- r }()
	var toolEvent worker.Event
	for toolEvent.ToolCall == nil {
		toolEvent = event(t, ctx, a)
	}
	if toolEvent.Thread != thread {
		t.Fatal("call identity is not the active execution")
	}
	answer := worker.Answer{RequestID: toolEvent.ToolCall.ID, Result: json.RawMessage(`{"success":true,"contentItems":[{"type":"inputText","text":"{\"accepted\":true}"}]}`)}
	foreign := thread
	foreign.ProcessGeneration++
	if a.Answer(ctx, foreign, answer) == nil {
		t.Fatal("foreign answer accepted")
	}
	if err = a.Answer(ctx, thread, answer); err != nil {
		t.Fatal(err)
	}
	if a.Answer(ctx, thread, answer) == nil {
		t.Fatal("duplicate answer accepted")
	}
	select {
	case result := <-response:
		var mcp struct {
			IsError bool
			Content []struct{ Type, Text string }
		}
		if result.Error != "" || json.Unmarshal(result.Result, &mcp) != nil || mcp.IsError || len(mcp.Content) != 1 || mcp.Content[0].Type != "text" {
			t.Fatal("invalid MCP answer")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// A pending ask expires with the prompt, independently of a human answer.
	request.Name = "ask"
	go func() { r, _ := clientapi.CallWorkerTool(ctx, socket, request); response <- r }()
	for {
		if event(t, ctx, a).ToolCall != nil {
			break
		}
	}
	if err = a.Interrupt(ctx, thread, worker.UserCancel); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(spec.Workdir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	terminal(t, ctx, a)
	select {
	case result := <-response:
		if result.Error != "refused" {
			t.Fatal("pending call survived turn end")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if result := call(request); result.Error != "refused" {
		t.Fatal("ended turn accepted call")
	}
	if err = a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Dir(socket)); !os.IsNotExist(err) {
		t.Fatal("session socket retained")
	}
}
