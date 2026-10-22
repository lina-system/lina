// SPDX-License-Identifier: Apache-2.0

// Package exec binds Core tools to the validated, authorized kit registry.
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
)

// SkillProposal is the summary passed to the explicit-acceptance/work flow.
// Returning it never installs code, adds a grant or dispatches a worker.
type SkillProposal struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
}
type SkillFailure struct {
	Code     string         `json:"code"`
	Proposal *SkillProposal `json:"proposal,omitempty"`
}
type skillIndexArgs struct {
	Methodology string `json:"methodology,omitempty"`
}

func decodeSkill(raw json.RawMessage, out any) error {
	if len(raw) > 2<<20 {
		return tools.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return tools.ErrInvalid
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return tools.ErrInvalid
	}
	return nil
}
func skillInput(raw json.RawMessage, name string) (tools.Input, error) {
	var id string
	var args any
	switch name {
	case "skill_index":
		var a skillIndexArgs
		if err := decodeSkill(raw, &a); err != nil || a.Methodology != "" && a.Methodology != "dev" {
			return tools.Input{}, tools.ErrInvalid
		}
		args = a
	case "read_skill":
		var a skills.Version
		if err := decodeSkill(raw, &a); err != nil {
			return tools.Input{}, err
		}
		id = a.ID
		args = a
	case "write_skill":
		var a skills.Patch
		if err := decodeSkill(raw, &a); err != nil {
			return tools.Input{}, err
		}
		if a.Expected < 0 || len(a.Files) == 0 {
			return tools.Input{}, tools.ErrInvalid
		}
		id = a.ID
		args = a
	case "undo_skill":
		var a skills.Undo
		if err := decodeSkill(raw, &a); err != nil {
			return tools.Input{}, err
		}
		if a.Revision <= 0 {
			return tools.Input{}, tools.ErrInvalid
		}
		id = a.ID
		args = a
	default:
		return tools.Input{}, tools.ErrInvalid
	}
	if name != "skill_index" && id == "" {
		return tools.Input{}, tools.ErrInvalid
	}
	target := "skills"
	if id != "" {
		target = "skill:" + id
	}
	normalized, err := json.Marshal(args)
	if err != nil {
		return tools.Input{}, err
	}
	write := name == "write_skill" || name == "undo_skill"
	what := "Read skill instructions"
	if write {
		what = "Update skill instructions"
	}
	return tools.Input{Arguments: normalized, Target: target, What: what, Where: "Skill catalog", ScopeKey: "skills", Write: write, Undoable: write}, nil
}
func skillFailure(err error) tools.Result {
	failure := SkillFailure{Code: "skill_operation_failed"}
	for _, known := range []error{skills.ErrAcceptance, skills.ErrConflict, skills.ErrUndone, skills.ErrNotFound, skills.ErrInvalid} {
		if errors.Is(err, known) {
			failure.Code = known.Error()
			break
		}
	}
	if errors.Is(err, skills.ErrAcceptance) {
		failure.Proposal = &SkillProposal{Type: "summary", Title: "Skill change needs acceptance", Summary: "This skill includes executable code or additional capabilities. Accept the proposal before the work engine builds it."}
	}
	body, _ := json.Marshal(failure)
	return tools.Result{State: tools.Failed, Body: body}
}

// RegisterSkills returns the registered specs for the turn's tool declarations.
// A consuming turn presents the returned notice only after Dispatch returns its
// committed receipt. The catalog/read handles keep bodies out of the index.
func RegisterSkills(registry *tools.Registry, store *skills.Store) ([]tools.Spec, error) {
	if registry == nil || store == nil {
		return nil, tools.ErrInvalid
	}
	var specs []tools.Spec
	for _, name := range []string{"skill_index", "read_skill", "write_skill", "undo_skill"} {
		spec := tools.Spec{Name: name, Placement: tools.Core, Conc: 1, Deadline: 10 * time.Second, Schema: func(raw json.RawMessage) (tools.Input, error) { return skillInput(raw, name) }}
		handler := func(ctx context.Context, _ lease.Lease, input tools.Input) (tools.Result, error) {
			var value any
			var revision string
			var err error
			switch name {
			case "skill_index":
				var a skillIndexArgs
				err = decodeSkill(input.Arguments, &a)
				if err == nil {
					value, err = store.Index(ctx, a.Methodology)
				}
			case "read_skill":
				var v skills.Version
				err = decodeSkill(input.Arguments, &v)
				if err == nil {
					value, err = store.Read(ctx, v)
				}
			case "write_skill":
				var patch skills.Patch
				err = decodeSkill(input.Arguments, &patch)
				if err == nil {
					var applied skills.Applied
					applied, err = store.Patch(ctx, patch)
					value = applied
					revision = strconv.FormatInt(applied.Version.Revision, 10)
				}
			case "undo_skill":
				var undo skills.Undo
				err = decodeSkill(input.Arguments, &undo)
				if err == nil {
					var applied skills.Applied
					applied, err = store.Undo(ctx, undo)
					value = applied
					revision = strconv.FormatInt(applied.Version.Revision, 10)
				}
			}
			if err != nil {
				return skillFailure(err), nil
			}
			body, err := json.Marshal(value)
			if err != nil {
				return tools.Result{State: tools.Failed}, err
			}
			return tools.Result{State: tools.Applied, Body: body, Revision: revision}, nil
		}
		if err := registry.Register(spec, handler); err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}
