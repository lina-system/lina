// SPDX-License-Identifier: Apache-2.0
package turn

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

type Step string

const (
	BuildStep   Step = "build"
	AccountStep Step = "account"
	ModelStep   Step = "model"
	ContextStep Step = "context"
)

// PreparationFailure separates the one surface line from private error details.
// Show receives it once; callers must not also display the returned error.
type PreparationFailure struct {
	Step   Step
	Line   string
	SignIn bool
	cause  error
}

func (f *PreparationFailure) Error() string { return f.Line }
func (f *PreparationFailure) Unwrap() error { return f.cause }

type ModelPolicy struct {
	Model    provider.ModelRef
	Effort   provider.Effort
	Revision string
	Class    provider.Class
}
type Reference struct{ ID, Revision string }
type Assembly struct {
	AgentID, PersonaRevision string
	Skills, Items            []Reference
}
type PreparedRequest struct {
	Request  provider.Request
	Assembly Assembly
}
type Acknowledgement struct {
	TurnID, PolicyRevision string
	Assembly               Assembly
}
type RequestEvidence struct {
	TurnID, Hash string
	Round        int
	Class        provider.Class
	Assembly     Assembly
}

// Preparation composes store boundaries without importing another L4 flow.
// VerifyBuild checks the running executable against its build manifest. Account
// checks both the credential record and adapter readiness. Context completes
// retraction and permission checks, pins the agent/persona and builds one packet
// and request. It fails if mandatory input cannot fit. The composition root
// supplies Assemble through this callback; it must not read client history.
// Acknowledge and RecordRequest return only after their ledger commits.
type Preparation struct {
	VerifyBuild   func(context.Context) error
	Account       func(context.Context) (provider.Credential, error)
	Resolve       func(context.Context) (ModelPolicy, error)
	Context       func(context.Context, ModelPolicy) (PreparedRequest, error)
	Acknowledge   func(context.Context, Acknowledgement) error
	RecordRequest func(context.Context, RequestEvidence) error
	Digest        func(provider.Request) string
	Show          func(PreparationFailure)
}

func (p *Preparation) failure(step Step, err error) error {
	f := PreparationFailure{Step: step, cause: err}
	switch step {
	case BuildStep:
		f.Line = "This installation could not be verified. Reinstall LINA and try again."
	case AccountStep:
		f.Line = "The model account is not ready. Check the account and try again."
		var failure *provider.Failure
		if errors.As(err, &failure) && failure.Kind == provider.NeedsSignIn {
			f.Line = "Sign in to your model account to continue."
			f.SignIn = true
		}
	case ModelStep:
		f.Line = "The conversation model could not be selected. Check your model settings."
	case ContextStep:
		f.Line = "The conversation could not be prepared. Try again."
	}
	if p.Show != nil {
		p.Show(f)
	}
	return &f
}

func cloneAssembly(a Assembly) Assembly {
	a.Skills = append([]Reference(nil), a.Skills...)
	a.Items = append([]Reference(nil), a.Items...)
	return a
}

func (p *Preparation) prepare(ctx context.Context, id string, cfg loop.Config) (loop.Config, error) {
	check := func(step Step, fn func() error) error {
		if err := ctx.Err(); err != nil {
			return p.failure(step, err)
		}
		if err := fn(); err != nil {
			return p.failure(step, err)
		}
		if err := ctx.Err(); err != nil {
			return p.failure(step, err)
		}
		return nil
	}
	if err := check(BuildStep, func() error {
		if p.VerifyBuild == nil {
			return ErrInvalid
		}
		return p.VerifyBuild(ctx)
	}); err != nil {
		return cfg, err
	}
	var credential provider.Credential
	if err := check(AccountStep, func() (err error) {
		if p.Account == nil {
			return ErrInvalid
		}
		credential, err = p.Account(ctx)
		if err == nil && credential.Account == "" {
			return &provider.Failure{Kind: provider.NeedsSignIn}
		}
		return err
	}); err != nil {
		return cfg, err
	}
	var model ModelPolicy
	if err := check(ModelStep, func() (err error) {
		if p.Resolve == nil {
			return ErrInvalid
		}
		model, err = p.Resolve(ctx)
		if err == nil && (model.Model == "" || model.Revision == "" || (model.Class != provider.ModelAPI && model.Class != provider.AgentRuntime)) {
			return ErrInvalid
		}
		return err
	}); err != nil {
		return cfg, err
	}
	var prepared PreparedRequest
	if err := check(ContextStep, func() (err error) {
		if p.Context == nil || p.Acknowledge == nil || p.RecordRequest == nil || p.Digest == nil || cfg.Stream == nil {
			return ErrInvalid
		}
		prepared, err = p.Context(ctx, model)
		if err != nil {
			return err
		}
		if prepared.Assembly.AgentID == "" || prepared.Assembly.PersonaRevision == "" || len(prepared.Request.Items) == 0 {
			return ErrInvalid
		}
		prepared.Assembly = cloneAssembly(prepared.Assembly)
		return p.Acknowledge(ctx, Acknowledgement{id, model.Revision, cloneAssembly(prepared.Assembly)})
	}); err != nil {
		return cfg, err
	}
	cfg.Request = prepared.Request
	cfg.Request.Model, cfg.Request.Effort, cfg.Request.Credential = model.Model, model.Effort, credential
	cfg.Request.Purpose = provider.Turn
	stream := cfg.Stream
	round := 0
	cfg.Stream = func(ctx context.Context, r provider.Request) (provider.Stream, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hash := p.Digest(r)
		digest, err := hex.DecodeString(hash)
		if err != nil || len(digest) != 32 {
			return nil, p.failure(ContextStep, ErrInvalid)
		}
		e := RequestEvidence{id, hash, round, model.Class, cloneAssembly(prepared.Assembly)}
		if err := p.RecordRequest(ctx, e); err != nil {
			return nil, p.failure(ContextStep, err)
		}
		round++
		return stream(ctx, r)
	}
	return cfg, nil
}
