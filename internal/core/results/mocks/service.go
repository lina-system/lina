// SPDX-License-Identifier: Apache-2.0
package mocks

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"

	generatedui "github.com/thisisjun786/lina/design"
	"github.com/thisisjun786/lina/kit/result"
)

// State is checkpointed by the background-run owner before advancing a phase.
// Failed draws remain private evidence; only all_passed can become a result.
type State struct {
	Request    string            `json:"request"`
	Checklist  []result.MockItem `json:"checklist"`
	Attempts   []Attempt         `json:"attempts"`
	StopReason string            `json:"stop_reason"`
	Page       *result.AssetRef  `json:"page"`
	PNG        *result.AssetRef  `json:"png"`
	Card       *result.Card      `json:"card"`
}
type ModelInput struct {
	Phase     string             `json:"phase"`
	Request   string             `json:"request"`
	Checklist []result.MockItem  `json:"checklist,omitempty"`
	Previous  string             `json:"previous,omitempty"`
	Checks    []result.MockCheck `json:"checks,omitempty"`
}
type Renderer interface {
	Draw(context.Context, string, []result.MockItem) (Rendered, error)
}
type Config struct {
	Model    func(context.Context, ModelInput) (json.RawMessage, error)
	Renderer Renderer
	Save     func(context.Context, State) error
	Asset    func(context.Context, string, []byte) (result.AssetRef, error)
	Present  func(context.Context, result.Mock) (result.Card, error)
}

// Execute resumes from the last committed phase. Saving the exact candidate
// before rendering makes a restart repeat checks, never invent another redraw.
func Execute(ctx context.Context, c Config, state State, previous string, ref *result.ResultRef) (State, error) {
	if state.Card != nil || state.StopReason != "" && state.StopReason != "all_passed" {
		return state, nil
	}
	if len(state.Checklist) == 0 {
		raw, err := c.Model(ctx, ModelInput{Phase: "checklist", Request: state.Request, Previous: previous})
		if err != nil {
			return state, err
		}
		var plan struct {
			Checklist []result.MockItem `json:"checklist"`
		}
		if result.Decode(raw, &plan) != nil || result.ValidateMockChecklist(plan.Checklist) != nil {
			return state, result.ErrMalformed
		}
		state.Checklist = plan.Checklist
		if err = c.Save(ctx, state); err != nil {
			return state, err
		}
	}
	for state.StopReason == "" {
		last := len(state.Attempts) - 1
		if last < 0 || state.Attempts[last].Checks != nil {
			input := ModelInput{Phase: "draw", Request: state.Request, Checklist: state.Checklist, Previous: previous}
			if last >= 0 {
				input.Previous = state.Attempts[last].Fragment
				input.Checks = state.Attempts[last].Checks
			}
			raw, err := c.Model(ctx, input)
			if err != nil {
				return state, err
			}
			var draft struct {
				Fragment string `json:"fragment"`
			}
			if result.Decode(raw, &draft) != nil {
				return state, result.ErrMalformed
			}
			state.Attempts = append(state.Attempts, Attempt{Fragment: draft.Fragment})
			if err = c.Save(ctx, state); err != nil {
				return state, err
			}
			last++
		}
		draw, err := c.Renderer.Draw(ctx, state.Attempts[last].Fragment, state.Checklist)
		if err != nil {
			return state, err
		}
		state.Attempts[last].Checks = draw.Checks
		state.StopReason = Stop(state.Attempts)
		if err = c.Save(ctx, state); err != nil {
			return state, err
		}
		if state.StopReason == "all_passed" {
			return publish(ctx, c, state, draw, ref)
		}
	}
	if state.StopReason == "all_passed" {
		last := state.Attempts[len(state.Attempts)-1]
		draw, err := c.Renderer.Draw(ctx, last.Fragment, state.Checklist)
		if err != nil {
			return state, err
		}
		if Stop([]Attempt{{Checks: draw.Checks}}) != "all_passed" {
			return state, errors.New("mock_recheck_failed")
		}
		return publish(ctx, c, state, draw, ref)
	}
	return state, nil
}
func publish(ctx context.Context, c Config, state State, draw Rendered, ref *result.ResultRef) (State, error) {
	if state.Page == nil {
		bundle, err := Bundle(draw.Page, state.Attempts[len(state.Attempts)-1].Fragment)
		if err != nil {
			return state, err
		}
		a, err := c.Asset(ctx, "page.zip", bundle)
		if err != nil {
			return state, err
		}
		state.Page = &a
		if err = c.Save(ctx, state); err != nil {
			return state, err
		}
	}
	if state.PNG == nil {
		a, err := c.Asset(ctx, "preview.png", draw.PNG)
		if err != nil {
			return state, err
		}
		state.PNG = &a
		if err = c.Save(ctx, state); err != nil {
			return state, err
		}
	}
	card, err := c.Present(ctx, result.Mock{Request: state.Request, Checklist: state.Checklist, Page: *state.Page, PNG: *state.PNG, Previous: ref, StopReason: "all_passed", Checks: draw.Checks})
	if err != nil {
		return state, err
	}
	state.Card = &card
	return state, c.Save(ctx, state)
}
func Bundle(page, fragment string) ([]byte, error) {
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	for _, f := range []struct{ name, body string }{{"index.html", page}, {"source.xhtml", fragment}, {"OFL.txt", string(generatedui.FontLicense())}, {"phone.html", generatedui.MockBundleFrame(390)}, {"desktop.html", generatedui.MockBundleFrame(1280)}} {
		w, err := z.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err = io.WriteString(w, f.body); err != nil {
			return nil, err
		}
	}
	// Mini-app handoff starts from the same immutable template, with the checked
	// fragment as sample data. The later host supplies its scoped gateway.
	starter := generatedui.MiniAppFiles()
	encoded, _ := json.Marshal(fragment)
	starter["app.js"] = []byte("// SPDX-License-Identifier: Apache-2.0\n" + "document.getElementById('app').innerHTML=" + string(encoded) + ";\n" + generatedui.Script())
	names := make([]string, 0, len(starter))
	for name := range starter {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		w, err := z.Create("miniapp/" + name)
		if err != nil {
			return nil, err
		}
		if _, err = w.Write(starter[name]); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func Source(bundle []byte) (string, error) {
	z, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		return "", err
	}
	for _, f := range z.File {
		if f.Name != "source.xhtml" {
			continue
		}
		if f.UncompressedSize64 > 128<<10 {
			return "", result.ErrMalformed
		}
		r, err := f.Open()
		if err != nil {
			return "", err
		}
		defer r.Close()
		b, err := io.ReadAll(io.LimitReader(r, (128<<10)+1))
		if len(b) > 128<<10 {
			return "", result.ErrMalformed
		}
		return string(b), err
	}
	return "", result.ErrMalformed
}
