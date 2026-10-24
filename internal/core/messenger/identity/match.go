// SPDX-License-Identifier: Apache-2.0
package identity

import (
	"strings"
	"unicode"
)

// recordsListing returns the ids of people records that list an identifier.
func recordsListing(people []Person, kind Kind, value string) []string {
	var records []string
	for _, person := range people {
		for _, identifier := range person.Identifiers {
			if identifier.Kind == kind && identifier.Value == value {
				records = append(records, person.Record)
				break
			}
		}
	}
	return unique(records)
}

// candidates returns the people records the identity's identifiers point at,
// with the matching contacts as signals.
func candidates(people []Person, identifiers []storedIdentifier) (map[string][]Signal, error) {
	byRecord := map[string][]Signal{}
	for _, identifier := range identifiers {
		for _, person := range people {
			for _, contact := range person.Identifiers {
				if contact.Kind != identifier.Kind || contact.Value != identifier.Value {
					continue
				}
				// The signal says where the platform showed it; the record's own
				// source is the other side of the match.
				signal := Signal{Kind: string(identifier.Kind), Source: identifier.Source, Value: identifier.Value}
				byRecord[person.Record] = appendSignal(byRecord[person.Record], signal)
				if isContactCard(contact.Source) {
					byRecord[person.Record] = appendSignal(byRecord[person.Record],
						Signal{Kind: SignalCard, Source: contact.Source, Value: identifier.Source})
				}
			}
		}
	}
	return byRecord, nil
}

func isContactCard(source string) bool {
	return strings.Contains(strings.ToLower(source), "card")
}

func appendSignal(signals []Signal, signal Signal) []Signal {
	for _, existing := range signals {
		if existing == signal {
			return signals
		}
	}
	return append(signals, signal)
}

// nameCandidates returns the people records whose names one of the identity's
// display names matches. Names never decide; the result only ever proposes.
func nameCandidates(people []Person, names []string) map[string]Signal {
	matched := map[string]Signal{}
	for _, person := range people {
		for _, known := range person.Names {
			if known == "" {
				continue
			}
			for _, seen := range names {
				if !namesSimilar(known, seen) {
					continue
				}
				signal := Signal{Kind: SignalName, Source: "display name", Value: seen}
				if !sameText(known, seen) {
					signal.Kind = SignalNickname
				}
				if _, ok := matched[person.Record]; !ok {
					matched[person.Record] = signal
				}
			}
		}
	}
	return matched
}

func sameText(a, b string) bool { return normalizeName(a) == normalizeName(b) }

// namesSimilar reports whether two display names may belong to one person. It
// accepts the same name written differently and a short form of a longer name,
// and nothing weaker.
func namesSimilar(a, b string) bool {
	left, right := normalizeName(a), normalizeName(b)
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	leftTokens, rightTokens := strings.Fields(left), strings.Fields(right)
	if len(leftTokens) == 0 || len(rightTokens) == 0 {
		return false
	}
	if leftTokens[0] != rightTokens[0] || len(leftTokens[0]) < 3 {
		return false
	}
	// A name and its short form: "Anna" and "Anna Kim", not two different
	// people who share a first name.
	return len(leftTokens) == 1 || len(rightTokens) == 1
}

func normalizeName(text string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		case unicode.IsSpace(r):
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func unique(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
