// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

import "github.com/thisisjun786/lina/kit/provider"

var (
	ErrTooLarge     = errors.New("request_too_large")
	ErrContinuation = errors.New("continuation_required")
	ErrPass         = errors.New("invalid_compaction_pass")
)

// Policy counts the complete adapter request, including instructions and tools.
// Zero optional values select the documented defaults. Equality triggers fitting.
type Policy struct{ ContextWindow, TriggerPercent, KeepToolTokens, KeepUserTokens, EarlyPercent int }

func (p Policy) normalized() (Policy, error) {
	if p.TriggerPercent == 0 {
		p.TriggerPercent = 90
	}
	if p.EarlyPercent == 0 {
		p.EarlyPercent = 70
	}
	if p.KeepToolTokens == 0 {
		p.KeepToolTokens = 40000
	}
	if p.KeepUserTokens == 0 {
		p.KeepUserTokens = 20000
	}
	if p.ContextWindow <= 0 || p.TriggerPercent < 1 || p.TriggerPercent > 100 || p.KeepToolTokens < 0 || p.KeepUserTokens < 0 || p.EarlyPercent < 1 || p.EarlyPercent > p.TriggerPercent {
		return p, ErrPass
	}
	return p, nil
}
func (p Policy) threshold() int {
	return p.ContextWindow/100*p.TriggerPercent + p.ContextWindow%100*p.TriggerPercent/100
}

// earlyThreshold is P, the share at which a cold boundary may compact early.
func (p Policy) earlyThreshold() int {
	return p.ContextWindow/100*p.EarlyPercent + p.ContextWindow%100*p.EarlyPercent/100
}

// Record is a trusted, authorized ledger projection. Note is a deterministic
// tool receipt description, never a model-generated replacement or a preview.
type Record struct {
	ID   string
	Item provider.Item
	User bool
	Note string
}

// View separates immutable current-turn context from earlier ledger history.
// Root identifies a published summary; History contains only events beside it.
type View struct {
	Request       provider.Request
	Fixed, Packet []provider.Item
	Root          provider.Item
	RootID        string
	History       []Record
	Input         Record
	Active        []Record
}
type Published struct {
	ID   string
	Item provider.Item
}

type PassSegment struct {
	RootID            string
	Root              provider.Item
	Records, Verbatim []Record
	Protected         []Record
}
type Text struct {
	Requests string `json:"unanswered_requests"`
	Topics   string `json:"topics_and_conclusions"`
	Actions  string `json:"completed_actions"`
	Values   string `json:"specific_values"`
	Omitted  string `json:"omitted_detail"`
}

func (t Text) valid() bool {
	return strings.TrimSpace(t.Requests) != "" && strings.TrimSpace(t.Topics) != "" && strings.TrimSpace(t.Actions) != "" && strings.TrimSpace(t.Values) != "" && strings.TrimSpace(t.Omitted) != ""
}

type Pass struct {
	Policy Policy
	// Cold marks a turn boundary whose conversation cache has expired, so an
	// early pass may rewrite history before the cache is warm again. Follow-up
	// requests inside a turn never set it.
	Cold      bool
	Count     func(provider.Request) int
	Summarize func(context.Context, PassSegment) (Text, error)
	// Prepared returns immediately. Only a completed preparation for this exact
	// segment/frontier/epoch may be used; publication rechecks those conditions.
	Prepared func(context.Context, PassSegment) (Text, bool, error)
	Publish  func(context.Context, PassSegment, Text) (Published, error)
}
type Fitted struct {
	View    View
	Request provider.Request
	Sizes   Sizes
}

// Sizes contains no content and can safely enter Details or the ledger.
// Trigger is the boundary this pass fits below: P for a cold early pass and
// Threshold otherwise.
type Sizes struct{ Total, Threshold, Trigger, Window, Fixed, Summary, History, Packet, Input, Active int }

func textItem(role, text string) provider.Item {
	raw, _ := json.Marshal(struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{role, text})
	return raw
}
func copyItems(items []provider.Item) []provider.Item {
	out := make([]provider.Item, len(items))
	for i, b := range items {
		out[i] = append(provider.Item(nil), b...)
	}
	return out
}
func copyRecords(records []Record) []Record {
	out := append([]Record(nil), records...)
	for i := range out {
		out[i].Item = append(provider.Item(nil), out[i].Item...)
	}
	return out
}
func copyView(v View) View {
	v.Fixed = copyItems(v.Fixed)
	v.Packet = copyItems(v.Packet)
	v.Root = append(provider.Item(nil), v.Root...)
	v.History = copyRecords(v.History)
	v.Active = copyRecords(v.Active)
	v.Input.Item = append(provider.Item(nil), v.Input.Item...)
	return v
}
func (v View) request() provider.Request {
	r := v.Request
	r.Items = copyItems(v.Fixed)
	r.Tools = copyItems(r.Tools)
	if len(v.Root) > 0 {
		r.Items = append(r.Items, append(provider.Item(nil), v.Root...))
	}
	for _, e := range v.History {
		r.Items = append(r.Items, append(provider.Item(nil), e.Item...))
	}
	r.Items = append(r.Items, append(provider.Item(nil), v.Input.Item...))
	r.Items = append(r.Items, copyItems(v.Packet)...)
	for _, e := range v.Active {
		r.Items = append(r.Items, append(provider.Item(nil), e.Item...))
	}
	return r
}
func (p Pass) measure(v View) (Sizes, error) {
	s := Sizes{Window: p.Policy.ContextWindow, Threshold: p.Policy.threshold()}
	s.Trigger = s.Threshold
	if p.Cold {
		s.Trigger = p.Policy.earlyThreshold()
	}
	r := v.Request
	r.Items = copyItems(v.Fixed)
	s.Fixed = p.Count(r)
	add := func(items ...provider.Item) int {
		before := p.Count(r)
		r.Items = append(r.Items, items...)
		return p.Count(r) - before
	}
	if len(v.Root) > 0 {
		s.Summary = add(v.Root)
	}
	for _, e := range v.History {
		s.History += add(e.Item)
	}
	s.Packet = add(v.Packet...)
	s.Input = add(v.Input.Item)
	for _, e := range v.Active {
		s.Active += add(e.Item)
	}
	s.Total = p.Count(r)
	if s.Total < 0 || s.Fixed < 0 || s.Summary < 0 || s.History < 0 || s.Packet < 0 || s.Input < 0 || s.Active < 0 {
		return s, ErrPass
	}
	return s, nil
}
func (p Pass) setup(v View) (Pass, error) {
	var err error
	p.Policy, err = p.Policy.normalized()
	if err != nil || p.Count == nil || v.Request.Model == "" || v.Input.ID == "" || !json.Valid(v.Input.Item) {
		return p, ErrPass
	}
	for _, list := range [][]provider.Item{v.Fixed, v.Packet, v.Request.Tools} {
		for _, item := range list {
			if !json.Valid(item) {
				return p, ErrPass
			}
		}
	}
	if len(v.Root) > 0 && !json.Valid(v.Root) {
		return p, ErrPass
	}
	seen := map[string]bool{v.Input.ID: true}
	for _, list := range [][]Record{v.History, v.Active} {
		for _, r := range list {
			if r.ID == "" || seen[r.ID] || !json.Valid(r.Item) {
				return p, ErrPass
			}
			seen[r.ID] = true
		}
	}
	return p, nil
}
func (p Pass) itemTokens(base provider.Request, item provider.Item) int {
	base.Instructions = ""
	base.Tools = nil
	base.Items = nil
	before := p.Count(base)
	base.Items = []provider.Item{item}
	after := p.Count(base)
	if before < 0 || after < before {
		return -1
	}
	return after - before
}

// Fit never emits a request on failure. Its returned View retains every event
// that was not successfully published. The caller records/shows the typed error
// and may retry; no head-only fallback is ever called a summary.
func (p Pass) Fit(ctx context.Context, v View, inTurn bool) (Fitted, error) {
	var err error
	p, err = p.setup(v)
	if err != nil {
		return Fitted{View: v}, err
	}
	v = copyView(v)
	source := copyRecords(v.History)
	result := func() (Fitted, error) { s, e := p.measure(v); return Fitted{View: v, Sizes: s}, e }
	gate := p.Policy.threshold()
	// Only a turn boundary whose cache already expired may rewrite history
	// early; a warm cache or a follow-up request keeps the trigger.
	cold := p.Cold && !inTurn
	if cold {
		gate = p.Policy.earlyThreshold()
	}
	fit := func() (Fitted, bool, error) {
		o, e := result()
		if e != nil {
			return o, false, e
		}
		if o.Sizes.Total < o.Sizes.Threshold {
			o.Request = v.request()
			return o, true, nil
		}
		return o, false, nil
	}
	first, err := result()
	if err != nil {
		return first, err
	}
	// Cold compares against P: it rewrites the history now because the rewrite
	// costs no cache, and does not stop merely because the request fits.
	limit := first.Sizes.Threshold
	if cold {
		limit = gate
	}
	if first.Sizes.Total < limit {
		first.Request = v.request()
		return first, nil
	}
	// Only old tool outputs outside the fresh tail may be replaced. Calls,
	// reasoning, tool ids and item placement are retained byte-for-byte.
	tail := 0
	for i := len(v.History) - 1; i >= 0; i-- {
		e := &v.History[i]
		tokens := p.itemTokens(v.Request, e.Item)
		if tokens < 0 {
			return Fitted{View: v}, ErrPass
		}
		fresh := tail < p.Policy.KeepToolTokens
		tail += tokens
		if fresh || e.Note == "" {
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(e.Item, &obj) != nil {
			continue
		}
		var kind, call string
		_ = json.Unmarshal(obj["type"], &kind)
		_ = json.Unmarshal(obj["call_id"], &call)
		if (kind != "function_call_output" && kind != "custom_tool_call_output") || call == "" {
			continue
		}
		obj["output"], _ = json.Marshal(fmt.Sprintf("%s; expand event %s for the full output", e.Note, e.ID))
		stub, _ := json.Marshal(obj)
		if p.itemTokens(v.Request, stub) < tokens {
			e.Item = stub
		}
	}
	if !cold {
		if out, ok, e := fit(); ok || e != nil {
			return out, e
		}
	}
	// Summaries consume original source items, never the deterministic stubs.
	// Publication uses exact source items, never the shortened request projection.
	boundary, err := p.boundarySegment(v)
	if err != nil {
		return Fitted{View: v}, err
	}
	kept := boundary.Verbatim
	summarize := func(verbatim []Record, preparedOnly bool) error {
		keep := map[string]bool{}
		for _, r := range verbatim {
			keep[r.ID] = true
		}
		segment := PassSegment{RootID: v.RootID, Root: append(provider.Item(nil), v.Root...), Verbatim: copyRecords(verbatim), Protected: append([]Record{v.Input}, copyRecords(v.Active)...)}
		for _, r := range source {
			if !keep[r.ID] {
				segment.Records = append(segment.Records, r)
			}
		}
		if len(segment.Records) == 0 && segment.RootID == "" {
			return nil
		}
		var summary Text
		ready := false
		if p.Prepared != nil {
			summary, ready, err = p.Prepared(ctx, segment)
			if err != nil {
				return err
			}
		}
		if !ready {
			if preparedOnly {
				return nil
			}
			if p.Summarize == nil {
				return ErrPass
			}
			summary, err = p.Summarize(ctx, segment)
			if err != nil {
				return err
			}
		}
		if !summary.valid() || p.Publish == nil {
			return ErrPass
		}
		root, e := p.Publish(ctx, segment, summary)
		if e != nil {
			return e
		}
		if root.ID == "" || !json.Valid(root.Item) {
			return ErrPass
		}
		v.Root, v.RootID = root.Item, root.ID
		v.History = copyRecords(verbatim)
		return nil
	}
	if err := summarize(kept, inTurn); err != nil {
		out, _ := result()
		return out, err
	}
	if out, ok, e := fit(); ok || e != nil {
		return out, e
	}
	if inTurn {
		// The second stage includes the former verbatim tail. Use the same original
		// segment if no preparation was ready; otherwise only the remaining tail.
		if len(v.History) != len(source) {
			source = copyRecords(v.History)
		}
		if err := summarize(nil, false); err != nil {
			out, _ := result()
			return out, err
		}
		if out, ok, e := fit(); ok || e != nil {
			return out, e
		}
		out, _ := result()
		return out, ErrContinuation
	}
	out, _ := result()
	return out, ErrTooLarge
}

type ContinuationRecord struct{ EndedTurn, InputID string }
type Continuation struct {
	EndedTurn, InputID string
	// Record durably ends the prior turn and records its continuation/input refs.
	Record func(context.Context, ContinuationRecord) error
	Packet func(context.Context) ([]provider.Item, error)
	Log    func(context.Context, Sizes) error
	Send   func(context.Context, provider.Request) error
}

// Continue starts a fresh reasoning round trip. Record happens even if fitting
// fails, and the original input occurs once after the newly assembled packet.
func (p Pass) Continue(ctx context.Context, v View, c Continuation) (Fitted, error) {
	var err error
	p, err = p.setup(v)
	if err != nil {
		return Fitted{View: v}, err
	}
	if c.EndedTurn == "" || c.InputID != v.Input.ID || c.Record == nil || c.Packet == nil || c.Log == nil || c.Send == nil || p.Summarize == nil || p.Publish == nil {
		return Fitted{View: v}, ErrPass
	}
	if err = c.Record(ctx, ContinuationRecord{c.EndedTurn, c.InputID}); err != nil {
		return Fitted{View: v}, err
	}
	failed := func(cause error) (Fitted, error) {
		sizes, countErr := p.measure(v)
		var logErr error
		if countErr == nil {
			logErr = c.Log(ctx, sizes)
		}
		return Fitted{View: v, Sizes: sizes}, errors.Join(cause, countErr, logErr)
	}
	segment := PassSegment{RootID: v.RootID, Root: append(provider.Item(nil), v.Root...), Records: append(copyRecords(v.History), copyRecords(v.Active)...), Verbatim: []Record{v.Input}}
	summary, err := p.Summarize(ctx, segment)
	if err != nil {
		return failed(err)
	}
	if !summary.valid() {
		return failed(ErrPass)
	}
	root, err := p.Publish(ctx, segment, summary)
	if err != nil {
		return failed(err)
	}
	if root.ID == "" || !json.Valid(root.Item) {
		return failed(ErrPass)
	}
	v = copyView(v)
	v.Root, v.RootID = root.Item, root.ID
	v.History = nil
	v.Active = nil
	v.Packet, err = c.Packet(ctx)
	if err != nil {
		return Fitted{View: v}, err
	}
	p, err = p.setup(v)
	if err != nil {
		return Fitted{View: v}, err
	}
	sizes, err := p.measure(v)
	out := Fitted{View: v, Sizes: sizes}
	if err != nil {
		return out, err
	}
	if err = c.Log(ctx, sizes); err != nil {
		return out, err
	}
	if sizes.Total >= sizes.Threshold {
		return out, ErrTooLarge
	}
	out.Request = v.request()
	if err = ctx.Err(); err != nil {
		return Fitted{View: v, Sizes: sizes}, err
	}
	return out, c.Send(ctx, out.Request)
}
