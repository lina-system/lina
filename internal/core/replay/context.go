// SPDX-License-Identifier: Apache-2.0
package replay

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/kit/provider"
)

// Context is transient, rebuilt from current authorized sources on each load.
// Only its text-free Source is retained by the native turn producer.
type Context struct {
	Source            ledger.ContextSource
	Text, Description string
	Memory            *memory.Item
}

func pinnedMemory(ctx context.Context, tx *canon.Tx, scope Scope, ref ledger.Reference, at int64) (memory.Item, error) {
	var revision int64
	var state, visibility string
	err := tx.QueryRowContext(ctx, "SELECT json_extract(record,'$.revision'),state,scope FROM memory_item WHERE id=?", ref.ID).Scan(&revision, &state, &visibility)
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Item{}, Exclusion("forgotten")
	}
	if err != nil {
		return memory.Item{}, err
	}
	var excluded int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM retract_memory_exclusion WHERE id=?", ref.ID).Scan(&excluded); err != nil {
		return memory.Item{}, err
	}
	if state != "active" || excluded != 0 {
		return memory.Item{}, Exclusion("forgotten")
	}
	if strconv.FormatInt(revision, 10) != ref.Revision || visibility != "local" {
		return memory.Item{}, Exclusion("source_changed")
	}
	// Corrections retain their predecessor's revision and do not necessarily
	// advance the revocation epoch. Check again at publication/dispatch too.
	var corrected bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM memory_item WHERE supersedes=?)", ref.ID).Scan(&corrected); err != nil {
		return memory.Item{}, err
	}
	if corrected {
		return memory.Item{}, Exclusion("source_changed")
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT record FROM retract_visible_memory WHERE id=? AND person=? AND (holder=? OR holder='all') AND valid_from<=? AND (valid_until=0 OR valid_until>?)`, ref.ID, scope.Person, scope.Agent, at, at).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Item{}, Exclusion("source_changed")
	}
	if err != nil {
		return memory.Item{}, err
	}
	var item memory.Item
	if err = json.Unmarshal(raw, &item); err != nil {
		return memory.Item{}, err
	}
	for _, sensitivity := range item.Sensitivity {
		if sensitivity == "secret" {
			return memory.Item{}, Exclusion("source_changed")
		}
	}
	if err = memoryReference(ctx, tx, scope, ref.ID, revision, map[string]bool{}); err != nil {
		var excluded Exclusion
		if errors.As(err, &excluded) {
			return memory.Item{}, Exclusion("forgotten")
		}
		return memory.Item{}, err
	}
	return item, nil
}

func (s Source) contextSnapshot(ctx context.Context, tx *canon.Tx, scope Scope, ep Episode) ([]Context, []provider.Item, error) {
	a := ep.Evidence.Assembly
	fail := func() ([]Context, []provider.Item, error) { return nil, nil, Exclusion("missing_assembly") }
	at, err := time.Parse(time.RFC3339Nano, a.PreparedAt)
	if err != nil || len(a.Context) == 0 {
		return fail()
	}
	refs := append([]ledger.Reference(nil), a.Profile...)
	var skillRefs []ledger.Reference
	for _, source := range a.Context {
		refs = append(refs, source.Reference)
		if source.Source == "skill-v1" {
			skillRefs = append(skillRefs, source.Reference)
		}
	}
	if !slices.Equal(refs, a.Items) || !slices.Equal(skillRefs, a.Skills) {
		return fail()
	}
	var profile []provider.Item
	for _, ref := range a.Profile {
		item, err := pinnedMemory(ctx, tx, scope, ref, at.Unix())
		if err != nil {
			return nil, nil, err
		}
		raw, _ := json.Marshal(map[string]string{"role": "user", "content": "Remembered statement (" + item.ID + "): " + memory.ProfileText(item.Content)})
		profile = append(profile, raw)
	}
	// Check memory before any persona material or evidence fetch. Forgetting
	// may also hide an ancestor event used by another context projection.
	memories := map[string]memory.Item{}
	for _, source := range a.Context {
		if source.Source != "relationship-v1" && source.Source != "name-v1" {
			continue
		}
		id, ok := strings.CutPrefix(source.ID, "memory:")
		if !ok {
			return fail()
		}
		item, err := pinnedMemory(ctx, tx, scope, ledger.Reference{ID: id, Revision: source.Revision}, at.Unix())
		if err != nil {
			return nil, nil, err
		}
		memories[source.ID] = item
	}
	var out []Context
	for _, source := range a.Context {
		c := Context{Source: source}
		if source.Source != "persona-state-v1" && source.Source != "input-binding-v1" && source.Source != "input-choice-v1" && (source.Material != nil || len(source.Evidence) > 0) {
			return fail()
		}
		switch source.Source {
		case "input-binding-v1", "input-choice-v1":
			if source.Material != nil || source.Revision != "" || len(source.Evidence) != 1 || (source.Source == "input-binding-v1" && source.ID != "binding:"+source.Evidence[0].ID) {
				return fail()
			}
			if err = visible(ctx, tx, scope, source.ID); err != nil {
				return nil, nil, err
			}
			if err = visible(ctx, tx, scope, source.Evidence[0].ID); err != nil {
				return nil, nil, err
			}
			event, e := ledger.New(readTx{tx}, s.Materials).Get(ctx, source.ID)
			if e != nil {
				return nil, nil, e
			}
			if (source.Source == "input-binding-v1" && event.ScaffoldKind != "input_binding" || source.Source == "input-choice-v1" && event.ScaffoldKind != "input_binding_choice") || event.ConversationID != ep.Conversation || event.ParentID != source.Evidence[0].ID {
				return fail()
			}
			c.Text = event.Body.Text()
		case "clock-v1":
			if source.ID != "clock:"+ep.TurnID || source.Revision != "" {
				return fail()
			}
			c.Text = a.PreparedAt
		case "skill-v1":
			if !strings.HasPrefix(source.ID, "skill:") || source.Revision == "" {
				return fail()
			}
		case "persona-state-v1":
			switch source.ID {
			case "persona:" + scope.Agent + ":revision", "persona-expression:" + scope.Agent, "persona-settings:" + scope.Agent, "persona-methodology:" + scope.Agent, "working-preference:" + scope.Agent, "persona-emotion:" + scope.Agent + ":" + ep.Conversation:
			default:
				return fail()
			}
			if source.Material == nil || s.Materials == nil {
				return fail()
			}
			// Resolve visibility and ownership before asking the material cache for bytes.
			var n int
			err = tx.QueryRowContext(ctx, `SELECT count(*) FROM assets a JOIN asset_revisions r ON r.asset_id=a.id WHERE a.id=? AND r.revision=? AND a.person_id=? AND a.agent_id=? AND a.trashed_at=0`, source.Material.ID, source.Material.Revision, scope.Person, scope.Agent).Scan(&n)
			if err != nil {
				return nil, nil, err
			}
			if n != 1 {
				return fail()
			}
			for _, ref := range source.Evidence {
				if err = visible(ctx, tx, scope, ref.ID); err != nil {
					return nil, nil, err
				}
			}
			c.Text, err = s.Materials.Read(ctx, *source.Material)
			if err != nil {
				return nil, nil, err
			}
			hash := sha256.Sum256([]byte(c.Text))
			if hex.EncodeToString(hash[:]) != source.Revision {
				return fail()
			}
		case "relationship-v1", "name-v1":
			item := memories[source.ID]
			if source.Source == "relationship-v1" && item.Kind != memory.Relationship || source.Source == "name-v1" && item.Kind != memory.Name {
				return fail()
			}
			// Resolve own sources through the replay cache first. Its missing
			// material signal must reach withMaterials before quote projection
			// turns unavailable bytes into display metadata.
			for _, ref := range item.Evidence {
				if ref.Kind == memory.Event && ref.SourceAgent == scope.Agent {
					if _, err := ledger.New(readTx{tx}, s.Materials).Get(ctx, ref.ID); err != nil {
						return nil, nil, err
					}
				}
			}
			item.Evidence, err = memory.New(readTx{tx}).WithQuotes(ledger.New(readTx{tx}, s.Materials).Citation).Quotes(ctx, memory.RecallScope{Person: scope.Person, Agent: scope.Agent, Projection: true}, "memory", item.ID, item.Revision, item.Evidence)
			if err != nil {
				return nil, nil, err
			}
			c.Memory = &item
		default:
			return fail()
		}
		out = append(out, c)
	}
	return out, profile, nil
}

// ValidateEpisode also catches memory revisions changed without a revocation.
func (s Source) ValidateEpisode(ctx context.Context, ep Episode) error {
	a := ep.Evidence.Assembly
	if a.InstructionFormat == "persona-v1" || a.InstructionFormat == "persona-tui-v1" || a.InstructionFormat == "persona-tui-work-method-v1" || a.InstructionFormat == "persona-tui-work-method-v2" {
		at, err := time.Parse(time.RFC3339Nano, a.PreparedAt)
		if err != nil {
			return Exclusion("missing_assembly")
		}
		refs := append([]ledger.Reference(nil), a.Profile...)
		for _, c := range a.Context {
			if c.Source == "relationship-v1" || c.Source == "name-v1" {
				refs = append(refs, ledger.Reference{ID: strings.TrimPrefix(c.ID, "memory:"), Revision: c.Revision})
			}
		}
		err = s.DB.Read(ctx, func(tx *canon.Tx) error {
			for _, ref := range refs {
				if _, err := pinnedMemory(ctx, tx, Scope{Person: a.OwnerPersonID, Agent: a.AgentID}, ref, at.Unix()); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return s.Validate(ctx, ep.RevocationEpoch)
}
