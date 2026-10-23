// SPDX-License-Identifier: Apache-2.0
package messages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

// apiVersion is the pinned Messages API version sent with every request.
const apiVersion = "2023-06-01"

type Adapter struct {
	config  Config
	client  *http.Client
	baseURL string
}

var _ provider.Adapter = (*Adapter)(nil)

func New(config Config, client *http.Client) (*Adapter, error) {
	return newAdapter(config, client, "https://api.anthropic.com/v1")
}

// NewWithBaseURL selects an explicitly configured Messages-compatible endpoint.
// The base includes the API prefix; messages, models and batches append to it.
func NewWithBaseURL(config Config, client *http.Client, baseURL string) (*Adapter, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(baseURL, "#") {
		return nil, errors.New("invalid Messages base URL")
	}
	return newAdapter(config, client, strings.TrimRight(u.String(), "/"))
}

func newAdapter(config Config, client *http.Client, baseURL string) (*Adapter, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config.RequestFields = slices.Clone(config.RequestFields)
	config.Capabilities = slices.Clone(config.Capabilities)
	if client == nil {
		client = &http.Client{}
	}
	copy := *client
	// Redirects are another request, and must never replay a model call.
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Adapter{config: config, client: &copy, baseURL: baseURL}, nil
}

func (*Adapter) Provider() provider.ProviderID { return provider.Claude }
func (*Adapter) Class() provider.Class         { return provider.ModelAPI }
func (a *Adapter) Capabilities(model provider.ModelRef) provider.CapabilitySet {
	if model == "" {
		return nil
	}
	return slices.Clone(a.config.Capabilities)
}

// AdmittedCapabilities returns this route's admitted narrowing. It can only
// remove capabilities from the build's route admission set.
func (a *Adapter) AdmittedCapabilities(model provider.ModelRef) provider.CapabilitySet {
	if model == "" {
		return nil
	}
	var admitted provider.CapabilitySet
	for _, capability := range a.config.Capabilities {
		if provider.Admitted("claude_api_key").Has(capability) {
			admitted = append(admitted, capability)
		}
	}
	return admitted
}

func (a *Adapter) request(ctx context.Context, c provider.Credential, method, path string, body []byte) (*http.Response, error) {
	// This route serves an API key only; a subscription token belongs to the
	// agent-runtime adapter and never reaches the Messages API.
	if c.Kind != provider.APIKey {
		return nil, &provider.Failure{Kind: provider.NotAccepted}
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+"/"+path, bytes.NewReader(body))
	if err != nil {
		return nil, classifyError(ctx, err, "")
	}
	if err := c.ApplyHeader(req, "x-api-key", ""); err != nil {
		return nil, err
	}
	req.Header.Set("anthropic-version", apiVersion)
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
	}
	response, err := a.client.Do(req)
	if err != nil {
		return nil, classifyError(ctx, err, "")
	}
	return response, nil
}

func (a *Adapter) Models(ctx context.Context, l lease.Lease, c provider.Credential) (provider.ModelList, error) {
	var out provider.ModelList
	err := lease.Do(ctx, l, func(ctx context.Context) error {
		response, err := a.request(ctx, c, http.MethodGet, "models", nil)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return responseFailure(response)
		}
		var page struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, maxEventBytes)).Decode(&page); err != nil {
			return classifyError(ctx, err, response.Header.Get("request-id"))
		}
		for _, model := range page.Data {
			out.Models = append(out.Models, provider.ModelRef(model.ID))
		}
		out.Observed = time.Now().UTC()
		return nil
	})
	return out, err
}

// The Messages API reports quota only through error codes. A caller can enrich
// this unknown row with the typed failure or its own usage count.
func (*Adapter) Quota(ctx context.Context, c provider.Credential) ([]provider.QuotaRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []provider.QuotaRow{{Provider: provider.Claude, Account: c.Account, Window: provider.UnknownWindow, State: provider.QuotaUnknown, Source: provider.ErrorCode, Observed: time.Now().UTC()}}, nil
}

type cacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

// outBlock keeps a fixed field order so a marked block's wire form is stable.
type outBlock struct {
	Type         string          `json:"type"`
	Thinking     string          `json:"thinking,omitempty"`
	Signature    string          `json:"signature,omitempty"`
	Data         string          `json:"data,omitempty"`
	Text         string          `json:"text,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      json.RawMessage `json:"content,omitempty"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

type outMessage struct {
	Role    string     `json:"role"`
	Content []outBlock `json:"content"`
}

type outTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

type outThinking struct {
	Type         string `json:"type"`
	BudgetTokens int64  `json:"budget_tokens"`
}

type wireParams struct {
	Model     provider.ModelRef `json:"model"`
	System    string            `json:"system,omitempty"`
	Thinking  *outThinking      `json:"thinking,omitempty"`
	Stream    *bool             `json:"stream,omitempty"`
	MaxTokens int64             `json:"max_tokens"`
	Messages  []outMessage      `json:"messages"`
	Tools     []outTool         `json:"tools,omitempty"`
}

var errInvalid = errors.New("invalid Messages request")

// effortBudget maps a reasoning effort to the API's thinking budget. The budget
// always stays below the output limit, as the API requires.
func effortBudget(effort provider.Effort) (int64, bool) {
	switch effort {
	case "low", "minimal":
		return 2048, true
	case "medium":
		return 8192, true
	case "high", "max":
		return 16384, true
	}
	return 0, false
}

type blockPos struct{ message, block int }

func (a *Adapter) allowed(r provider.Request) func(string) bool {
	return func(key string) bool {
		return slices.Contains(a.config.RequestFields, key) && (len(r.Fields) == 0 || slices.Contains(r.Fields, key))
	}
}

// params builds the wire request. Stream is set by the caller, because a batch
// submission carries the same parameters without it.
func (a *Adapter) params(r provider.Request) (wireParams, error) {
	if err := provider.ValidateUse(r, a.Capabilities(r.Model), a.AdmittedCapabilities(r.Model)); err != nil {
		return wireParams{}, err
	}
	reject := func() (wireParams, error) { return wireParams{}, &provider.Failure{Kind: provider.NotAccepted} }
	if r.Model == "" || !r.Purpose.Valid() || r.Bridge != nil {
		return reject()
	}
	allowed := a.allowed(r)
	for _, key := range []string{"model", "max_tokens", "stream", "messages"} {
		if !allowed(key) {
			return reject()
		}
	}
	p := wireParams{Model: r.Model, MaxTokens: a.config.MaxOutputTokens}
	if r.Instructions != "" {
		if !allowed("system") {
			return reject()
		}
		p.System = r.Instructions
	}
	if r.Effort != "" {
		budget, ok := effortBudget(r.Effort)
		if !ok || !allowed("thinking") {
			return reject()
		}
		if budget >= p.MaxTokens {
			budget = p.MaxTokens / 2
		}
		if budget < 1024 {
			return reject()
		}
		p.Thinking = &outThinking{Type: "enabled", BudgetTokens: budget}
	}
	if len(r.Tools) > 0 {
		if !allowed("tools") {
			return reject()
		}
		for _, tool := range r.Tools {
			var v struct {
				Type        string          `json:"type"`
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			}
			var schema struct{ Type string }
			if json.Unmarshal(tool, &v) != nil || v.Type != "function" || v.Name == "" || len(v.Parameters) == 0 || json.Unmarshal(v.Parameters, &schema) != nil || schema.Type != "object" {
				return reject()
			}
			p.Tools = append(p.Tools, outTool{Name: v.Name, Description: v.Description, InputSchema: v.Parameters})
		}
	}
	messages, at, err := convertItems(r.Items)
	if err != nil {
		return reject()
	}
	p.Messages = messages
	if err := applyMarks(&p, r, at); err != nil {
		return reject()
	}
	return p, nil
}

// convertItems keeps every provider-state item byte-for-byte where the wire
// allows it: a thinking block returns with its signature, and a tool call keeps
// the arguments the model produced.
func convertItems(items []provider.Item) ([]outMessage, map[int]blockPos, error) {
	var messages []outMessage
	at := map[int]blockPos{}
	last := blockPos{message: -1, block: -1}
	add := func(role string, b outBlock) (blockPos, error) {
		if len(messages) == 0 {
			if role != "user" {
				return blockPos{}, errInvalid
			}
			messages = append(messages, outMessage{Role: role})
		}
		if last := &messages[len(messages)-1]; last.Role != role {
			messages = append(messages, outMessage{Role: role})
		}
		message := &messages[len(messages)-1]
		message.Content = append(message.Content, b)
		return blockPos{message: len(messages) - 1, block: len(message.Content) - 1}, nil
	}
	for i, raw := range items {
		var v struct {
			Type             string          `json:"type"`
			Role             string          `json:"role"`
			Content          json.RawMessage `json:"content"`
			CallID           string          `json:"call_id"`
			Name             string          `json:"name"`
			Arguments        string          `json:"arguments"`
			Output           json.RawMessage `json:"output"`
			Thinking         string          `json:"thinking"`
			Signature        string          `json:"signature"`
			RedactedThinking string          `json:"redacted_thinking"`
		}
		if json.Unmarshal(raw, &v) != nil {
			return nil, nil, errInvalid
		}
		var (
			pos blockPos
			err error
			ok  = true
		)
		switch v.Type {
		case "reasoning":
			if v.RedactedThinking != "" {
				pos, err = add("assistant", outBlock{Type: "redacted_thinking", Data: v.RedactedThinking})
			} else {
				pos, err = add("assistant", outBlock{Type: "thinking", Thinking: v.Thinking, Signature: v.Signature})
			}
		case "function_call":
			if v.CallID == "" || v.Name == "" || !json.Valid([]byte(v.Arguments)) {
				return nil, nil, errInvalid
			}
			pos, err = add("assistant", outBlock{Type: "tool_use", ID: v.CallID, Name: v.Name, Input: json.RawMessage(v.Arguments)})
		case "function_call_output", "custom_tool_call_output":
			if v.CallID == "" {
				return nil, nil, errInvalid
			}
			var content json.RawMessage
			content, ok = toolResult(v.Output)
			if ok {
				pos, err = add("user", outBlock{Type: "tool_result", ToolUseID: v.CallID, Content: content})
			}
		case "", "message":
			var text string
			switch v.Role {
			case "user", "developer":
				text, ok = textContent(v.Content)
				if ok && text != "" {
					pos, err = add("user", outBlock{Type: "text", Text: text})
				}
			case "assistant":
				text, ok = textContent(v.Content)
				if ok && text != "" {
					pos, err = add("assistant", outBlock{Type: "text", Text: text})
				}
			default:
				return nil, nil, errInvalid
			}
		default:
			return nil, nil, errInvalid
		}
		if err != nil || !ok {
			return nil, nil, errInvalid
		}
		if pos.message < 0 {
			// An item with empty text adds no block; it inherits the previous
			// boundary so a mark there still names the same text.
			if last.message < 0 {
				continue
			}
			pos = last
		} else {
			last = pos
		}
		at[i] = pos
	}
	if len(messages) == 0 {
		return nil, nil, errInvalid
	}
	return messages, at, nil
}

func textContent(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, true
	}
	var parts []struct{ Type, Text string }
	if json.Unmarshal(raw, &parts) != nil {
		return "", false
	}
	var out strings.Builder
	for _, part := range parts {
		if part.Type != "output_text" && part.Type != "text" {
			return "", false
		}
		out.WriteString(part.Text)
	}
	return out.String(), true
}

func toolResult(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return json.RawMessage(`""`), true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		encoded, _ := json.Marshal(text)
		return encoded, true
	}
	if !json.Valid(raw) {
		return nil, false
	}
	encoded, _ := json.Marshal(string(raw))
	return encoded, true
}

// applyMarks places one explicit cache mark per Core boundary: on the last
// content block derived from the boundary's item, or on the boundary's last
// tool declaration when the boundary has no items.
func applyMarks(p *wireParams, r provider.Request, at map[int]blockPos) error {
	for _, mark := range r.Capabilities.CacheMarks {
		control := &cacheControl{Type: "ephemeral"}
		switch mark.Lifetime {
		case provider.Cache1Hour:
			control.TTL = "1h"
		case provider.Cache5Minutes:
			control.TTL = "5m"
		case "":
		default:
			return errInvalid
		}
		switch {
		case mark.Position.Items > 0:
			pos, ok := at[mark.Position.Items-1]
			if !ok {
				return errInvalid
			}
			p.Messages[pos.message].Content[pos.block].CacheControl = control
		case mark.Position.Tools > 0:
			if mark.Position.Tools > len(p.Tools) {
				return errInvalid
			}
			p.Tools[mark.Position.Tools-1].CacheControl = control
		default:
			return errInvalid
		}
	}
	return nil
}

func (a *Adapter) body(r provider.Request) ([]byte, error) {
	p, err := a.params(r)
	if err != nil {
		return nil, err
	}
	stream := true
	p.Stream = &stream
	return json.Marshal(p)
}

type blockState struct {
	kind      string
	id, name  string
	data      string
	input     json.RawMessage
	text      strings.Builder
	thinking  strings.Builder
	signature strings.Builder
	args      strings.Builder
	// whole marks a block that arrived complete in a batch result, so its
	// input object is authoritative instead of accumulated deltas.
	whole bool
}

var errSkipBlock = errors.New("no visible content")

type reasoningItem struct {
	Type             string `json:"type"`
	Thinking         string `json:"thinking,omitempty"`
	Signature        string `json:"signature,omitempty"`
	RedactedThinking string `json:"redacted_thinking,omitempty"`
}

type textItem struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type callItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// finalize turns one completed content block into the LINA item the loop
// records and sends back. A tool call is authoritative only here.
func finalize(index int64, state *blockState) (provider.Event, *provider.ToolCall, error) {
	switch state.kind {
	case "thinking":
		item, err := json.Marshal(reasoningItem{Type: "reasoning", Thinking: state.thinking.String(), Signature: state.signature.String()})
		if err != nil {
			return provider.Event{}, nil, err
		}
		return provider.Event{Kind: provider.ItemDone, Index: index, Item: item}, nil, nil
	case "redacted_thinking":
		item, err := json.Marshal(reasoningItem{Type: "reasoning", RedactedThinking: state.data})
		if err != nil {
			return provider.Event{}, nil, err
		}
		return provider.Event{Kind: provider.ItemDone, Index: index, Item: item}, nil, nil
	case "text":
		text := state.text.String()
		if text == "" {
			return provider.Event{}, nil, errSkipBlock
		}
		item := textItem{Type: "message", Role: "assistant"}
		item.Content = append(item.Content, struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{"output_text", text})
		encoded, err := json.Marshal(item)
		if err != nil {
			return provider.Event{}, nil, err
		}
		return provider.Event{Kind: provider.ItemDone, Index: index, Item: encoded}, nil, nil
	case "tool_use":
		// The streamed input deltas are authoritative; a block that arrived
		// whole, or with no deltas, carries its input object instead.
		arguments := strings.TrimSpace(state.args.String())
		if state.whole || arguments == "" {
			arguments = strings.TrimSpace(string(state.input))
		}
		if arguments == "" {
			arguments = "{}"
		}
		if state.id == "" || state.name == "" || !json.Valid([]byte(arguments)) {
			return provider.Event{}, nil, errInvalid
		}
		encoded, err := json.Marshal(callItem{Type: "function_call", CallID: state.id, Name: state.name, Arguments: arguments})
		if err != nil {
			return provider.Event{}, nil, err
		}
		call := provider.ToolCall{ID: state.id, Name: state.name, Arguments: arguments}
		return provider.Event{Kind: provider.ItemDone, Index: index, Item: encoded}, &call, nil
	}
	return provider.Event{}, nil, errInvalid
}

type stream struct {
	cancel context.CancelFunc
	done   chan struct{}
	events chan provider.Event
	mu     sync.Mutex
	meta   provider.Meta
	err    error
}

func (a *Adapter) Stream(ctx context.Context, l lease.Lease, r provider.Request) (provider.Stream, error) {
	body, err := a.body(r)
	if err != nil {
		return nil, err
	}
	// A background job past the batch window may defer; any failed or expired
	// submission falls through to one normal request.
	if a.batchApplies(ctx, r) {
		out, err := a.batch(ctx, l, r)
		if err != nil {
			return nil, err
		}
		if out.served {
			return &eventsStream{events: out.events, meta: provider.Meta{Class: provider.ModelAPI, Usage: out.usage, Batch: out.id, RequestID: out.requestID}}, nil
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &stream{cancel: cancel, done: make(chan struct{}), events: make(chan provider.Event), meta: provider.Meta{Class: provider.ModelAPI}}
	ready := make(chan error, 1)
	go func() {
		defer close(s.done)
		defer close(s.events)
		defer cancel()
		opened := false
		err := lease.Do(ctx, l, func(ctx context.Context) error {
			response, err := a.request(ctx, r.Credential, http.MethodPost, "messages", body)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			id := response.Header.Get("request-id")
			s.mu.Lock()
			s.meta.RequestID = id
			s.mu.Unlock()
			if response.StatusCode != http.StatusOK {
				return responseFailure(response)
			}
			wire := newEventReader(response.Body)
			opened = true
			ready <- nil
			blocks := map[int64]*blockState{}
			calls := map[string]bool{}
			var pending []provider.Event
			for {
				event, err := wire.next()
				if err != nil {
					return classifyError(ctx, err, id)
				}
				switch event.Type {
				case "message_start":
					if event.Message == nil || event.Message.Role != "assistant" {
						return classify(wireFailure{}, 0, id)
					}
					s.mergeUsage(event.Message.Usage)
				case "content_block_start":
					var b wireBlock
					if len(event.ContentBlock) == 0 || json.Unmarshal(event.ContentBlock, &b) != nil || blocks[event.Index] != nil {
						return classify(wireFailure{}, 0, id)
					}
					state := &blockState{kind: b.Type, id: b.ID, name: b.Name, input: b.Input, data: b.Data}
					switch b.Type {
					case "text":
						state.text.WriteString(b.Text)
					case "thinking":
						state.thinking.WriteString(b.Thinking)
						state.signature.WriteString(b.Signature)
					case "redacted_thinking", "tool_use":
					default:
						return classify(wireFailure{}, 0, id)
					}
					blocks[event.Index] = state
				case "content_block_delta":
					state, delta := blocks[event.Index], event.Delta
					if state == nil || delta == nil {
						return classify(wireFailure{}, 0, id)
					}
					switch delta.Type {
					case "text_delta":
						state.text.WriteString(delta.Text)
						// Only visible text enters the reply stream.
						if !s.send(ctx, provider.Event{Kind: provider.Delta, Index: event.Index, Delta: delta.Text}) {
							return ctx.Err()
						}
					case "thinking_delta":
						state.thinking.WriteString(delta.Thinking)
					case "signature_delta":
						state.signature.WriteString(delta.Signature)
					case "input_json_delta":
						state.args.WriteString(delta.PartialJSON)
					default:
						return classify(wireFailure{}, 0, id)
					}
				case "content_block_stop":
					state := blocks[event.Index]
					if state == nil {
						return classify(wireFailure{}, 0, id)
					}
					delete(blocks, event.Index)
					item, call, err := finalize(event.Index, state)
					if err == errSkipBlock {
						continue
					}
					if err != nil {
						return classify(wireFailure{}, 0, id)
					}
					if call != nil {
						if calls[call.ID] {
							return classify(wireFailure{}, 0, id)
						}
						calls[call.ID] = true
					}
					if !s.send(ctx, item) {
						return ctx.Err()
					}
					// Tool calls become ready only after the terminal event.
					if call != nil {
						pending = append(pending, provider.Event{Kind: provider.ToolCallReady, Index: event.Index, Call: call})
					}
				case "message_delta":
					s.mergeUsage(event.Usage)
				case "message_stop":
					for _, call := range pending {
						if !s.send(ctx, call) {
							return ctx.Err()
						}
					}
					if !s.send(ctx, provider.Event{Kind: provider.Terminal}) {
						return ctx.Err()
					}
					return nil
				case "ping":
				case "error":
					if event.Error != nil {
						return classify(*event.Error, 0, id)
					}
					return classify(wireFailure{}, 0, id)
				default:
					return classify(wireFailure{}, 0, id)
				}
			}
		})
		if !opened {
			ready <- err
		}
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
	}()
	if err := <-ready; err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *stream) mergeUsage(w *wireUsage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mergeUsage(&s.meta.Usage, w)
}

// mergeUsage keeps what the provider reports: plain input, cache reads and
// cache writes by lifetime. An unreported value stays unknown.
func mergeUsage(u *provider.Usage, w *wireUsage) {
	if w == nil {
		return
	}
	if w.Input != nil {
		u.Input = w.Input
	}
	if w.Output != nil {
		u.Output = w.Output
	}
	if w.CacheRead != nil {
		u.Cached = w.CacheRead
	}
	if w.CacheWrite != nil {
		u.CacheWrite = w.CacheWrite
	}
	if len(w.CacheCreation) > 0 {
		var buckets struct {
			FiveMinutes *int64 `json:"ephemeral_5m_input_tokens"`
			OneHour     *int64 `json:"ephemeral_1h_input_tokens"`
		}
		if json.Unmarshal(w.CacheCreation, &buckets) == nil && (buckets.FiveMinutes != nil || buckets.OneHour != nil) {
			if u.CacheByLifetime == nil {
				u.CacheByLifetime = map[provider.CacheLifetime]provider.CacheUsage{}
			}
			if buckets.FiveMinutes != nil {
				u.CacheByLifetime[provider.Cache5Minutes] = provider.CacheUsage{Write: buckets.FiveMinutes}
			}
			if buckets.OneHour != nil {
				u.CacheByLifetime[provider.Cache1Hour] = provider.CacheUsage{Write: buckets.OneHour}
			}
		}
	}
	if !u.Valid() {
		*u = provider.Usage{}
	}
}

func (s *stream) send(ctx context.Context, e provider.Event) bool {
	select {
	case s.events <- e:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *stream) Next(ctx context.Context) (provider.Event, error) {
	if err := ctx.Err(); err != nil {
		s.Close()
		return provider.Event{}, err
	}
	select {
	case e, ok := <-s.events:
		if ok {
			return e, nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.err != nil {
			return provider.Event{}, s.err
		}
		return provider.Event{}, io.EOF
	case <-ctx.Done():
		s.Close()
		return provider.Event{}, ctx.Err()
	}
}

func (s *stream) Close() error { s.cancel(); <-s.done; return nil }

func (s *stream) Meta() provider.Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.meta
	// Return a snapshot, including independent optional usage values.
	b, _ := json.Marshal(m.Usage)
	m.Usage = provider.Usage{}
	_ = json.Unmarshal(b, &m.Usage)
	return m
}

// eventsStream replays a completed batch as the same event sequence a stream
// would produce.
type eventsStream struct {
	events []provider.Event
	meta   provider.Meta
	next   int
	closed bool
}

func (s *eventsStream) Next(ctx context.Context) (provider.Event, error) {
	if err := ctx.Err(); err != nil {
		return provider.Event{}, err
	}
	if s.next >= len(s.events) {
		return provider.Event{}, io.EOF
	}
	e := s.events[s.next]
	s.next++
	return e, nil
}
func (s *eventsStream) Meta() provider.Meta {
	m := s.meta
	b, _ := json.Marshal(m.Usage)
	m.Usage = provider.Usage{}
	_ = json.Unmarshal(b, &m.Usage)
	return m
}
func (s *eventsStream) Close() error { s.closed = true; return nil }

// RequestBudget retains conservative byte accounting until this adapter can
// pair model context metadata with a tokenizer for its complete wire request.
func (*Adapter) RequestBudget(provider.ModelRef) (int, func(provider.Request) int) {
	return 128000, provider.RequestBytes
}
