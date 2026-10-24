// SPDX-License-Identifier: Apache-2.0
package records

import (
	"context"
	"errors"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Contact is one phone number or email address a people record keeps, with
// where it was seen. Values are stored as the person's source showed them;
// sender identity normalizes them before matching.
type Contact struct {
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

func (c Contact) valid() bool {
	return (c.Kind == "phone" || c.Kind == "email") && strings.TrimSpace(c.Value) != "" && strings.TrimSpace(c.Source) != ""
}

// contactsHeading is the section of a people record that holds its contact
// data with the source of each item (materials-and-knowledge.md).
const contactsHeading = "## Contacts"

// contactSeparator splits a contact's value from its source. It is the em dash
// with spaces on both sides, so a value never has to be quoted.
const contactSeparator = " \u2014 "

// parseContacts reads the contact section of a people record body. Each line is
// one identifier: "- <phone|email> <value> <em dash> <source>". A line outside
// the section is prose; a malformed line inside it is refused.
func parseContacts(body string) ([]Contact, error) {
	lines := strings.Split(body, "\n")
	inside := false
	var contacts []Contact
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "# ") {
			inside = trimmed == contactsHeading
			continue
		}
		if !inside || trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "- ") {
			return nil, ErrInvalid
		}
		entry := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		kind, rest, ok := strings.Cut(entry, " ")
		if !ok {
			return nil, ErrInvalid
		}
		value, source, ok := strings.Cut(rest, contactSeparator)
		contact := Contact{Kind: kind, Value: strings.TrimSpace(value), Source: strings.TrimSpace(source)}
		if !ok || !contact.valid() {
			return nil, ErrInvalid
		}
		contacts = append(contacts, contact)
	}
	return contacts, nil
}

// PersonRecord is one people record as sender identity reads it: the record
// id, its title, the aliases the person keeps for it, and its contacts.
type PersonRecord struct {
	ID       string
	Title    string
	Names    []string
	Contacts []Contact
}

// People lists the person's active people records with the identifiers and
// aliases they carry. A people record is a record of kind person under an area
// or a resource; its contact section holds the identifiers that may link a
// messenger sender on their own, and its aliases are the names the person keeps
// for it. Sender identity never merges people by name.
func (s *Store) People(ctx context.Context, person string) ([]PersonRecord, error) {
	if person == "" {
		return nil, ErrInvalid
	}
	var ids []string
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM records_text WHERE person=? AND kind='person' AND state='active' ORDER BY id", person)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	people := make([]PersonRecord, 0, len(ids))
	for _, id := range ids {
		record, _, err := s.Read(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		doc, err := parseDocument(record.Data)
		if err != nil {
			return nil, err
		}
		if doc.kind != "person" || doc.state != "active" || doc.person != person {
			continue
		}
		people = append(people, PersonRecord{ID: id, Title: doc.title, Names: doc.names, Contacts: doc.contacts})
	}
	return people, nil
}
