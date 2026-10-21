// SPDX-License-Identifier: Apache-2.0

// Package responses adapts the pinned OpenAI SDK without retries or tool execution.
package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	sdk "github.com/openai/openai-go/v3/responses"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

type Adapter struct {
	config Config
	client *http.Client
}

var _ provider.Adapter = (*Adapter)(nil)

func New(config Config, client *http.Client) (*Adapter, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config.RequestFields = slices.Clone(config.RequestFields)
	if client == nil {
		client = &http.Client{}
	}
	copy := *client
	// Redirects are another request, and must never replay a model call.
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Adapter{config: config, client: &copy}, nil
}
func (*Adapter) Provider() provider.ProviderID { return provider.ChatGPT }
func (*Adapter) Class() provider.Class         { return provider.ModelAPI }
func (a *Adapter) options(c provider.Credential) ([]option.RequestOption, error) {
	request, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", nil)
	if err := c.Authorize(request); err != nil {
		return nil, err
	}
	return []option.RequestOption{option.WithBaseURL("https://api.openai.com/v1/"), option.WithMaxRetries(0), option.WithHTTPClient(a.client), option.WithHeader("Authorization", request.Header.Get("Authorization"))}, nil
}
func (a *Adapter) Models(ctx context.Context, l lease.Lease, c provider.Credential) (provider.ModelList, error) {
	opts, err := a.options(c)
	if err != nil {
		return provider.ModelList{}, err
	}
	var out provider.ModelList
	err = lease.Do(ctx, l, func(ctx context.Context) error {
		service := openai.NewModelService(opts...)
		var raw *http.Response
		page, err := service.List(ctx, option.WithResponseInto(&raw))
		if err != nil {
			var id string
			if raw != nil {
				id = raw.Header.Get("x-request-id")
			}
			return classifyError(ctx, err, id)
		}
		for _, model := range page.Data {
			out.Models = append(out.Models, provider.ModelRef(model.ID))
		}
		out.Observed = time.Now().UTC()
		return nil
	})
	return out, err
}

// The plan route exposes no numeric quota endpoint. A caller can enrich this
// unknown row with the typed failure or its own usage count.
func (*Adapter) Quota(ctx context.Context, c provider.Credential) ([]provider.QuotaRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []provider.QuotaRow{{Provider: provider.ChatGPT, Account: c.Account, Window: provider.UnknownWindow, State: provider.QuotaUnknown, Source: provider.ErrorCode, Observed: time.Now().UTC()}}, nil
}
func (a *Adapter) body(r provider.Request) ([]byte, error) {
	reject := func() ([]byte, error) { return nil, &provider.Failure{Kind: provider.NotAccepted} }
	if r.Model == "" || !r.Purpose.Valid() || r.Bridge != nil {
		return reject()
	}
	allowed := func(key string) bool {
		return slices.Contains(a.config.RequestFields, key) && (len(r.Fields) == 0 || slices.Contains(r.Fields, key))
	}
	for _, key := range []string{"model", "input", "store", "stream"} {
		if !allowed(key) {
			return reject()
		}
	}
	items := r.Items
	if items == nil {
		items = []provider.Item{}
	}
	for _, item := range items {
		var v map[string]json.RawMessage
		if json.Unmarshal(item, &v) != nil || v == nil {
			return reject()
		}
		var role string
		if roleJSON, ok := v["role"]; ok {
			if json.Unmarshal(roleJSON, &role) != nil || role == "system" {
				return reject()
			}
		}
	}
	body := map[string]any{"model": r.Model, "store": false, "stream": true}
	if r.Instructions != "" && r.Purpose != provider.Auxiliary && allowed("instructions") {
		body["instructions"] = r.Instructions
	}
	if r.Effort != "" {
		if !allowed("reasoning") || !allowed("include") {
			return reject()
		}
		body["reasoning"] = map[string]string{"effort": string(r.Effort)}
		body["include"] = []string{"reasoning.encrypted_content"}
	}
	if len(r.Tools) > 0 {
		if r.Purpose == provider.Auxiliary || !allowed("tools") {
			return reject()
		}
		for _, tool := range r.Tools {
			var v struct {
				Type, Name string
				Parameters json.RawMessage
			}
			if json.Unmarshal(tool, &v) != nil || v.Name == "" || (v.Type != "function" && v.Type != "custom") {
				return reject()
			}
			if v.Type == "function" && len(v.Parameters) == 0 {
				return reject()
			}
			if v.Type == "function" && v.Name == "apply_patch" {
				var schema struct {
					Type       string
					Properties map[string]struct{ Type string }
					Required   []string
				}
				if json.Unmarshal(v.Parameters, &schema) != nil || schema.Type != "object" || len(schema.Properties) != 1 || len(schema.Required) != 1 || schema.Properties[schema.Required[0]].Type != "string" {
					return reject()
				}
			}
		}
	}
	// Marshal only adapter-owned fields. Splice validated item/tool JSON as-is:
	// encoding/json would compact and HTML-escape opaque provider state.
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(encoded[:len(encoded)-1])
	appendArray := func(key string, values []json.RawMessage) {
		out.WriteString(",\"" + key + "\":[")
		for i, value := range values {
			if i > 0 {
				out.WriteByte(',')
			}
			out.Write(value)
		}
		out.WriteByte(']')
	}
	appendArray("input", items)
	if len(r.Tools) > 0 {
		appendArray("tools", r.Tools)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
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
	opts, err := a.options(r.Credential)
	if err != nil {
		return nil, err
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
			service := sdk.NewResponseService(opts...)
			var response *http.Response
			wire := service.NewStreaming(ctx, sdk.ResponseNewParams{}, option.WithRequestBody("application/json", body), option.WithResponseInto(&response))
			defer wire.Close()
			id := ""
			if response != nil {
				id = response.Header.Get("x-request-id")
			}
			s.mu.Lock()
			s.meta.RequestID = id
			s.mu.Unlock()
			if err := wire.Err(); err != nil {
				return classifyError(ctx, err, id)
			}
			if response == nil || response.StatusCode != http.StatusOK {
				return classify(wireFailure{}, status(response), id)
			}
			opened = true
			ready <- nil
			pending := []provider.Event{}
			seen := map[int64]bool{}
			calls := map[string]bool{}
			for wire.Next() {
				var event wireEvent
				current := wire.Current()
				if err := json.Unmarshal([]byte(current.RawJSON()), &event); err != nil {
					return classify(wireFailure{}, 0, id)
				}
				if event.Response != nil {
					s.mu.Lock()
					s.meta.Usage = usage(event.Response.Usage)
					s.mu.Unlock()
				}
				switch event.Type {
				case "response.output_text.delta", "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
					if !s.send(ctx, provider.Event{Kind: provider.Delta, Index: event.Index, Delta: event.Delta}) {
						return ctx.Err()
					}
				case "response.output_item.done":
					if seen[event.Index] {
						return classify(wireFailure{}, 0, id)
					}
					seen[event.Index] = true
					var item struct {
						Type, Name, Arguments, Input string
						CallID                       string `json:"call_id"`
					}
					if json.Unmarshal(event.Item, &item) != nil || item.Type == "" {
						return classify(wireFailure{}, 0, id)
					}
					if item.Type == "function_call" || item.Type == "custom_tool_call" {
						if item.CallID == "" || item.Name == "" || calls[item.CallID] || item.Type == "function_call" && !json.Valid([]byte(item.Arguments)) {
							return classify(wireFailure{}, 0, id)
						}
						calls[item.CallID] = true
						call := provider.ToolCall{ID: item.CallID, Name: item.Name, Arguments: item.Arguments, Custom: item.Type == "custom_tool_call"}
						if call.Custom {
							call.Arguments = item.Input
						}
						pending = append(pending, provider.Event{Kind: provider.ToolCallReady, Index: event.Index, Call: &call})
					}
					if !s.send(ctx, provider.Event{Kind: provider.ItemDone, Index: event.Index, Item: slices.Clone(event.Item)}) {
						return ctx.Err()
					}
				case "response.completed":
					if event.Response == nil || event.Response.Status != "completed" {
						return classify(wireFailure{}, 0, id)
					}
					for _, call := range pending {
						if !s.send(ctx, call) {
							return ctx.Err()
						}
					}
					if !s.send(ctx, provider.Event{Kind: provider.Terminal}) {
						return ctx.Err()
					}
					return nil
				case "response.failed", "response.incomplete", "response.cancelled":
					if event.Response != nil {
						return classify(event.Response.Error, 0, id)
					}
					return classify(wireFailure{}, 0, id)
				case "error":
					return classify(wireFailure{Code: event.Code, Type: event.ErrorType}, 0, id)
				}
			}
			return classifyError(ctx, wire.Err(), id)
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
func status(r *http.Response) int {
	if r == nil {
		return 0
	}
	return r.StatusCode
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

type wireEvent struct {
	Type      string          `json:"type"`
	Index     int64           `json:"output_index"`
	Delta     string          `json:"delta"`
	Item      json.RawMessage `json:"item"`
	Code      string          `json:"code"`
	ErrorType string          `json:"error_type"`
	Response  *struct {
		Status string      `json:"status"`
		Error  wireFailure `json:"error"`
		Usage  *wireUsage  `json:"usage"`
	} `json:"response"`
}
type wireUsage struct {
	Input        *int64 `json:"input_tokens"`
	Output       *int64 `json:"output_tokens"`
	InputDetails struct {
		Cached     *int64 `json:"cached_tokens"`
		CacheWrite *int64 `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputDetails struct {
		Reasoning *int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func usage(w *wireUsage) provider.Usage {
	if w == nil {
		return provider.Usage{}
	}
	u := provider.Usage{Input: w.Input, Output: w.Output, Cached: w.InputDetails.Cached, CacheWrite: w.InputDetails.CacheWrite, Reasoning: w.OutputDetails.Reasoning}
	if !u.Valid() {
		return provider.Usage{}
	}
	return u
}
