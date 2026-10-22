// SPDX-License-Identifier: Apache-2.0
// Package claudeproc connects an explicitly installed C-45 adapter to Core.
package claudeproc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/secret"
)

type Adapter struct {
	Node, Program string
	Environment   []string
	Token         secret.Secret
}

func (*Adapter) Provider() provider.ProviderID { return provider.Claude }
func (*Adapter) Class() provider.Class         { return provider.AgentRuntime }
func (*Adapter) Models(context.Context, lease.Lease, provider.Credential) (provider.ModelList, error) {
	return provider.ModelList{}, &provider.Failure{Kind: provider.Unavailable}
}
func (*Adapter) Quota(context.Context, provider.Credential) ([]provider.QuotaRow, error) {
	return nil, nil
}
func (a *Adapter) Stream(ctx context.Context, l lease.Lease, req provider.Request) (provider.Stream, error) {
	if !filepath.IsAbs(a.Node) || !filepath.IsAbs(a.Program) || req.Model == "" {
		return nil, &provider.Failure{Kind: provider.NotAccepted}
	}
	ctx, cancel := context.WithCancel(ctx)
	in, stdin := io.Pipe()
	stdout, out := io.Pipe()
	s := &stream{cancel: cancel, stdin: stdin, stdout: stdout, done: make(chan struct{}), request: req, meta: provider.Meta{Class: provider.AgentRuntime}, tools: map[string]bool{}}
	for _, raw := range req.Tools {
		var d struct{ Name string }
		if json.Unmarshal(raw, &d) != nil || d.Name == "" {
			cancel()
			in.Close()
			stdin.Close()
			out.Close()
			stdout.Close()
			return nil, &provider.Failure{Kind: provider.NotAccepted}
		}
		s.tools[d.Name] = true
	}
	env := append([]string{}, a.Environment...)
	if token, err := secret.Environment("CLAUDE_CODE_OAUTH_TOKEN", a.Token); err == nil {
		env = append(env, token)
	}
	owner := proc.NewOwner()
	go func() {
		defer close(s.done)
		err := owner.Run(ctx, l, proc.Command{Path: a.Node, Args: []string{a.Program}, Env: env, Stdin: in, Stdout: out, Stderr: io.Discard})
		_ = in.CloseWithError(err)
		_ = out.CloseWithError(err)
	}()
	s.scanner = bufio.NewScanner(stdout)
	s.scanner.Buffer(make([]byte, 4096), 8*1024*1024)
	if err := s.send(map[string]any{"type": "start", "id": "turn", "system": req.Instructions, "items": append([]provider.Item{}, req.Items...), "tools": append([]provider.ToolDecl{}, req.Tools...), "model": req.Model, "effort": string(req.Effort)}); err != nil {
		s.Close()
		return nil, &provider.Failure{Kind: provider.Unavailable}
	}
	return s, nil
}

type frame struct {
	Type      string                                  `json:"type"`
	ID        string                                  `json:"id"`
	Text      string                                  `json:"text"`
	Code      string                                  `json:"code"`
	Tools     []string                                `json:"tools"`
	Servers   []struct{ Name, Status, Source string } `json:"mcp_servers"`
	Plugins   []json.RawMessage                       `json:"plugins"`
	Item      json.RawMessage                         `json:"item"`
	CallID    string                                  `json:"call_id"`
	Name      string                                  `json:"name"`
	Arguments json.RawMessage                         `json:"arguments"`
	Usage     provider.Usage                          `json:"usage"`
	Reason    string                                  `json:"reason"`
	SessionID string                                  `json:"session_id"`
	Cost      *provider.Money                         `json:"cost_estimate"`
}
type stream struct {
	cancel                context.CancelFunc
	stdin                 *io.PipeWriter
	stdout                *io.PipeReader
	done                  chan struct{}
	once                  sync.Once
	scanner               *bufio.Scanner
	request               provider.Request
	meta                  provider.Meta
	tools                 map[string]bool
	initialized, terminal bool
	index                 int64
}

func (s *stream) send(value any) error { return json.NewEncoder(s.stdin).Encode(value) }
func (s *stream) Next(ctx context.Context) (provider.Event, error) {
	if s.terminal {
		return provider.Event{}, io.EOF
	}
	fail := func() (provider.Event, error) { return provider.Event{}, &provider.Failure{Kind: provider.Unavailable} }
	for s.scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return provider.Event{}, err
		}
		var f frame
		if json.Unmarshal(s.scanner.Bytes(), &f) != nil || f.ID != "turn" {
			return fail()
		}
		if f.Type == "error" {
			if f.Code == "needs_sign_in" {
				return provider.Event{}, &provider.Failure{Kind: provider.NeedsSignIn}
			}
			return fail()
		}
		if f.Type == "init" {
			if s.initialized || f.Tools == nil || f.Servers == nil || f.Plugins == nil || len(f.Plugins) > 0 {
				return fail()
			}
			for _, name := range f.Tools {
				allowed := false
				for tool := range s.tools {
					allowed = allowed || name == "mcp__lina__"+tool
				}
				if !allowed {
					return fail()
				}
			}
			for _, server := range f.Servers {
				if len(s.tools) == 0 || server.Name != "lina" || server.Source != "sdk" || server.Status != "connected" {
					return fail()
				}
			}
			s.initialized = true
			continue
		}
		if !s.initialized {
			return fail()
		}
		switch f.Type {
		case "delta":
			return provider.Event{Kind: provider.Delta, Index: s.index, Delta: f.Text}, nil
		case "item_done":
			var item struct {
				Role    string           `json:"role"`
				Content []map[string]any `json:"content"`
			}
			if json.Unmarshal(f.Item, &item) != nil || item.Role != "assistant" {
				return fail()
			}
			for _, block := range item.Content {
				if block["type"] == "text" {
					block["type"] = "output_text"
				}
			}
			raw, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": item.Content})
			event := provider.Event{Kind: provider.ItemDone, Index: s.index, Item: raw}
			s.index++
			return event, nil
		case "tool_call":
			if !s.tools[f.Name] || f.CallID == "" || s.request.Bridge == nil || !json.Valid(f.Arguments) {
				return fail()
			}
			result, err := s.request.Bridge.Call(ctx, provider.ToolCall{ID: f.CallID, Name: f.Name, Arguments: string(f.Arguments)})
			if err != nil {
				return provider.Event{}, err
			}
			if err := s.send(map[string]any{"type": "tool_result", "id": "turn", "call_id": f.CallID, "result": map[string]any{"content": []any{map[string]string{"type": "text", "text": string(result.Output)}}}}); err != nil {
				return fail()
			}
		case "usage":
			if !f.Usage.Valid() {
				return fail()
			}
			s.meta.Usage = f.Usage
		case "limit": // Account quota projection is owned by #168; raw events stay on C-45.
		case "terminal":
			if f.Reason != "completed" {
				return fail()
			}
			s.meta.RuntimeSessionID = f.SessionID
			s.meta.Usage.CostEstimate = f.Cost
			if !s.meta.Usage.Valid() {
				return fail()
			}
			s.terminal = true
			return provider.Event{Kind: provider.Terminal, Index: s.index}, nil
		default:
			return fail()
		}
	}
	return fail()
}
func (s *stream) Meta() provider.Meta { return s.meta }
func (s *stream) Close() error {
	s.once.Do(func() { s.cancel(); s.stdin.Close(); s.stdout.Close() })
	select {
	case <-s.done:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("adapter shutdown timed out")
	}
}
