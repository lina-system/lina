// SPDX-License-Identifier: Apache-2.0
package addon

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/messenger"
)

// SignInStep is transient relay data for the account connection surface. Core
// does not persist prompts, answers, passcodes or QR payloads.
type SignInStep struct {
	State  messenger.SignInState `json:"state"`
	Prompt *SignInPrompt         `json:"prompt,omitempty"`
	Show   *SignInDisplay        `json:"show,omitempty"`
}
type SignInPrompt struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}
type SignInDisplay struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

func (s *Session) signInStep(ctx context.Context, op string, args map[string]any) (SignInStep, error) {
	var step SignInStep
	if err := s.process.call(ctx, op, args, &step); err != nil {
		return step, err
	}
	switch step.State {
	case messenger.SignedIn, messenger.NeedsSignIn, messenger.Failing:
	default:
		return SignInStep{}, ErrUpstreamChanged
	}
	if step.Prompt != nil && (!validIdentifier(step.Prompt.Key) || (step.Prompt.Kind != "text" && step.Prompt.Kind != "secret")) {
		return SignInStep{}, ErrUpstreamChanged
	}
	if step.Show != nil && (step.Show.Value == "" || (step.Show.Kind != "url" && step.Show.Kind != "code")) {
		return SignInStep{}, ErrUpstreamChanged
	}
	return step, nil
}

// BeginSignIn invokes only the add-on's own login flow for this account.
func (s *Session) BeginSignIn(ctx context.Context) (SignInStep, error) {
	return s.signInStep(ctx, "beginSignIn", nil)
}

// Answer forwards user input directly to the same child without saving it.
func (s *Session) Answer(ctx context.Context, prompt, value string) (SignInStep, error) {
	if !validIdentifier(prompt) || len(value) > 4096 {
		return SignInStep{}, messenger.ErrInvalid
	}
	return s.signInStep(ctx, "answer", map[string]any{"prompt": prompt, "value": value})
}

// WhoAmI reads the signed-in account's public profile.
func (s *Session) WhoAmI(ctx context.Context) (messenger.Author, error) {
	var result struct {
		Author wireAuthor `json:"author"`
	}
	if err := s.process.call(ctx, "whoami", nil, &result); err != nil {
		return messenger.Author{}, err
	}
	return result.Author.author()
}
