// SPDX-License-Identifier: Apache-2.0
package exchange_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/exchange"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/provider"
)

type artifactStream struct{ events []provider.Event }

func (s *artifactStream) Next(context.Context) (provider.Event, error) {
	if len(s.events) == 0 {
		return provider.Event{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func (*artifactStream) Meta() provider.Meta { return provider.Meta{} }
func (*artifactStream) Close() error        { return nil }

// The composition root connects peer L4 flows through their callbacks. This
// scenario uses the real run supervisor, assets, ledger and mailbox on one canon.
func TestExchangeThroughBackgroundRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	db := openAt(t, root, 1)
	a, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s := exchange.NewWithMaterials(db, materials(t, a))
	r := start(t, s)
	var calls atomic.Int64
	var prepared *exchange.Prepared
	service, err := runs.New(runs.Config{DB: db, Assets: a, WorkDir: filepath.Join(root, "work"), Epoch: 1,
		Authorize: func(_ context.Context, o runs.Origin, b runs.Brief) error {
			if o.Agent != "answerer" || o.Conversation != r.ID || o.Turn != r.DeliveryID || len(b.Allow) != 0 {
				t.Errorf("wrong run authority: %+v %+v", o, b)
			}
			return nil
		},
		Prepare: func(ctx context.Context, run runs.Record, _ []provider.Item) ([]provider.Item, error) {
			if prepared == nil {
				p, err := s.Prepare(ctx, run.Origin.Turn, run.Origin.Agent, now, &knowledge{t: t})
				if err != nil {
					return nil, err
				}
				prepared = &p
			}
			persona, _ := json.Marshal(map[string]string{"role": "developer", "content": prepared.Request.Instructions})
			return append([]provider.Item{persona}, prepared.Request.Items...), nil
		},
		Stream: func(_ context.Context, req provider.Request) (provider.Stream, error) {
			calls.Add(1)
			if req.Purpose != provider.Background || len(req.Tools) != 0 {
				t.Errorf("wrong model scope: %+v", req)
			}
			all := string(mustJSON(t, req.Items))
			for _, want := range []string{"persona-core", "shared-fact", "named-material", "external_evidence"} {
				if !strings.Contains(all, want) {
					t.Errorf("missing scoped input: %s", want)
				}
			}
			for _, forbidden := range []string{"private-fact", "person-conversation", "owner-person"} {
				if strings.Contains(all, forbidden) {
					t.Errorf("private input leaked: %s", forbidden)
				}
			}
			artifact := string(mustJSON(t, runs.Artifact{Body: "Both drafts cover the named sections", Done: true}))
			item := mustJSON(t, map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": artifact}}})
			return &artifactStream{events: []provider.Event{{Kind: provider.ItemDone, Item: item}, {Kind: provider.Terminal}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	h := request().HandOff
	runID, _, err := service.Start(t.Context(), runs.Origin{Person: "owner-person", Agent: "answerer", Conversation: r.ID, Turn: r.DeliveryID}, runs.Brief{Goal: h.Wanted, Scope: "named exchange materials", NotTouch: h.OutOfScope, Done: h.Done, Result: "document_draft", Checks: []runs.Check{{Name: h.Check, Kind: runs.Claimed}}})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("model called before run receipt")
	}
	if err = service.Release(t.Context(), runID); err != nil {
		t.Fatal(err)
	}
	if err = service.Wait(ctx, runID); err != nil {
		t.Fatal(err)
	}
	run, err := service.Get(t.Context(), runID)
	if err != nil || run.Outcome != runs.Complete {
		t.Fatalf("run: %+v %v", run, err)
	}
	checks := 0
	err = s.Answer(t.Context(), r.DeliveryID, "answerer", exchange.Answer{Text: run.Artifact.Body, Evidence: []exchange.Evidence{{Material: exchange.Material{ID: run.Material.ID, Revision: strconv.FormatInt(run.Material.Revision, 10)}, At: now}}}, now, func(_ context.Context, asker, name string, a exchange.Answer) (bool, error) {
		checks++
		if asker != "asker" || name != h.Check || a.Text != run.Artifact.Body {
			t.Fatalf("wrong asker check: %s %s %+v", asker, name, a)
		}
		return true, nil
	})
	if err != nil || calls.Load() != 1 || checks != 1 || record(t, s, r.ID).Outcome != exchange.Checked {
		t.Fatalf("exchange: calls=%d checks=%d err=%v", calls.Load(), checks, err)
	}
	notices, err := service.Notices(t.Context(), "person-conversation")
	if err != nil || len(notices) != 0 {
		t.Fatalf("exchange routed to person conversation: %+v %v", notices, err)
	}
}
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
