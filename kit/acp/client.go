// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/thisisjun786/lina/kit/rpc"
)

var (
	ErrAnswered       = errors.New("ACP request already answered")
	ErrBusy           = errors.New("ACP session has an outstanding operation")
	ErrNotInitialized = errors.New("ACP client is not initialized")
	ErrOverflow       = errors.New("ACP event queue is full")
)

// RPCError retains the agent's code and data, including authentication errors.
type RPCError Error

func (e *RPCError) Error() string { return fmt.Sprintf("ACP RPC %d: %s", e.Code, e.Message) }

type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}
type pendingCall struct {
	reply   chan envelope
	session SessionID
	method  string
}
type sessionState struct{ busy, replay, cancelled bool }

// Update marks history delivered before session/load's response as replay.
// The read loop establishes that boundary before processing the next frame.
type Update struct {
	Notification SessionNotification
	Replay       bool
}

// Client multiplexes one version 1 connection. Drain Updates, Permissions and
// Requests while calls run. Bounded queues fail the connection rather than lose
// events or deadlock a reply. Individual call cancellation stops waiting; it does
// not send session/cancel or retry work. An outstanding session stays busy until
// its reply arrives. Cancel is the explicit protocol cancellation operation.
type Client struct {
	control             sync.Mutex
	transportDone       chan struct{}
	transport           rpc.Transport
	ctx                 context.Context
	cancel              context.CancelFunc
	stop                func() bool
	mu                  sync.Mutex
	next                uint64
	initializing, ready bool
	failure             error
	pending             map[string]pendingCall
	sessions            map[SessionID]*sessionState
	requests            map[string]*Request
	updates             chan Update
	permissions         chan *Permission
	incoming            chan *Request
	done, readDone      chan struct{}
}

// New owns transport until Close or cancellation of the connection lifetime.
// Initialize must succeed before session calls are admitted.
func New(ctx context.Context, transport rpc.Transport) *Client {
	life, cancel := context.WithCancel(ctx)
	c := &Client{transportDone: make(chan struct{}), transport: transport, ctx: life, cancel: cancel, pending: map[string]pendingCall{}, sessions: map[SessionID]*sessionState{}, requests: map[string]*Request{}, updates: make(chan Update, 256), permissions: make(chan *Permission, 256), incoming: make(chan *Request, 256), done: make(chan struct{}), readDone: make(chan struct{})}
	c.stop = context.AfterFunc(life, func() { c.fail(life.Err()) })
	go c.read()
	return c
}
func (c *Client) Updates() <-chan Update          { return c.updates }
func (c *Client) Permissions() <-chan *Permission { return c.permissions }

// Requests contains form elicitation requests. The adapter validates the form
// and authenticates its answer. Other methods receive method-not-found.
func (c *Client) Requests() <-chan *Request { return c.incoming }
func (c *Client) Done() <-chan struct{}     { return c.done }
func (c *Client) Err() error                { c.mu.Lock(); defer c.mu.Unlock(); return c.failure }
func (c *Client) Close() error              { c.fail(io.ErrClosedPipe); <-c.readDone; c.stop(); return nil }
func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.failure != nil {
		c.mu.Unlock()
		return
	}
	c.failure = err
	c.pending = map[string]pendingCall{}
	c.requests = map[string]*Request{}
	close(c.done)
	c.mu.Unlock()
	c.cancel()
	c.transport.Close()
	close(c.transportDone)
}
func (c *Client) write(ctx context.Context, f envelope) error {
	f.JSONRPC = "2.0"
	if err := c.transport.Write(ctx, f); err != nil {
		c.fail(err)
		return err
	}
	return nil
}
func (c *Client) beginCall(ctx context.Context, method string, params any, session SessionID) (<-chan envelope, error) {
	c.control.Lock()
	defer c.control.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.failure != nil {
		err = c.failure
		c.mu.Unlock()
		return nil, err
	}
	if method == "initialize" {
		if c.initializing || c.ready {
			c.mu.Unlock()
			return nil, errors.New("ACP initialize already attempted")
		}
		c.initializing = true
	} else if !c.ready {
		c.mu.Unlock()
		return nil, ErrNotInitialized
	}
	if session != "" {
		s := c.sessions[session]
		if s == nil {
			s = &sessionState{}
			c.sessions[session] = s
		}
		if s.busy {
			c.mu.Unlock()
			return nil, ErrBusy
		}
		s.busy = true
		s.replay = method == "session/load"
		if method == "session/prompt" {
			s.cancelled = false
		}
	}
	if len(c.pending) >= 256 {
		c.mu.Unlock()
		c.fail(ErrOverflow)
		return nil, ErrOverflow
	}
	c.next++
	id := strconv.FormatUint(c.next, 10)
	ch := make(chan envelope, 1)
	c.pending[id] = pendingCall{ch, session, method}
	c.mu.Unlock()
	if err = c.write(ctx, envelope{ID: json.RawMessage(id), Method: method, Params: raw}); err != nil {
		return nil, err
	}
	return ch, nil
}
func (c *Client) call(ctx context.Context, method string, params, out any, session SessionID) error {
	ch, err := c.beginCall(ctx, method, params, session)
	if err != nil {
		return err
	}

	return c.awaitCall(ctx, method, ch, out)
}

func (c *Client) awaitCall(ctx context.Context, method string, ch <-chan envelope, out any) error {
	var f envelope
	select {
	case f = <-ch:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		// A complete reply followed by EOF is still a complete reply.
		select {
		case f = <-ch:
		default:
			return c.Err()
		}
	}
	if f.Error != nil {
		return (*RPCError)(f.Error)
	}
	if err := json.Unmarshal(f.Result, out); err != nil {
		c.fail(err)
		return fmt.Errorf("ACP %s response: %w", method, err)
	}
	return nil
}
func (c *Client) Initialize(ctx context.Context, info Implementation) (InitializeResponse, error) {
	var result InitializeResponse
	err := c.call(ctx, "initialize", InitializeRequest{ProtocolVersion: 1, ClientInfo: &info, ClientCapabilities: &ClientCapabilities{Elicitation: &ElicitationCapabilities{Form: &ElicitationFormCapabilities{}}}}, &result, "")
	if err == nil && result.ProtocolVersion != 1 {
		err = fmt.Errorf("ACP protocol version %d is unsupported", result.ProtocolVersion)
	}
	if err != nil {
		c.fail(err)
		return result, err
	}
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
	return result, nil
}
func (c *Client) NewSession(ctx context.Context, p NewSessionRequest) (NewSessionResponse, error) {
	var out NewSessionResponse
	if p.McpServers == nil {
		p.McpServers = []McpServer{}
	}
	err := c.call(ctx, "session/new", p, &out, "")
	if err == nil && out.SessionID == "" {
		err = errors.New("ACP session/new omitted sessionId")
		c.fail(err)
	}
	return out, err
}

// PromptResult is the single terminal observation for a sent prompt.
type PromptResult struct {
	Response PromptResponse
	Err      error
}

// StartPrompt returns only after session/prompt has been written. A subsequent
// Cancel therefore cannot overtake it. The result channel receives one reply or
// connection failure even if the caller stops waiting. Drain Updates and
// requests while it runs; only Cancel requests protocol cancellation.
func (c *Client) StartPrompt(ctx context.Context, p PromptRequest) (<-chan PromptResult, error) {
	if p.SessionID == "" || len(p.Prompt) == 0 {
		return nil, errors.New("ACP prompt requires a session and content")
	}
	reply, err := c.beginCall(ctx, "session/prompt", p, p.SessionID)
	if err != nil {
		return nil, err
	}
	result := make(chan PromptResult, 1)
	go func() {
		var out PromptResponse
		// awaitCall also observes the connection lifetime. A complete reply followed
		// by EOF remains a complete reply, even if the transport context has ended.
		err := c.awaitCall(context.Background(), "session/prompt", reply, &out)
		if err == nil {
			switch out.StopReason {
			case StopReasonEndTurn, StopReasonMaxTokens, StopReasonMaxTurnRequests, StopReasonRefusal, StopReasonCancelled:
			default:
				err = fmt.Errorf("ACP unknown stop reason %q", out.StopReason)
			}
		}
		result <- PromptResult{Response: out, Err: err}
	}()
	return result, nil
}

func (c *Client) Prompt(ctx context.Context, p PromptRequest) (PromptResponse, error) {
	result, err := c.StartPrompt(ctx, p)
	if err != nil {
		return PromptResponse{}, err
	}
	select {
	case r := <-result:
		return r.Response, r.Err
	case <-ctx.Done():
		return PromptResponse{}, ctx.Err()
	}
}
func (c *Client) LoadSession(ctx context.Context, p LoadSessionRequest) (LoadSessionResponse, error) {
	var out LoadSessionResponse
	if p.SessionID == "" {
		return out, errors.New("ACP load requires a session")
	}
	if p.McpServers == nil {
		p.McpServers = []McpServer{}
	}
	err := c.call(ctx, "session/load", p, &out, p.SessionID)
	return out, err
}
func (c *Client) ResumeSession(ctx context.Context, p ResumeSessionRequest) (ResumeSessionResponse, error) {
	var out ResumeSessionResponse
	if p.SessionID == "" {
		return out, errors.New("ACP resume requires a session")
	}
	err := c.call(ctx, "session/resume", p, &out, p.SessionID)
	return out, err
}
func (c *Client) SetConfigOption(ctx context.Context, p SetSessionConfigOptionRequest) (SetSessionConfigOptionResponse, error) {
	var out SetSessionConfigOptionResponse
	var scope struct {
		SessionID SessionID `json:"sessionId"`
	}
	if err := json.Unmarshal(p, &scope); err != nil {
		return out, err
	}
	if scope.SessionID == "" {
		return out, errors.New("ACP config requires a session")
	}
	err := c.call(ctx, "session/set_config_option", p, &out, scope.SessionID)
	return out, err
}
func idKey(raw json.RawMessage) (string, error) {
	var s string
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		b, _ := json.Marshal(s)
		return string(b), nil
	}
	// Keep integers as decimal text, without a float64 conversion.
	if len(raw) == 0 {
		return "", errors.New("missing request id")
	}
	for i, b := range raw {
		if b < '0' || b > '9' {
			if i == 0 && b == '-' && len(raw) > 1 {
				continue
			}
			return "", errors.New("invalid request id")
		}
	}
	return string(raw), nil
}
func (c *Client) read() {
	defer close(c.readDone)
	defer close(c.updates)
	defer close(c.permissions)
	defer close(c.incoming)
	for {
		raw, err := c.transport.Read(c.ctx)
		if err != nil {
			c.fail(err)
			return
		}
		var f envelope
		if err = json.Unmarshal(raw, &f); err != nil || f.JSONRPC != "2.0" {
			c.fail(errors.New("ACP malformed JSON-RPC frame"))
			return
		}
		if f.Method != "" {
			if f.Result != nil || f.Error != nil {
				c.fail(errors.New("ACP invalid request envelope"))
				return
			}
			if len(f.ID) > 0 {
				err = c.request(f)
			} else {
				err = c.notification(f)
			}
			if err != nil {
				c.fail(err)
				return
			}
			continue
		}
		key, err := idKey(f.ID)
		if err != nil || (f.Error == nil) == (len(f.Result) == 0) || bytes.Equal(f.Result, []byte("null")) {
			c.fail(errors.New("ACP malformed response"))
			return
		}
		c.mu.Lock()
		p, ok := c.pending[key]
		if ok {
			delete(c.pending, key)
			if p.session != "" {
				s := c.sessions[p.session]
				s.busy = false
				s.replay = false
				if p.method == "session/prompt" {
					for key, r := range c.requests {
						if r.session == p.session {
							delete(c.requests, key)
						}
					}
				}
			}
			p.reply <- f
		}
		c.mu.Unlock()
	}
}
func (c *Client) notification(f envelope) error {
	if f.Method != "session/update" {
		return nil
	}
	var n SessionNotification
	if err := json.Unmarshal(f.Params, &n); err != nil {
		return err
	}
	if n.SessionID == "" {
		return errors.New("ACP update omitted sessionId")
	}
	c.mu.Lock()
	s := c.sessions[n.SessionID]
	replay := s != nil && s.replay
	c.mu.Unlock()
	select {
	case c.updates <- Update{n, replay}:
		return nil
	default:
		return ErrOverflow
	}
}
