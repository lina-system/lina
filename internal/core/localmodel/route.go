// SPDX-License-Identifier: Apache-2.0

// Package localmodel routes short tasks to the on-device model or the main model.
package localmodel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/applefm"
)

const (
	Intent       = "intent"
	Autocomplete = "autocomplete"
	Rewrite      = "rewrite"
	Title        = "title"
	Classify     = "classify"
	Extract      = "extract"
	Summary      = "summary"

	Auto       = "auto"
	ForceLocal = "local"
	ForceMain  = "main"

	RouteLocal = "local"
	RouteMain  = "main"

	ReasonFit         = "fit"
	ReasonForced      = "forced"
	ReasonSchemaMiss  = "schema_miss"
	ReasonUnavailable = "unavailable"
	ReasonOverContext = "over_context"
	ReasonTools       = "tools"
	ReasonImage       = "image"
	ReasonKind        = "kind"
	ReasonGuided      = "guided"
	ReasonEmpty       = "empty"
	instructionTokens = 64
)

var (
	ErrMalformed   = errors.New("malformed_local_task")
	ErrUnavailable = errors.New("local_model_unavailable")
)

// Task is one short piece of work. Text is never written to the ledger.
type Task struct {
	Kind, Preference, Text string
	Schema                 json.RawMessage
	Images                 int
	Tools                  []string
	MacLocal               bool
}

type Call struct {
	Instructions, Input string
	Schema              json.RawMessage
}

type Completion struct {
	Text   string
	Object json.RawMessage
	Usage  provider.Usage
}

// Model is one serving endpoint. Identity is what the ledger records.
type Model interface {
	Identity() (provider.ProviderID, provider.ModelRef)
	Complete(context.Context, lease.Lease, Call) (Completion, error)
}

type Row struct {
	ID, Kind, Model, Route, Reason, Digest, Created string
	Provider                                        provider.ProviderID
	Input, Output                                   *int64
}

type Ledger interface {
	Record(context.Context, Row) error
}

type Deps struct {
	Local   Model
	Profile applefm.Profile
	Main    Model
	Ledger  Ledger
}

type Outcome struct {
	Row    Row
	Text   string
	Object json.RawMessage
}

// Decide chooses a route without calling a model.
func Decide(task Task, profile applefm.Profile, ready bool) (string, string) {
	if !allowedKind(task.Kind) {
		return RouteMain, ReasonKind
	}
	if task.Preference == ForceMain {
		return RouteMain, ReasonForced
	}
	if len(task.Tools) > 0 {
		return RouteMain, ReasonTools
	}
	if task.Images > 0 {
		return RouteMain, ReasonImage
	}
	if !ready || profile.ContextTokens <= 0 {
		return RouteMain, ReasonUnavailable
	}
	if len(task.Schema) > 0 && !profile.GuidedGeneration {
		return RouteMain, ReasonGuided
	}
	if Estimate(task) > profile.ContextTokens {
		return RouteMain, ReasonOverContext
	}
	if task.Preference == ForceLocal {
		return RouteLocal, ReasonForced
	}
	return RouteLocal, ReasonFit
}

// Estimate is a high token count, so an over-context task is not sent locally.
func Estimate(task Task) int {
	n := instructionTokens + tokens(task.Text) + tokens(string(task.Schema))
	for _, tool := range task.Tools {
		n += tokens(tool)
	}
	return n
}

func tokens(text string) int {
	n := 0
	for _, r := range text {
		if r < 128 {
			n++
		} else {
			n += 2
		}
	}
	if !utf8.ValidString(text) {
		return len(text) + n
	}
	return n
}

func allowedKind(kind string) bool {
	switch kind {
	case Intent, Autocomplete, Rewrite, Title, Classify, Extract, Summary:
		return true
	default:
		return false
	}
}

// Serve runs the decided model. A schema miss or an unavailable local model
// tries the main model once and does not invent fields.
func Serve(ctx context.Context, l lease.Lease, task Task, deps Deps) (Outcome, error) {
	if strings.TrimSpace(task.Text) == "" {
		return Outcome{}, ErrMalformed
	}
	if task.Preference == "" {
		task.Preference = Auto
	}
	if task.Preference != Auto && task.Preference != ForceLocal && task.Preference != ForceMain {
		return Outcome{}, ErrMalformed
	}
	if len(task.Schema) > 0 && !applefm.ValidSchema(task.Schema) {
		return Outcome{}, ErrMalformed
	}
	ready := deps.Local != nil && deps.Profile.ContextTokens > 0
	route, reason := Decide(task, deps.Profile, ready)
	call := Call{Instructions: instructions(task), Input: task.Text, Schema: task.Schema}
	var completion Completion
	var model Model
	if route == RouteLocal {
		var err error
		completion, err = deps.Local.Complete(ctx, l, call)
		if err == nil && len(task.Schema) > 0 {
			object, verr := structured(completion, task.Schema)
			if verr != nil {
				err = applefm.ErrSchemaMiss
			} else {
				completion.Object = object
			}
		}
		switch {
		case errors.Is(err, applefm.ErrSchemaMiss):
			route, reason = RouteMain, ReasonSchemaMiss
		case isUnavailable(err):
			route, reason = RouteMain, ReasonUnavailable
		case err != nil:
			return Outcome{}, err
		case len(task.Schema) == 0 && strings.TrimSpace(completion.Text) == "":
			route, reason = RouteMain, ReasonEmpty
		default:
			model = deps.Local
		}
	}
	if route == RouteMain {
		if deps.Main == nil {
			return Outcome{}, ErrUnavailable
		}
		var err error
		completion, err = deps.Main.Complete(ctx, l, call)
		if isUnavailable(err) || err != nil && !errors.Is(err, applefm.ErrSchemaMiss) {
			if isUnavailable(err) {
				return Outcome{}, ErrUnavailable
			}
			return Outcome{}, err
		}
		if errors.Is(err, applefm.ErrSchemaMiss) {
			completion = Completion{Usage: completion.Usage}
			reason = ReasonSchemaMiss
		}
		model = deps.Main
	}
	out := Outcome{Text: completion.Text}
	if len(task.Schema) > 0 {
		object, err := structured(completion, task.Schema)
		if err != nil {
			out.Text = ""
			reason = ReasonSchemaMiss
		} else {
			out.Object = object
			out.Text = ""
		}
	}
	providerID, modelName := model.Identity()
	row, err := newRow(task, providerID, modelName, route, reason, completion.Usage)
	if err != nil {
		return Outcome{}, err
	}
	if deps.Ledger != nil {
		if err := deps.Ledger.Record(ctx, row); err != nil {
			return Outcome{}, err
		}
	}
	out.Row = row
	return out, nil
}

func structured(completion Completion, schema json.RawMessage) (json.RawMessage, error) {
	raw := completion.Object
	if len(raw) == 0 {
		raw = json.RawMessage(stripFence(strings.TrimSpace(completion.Text)))
	}
	return applefm.ValidateObject(schema, raw)
}

func stripFence(text string) string {
	if !strings.HasPrefix(text, "```") {
		return text
	}
	rest := strings.TrimPrefix(text, "```")
	if line, after, ok := strings.Cut(rest, "\n"); ok {
		if strings.TrimSpace(line) == "json" || strings.TrimSpace(line) == "" {
			rest = after
		}
	}
	rest = strings.TrimSuffix(strings.TrimSpace(rest), "```")
	return strings.TrimSpace(rest)
}

func instructions(task Task) string {
	base := map[string]string{
		Intent:       "Parse the launcher line.",
		Autocomplete: "Continue the line briefly.",
		Rewrite:      "Rewrite the text. Keep the meaning.",
		Title:        "Name the text in a few words.",
		Classify:     "Classify the text.",
		Extract:      "Extract the fields.",
		Summary:      "Summarise the text.",
	}[task.Kind]
	if len(task.Schema) > 0 {
		return base + " Return only a JSON object that matches this schema:\n" + string(task.Schema)
	}
	return base
}

func newRow(task Task, id provider.ProviderID, model provider.ModelRef, route, reason string, usage provider.Usage) (Row, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return Row{}, err
	}
	payload, err := json.Marshal(struct {
		Kind, Preference, Text string
		Schema                 string
		Images                 int
		Tools                  []string
		MacLocal               bool
	}{task.Kind, task.Preference, task.Text, string(task.Schema), task.Images, task.Tools, task.MacLocal})
	if err != nil {
		return Row{}, err
	}
	sum := sha256.Sum256(payload)
	return Row{
		ID: hex.EncodeToString(buf[:]), Kind: task.Kind, Provider: id, Model: string(model),
		Route: route, Reason: reason, Digest: hex.EncodeToString(sum[:]),
		Input: usage.Input, Output: usage.Output, Created: time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func isUnavailable(err error) bool {
	var failure *provider.Failure
	return errors.As(err, &failure) && failure.Kind == provider.Unavailable
}
