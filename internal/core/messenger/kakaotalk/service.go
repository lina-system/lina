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
	// maxAttempts bounds concurrent relays.
	maxAttempts = 64
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

	// flow serializes the sign-in steps, so an abandoned attempt is cancelled
	// before a replacement begins on the same add-on session.
	flow sync.Mutex

	mu       sync.Mutex
	attempts map[string]*attempt
	// holder is the connection whose sign-in the add-on is running. The
	// platform keeps one pending flow per account, so a second connection must
	// not resume or answer it: it would receive the first person's prompt or
	// passcode. The holder is live while its attempt is.
	holder string
}

type attempt struct {
	// account is the existing source being signed in again; empty while a new
	// connection has no platform identity yet.
	account string
	// terms is set by a connect, which records the acceptance when the
	// platform identity is known.
	terms   *messenger.Terms
	expires time.Time
}

// NewService composes the control flow over a store and the add-on's sessions.
func NewService(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, attempts: map[string]*attempt{}}
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
	if account, err := s.active(ctx); err != nil {
		return Step{}, err
	} else if account != "" {
		return Step{}, ErrConnected
	}
	terms := &messenger.Terms{Person: s.cfg.Person, At: s.now(), Text: Terms}
	return s.begin(ctx, connection, &attempt{terms: terms}, true)
}

// SignIn starts a sign-in for an existing source, such as one whose stored
// session the platform rejected. Its recorded acceptance is kept.
func (s *Service) SignIn(ctx context.Context, connection, account string) (Step, error) {
	sources, err := s.Sources(ctx)
	if err != nil {
		return Step{}, err
	}
	found := false
	for _, source := range sources {
		if source.Account == account && source.State != messenger.StateDisconnected {
			found = true
		}
	}
	if !found {
		return Step{}, messenger.ErrNotFound
	}
	return s.begin(ctx, connection, &attempt{account: account}, false)
}

// begin starts or resumes the add-on's own sign-in flow for this connection.
// Only the connection that holds the platform's pending flow may begin, resume
// or answer it; another connection is refused until it ends or expires.
func (s *Service) begin(ctx context.Context, connection string, pending *attempt, fresh bool) (Step, error) {
	s.flow.Lock()
	defer s.flow.Unlock()
	s.mu.Lock()
	expired := s.expireLocked()
	if s.holder != "" && s.holder != connection {
		s.mu.Unlock()
		return Step{}, ErrSignInBusy
	}
	resumed := false
	if !fresh {
		if live, ok := s.attempts[connection]; ok && live.account == pending.account {
			pending, resumed = live, true
		}
	}
	if !resumed && fresh && len(s.attempts) >= maxAttempts {
		s.mu.Unlock()
		return Step{}, messenger.ErrInvalid
	}
	if !resumed {
		pending.expires = s.now().Add(attemptLifetime)
		s.attempts[connection] = pending
	}
	s.holder = connection
	s.mu.Unlock()
	// The add-on keeps one pending flow per account, so an abandoned attempt
	// must be dropped before this connection begins its own.
	for _, abandoned := range expired {
		s.cancel(ctx, abandoned)
	}
	session, err := s.cfg.Sessions.Start(Platform, pending.account)
	if err != nil {
		return Step{}, err
	}
	step, err := session.BeginSignIn(ctx)
	if err != nil {
		return Step{}, err
	}
	return s.finish(ctx, connection, pending, step)
}

// Answer relays one typed value to the same add-on session and keeps nothing.
func (s *Service) Answer(ctx context.Context, connection, prompt, value string) (Step, error) {
	s.flow.Lock()
	defer s.flow.Unlock()
	s.mu.Lock()
	expired := s.expireLocked()
	pending, ok := s.attempts[connection]
	held := s.holder == connection
	s.mu.Unlock()
	// The deadline already removed the relay. Cancel its add-on flow before
	// refusing the answer, or the next connection resumes the abandoned one.
	for _, abandoned := range expired {
		s.cancel(ctx, abandoned)
	}
	if !ok || !held || !s.now().Before(pending.expires) {
		return Step{}, ErrNoAttempt
	}
	session, err := s.cfg.Sessions.Start(Platform, pending.account)
	if err != nil {
		return Step{}, err
	}
	step, err := session.Answer(ctx, prompt, value)
	if err != nil {
		return Step{}, err
	}
	return s.finish(ctx, connection, pending, step)
}

// finish records the source once the platform names the account, and settles
// the reload of a source that already exists.
func (s *Service) finish(ctx context.Context, connection string, pending *attempt, step addon.SignInStep) (Step, error) {
	out := Step{State: step.State}
	if step.Prompt != nil {
		out.Prompt = &Prompt{Key: step.Prompt.Key, Kind: step.Prompt.Kind, Text: step.Prompt.Text}
	}
	if step.Show != nil {
		out.Show = &Show{Kind: step.Show.Kind, Value: step.Show.Value}
	}
	if step.State != messenger.SignedIn {
		if step.State == messenger.Failing {
			s.mu.Lock()
			s.releaseLocked(connection)
			s.mu.Unlock()
		}
		return out, nil
	}
	session, err := s.cfg.Sessions.Start(Platform, pending.account)
	if err != nil {
		return Step{}, err
	}
	author, err := session.WhoAmI(ctx)
	if err != nil {
		return Step{}, err
	}
	if author.ID == "" || len(author.ID) > 512 {
		return Step{}, messenger.ErrInvalid
	}
	source := messenger.Source{Person: s.cfg.Person, Platform: Platform, Account: author.ID, Mode: messenger.ModeUnofficial,
		Adapter: AdapterName, State: messenger.StateConnected, ConnectedAt: s.now()}
	if pending.terms != nil {
		source.AcceptedTerms = pending.terms
	} else if existing, err := s.source(ctx, author.ID); err != nil {
		return Step{}, err
	} else if existing != nil {
		if existing.AcceptedTerms == nil {
			return Step{}, messenger.ErrInvalid
		}
		source.AcceptedTerms = existing.AcceptedTerms
		source.ConnectedAt = existing.ConnectedAt
	} else {
		return Step{}, messenger.ErrNotFound
	}
	if err := s.cfg.Store.Connect(ctx, source); err != nil {
		return Step{}, err
	}
	s.mu.Lock()
	s.releaseLocked(connection)
	s.mu.Unlock()
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

// End drops the sign-in this connection was holding and cancels it in the
// add-on. A client that leaves during sign-in must not keep the flow or let
// a background registration finish for a connection that is gone.
func (s *Service) End(ctx context.Context, connection string) {
	if connection == "" {
		return
	}
	s.flow.Lock()
	defer s.flow.Unlock()
	s.mu.Lock()
	pending, ok := s.attempts[connection]
	if ok {
		s.releaseLocked(connection)
	}
	s.mu.Unlock()
	if ok {
		s.cancel(ctx, pending)
	}
}

// Disconnect stops the account's reads and live events, ends its add-on session
// and deletes the add-on's data directory. Records already read stay.
func (s *Service) Disconnect(ctx context.Context, account string) error {
	if account == "" {
		return messenger.ErrInvalid
	}
	source := messenger.Source{Person: s.cfg.Person, Platform: Platform, Account: account}
	if s.cfg.Stop != nil {
		if err := s.cfg.Stop(ctx, source); err != nil {
			return err
		}
	} else if err := s.cfg.Store.Disconnect(ctx, s.cfg.Person, Platform, account); err != nil {
		return err
	}
	s.mu.Lock()
	abandoned := []*attempt{}
	for connection, pending := range s.attempts {
		if pending.account == account {
			abandoned = append(abandoned, pending)
			s.releaseLocked(connection)
		}
	}
	s.mu.Unlock()
	// A sign-in for this account is over: its flow must not outlive it.
	for _, pending := range abandoned {
		s.cancel(ctx, pending)
	}
	return s.cfg.Sessions.Remove(ctx, Platform, account)
}

// expireLocked drops relays past their lifetime and returns them, so the
// caller can cancel their add-on flow. It requires s.mu.
func (s *Service) expireLocked() []*attempt {
	current := s.now()
	var expired []*attempt
	for connection, pending := range s.attempts {
		if !current.Before(pending.expires) {
			expired = append(expired, pending)
			s.releaseLocked(connection)
		}
	}
	return expired
}

// cancel drops an abandoned attempt's flow in the add-on. A session that
// cannot be started has no flow to cancel.
func (s *Service) cancel(ctx context.Context, abandoned *attempt) {
	session, err := s.cfg.Sessions.Start(Platform, abandoned.account)
	if err != nil {
		return
	}
	_ = session.CancelSignIn(ctx)
}

// releaseLocked ends one relay and, when it held the platform's flow, frees it
// for the next connection. It requires s.mu.
func (s *Service) releaseLocked(connection string) {
	delete(s.attempts, connection)
	if s.holder == connection {
		s.holder = ""
	}
}
