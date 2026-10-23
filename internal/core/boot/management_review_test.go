// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/assets"
	memoryexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/internal/core/turn"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/rpc"
)

func TestDeleteCancelsActiveAgentTurn(t *testing.T) {
	r, session, a, _ := managementFixture(t)
	defer r.lanes.Close(context.Background())
	started := make(chan struct{})
	result, err := r.lanes.Submit(t.Context(), "main:"+a, func(ctx context.Context) error { close(started); <-ctx.Done(); return context.Cause(ctx) })
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err = r.Agent(t.Context(), session, protocol.AgentRequest{Operation: textptr("delete"), AgentID: &a}); err != nil {
		t.Fatal(err)
	}
	select {
	case cause := <-result:
		var interruption turn.Interrupt
		if !errors.As(cause, &interruption) || interruption.Issuer != turn.Person {
			t.Fatal("deletion did not use the person interruption flow", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("deleted agent's active turn was not cancelled")
	}
}
func TestMemoryListFitsTransportFrame(t *testing.T) {
	r, session, a, _ := managementFixture(t)
	for i := 0; i < 20; i++ {
		text := fmt.Sprintf("fixture %d ", i) + strings.Repeat("<", 16000)
		if _, err := r.Memory(t.Context(), session, protocol.MemoryRequest{Operation: textptr("remember"), AgentID: &a, Text: &text}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := r.Memory(t.Context(), session, protocol.MemoryRequest{Operation: textptr("list"), AgentID: &a})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= rpc.MaxFrameBytes-4096 {
		t.Fatalf("memory list payload exceeds frame budget: %d", len(raw))
	}
}

func TestMemoryPagesReachEveryItem(t *testing.T) {
	r, session, a, _ := managementFixture(t)
	for i := 0; i < 20; i++ {
		text := fmt.Sprintf("page %d ", i) + strings.Repeat("<", 16000)
		if _, err := r.Memory(t.Context(), session, protocol.MemoryRequest{Operation: textptr("remember"), AgentID: &a, Text: &text}); err != nil {
			t.Fatal(err)
		}
	}
	request := protocol.MemoryRequest{Operation: textptr("list"), AgentID: &a, Text: textptr("page")}
	seen := map[string]bool{}
	for pages := 0; pages < 20; pages++ {
		result, err := r.Memory(t.Context(), session, request)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(result)
		if len(raw) >= rpc.MaxFrameBytes-4096 {
			t.Fatal("page exceeds transport")
		}
		if result.Items == nil {
			t.Fatal("missing page")
		}
		for _, item := range *result.Items {
			if seen[item.ItemID] {
				t.Fatal("duplicate cursor page")
			}
			seen[item.ItemID] = true
		}
		if result.Next == nil {
			if len(seen) != 20 {
				t.Fatalf("pagination lost items: %d", len(seen))
			}
			return
		}
		request.After = result.Next
	}
	t.Fatal("pagination did not terminate")
}
func TestForgottenLedgerSourceCannotEnterNextModelContext(t *testing.T) {
	r, session, a, _ := managementFixture(t)
	r.agent = a
	r.conversation = "main:" + a
	now := time.Now()
	text := "fixture sourced orchard statement"
	event, err := ledger.New(r.db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "source", ConversationID: r.conversation, AgentID: a, PersonID: r.person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: text})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := memoryexec.NewMemory(r.db, nil, nil).Remember(t.Context(), memory.Write{Person: r.person, Agent: a, Item: memory.Item{Kind: memory.Fact, Content: text, Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated, Standing: true, Evidence: []memory.Evidence{{Kind: memory.Event, ID: event.ID, Precision: memory.Exact}}}})
	if err != nil {
		t.Fatal(err)
	}
	before, refs, epoch, _, err := r.modelContext(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(before)
	if !bytes.Contains(raw, []byte(text)) || len(refs) != 1 {
		t.Fatal("source was not in prepared context")
	}
	if _, err = r.Memory(t.Context(), session, protocol.MemoryRequest{Operation: textptr("forget"), AgentID: &a, ItemID: &receipt.ID}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(r.validateModelContext(t.Context(), epoch, refs), retract.ErrStaleSnapshot) {
		t.Fatal("old preparation remains valid")
	}
	after, _, _, _, err := r.modelContext(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(after)
	if bytes.Contains(raw, []byte(text)) {
		t.Fatal("forgotten source survived in model history")
	}
	original, err := r.events(t.Context(), "")
	if err != nil || len(original) != 1 {
		t.Fatal("forget erased the person's ledger", err)
	}
}
func TestCorrectionInvalidatesPreparedProfile(t *testing.T) {
	r, session, a, _ := managementFixture(t)
	r.agent = a
	r.conversation = "main:" + a
	saved, err := r.Memory(t.Context(), session, protocol.MemoryRequest{Operation: textptr("remember"), AgentID: &a, Text: textptr("old orchard")})
	if err != nil {
		t.Fatal(err)
	}
	_, refs, epoch, _, err := r.modelContext(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.validateModelContext(t.Context(), epoch, refs); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Memory(t.Context(), session, protocol.MemoryRequest{Operation: textptr("correct"), AgentID: &a, ItemID: saved.ItemID, Text: textptr("new meadow")}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(r.validateModelContext(t.Context(), epoch, refs), retract.ErrStaleSnapshot) {
		t.Fatal("superseded profile would enter the next model round")
	}
	after, refs, epoch, _, err := r.modelContext(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(after)
	if bytes.Contains(raw, []byte("old orchard")) || !bytes.Contains(raw, []byte("new meadow")) {
		t.Fatal("fresh preparation did not replace corrected memory")
	}
	if err = r.validateModelContext(t.Context(), epoch, refs); err != nil {
		t.Fatal(err)
	}
}
func TestDeletedAgentTopicAuthorization(t *testing.T) {
	r, session, a, _ := managementFixture(t)
	for _, prefix := range []string{"conversation:main:", "results:main:"} {
		if !r.topicAuthorized(t.Context(), prefix+a) {
			t.Fatal("active agent topic refused")
		}
	}
	if _, err := r.Agent(t.Context(), session, protocol.AgentRequest{Operation: textptr("delete"), AgentID: &a}); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"conversation:main:", "results:main:"} {
		if r.topicAuthorized(t.Context(), prefix+a) {
			t.Fatal("deleted agent topic still authorized")
		}
	}
}

func TestMemoryListKeepsItemsWithOversizedEvidence(t *testing.T) {
	for _, material := range []bool{false, true} {
		t.Run(fmt.Sprint(material), func(t *testing.T) {
			r, session, agent, _ := managementFixture(t)
			store, err := assets.Open(t.Context(), r.root, r.db)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			writer, err := assets.OpenMaterialWriter(t.Context(), store)
			if err != nil {
				t.Fatal(err)
			}
			r.materials = &conversationMaterials{store: store, writer: writer, actor: assets.Actor{PersonID: r.person, AgentID: agent}}
			older, err := r.Memory(t.Context(), session, protocol.MemoryRequest{Operation: textptr("remember"), AgentID: &agent, Text: textptr("earlier memory")})
			if err != nil {
				t.Fatal(err)
			}
			count, size := 8, 16000
			if material {
				count, size = 1, 100000
			}
			var id string
			for i := 0; i < count; i++ {
				now := time.Now()
				event, err := ledger.New(r.db, r.materials).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: fmt.Sprintf("large-%d", i), ConversationID: "large", AgentID: agent, PersonID: r.person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: strings.Repeat("<", size)})
				if err != nil {
					t.Fatal(err)
				}
				receipt, err := memoryexec.NewMemory(r.db, r.materials, nil).Remember(t.Context(), memory.Write{Agent: agent, Person: r.person, Item: memory.Item{Kind: memory.Fact, Content: "prefers tea", Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: event.ID, Precision: memory.Exact}}}})
				if err != nil {
					t.Fatal(err)
				}
				id = receipt.ID
			}
			result, err := r.Memory(t.Context(), session, protocol.MemoryRequest{Operation: textptr("list"), AgentID: &agent})
			if err != nil || result.Items == nil || len(*result.Items) != 2 {
				t.Fatalf("large quote blocked listing: %+v %v", result, err)
			}
			items := *result.Items
			if items[0].ItemID != id || items[1].ItemID != *older.ItemID {
				t.Fatal("item order or continuation lost")
			}
			if items[0].EvidenceDetails == nil {
				t.Fatal("missing quote availability")
			}
			for _, ref := range *items[0].EvidenceDetails {
				if ref.Quote != nil || ref.Status == nil || *ref.Status != "quote_too_large" {
					t.Fatal("missing bounded-quote status")
				}
			}
			raw, err := json.Marshal(result)
			if err != nil || len(raw) > 512<<10 {
				t.Fatalf("page exceeds budget: %d %v", len(raw), err)
			}
		})
	}
}
