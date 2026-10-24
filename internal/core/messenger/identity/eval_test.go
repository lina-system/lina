// SPDX-License-Identifier: Apache-2.0
package identity_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if measures.OwnerGate() {
		t.Fatal("the synthetic set must not pass the owner-sample gate")
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
	if !measures.Repair.Clean {
		t.Fatalf("repair = %+v", measures.Repair)
	}
	if measures.Repair.ItemsRetracted != 1 || measures.Repair.Requeued != 1 || measures.Repair.OtherRequeued != 0 {
		t.Fatalf("repair counts = %+v", measures.Repair)
	}
}

func TestOwnerSampleWritesCountsOnly(t *testing.T) {
	t.Parallel()
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
	write("people.json", []identity.SetPerson{{
		ID: "record-1", Names: []string{"Synthetic Person"},
		Contacts: []identity.SetContact{{Kind: identity.Phone, Value: "+821012345678", Source: "owner sample"}},
	}})
	write("identities.json", []identity.SetIdentity{{
		Platform: "imessage", User: "+821012345678", At: "2026-10-01T09:00:00Z",
		Names:       []string{"Synthetic Person"},
		Identifiers: []identity.SetContact{{Kind: identity.Phone, Value: "+821012345678", Source: "owner sample"}},
	}})
	write("labels.json", map[string]string{"imessage//+821012345678": "record-1"})

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
	for _, forbidden := range []string{"Synthetic Person", "+821012345678", "record-1"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("counts kept private data: %q", forbidden)
		}
	}
	var written map[string]any
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"usable_links", "wrong_links", "precision", "linked_share", "rule_of_three", "gate"} {
		if _, ok := written[key]; !ok {
			t.Fatalf("counts are missing %q: %s", key, data)
		}
	}
	if written["gate"] != false || written["wrong_links"] != float64(0) || written["usable_links"] != float64(1) {
		t.Fatalf("a one-link sample must not pass the owner gate: %s", data)
	}
	info, err := os.Stat(counts)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("counts mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestRepeatedObservationsCountAsOneUsableLink(t *testing.T) {
	t.Parallel()
	person := identity.SetPerson{ID: "record-1", Contacts: []identity.SetContact{{Kind: identity.Phone, Value: "+821012345678", Source: "sample"}}}
	one := identity.SetIdentity{
		Platform: "imessage", User: "+821012345678", At: "2026-10-01T09:00:00Z", Expect: "record-1",
		Identifiers: []identity.SetContact{{Kind: identity.Phone, Value: "+821012345678", Source: "sample"}},
	}
	set := identity.Set{Version: 1, People: []identity.SetPerson{person}}
	for i := 0; i < 300; i++ {
		row := one
		row.At = time.Date(2026, 10, 1, 9, 0, i, 0, time.UTC).Format(time.RFC3339)
		set.Identities = append(set.Identities, row)
	}
	f := newFixture(t, nil)
	measures, err := identity.Evaluate(t.Context(), f.store, set)
	if err != nil {
		t.Fatal(err)
	}
	if measures.UsableLinks != 1 || measures.Identities != 1 {
		t.Fatalf("repeated observations counted as separate trials: identities=%d usable=%d", measures.Identities, measures.UsableLinks)
	}
	if measures.OwnerGate() {
		t.Fatal("300 copies of one link passed the owner-sample gate")
	}
}

func TestOwnerSampleGateNeedsThreeHundredTrials(t *testing.T) {
	t.Parallel()
	short := identity.CountsOf(identity.Measures{UsableLinks: 1})
	if short.Gate {
		t.Fatal("one usable link passed the owner-sample gate")
	}
	enough := identity.CountsOf(identity.Measures{UsableLinks: 300})
	if !enough.Gate || enough.WrongLinks != 0 {
		t.Fatalf("counts = %+v", enough)
	}
	wrong := identity.CountsOf(identity.Measures{UsableLinks: 300, WrongLinks: 1})
	if wrong.Gate {
		t.Fatal("a wrong link passed the owner-sample gate")
	}
	shortfall := identity.CountsOf(identity.Measures{UsableLinks: 299})
	if shortfall.Gate {
		t.Fatal("299 usable links passed the owner-sample gate")
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
