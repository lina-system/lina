// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
)

func decodeMemoryArgs(raw json.RawMessage, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return tools.ErrInvalid
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return tools.ErrInvalid
	}
	return nil
}
func memoryTarget(person, agent string) string {
	return "memory/" + url.PathEscape(person) + "/" + url.PathEscape(agent)
}

// RememberTool is registered in the conversation's registry with trusted
// identity and scope decisions. Model input supplies an item, never authority.
func (m *Memory) RememberTool(binding memory.Write) (tools.Spec, tools.Handler) {
	target := memoryTarget(binding.Person, binding.Agent)
	spec := tools.Spec{Name: "memory_write", Placement: tools.Core, Conc: 1, Deadline: 30 * time.Second,
		Schema: func(raw json.RawMessage) (tools.Input, error) {
			var item memory.Item
			if err := decodeMemoryArgs(raw, &item); err != nil {
				return tools.Input{}, err
			}
			if strings.TrimSpace(item.Content) == "" || item.ID != "" || item.Revision != 0 || item.State != "" || item.Person != "" || item.SourceAgent != "" || item.SourcePerson != "" || len(item.Evidence) == 0 {
				return tools.Input{}, tools.ErrInvalid
			}
			canonical, err := json.Marshal(item)
			if err != nil {
				return tools.Input{}, err
			}
			return tools.Input{Arguments: canonical, Target: target, What: "Remember a statement", Where: "Memory", ScopeKey: target, Write: true}, nil
		}}
	handler := func(ctx context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
		request := binding
		if err := decodeMemoryArgs(in.Arguments, &request.Item); err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		receipt, err := m.Remember(ctx, request)
		if err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		body, err := json.Marshal(struct {
			ID         string `json:"id"`
			Revision   int64  `json:"revision"`
			Remembered bool   `json:"remembered"`
		}{receipt.ID, receipt.Revision, receipt.Remembered()})
		if err != nil {
			return tools.Result{State: tools.Unknown}, err
		}
		return tools.Result{State: tools.Applied, Body: body, Revision: strconv.FormatInt(receipt.Revision, 10)}, nil
	}
	return spec, handler
}

// RecallPersonTool defaults to depth zero. The clock and scope come from Core;
// tool arguments cannot expand either the holder set or the person's identity.
func (m *Memory) RecallPersonTool(scope memory.RecallScope, now func() int64) (tools.Spec, tools.Handler) {
	type args struct {
		Question string `json:"question"`
		Query    string `json:"query,omitempty"`
		Depth    int    `json:"depth,omitempty"`
		History  bool   `json:"history,omitempty"`
	}
	target := memoryTarget(scope.Person, scope.Agent)
	spec := tools.Spec{Name: "recall_person", Placement: tools.Core, Conc: 1, Deadline: 30 * time.Second,
		Schema: func(raw json.RawMessage) (tools.Input, error) {
			var request args
			if err := decodeMemoryArgs(raw, &request); err != nil {
				return tools.Input{}, err
			}
			if strings.TrimSpace(request.Question) == "" || request.Depth < 0 {
				return tools.Input{}, tools.ErrInvalid
			}
			canonical, err := json.Marshal(request)
			if err != nil {
				return tools.Input{}, err
			}
			return tools.Input{Arguments: canonical, Target: target, What: "Recall the person", Where: "Memory", ScopeKey: target}, nil
		}}
	handler := func(ctx context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
		var request args
		if err := decodeMemoryArgs(in.Arguments, &request); err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		if now == nil {
			return tools.Result{State: tools.Failed}, tools.ErrInvalid
		}
		answer, err := m.RecallPerson(ctx, scope, PersonRecall{Question: request.Question, Query: request.Query, Depth: request.Depth, History: request.History, At: now()})
		if err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		body, err := json.Marshal(answer)
		if err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		return tools.Result{State: tools.Applied, Body: body}, nil
	}
	return spec, handler
}
