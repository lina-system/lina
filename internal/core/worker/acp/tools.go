// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/worker"
	wire "github.com/thisisjun786/lina/kit/acp"
	"github.com/thisisjun786/lina/kit/rpc"
)

type toolPending struct {
	thread worker.Thread
	result chan json.RawMessage
}

type toolServer struct {
	adapter    *Adapter
	listener   net.Listener
	dir, token string
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	clients    sync.WaitGroup
	once       sync.Once
}

func (a *Adapter) startTools() (wire.McpServer, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "lina-worker-tools-")
	if err != nil {
		return nil, err
	}
	socket := filepath.Join(dir, "core.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err = os.Chmod(socket, 0600); err != nil {
		listener.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	ctx, cancel := context.WithCancel(a.life)
	s := &toolServer{adapter: a, listener: listener, dir: dir, token: rand.Text(), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	a.tools = s
	go s.serve()
	return json.Marshal(wire.McpServerStdio{Name: "lina", Command: executable, Args: []string{"worker-tools"}, Env: []wire.EnvVariable{
		{Name: "LINA_WORKER_TOOLS_SOCKET", Value: socket},
		{Name: "LINA_WORKER_TOOLS_TOKEN", Value: s.token},
	}})
}

func (s *toolServer) serve() {
	defer close(s.done)
	stop := context.AfterFunc(s.ctx, func() { s.listener.Close() })
	defer stop()
	slots := make(chan struct{}, 32)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			break
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		s.clients.Add(1)
		go func() {
			defer s.clients.Done()
			defer func() { <-slots }()
			defer conn.Close()
			stop := context.AfterFunc(s.ctx, func() { conn.Close() })
			defer stop()
			// Bound incomplete ingress; ask may then wait for its owner until
			// the turn ends. Core's existing waiting policy owns that deadline.
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			raw, err := rpc.NewReader(conn).Read()
			if err != nil {
				return
			}
			var request clientapi.WorkerToolRequest
			response := clientapi.WorkerToolResponse{Error: "invalid_request"}
			if json.Unmarshal(raw, &request) == nil {
				response = s.call(request)
			}
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_ = rpc.NewWriter(conn).Write(s.ctx, response)
		}()
	}
	s.cancel()
	s.clients.Wait()
}

func (s *toolServer) close() {
	s.once.Do(func() {
		s.cancel()
		s.listener.Close()
		<-s.done
		os.RemoveAll(s.dir)
	})
}

func (s *toolServer) call(request clientapi.WorkerToolRequest) clientapi.WorkerToolResponse {
	refused := clientapi.WorkerToolResponse{Error: "refused"}
	if subtle.ConstantTimeCompare([]byte(request.Token), []byte(s.token)) != 1 {
		return refused
	}
	a := s.adapter
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return refused
	}
	if request.Method == "tools/list" {
		result := clientapi.WorkerToolResponse{Tools: []clientapi.WorkerTool{}}
		for _, tool := range a.spec.Tools {
			result.Tools = append(result.Tools, clientapi.WorkerTool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
		}
		a.mu.Unlock()
		return result
	}
	if request.Method != "tools/call" || !json.Valid(request.Arguments) {
		a.mu.Unlock()
		return clientapi.WorkerToolResponse{Error: "invalid_request"}
	}
	known := false
	for _, tool := range a.spec.Tools {
		known = known || tool.Name == request.Name && tool.Namespace == "lina"
	}
	if !known || a.state != worker.Active && a.state != worker.Waiting || len(a.pending) >= 32 {
		a.mu.Unlock()
		return refused
	}
	id := rand.Text()
	thread, ended := a.thread, a.turnEnded
	pending := toolPending{thread: thread, result: make(chan json.RawMessage, 1)}
	a.pending[id] = pending
	// Admission and finish share this lock, ordering every accepted call before
	// the terminal event. A full queue refuses before sending an event.
	select {
	case a.events <- worker.Event{Thread: thread, Kind: worker.ToolCallEvent, ToolCall: &worker.ToolCall{ID: id, CallID: id, Namespace: "lina", Name: request.Name, Arguments: request.Arguments}}:
	default:
		delete(a.pending, id)
		a.mu.Unlock()
		return refused
	}
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.pending, id); a.mu.Unlock() }()
	select {
	case result := <-pending.result:
		return clientapi.WorkerToolResponse{Result: result}
	case <-ended:
		return refused
	case <-s.ctx.Done():
		return refused
	}
}

// Core's common work-tool bridge returns the Codex response shape. Translate
// that one shared result to MCP, retaining only text content and success.
func (a *Adapter) answerTool(t worker.Thread, answer worker.Answer) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	pending, ok := a.pending[answer.RequestID]
	if !a.matches(t) || a.state != worker.Active && a.state != worker.Waiting || !ok || pending.thread != t {
		return ErrThread
	}
	var response struct {
		Success      bool                          `json:"success"`
		ContentItems []struct{ Type, Text string } `json:"contentItems"`
	}
	if err := json.Unmarshal(answer.Result, &response); err != nil {
		return err
	}
	content := []map[string]string{}
	for _, item := range response.ContentItems {
		if item.Type != "inputText" {
			return errors.New("unsupported work-tool result")
		}
		content = append(content, map[string]string{"type": "text", "text": item.Text})
	}
	raw, err := json.Marshal(map[string]any{"content": content, "isError": !response.Success})
	if err != nil {
		return err
	}
	select {
	case a.events <- worker.Event{Thread: t, Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: "server_request_resolved", ItemID: answer.RequestID}}:
	default:
		return errors.New("work-tool event queue full")
	}
	delete(a.pending, answer.RequestID)
	pending.result <- raw
	return nil
}
