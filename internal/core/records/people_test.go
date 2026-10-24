// SPDX-License-Identifier: Apache-2.0
package records

import (
	"strings"
	"testing"
)

func personDoc(front, title, contacts string) []byte {
	return []byte("---\n" + front + "\n---\n\n# " + title + "\n\nA short profile.\n\n## Contacts\n\n" + contacts)
}

// TestPeopleReadsContactsAndAliases proves sender identity can read a people
// record's identifiers from its contact section, with their source, and the
// aliases the person keeps for it, for the record's own owner only.
func TestPeopleReadsContactsAndAliases(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	commit(t, s, Change{ID: "person-anna", Path: "areas/people/anna.md", Data: personDoc(`id: "person-anna"
kind: person
state: active
person_id: "person"
names: ["Anna","Annie"]`, "Anna Kim", `- phone +82 10-1234-0001 — iMessage profile
- email Anna@Example.com — Google contact`)})
	commit(t, s, Change{ID: "person-other", Path: "areas/people/other.md", Data: personDoc(`id: "person-other"
kind: person
state: active
person_id: "elsewhere"
names: ["Other"]`, "Other Person", "")})
	commit(t, s, Change{ID: "memo", Path: "inbox/memo.md", Data: doc("memo", "Not a person")})

	people, err := s.People(t.Context(), "person")
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 1 || people[0].ID != "person-anna" {
		t.Fatalf("people = %+v", people)
	}
	record := people[0]
	if record.Title != "Anna Kim" || strings.Join(record.Names, ",") != "Anna,Annie" {
		t.Fatalf("record = %+v", record)
	}
	if len(record.Contacts) != 2 || record.Contacts[0].Kind != "phone" || record.Contacts[0].Value != "+82 10-1234-0001" || record.Contacts[0].Source != "iMessage profile" {
		t.Fatalf("contacts = %+v", record.Contacts)
	}
	if record.Contacts[1].Kind != "email" || record.Contacts[1].Value != "Anna@Example.com" || record.Contacts[1].Source != "Google contact" {
		t.Fatalf("contacts = %+v", record.Contacts)
	}
}

func TestPeopleRecordRejectsAMalformedContact(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	for _, contacts := range []string{
		"- fax +821012340001 — card",
		"- phone  — card",
		"- phone +821012340001",
		"- email anna@example.com — ",
		"phone +821012340001 — card",
	} {
		data := personDoc(`id: "person-bad"
kind: person
state: active
person_id: "person"`, "Bad", contacts)
		if _, err := s.Commit(t.Context(), Change{ID: "person-bad", Path: "areas/people/bad.md", Data: data}, actor); err == nil {
			t.Fatalf("accepted contact section %q", contacts)
		}
	}
}

func TestContactProseOutsideTheSectionIsIgnored(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	data := personDoc(`id: "person-anna"
kind: person
state: active
person_id: "person"`, "Anna", `- phone +821012340001 — iMessage profile`)
	data = append(data, []byte("\n## Notes\n\n- not a contact\n")...)
	commit(t, s, Change{ID: "person-anna", Path: "areas/people/anna.md", Data: data})
	people, err := s.People(t.Context(), "person")
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 1 || len(people[0].Contacts) != 1 {
		t.Fatalf("people = %+v", people)
	}
}

func TestArchivedPersonIsNotMatched(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	commit(t, s, Change{ID: "person-old", Path: "areas/people/old.md", Data: personDoc(`id: "person-old"
kind: person
state: archived
person_id: "person"`, "Old", "- phone +821012340001 — iMessage profile")})
	people, err := s.People(t.Context(), "person")
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 0 {
		t.Fatalf("an archived person was matched: %+v", people)
	}
}
