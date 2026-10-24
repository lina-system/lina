// SPDX-License-Identifier: Apache-2.0
package replay_test

import (
	"strconv"
	"testing"
	"time"

	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/replay"
)

func TestProfileRevalidationRejectsCorrection(t *testing.T) {
	t.Parallel()
	db, source, _ := fixture(t)
	at := time.Unix(100, 0).UTC()
	_, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{
		ID: "preference", ConversationID: "preferences", AgentID: "agent", PersonID: "person",
		Surface: ledger.Surface{Kind: "tui"}, OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: "I prefer tea.",
	})
	if err != nil {
		t.Fatal(err)
	}
	flow := coreexec.NewMemory(db, nil, nil)
	in := memory.Write{Agent: "agent", Person: "person", Item: memory.Item{
		Kind: memory.Preference, Subject: memory.Subject{Kind: memory.Person, ID: "person"},
		Content: "I prefer tea.", Basis: memory.Stated, Standing: true,
		Evidence: []memory.Evidence{{Kind: memory.Event, ID: "preference", Precision: memory.Exact}},
	}}
	saved, err := flow.Remember(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := source.Epoch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ep := replay.Episode{RevocationEpoch: epoch, Evidence: ledger.RequestEvidence{Assembly: ledger.Assembly{
		InstructionFormat: "persona-tui-work-method-v1", PreparedAt: at.Format(time.RFC3339Nano),
		OwnerPersonID: "person", AgentID: "agent",
		Profile: []ledger.Reference{{ID: saved.ID, Revision: strconv.FormatInt(saved.Revision, 10)}},
	}}}
	if err = source.ValidateEpisode(t.Context(), ep); err != nil {
		t.Fatalf("original profile is not eligible: %v", err)
	}
	// A correction can race a loaded episode without changing the revocation
	// epoch or predecessor revision. The final publication check must catch it.
	in.Item.Content, in.Item.Supersedes = "I prefer coffee.", saved.ID
	if _, err = flow.Remember(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if err = source.Validate(t.Context(), epoch); err != nil {
		t.Fatalf("fixture changed the revocation epoch: %v", err)
	}
	if err = source.ValidateEpisode(t.Context(), ep); err != replay.Exclusion("source_changed") {
		t.Fatalf("corrected profile remains publishable: %v", err)
	}
}
