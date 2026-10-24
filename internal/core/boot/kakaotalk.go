// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"errors"
	"os"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/kakaotalk"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

// initKakaoTalk composes the source's control flow over the add-on's sessions.
func initKakaoTalk(r *conversationRuntime, cfg *MessengerConfig) {
	if cfg == nil {
		return
	}
	sessions, ok := cfg.Addon.(kakaotalk.Sessions)
	if !ok {
		return
	}
	r.kakao = kakaotalk.NewService(kakaotalk.Config{Person: r.person, Store: messenger.Open(r.db, r.materials, r.removals), Sessions: sessions})
}

func kakaotalkError(err error) error {
	if err == nil {
		return nil
	}
	code := protocol.ErrorUnavailable
	if errors.Is(err, messenger.ErrInvalid) || errors.Is(err, messenger.ErrNotFound) || errors.Is(err, kakaotalk.ErrConnected) {
		code = protocol.ErrorMalformed
	}
	if errors.Is(err, kakaotalk.ErrNoAttempt) {
		code = protocol.ErrorNotAuthorized
	}
	return &clientsrv.OperationError{Code: code}
}

// KakaoTalk answers one control request for the person's KakaoTalk source. The
// connect card's acceptance, the passcode relay and disconnecting are the
// person's own acts; sync runs in Core independently.
func (r *conversationRuntime) KakaoTalk(ctx context.Context, s clientsrv.Session, p protocol.KakaotalkRequest) (clientsrv.Result, error) {
	if err := r.authorize(s, nil); err != nil {
		return clientsrv.Result{}, err
	}
	if !s.CanDecide || s.Epoch != r.epoch {
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	}
	if r.kakao == nil {
		// The add-on is not configured on this install, so the source cannot be
		// read here; Core and every other source keep working.
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
	}
	if p.Operation == "" || p.Platform != nil && *p.Platform != kakaotalk.Platform {
		return clientsrv.Result{}, malformedManagement()
	}
	op := p.Operation
	// Each operation carries only its own fields, so an acceptance or an
	// answer cannot ride along with a different request.
	if p.Accept != nil && op != "connect" || p.Prompt != nil && op != "answer" || p.Value != nil && op != "answer" ||
		p.Account != nil && op != "sign_in" && op != "disconnect" {
		return clientsrv.Result{}, malformedManagement()
	}
	out := protocol.KakaotalkResult{Operation: &op, Platform: new(kakaotalk.Platform)}
	effect := protocol.EffectNone
	var err error
	switch op {
	case "card":
		language := design.EnvironmentLanguage(os.Getenv)
		text := design.Text(language, "kakaotalk.card") + "\n\n" + kakaotalk.Terms
		links := kakaotalk.Links
		out.Terms, out.Links = &text, &links
	case "connect":
		if p.Accept == nil {
			return clientsrv.Result{}, malformedManagement()
		}
		var step kakaotalk.Step
		step, err = r.kakao.Connect(ctx, s.ConnectionID, *p.Accept)
		effect = protocol.EffectApplied
		kakaotalkStep(&out, step)
	case "sign_in":
		if p.Account == nil {
			return clientsrv.Result{}, malformedManagement()
		}
		var step kakaotalk.Step
		step, err = r.kakao.SignIn(ctx, s.ConnectionID, *p.Account)
		kakaotalkStep(&out, step)
	case "answer":
		if p.Prompt == nil || p.Value == nil {
			return clientsrv.Result{}, malformedManagement()
		}
		var step kakaotalk.Step
		step, err = r.kakao.Answer(ctx, s.ConnectionID, *p.Prompt, *p.Value)
		kakaotalkStep(&out, step)
	case "disconnect":
		if p.Account == nil {
			return clientsrv.Result{}, malformedManagement()
		}
		err = r.kakao.Disconnect(ctx, *p.Account)
		out.Account, out.Status = p.Account, new("disconnected")
		effect = protocol.EffectApplied
	case "list":
		var rows []messenger.Source
		rows, err = r.kakao.Sources(ctx)
		sources := []protocol.KakaotalkSource{}
		for _, row := range rows {
			source := protocol.KakaotalkSource{Platform: row.Platform, Account: row.Account, State: string(row.State)}
			if row.Failure != "" {
				source.Failure = &row.Failure
			}
			if row.AcceptedTerms != nil {
				source.Terms = &row.AcceptedTerms.Text
			}
			sources = append(sources, source)
		}
		out.Sources = &sources
	default:
		return clientsrv.Result{}, malformedManagement()
	}
	if err != nil {
		return clientsrv.Result{}, kakaotalkError(err)
	}
	return clientsrv.Result{Kind: protocol.KindKakaotalkResult, Payload: out, Effect: effect}, nil
}

// kakaotalkStep copies one relay step; prompts and codes stay transient.
func kakaotalkStep(out *protocol.KakaotalkResult, step kakaotalk.Step) {
	wire := protocol.KakaotalkStep{State: string(step.State)}
	if step.State == messenger.SignedIn {
		out.Status = new("connected")
	} else if step.State == messenger.NeedsSignIn {
		out.Status = new("needs_sign_in")
	} else if step.State == messenger.Failing {
		out.Status = new("failing")
	}
	if step.Account != "" {
		wire.Account, out.Account = &step.Account, &step.Account
	}
	if step.Prompt != nil {
		wire.Prompt = &protocol.KakaotalkPrompt{Key: step.Prompt.Key, Kind: step.Prompt.Kind, Text: step.Prompt.Text}
	}
	if step.Show != nil {
		wire.Show = &protocol.KakaotalkShow{Kind: step.Show.Kind, Value: step.Show.Value}
	}
	out.Step = &wire
}
