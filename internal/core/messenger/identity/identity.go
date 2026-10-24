// SPDX-License-Identifier: Apache-2.0

// Package identity matches messenger senders to known people. It owns the
// platform key of an author, the identifiers and display names it was seen
// with, the links that tie it to a people record, the person's confirm, reject,
// remove, merge and split acts, the repair that follows a changed link, and the
// evaluation that measures the matcher before any learning about other people.
//
// A link is a record: its class, each signal with its source, who made it, the
// time, its revision and its state. Only a confirmed link or an exact link with
// no ambiguity is usable for learning. Names never decide; they only ask.
package identity

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/retract"
)

var (
	ErrInvalid  = errors.New("invalid_sender_identity")
	ErrNotFound = errors.New("sender_identity_not_found")
	// ErrAmbiguous reports an identity whose exact identifiers point at more
	// than one people record, or at a record another identity already holds.
	ErrAmbiguous = errors.New("sender_identity_ambiguous")
)

// MatcherVersion names the matcher that made a link. A change to the matcher
// changes this string, and the evaluation runs again for it.
const MatcherVersion = "identity-matcher/1"

// Kind is the kind of an identifier a platform shows for an author.
type Kind string

const (
	Phone Kind = "phone"
	Email Kind = "email"
)

// Identity is the platform's own key for an author: the platform, the workspace
// or tenant where the platform scopes its ids, and the user id or handle.
type Identity struct {
	Platform string
	Scope    string
	User     string
}

func (i Identity) valid() bool {
	return i.Platform != "" && i.User != "" && !strings.ContainsRune(i.Platform+i.Scope+i.User, 0)
}

// Key is the identity's stable key.
func (i Identity) Key() string { return i.Platform + "\x00" + i.Scope + "\x00" + i.User }

// Key returns the identity's stable key.
func (i Identity) String() string { return i.Platform + "/" + i.Scope + "/" + i.User }

// Identifier is one phone number or email address a platform shows for an
// author, normalized to E.164 or lower case.
type Identifier struct {
	Kind  Kind
	Value string
}

// SeenIdentifier is an identifier with where it was seen and when.
type SeenIdentifier struct {
	Kind   Kind
	Value  string
	Source string
	At     time.Time
}

// NameSeen is a display name as the platform showed it at one time. A display
// name is never an identifier.
type NameSeen struct {
	Value string
	At    time.Time
}

// Observation is what an adapter saw for one author.
type Observation struct {
	Identity Identity
	// At is when the observation was made. A zero time uses the store clock.
	At time.Time
	// Self marks the person's own account on the platform. It is never linked
	// as another person.
	Self        bool
	Names       []NameSeen
	Identifiers []SeenIdentifier
}

// Class is how a link was made.
type Class string

const (
	// Confirmed is the person's answer.
	Confirmed Class = "confirmed"
	// Exact is a shared phone number or email address, or a contact card.
	Exact Class = "exact"
	// Proposed rests on weak signals only and is never usable.
	Proposed Class = "proposed"
)

// LinkState is a link's state.
type LinkState string

const (
	Active   LinkState = "active"
	Rejected LinkState = "rejected"
	Removed  LinkState = "removed"
)

// Signal kinds.
const (
	SignalPhone      = "phone"
	SignalEmail      = "email"
	SignalCard       = "contact_card"
	SignalName       = "display_name"
	SignalNickname   = "nickname"
	SignalAnswer     = "person_answer"
	SignalPhotograph = "photo"
	SignalChat       = "shared_chat"
	SignalStyle      = "writing_style"
	SignalTiming     = "timing"
)

// Ambiguity reasons.
const (
	ReasonSharedIdentifier   = "shared_identifier"
	ReasonTwoLinks           = "two_links"
	ReasonIdentifierConflict = "identifier_conflict"
	ReasonIdentifierReused   = "identifier_reused"
)

// Signal is one piece of evidence behind a link, with its source.
type Signal struct {
	Kind   string
	Source string
	Value  string
}

// Link ties one sender identity to one people record.
type Link struct {
	ID           string
	Identity     Identity
	Record       string
	Class        Class
	Signals      []Signal
	Maker        string
	At           time.Time
	Revision     int
	State        LinkState
	ValidFrom    time.Time
	ValidThrough time.Time
	// Ambiguous and Reason are derived from the identity's current links and
	// identifiers; they are never stored.
	Ambiguous bool
	Reason    string
}

// Usable reports whether the link may be used for learning about the record at
// a time. Confirmed links are the person's word; exact links must be
// unambiguous and must still hold.
func (l Link) Usable(at time.Time) bool {
	if l.State != Active || l.Ambiguous {
		return false
	}
	if l.Class == Confirmed {
		return true
	}
	if l.Class != Exact {
		return false
	}
	if !l.ValidFrom.IsZero() && at.Before(l.ValidFrom) {
		return false
	}
	if !l.ValidThrough.IsZero() && !at.Before(l.ValidThrough) {
		return false
	}
	return true
}

// People is the source of known people records with their identifiers. Core
// supplies the records repository.
type People interface {
	People(context.Context, string) ([]Person, error)
}

// Person is one known person as matching reads it. Names include the record
// title and the aliases the person keeps.
type Person struct {
	Record      string
	Names       []string
	Identifiers []SeenIdentifier
}

// Removals is the one retraction path. A removed, corrected, or superseded link
// retracts the items it produced through it. A removal path that can journal
// inside the caller's writer commits that retraction with the link change.
type Removals interface {
	Forget(context.Context, retract.Request) error
}

// Normalize returns the canonical form of an identifier value.
func Normalize(kind Kind, raw string) (string, error) {
	switch kind {
	case Phone:
		return NormalizePhone(raw)
	case Email:
		return NormalizeEmail(raw)
	}
	return "", ErrInvalid
}

// NormalizePhone returns the number in E.164 form. A number without a country
// code is refused rather than guessed.
func NormalizePhone(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ErrInvalid
	}
	digits := make([]rune, 0, len(trimmed))
	international := false
	for _, r := range trimmed {
		switch {
		case r >= '0' && r <= '9':
			digits = append(digits, r)
		case r == '+':
			if len(digits) != 0 || international {
				return "", ErrInvalid
			}
			international = true
		case r == ' ' || r == '-' || r == '.' || r == '(' || r == ')':
		default:
			return "", ErrInvalid
		}
	}
	if len(digits) >= 2 && digits[0] == '0' && digits[1] == '0' && !international {
		digits = digits[2:]
		international = true
	}
	if !international || len(digits) < 8 || len(digits) > 15 {
		return "", ErrInvalid
	}
	return "+" + string(digits), nil
}

// NormalizeEmail returns the address in lower case. It keeps the local part as
// written apart from case, which is the most a matcher may assume.
func NormalizeEmail(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !utf8.ValidString(trimmed) {
		return "", ErrInvalid
	}
	lowered := strings.ToLower(trimmed)
	local, domain, ok := strings.Cut(lowered, "@")
	if !ok || local == "" || strings.Contains(domain, "@") || !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", ErrInvalid
	}
	for _, r := range lowered {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", ErrInvalid
		}
	}
	return lowered, nil
}

// identifierOf returns the identifier an identity's handle itself states, when
// the handle is a phone number or an email address. iMessage and WhatsApp key
// senders this way.
func identifierOf(identity Identity) (Identifier, bool) {
	for _, kind := range []Kind{Phone, Email} {
		if value, err := Normalize(kind, identity.User); err == nil {
			return Identifier{Kind: kind, Value: value}, true
		}
	}
	return Identifier{}, false
}
