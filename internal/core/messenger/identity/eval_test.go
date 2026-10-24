// SPDX-License-Identifier: Apache-2.0
package identity_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/messenger/identity"
)

// TestSyntheticSetHasNoWrongUsableLinkAndFlagsEveryAmbiguity is the gate on the
// synthetic set: the matcher may make no wrong usable link and must flag every
// ambiguity the set plants.
func TestSyntheticSetHasNoWrongUsableLinkAndFlagsEveryAmbiguity(t *testing.T) {
	t.Parallel()
	set, err := identity.LoadSet(filepath.Join("..", "..", "..", "..", "testdata", "identity", "synthetic.json"))
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, nil)
	measures, err := identity.Evaluate(t.Context(), f.store, set)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(identity.Report(measures))
	if measures.WrongLinks != 0 {
		t.Fatalf("wrong usable links = %d, want 0", measures.WrongLinks)
	}
	if measures.AmbiguitiesFlagged != measures.Ambiguities {
		t.Fatalf("ambiguities flagged = %d/%d", measures.AmbiguitiesFlagged, measures.Ambiguities)
	}
	if !measures.Gate() {
		t.Fatal("the synthetic set does not pass the gate")
	}
	if measures.UsableLinks == 0 || measures.Precision() != 1 {
		t.Fatalf("usable links = %d precision = %v", measures.UsableLinks, measures.Precision())
	}
	if measures.LinkedAutomatically == 0 || measures.LinkedShare() == 0 {
		t.Fatal("no identity was linked automatically")
	}
	if measures.Proposals == 0 || measures.ProposalTop1Rate() != 1 {
		t.Fatalf("proposals = %d top-1 = %v", measures.Proposals, measures.ProposalTop1Rate())
	}
	if measures.Windows == 0 || measures.WindowsRejected != measures.Windows {
		t.Fatalf("moved windows = %d/%d rejected", measures.WindowsRejected, measures.Windows)
	}
	if !measures.Repair.Clean {
		t.Fatalf("repair = %+v", measures.Repair)
	}
	if measures.Repair.ItemsRetracted != 1 || measures.Repair.Requeued != 1 || measures.Repair.OtherRequeued != 0 {
		t.Fatalf("repair counts = %+v", measures.Repair)
	}
}

// TestRepeatedIdentityCountsItsLinkOnce proves the measures are over identities,
// not observations: an identity seen twice is one identity with one usable link.
func TestRepeatedIdentityCountsItsLinkOnce(t *testing.T) {
	t.Parallel()
	contact := identity.SetContact{Kind: identity.Phone, Value: "+821012340001", Source: "iMessage profile"}
	seen := identity.SetIdentity{Platform: "imessage", User: "+821012340001", Names: []string{"Anna"},
		Identifiers: []identity.SetContact{contact}, Expect: "record-anna"}
	first, second := seen, seen
	first.At, second.At = "2026-10-01T09:00:00Z", "2026-10-02T09:00:00Z"
	set := identity.Set{Version: 1,
		People:     []identity.SetPerson{{ID: "record-anna", Names: []string{"Anna Kim"}, Contacts: []identity.SetContact{contact}}},
		Identities: []identity.SetIdentity{first, second}}
	f := newFixture(t, nil)
	measures, err := identity.Evaluate(t.Context(), f.store, set)
	if err != nil {
		t.Fatal(err)
	}
	if measures.Identities != 1 || measures.UsableLinks != 1 || measures.LinkedAutomatically != 1 {
		t.Fatalf("identities = %d usable links = %d linked = %d, want 1 each", measures.Identities, measures.UsableLinks, measures.LinkedAutomatically)
	}
}

func TestSetRejectsConflictingLabelsForOneIdentity(t *testing.T) {
	t.Parallel()
	data := `{"version":1,"identities":[
{"platform":"imessage","user":"+821012340001","at":"2026-10-01T09:00:00Z","expect":"record-a"},
{"platform":"imessage","user":"+821012340001","at":"2026-10-02T09:00:00Z","expect":"record-b"}]}`
	if _, err := identity.ParseSet([]byte(data)); err == nil {
		t.Fatal("one identity with two labels was accepted")
	}
}

// writeOwnerSample writes an owner sample of n people, each with one identity
// that links exactly to its record.
func writeOwnerSample(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	people := make([]identity.SetPerson, 0, n)
	identities := make([]identity.SetIdentity, 0, n)
	labels := make(map[string]string, n)
	for i := range n {
		number := fmt.Sprintf("+82101%07d", i)
		record := fmt.Sprintf("record-%d", i)
		contact := identity.SetContact{Kind: identity.Phone, Value: number, Source: "owner sample"}
		people = append(people, identity.SetPerson{ID: record, Names: []string{"Synthetic Person"}, Contacts: []identity.SetContact{contact}})
		identities = append(identities, identity.SetIdentity{Platform: "imessage", User: number, At: "2026-10-01T09:00:00Z",
			Names: []string{"Synthetic Person"}, Identifiers: []identity.SetContact{contact}})
		labels["imessage//"+number] = record
	}
	write("people.json", people)
	write("identities.json", identities)
	write("labels.json", labels)
	return dir
}

func TestOwnerSampleWritesCountsOnly(t *testing.T) {
	t.Parallel()
	dir := writeOwnerSample(t, 1)
	set, err := identity.LoadOwnerSample(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, nil)
	measures, err := identity.Evaluate(t.Context(), f.store, set)
	if err != nil {
		t.Fatal(err)
	}
	counts := filepath.Join(dir, "counts.json")
	if err := identity.WriteCounts(counts, measures); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(counts)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Synthetic Person", "+821010000000", "record-0"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("counts kept private data: %q", forbidden)
		}
	}
	var written map[string]any
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"usable_links", "wrong_links", "precision", "linked_share", "rule_of_three", "required_usable_links", "gate"} {
		if _, ok := written[key]; !ok {
			t.Fatalf("counts are missing %q: %s", key, data)
		}
	}
	if written["wrong_links"] != float64(0) || written["usable_links"] != float64(1) {
		t.Fatalf("counts = %s", data)
	}
	info, err := os.Stat(counts)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("counts mode = %v, want 0600", info.Mode().Perm())
	}
}

// TestOwnerSampleGateNeedsEnoughUsableLinks proves the owner-sample gate holds
// only once enough usable links are checked with none wrong: one correct link
// says almost nothing about the wrong-link rate.
func TestOwnerSampleGateNeedsEnoughUsableLinks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		n    int
		gate bool
	}{{1, false}, {identity.OwnerSampleUsableLinks - 1, false}, {identity.OwnerSampleUsableLinks, true}} {
		t.Run(fmt.Sprint(tc.n), func(t *testing.T) {
			t.Parallel()
			set, err := identity.LoadOwnerSample(writeOwnerSample(t, tc.n))
			if err != nil {
				t.Fatal(err)
			}
			f := newFixture(t, nil)
			measures, err := identity.Evaluate(t.Context(), f.store, set)
			if err != nil {
				t.Fatal(err)
			}
			if measures.UsableLinks != tc.n || measures.WrongLinks != 0 {
				t.Fatalf("usable links = %d wrong = %d, want %d and 0", measures.UsableLinks, measures.WrongLinks, tc.n)
			}
			if measures.Gate() != tc.gate || identity.CountsOf(measures).Gate != tc.gate {
				t.Fatalf("gate = %t, want %t", measures.Gate(), tc.gate)
			}
		})
	}
}

func TestOwnerSampleNeedsALabelForEveryIdentity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "people.json"), []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identities.json"), []byte(`[{"platform":"imessage","user":"+821012345678","at":"2026-10-01T09:00:00Z","expect":""}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "labels.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.LoadOwnerSample(dir); err == nil {
		t.Fatal("an unlabelled identity was accepted")
	}
}
