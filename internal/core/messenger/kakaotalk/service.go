// SPDX-License-Identifier: Apache-2.0

// Package kakaotalk connects the person's own KakaoTalk account as a messenger
// source and relays its passcode sign-in. The account is read through the
// messenger add-on as a tablet sub-device, read-only, because Kakao's terms
// forbid automating a personal account
// ([docs/design/integrations.md#platforms]).
//
// This package owns the connect card's acceptance, the one-account rule, and the
// relay of the add-on's own sign-in steps. Prompts, passcodes and passwords pass
// through it in memory and are never stored: the acceptance record holds the
// term the person agreed to, and the add-on keeps the session in its data
// directory.
package kakaotalk

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/addon"
)

const (
	// Platform is the source platform this package connects.
	Platform = "kakaotalk"
	// AdapterName is the adapter a KakaoTalk source records: the messenger
	// add-on serves it, not a ported adapter.
	AdapterName = "messenger-addon"
	// attemptLifetime bounds one connection-bound sign-in relay.
	attemptLifetime = 10 * time.Minute
)

// Terms is the record of what the person accepted. It quotes the Kakao
// documents the connect card cites, in their words, and states the consequence
// ([docs/research/messengers.md]).
const Terms = `카카오가 허용하지 않은 비정상적인 프로그램 등을 이용하여 카카오톡의 프로토콜・API 등을 부정하게 호출하는 행위 (https://www.kakao.com/policy/terms?type=ts): Kakao may at once restrict all KakaoTalk services permanently (https://talksafety.kakao.com/policy/stability/abnormalusage). Automating a personal account breaks Kakao's terms, and even reading may get the account limited. LINA only reads: it never sends, never marks a message read and never takes another device's place.`

// Links are the documents the connect card cites.
var Links = []string{
	"https://www.kakao.com/policy/terms?type=ts",
	"https://talksafety.kakao.com/policy/stability/abnormalusage",
}

var (
	// ErrConnected reports that this main already has a KakaoTalk account.
	ErrConnected = errors.New("kakaotalk account already connected")
	// ErrNoAttempt reports a sign-in answer without a live attempt.
	ErrNoAttempt = errors.New("kakaotalk sign-in attempt expired")
	// ErrSignInBusy reports a sign-in begun while another connection still
	// holds the platform's one pending flow.
	ErrSignInBusy = errors.New("kakaotalk sign-in is held by another connection")
)

// Prompt is one typed answer the add-on asks for. Its text is the add-on's own
// wording; LINA adds nothing to it.
type Prompt struct {
	Key, Kind, Text string
}

// Show is a value the person uses on the platform, such as the passcode they
// confirm on their phone.
type Show struct {
	Kind, Value string
}

// Step is one sign-in step. State signed_in ends the relay.
type Step struct {
	State   messenger.SignInState
	Account string
	Prompt  *Prompt
	Show    *Show
}

// Sessions is the add-on's per-platform session boundary. KakaoTalk keeps one
// session per main, so the account is the platform identity rather than a
// session key.
type Sessions interface {
	Start(platform, account string) (*addon.Session, error)
	Remove(ctx context.Context, platform, account string) error
}

// Config composes the service. Person is the id the source belongs to.
type Config struct {
	Person   string
	Store    *messenger.Store
	Sessions Sessions
	// Stop ends the source's reads and waits for an admission already under
	// way. Core supplies the messenger service's Disconnect, so a page read
	// before the disconnect cannot commit after it. A nil Stop disconnects the
	// store row alone.
	Stop func(context.Context, messenger.Source) error
	// Now overrides the clock for deterministic tests.
	Now func() time.Time
}

// Service owns the KakaoTalk source, its accepted terms and connection-bound
// sign-in relays.
type Service struct {
	cfg Config

	// flow owns every lifecycle transition. Platform I/O runs outside it, using
	// the attempt's captured session and cancellable request. A disconnect can
	// invalidate that request without waiting for the platform to answer.
	flow     sync.Mutex
	pending  *attempt
	removing string
}

type attempt struct {
	connection string
	account    string
	terms      *messenger.Terms
	expires    time.Time
	session    *addon.Session
	cancel     context.CancelFunc
	busy       bool
	invalid    bool
}

// NewService composes the control flow over a store and the add-on's sessions.
func NewService(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg}
}

func (s *Service) now() time.Time { return s.cfg.Now().UTC() }

// Sources lists this platform's sources.
func (s *Service) Sources(ctx context.Context) ([]messenger.Source, error) {
	all, err := s.cfg.Store.Sources(ctx, s.cfg.Person)
	if err != nil {
		return nil, err
	}
	sources := []messenger.Source{}
	for _, source := range all {
		if source.Platform == Platform {
			sources = append(sources, source)
		}
	}
	return sources, nil
}

// active returns the account of the one connected source, if any.
func (s *Service) active(ctx context.Context) (string, error) {
	sources, err := s.Sources(ctx)
	if err != nil {
		return "", err
	}
	account := ""
	for _, source := range sources {
		if source.State == messenger.StateDisconnected {
			continue
		}
		if account != "" {
			return "", messenger.ErrInvalid
		}
		account = source.Account
	}
	return account, nil
}

// Connect records the person's acceptance and returns the add-on's first
// sign-in step. The source row appears only when the platform names the
// account, so a card accepted and abandoned leaves nothing behind.
func (s *Service) Connect(ctx context.Context, connection string, accepted bool) (Step, error) {
	if !accepted || connection == "" {
		return Step{}, messenger.ErrInvalid
	}
	return s.begin(ctx, connection, "", true)
}

// SignIn resumes only the named active source, keeping its recorded acceptance.
func (s *Service) SignIn(ctx context.Context, connection, account string) (Step, error) {
	if connection == "" || account == "" {
		return Step{}, messenger.ErrInvalid
	}
	return s.begin(ctx, connection, account, false)
}

func (s *Service) begin(ctx context.Context, connection, account string, fresh bool) (Step, error) {
	s.flow.Lock()
	if err := s.finishRemoval(ctx); err != nil {
		s.flow.Unlock()
		return Step{}, err
	}
	if err := s.expire(ctx); err != nil {
		s.flow.Unlock()
		return Step{}, err
	}
	active, err := s.active(ctx)
	if err != nil {
		s.flow.Unlock()
		return Step{}, err
	}
	if fresh && active != "" {
		s.flow.Unlock()
		return Step{}, ErrConnected
	}
	if !fresh && active != account {
		s.flow.Unlock()
		return Step{}, messenger.ErrNotFound
	}
	pending := s.pending
	if pending != nil && (pending.connection != connection || pending.account != account || pending.busy) {
		s.flow.Unlock()
		return Step{}, ErrSignInBusy
	}
	if pending == nil {
		session, err := s.cfg.Sessions.Start(Platform, account)
		if err != nil {
			s.flow.Unlock()
			return Step{}, err
		}
		pending = &attempt{connection: connection, account: account, session: session, expires: s.now().Add(attemptLifetime)}
		if fresh {
			pending.terms = &messenger.Terms{Person: s.cfg.Person, At: s.now(), Text: Terms}
		}
		s.pending = pending
	}
	request := s.request(ctx, pending)
	s.flow.Unlock()
	return s.run(request, pending, func(ctx context.Context) (addon.SignInStep, error) { return pending.session.BeginSignIn(ctx) })
}

// Answer relays a value only on the connection holding the pending flow.
func (s *Service) Answer(ctx context.Context, connection, prompt, value string) (Step, error) {
	s.flow.Lock()
	if err := s.expire(ctx); err != nil {
		s.flow.Unlock()
		return Step{}, err
	}
	pending := s.pending
	if pending == nil || pending.connection != connection {
		s.flow.Unlock()
		return Step{}, ErrNoAttempt
	}
	if pending.busy {
		s.flow.Unlock()
		return Step{}, ErrSignInBusy
	}
	request := s.request(ctx, pending)
	s.flow.Unlock()
	return s.run(request, pending, func(ctx context.Context) (addon.SignInStep, error) { return pending.session.Answer(ctx, prompt, value) })
}

// request requires flow. There is at most one outstanding step per attempt.
func (s *Service) request(ctx context.Context, pending *attempt) context.Context {
	request, cancel := context.WithCancel(ctx)
	pending.cancel, pending.busy = cancel, true
	return request
}

func (s *Service) run(ctx context.Context, pending *attempt, call func(context.Context) (addon.SignInStep, error)) (Step, error) {
	step, err := call(ctx)
	var author messenger.Author
	if err == nil && step.State == messenger.SignedIn {
		// Never Start here: disconnect may already have removed this session.
		author, err = pending.session.WhoAmI(ctx)
	}
	s.flow.Lock()
	defer s.flow.Unlock()
	defer pending.cancel()
	pending.busy = false
	if s.pending != pending || pending.invalid || !s.now().Before(pending.expires) {
		return Step{}, ErrNoAttempt
	}
	if err != nil {
		pending.invalid = true
		return Step{}, err
	}
	out := Step{State: step.State}
	if step.Prompt != nil {
		out.Prompt = &Prompt{Key: step.Prompt.Key, Kind: step.Prompt.Kind, Text: step.Prompt.Text}
	}
	if step.Show != nil {
		out.Show = &Show{Kind: step.Show.Kind, Value: step.Show.Value}
	}
	if step.State != messenger.SignedIn {
		if step.State == messenger.Failing {
			pending.invalid = true
		}
		return out, nil
	}
	if author.ID == "" || len(author.ID) > 512 || pending.account != "" && author.ID != pending.account {
		pending.invalid = true
		return Step{}, messenger.ErrInvalid
	}
	source := messenger.Source{Person: s.cfg.Person, Platform: Platform, Account: author.ID, Mode: messenger.ModeUnofficial,
		Adapter: AdapterName, State: messenger.StateConnected, ConnectedAt: s.now(), AcceptedTerms: pending.terms}
	if pending.terms == nil {
		existing, err := s.source(ctx, pending.account)
		if err != nil {
			return Step{}, err
		}
		if existing == nil || existing.State == messenger.StateDisconnected || existing.AcceptedTerms == nil {
			return Step{}, messenger.ErrInvalid
		}
		source.AcceptedTerms, source.ConnectedAt = existing.AcceptedTerms, existing.ConnectedAt
	}
	if err := s.cfg.Store.Connect(ctx, source); err != nil {
		return Step{}, err
	}
	if _, err := s.cfg.Sessions.Start(Platform, author.ID); err != nil {
		return Step{}, err
	}
	s.pending = nil
	out.Account = author.ID
	return out, nil
}

func (s *Service) source(ctx context.Context, account string) (*messenger.Source, error) {
	sources, err := s.Sources(ctx)
	if err != nil {
		return nil, err
	}
	for i, source := range sources {
		if source.Account == account {
			return &sources[i], nil
		}
	}
	return nil, nil
}

// Disconnect stops only this account's reads and invalidates its relay before
// removing the session. An already disconnected source owns no current session.
func (s *Service) Disconnect(ctx context.Context, account string) error {
	if account == "" {
		return messenger.ErrInvalid
	}
	s.flow.Lock()
	defer s.flow.Unlock()
	existing, err := s.source(ctx, account)
	if err != nil {
		return err
	}
	if existing == nil {
		return messenger.ErrNotFound
	}
	if existing.State == messenger.StateDisconnected {
		if s.removing == account {
			return s.finishRemoval(ctx)
		}
		return nil
	}
	if s.cfg.Stop != nil {
		if err := s.cfg.Stop(ctx, *existing); err != nil {
			return err
		}
	} else if err := s.cfg.Store.Disconnect(ctx, s.cfg.Person, Platform, account); err != nil {
		return err
	}
	if pending := s.pending; pending != nil && pending.account == account {
		pending.invalid = true
		if pending.cancel != nil {
			pending.cancel()
		}
		s.pending = nil
	}
	s.removing = account
	return s.finishRemoval(ctx)
}

// A failed removal is retried before a new source may take the shared session.
// Callers hold flow.
func (s *Service) finishRemoval(ctx context.Context) error {
	if s.removing == "" {
		return nil
	}
	if err := s.cfg.Sessions.Remove(ctx, Platform, s.removing); err != nil {
		return err
	}
	s.removing = ""
	return nil
}

// expire requires flow. A reset failure retains an invalid attempt and prevents
// a replacement from inheriting its credentials or passcode. The next operation
// retries the reset. Fresh abandoned sessions are also removed: registration may
// have completed before the relay expired, but no source accepted that session.
func (s *Service) expire(ctx context.Context) error {
	pending := s.pending
	if pending == nil || !pending.invalid && s.now().Before(pending.expires) {
		return nil
	}
	pending.invalid = true
	if pending.cancel != nil {
		pending.cancel()
	}
	if err := pending.session.CancelSignIn(ctx); err != nil {
		return err
	}
	if pending.account == "" {
		if err := s.cfg.Sessions.Remove(ctx, Platform, ""); err != nil {
			return err
		}
	}
	s.pending = nil
	return nil
}
