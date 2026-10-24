// SPDX-License-Identifier: Apache-2.0
package storebench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/recall"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

const person = "person:storebench"
const agent = "storebench-agent"

type distribution struct {
	Count int   `json:"count"`
	P50NS int64 `json:"p50_ns"`
	P95NS int64 `json:"p95_ns"`
	MaxNS int64 `json:"max_ns"`
}
type report struct {
	Version           int                     `json:"report_version"`
	Schema            int                     `json:"schema_version"`
	Corpus            counts                  `json:"corpus"`
	RecallDocuments   int                     `json:"recall_documents"`
	PersonEventsEarly int                     `json:"person_events_early"`
	PersonEventsExtra int                     `json:"person_events_extra_after_early"`
	RecallParity      int                     `json:"recall_parity_queries"`
	SourceBytes       int64                   `json:"source_bytes"`
	RecallTextBytes   int64                   `json:"recall_text_bytes"`
	RecallPairBytes   int64                   `json:"recall_pair_bytes"`
	MemoryItems       int                     `json:"memory_items"`
	MaterialBytes     int64                   `json:"material_bytes"`
	TableBytes        map[string]int64        `json:"table_bytes"`
	IndexBytes        int64                   `json:"index_bytes"`
	DatabaseBytes     int64                   `json:"database_bytes"`
	FreeBytes         int64                   `json:"free_bytes"`
	Latency           map[string]distribution `json:"latency"`
	Hits              map[string]int          `json:"hits"`
	ForgetHidden      int                     `json:"forget_hidden"`
}

// earlyQueries keeps the two depth-zero samples comparable: the same number of
// queries, drawn from the same generator.
func earlyQueries(c corpus) []string {
	queries := c.queries()
	if len(queries) > 12 {
		queries = queries[:12]
	}
	return queries
}

func sampleDepthZero(ctx context.Context, flow *coreexec.Memory, timed func(string, func() error) error, hits map[string]int, name string, queries []string) error {
	for _, q := range queries {
		if err := timed(name, func() error {
			answer, err := flow.RecallPerson(ctx, memory.RecallScope{Agent: agent, Person: person}, coreexec.PersonRecall{Question: q, Query: q, At: 1800000000})
			hits[name] += len(answer.Sources)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func summarize(samples []int64) distribution {
	slices.Sort(samples)
	if len(samples) == 0 {
		return distribution{}
	}
	return distribution{len(samples), samples[(len(samples)*50+99)/100-1], samples[(len(samples)*95+99)/100-1], samples[len(samples)-1]}
}

func measure(ctx context.Context, root string, c corpus) (_ report, err error) {
	r := report{Version: 1, Schema: len(canon.Migrations()), Corpus: c.stats(), TableBytes: map[string]int64{}, Latency: map[string]distribution{}, Hits: map[string]int{}}
	if len(c.Turns) == 0 {
		return r, errors.New("empty_corpus")
	}
	if err := os.Chmod(root, 0700); err != nil {
		return r, err
	}
	// The binding's VFS uses SQLITE_TMPDIR for sort/temporary databases. Keep
	// even large private workloads inside scratch. These tests are deliberately
	// serial; restore the caller's environment after every measured corpus.
	previousTemp, hadTemp := os.LookupEnv("SQLITE_TMPDIR")
	if err := os.Setenv("SQLITE_TMPDIR", root); err != nil {
		return r, err
	}
	defer func() {
		if hadTemp {
			err = errors.Join(err, os.Setenv("SQLITE_TMPDIR", previousTemp))
		} else {
			err = errors.Join(err, os.Unsetenv("SQLITE_TMPDIR"))
		}
	}()
	if err := os.MkdirAll(filepath.Join(root, "canon"), 0700); err != nil {
		return r, err
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		return r, err
	}
	defer func() { err = errors.Join(err, controls.Close()) }()
	if err := controls.Acquire(); err != nil {
		return r, err
	}
	db, err := canon.Open(ctx, canon.Config{Path: filepath.Join(root, "canon/canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		return r, err
	}
	defer func() { err = errors.Join(err, db.Close(context.Background())) }()
	a, err := assets.Open(ctx, root, db)
	if err != nil {
		return r, err
	}
	defer func() { err = errors.Join(err, a.Close()) }()
	w, err := assets.OpenMaterialWriter(ctx, a)
	if err != nil {
		return r, err
	}
	m := &materialStore{a, w}
	events := ledger.New(db, m)
	flow := coreexec.NewMemory(db, m, nil)
	summaries := compaction.New(db)
	removals, err := retract.Open(ctx, retract.Config{Canon: db, Root: root})
	if err != nil {
		return r, err
	}
	defer func() { err = errors.Join(err, removals.Close()) }()
	samples := map[string][]int64{}
	timed := func(name string, run func() error) error {
		start := time.Now()
		err := run()
		if err == nil {
			samples[name] = append(samples[name], time.Since(start).Nanoseconds())
		}
		return err
	}
	var ids []string
	bySession := map[string][]string{}
	index := 0
	// The early sample answers the same depth-zero question over the first
	// tenth of the history. The final sample answers over all of it; comparing
	// the two shows whether cost tracks person events or only hits.
	early := max(1, len(c.Turns)/10)
	seenTurns := 0
	for _, turn := range c.Turns {
		var batch []ledger.Admission
		for _, msg := range turn {
			at := time.Unix(1700000000+int64(index), 0)
			in := ledger.Input{ID: msg.ID, ConversationID: msg.Session, AgentID: agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Locator: "storebench", Body: msg.Text}
			if msg.Entry == guard.EntryOwner {
				in.PersonID = person
			} else {
				in.ParentID = turn[0].ID
			}
			batch = append(batch, ledger.Admission{Entry: msg.Entry, Input: in})
			bySession[msg.Session] = append(bySession[msg.Session], msg.ID)
			index++
		}
		var admitted []ledger.Event
		if err := timed("admit_turn", func() error { var e error; admitted, e = events.AdmitBatch(ctx, batch); return e }); err != nil {
			return r, err
		}
		for _, event := range admitted {
			if event.Material.ID != "" {
				r.MaterialBytes += int64(len(event.Body.Text()))
			}
			if event.SpeakerKind != guard.Person {
				continue
			}
			var receipt coreexec.RememberReceipt
			if err := timed("remember", func() error {
				var e error
				receipt, e = flow.Remember(ctx, memory.Write{Agent: agent, Person: person, Item: memory.Item{Kind: memory.Fact, Subject: memory.Subject{Kind: memory.Person, ID: person}, Content: event.Body.DerivedText(), Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: event.ID, Precision: memory.Exact}}}})
				return e
			}); err != nil {
				return r, err
			}
			if !receipt.Remembered() {
				return r, errors.New("memory_not_committed")
			}
			if !slices.Contains(ids, receipt.ID) {
				ids = append(ids, receipt.ID)
			}
		}
		if seenTurns++; seenTurns == early {
			r.PersonEventsEarly = r.Corpus.PersonEvents * early / len(c.Turns)
			if err := sampleDepthZero(ctx, flow, timed, r.Hits, "recall_person_early", earlyQueries(c)); err != nil {
				return r, err
			}
			if err := sampleDepthZero(ctx, flow, timed, r.Hits, "recall_person_miss_early", []string{c.missQuery()}); err != nil {
				return r, err
			}
		}
	}
	r.MemoryItems = len(ids)
	covered := map[string]int{}
	for _, node := range c.Summaries {
		frontier, err := summaries.Frontier(ctx, node.Session)
		if err != nil {
			return r, err
		}
		all := bySession[node.Session]
		end := slices.Index(all, node.End) + 1
		if end == 0 || end < covered[node.Session] {
			return r, errors.New("summary_coverage_invalid")
		}
		p := compaction.Proposal{ID: node.ID, Expected: frontier, Output: compaction.Output{Text: node.Text, Model: "storebench", PromptVersion: "1", BuiltAt: time.Unix(1800000000, 0)}, Verbatim: all[end:]}
		if frontier.Root != "" {
			p.Children = []string{frontier.Root}
		}
		for _, id := range all[covered[node.Session]:end] {
			p.Cover = append(p.Cover, compaction.Coverage{EventID: id})
		}
		if err := timed("publish_summary", func() error { _, e := summaries.Publish(ctx, p); return e }); err != nil {
			return r, err
		}
		covered[node.Session] = end
	}
	recallIndex := recall.New(db, func(ctx context.Context, id string) (string, error) {
		e, err := events.Get(ctx, id)
		if e.BytesUnavailable {
			return "", ledger.ErrBytesUnavailable
		}
		return e.Body.DerivedText(), err
	})
	for {
		n, err := recallIndex.DrainBatch(ctx)
		if err != nil {
			return r, err
		}
		if n == 0 {
			break
		}
	}
	if err := db.Read(ctx, func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM recall_doc").Scan(&r.RecallDocuments); err != nil {
			return err
		}
		var memoryBytes int64
		if err := tx.QueryRowContext(ctx, "SELECT coalesce(sum(length(cast(content AS BLOB))),0) FROM memory_item WHERE state='active'").Scan(&memoryBytes); err != nil {
			return err
		}
		r.SourceBytes = int64(r.Corpus.TextBytes+r.Corpus.SummaryBytes) + memoryBytes
		return nil
	}); err != nil {
		return r, err
	}
	if r.RecallDocuments != r.Corpus.Events+r.Corpus.Summaries+r.MemoryItems {
		return r, errors.New("recall_document_count")
	}
	scope := memory.RecallScope{Agent: agent, Person: person}
	eventScope := recall.Scope{Person: person, Agent: agent, Kinds: []recall.Kind{recall.Event}}
	for _, q := range c.queries() {
		var hits []recall.Hit
		if err := timed("ledger_search", func() error {
			var e error
			hits, e = recall.Match(ctx, db, eventScope, recall.Query{Text: q})
			r.Hits["ledger_search"] += len(hits)
			return e
		}); err != nil {
			return r, err
		}

		// Parity against the old ledger index, still written during the switch
		// window: single literal terms in recency order must be the same events.
		var want []string
		for _, hit := range hits {
			want = append(want, hit.ID)
		}
		table := "recall_text"
		if utf8.RuneCountInString(q) == 2 {
			table = "recall_pair"
		}
		var got []string
		e := db.Read(ctx, func(tx *canon.Tx) error {
			rows, e := tx.QueryContext(ctx, "SELECT source_id FROM recall_doc WHERE kind='event' AND person=? AND holder=? AND replay=0 AND id IN (SELECT rowid FROM "+table+" WHERE "+table+" MATCH ?) ORDER BY julianday(occurred_at) DESC,source_id", person, agent, canon.TrigramPhrase(q))
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if e = rows.Scan(&id); e != nil {
					return e
				}
				got = append(got, id)
			}
			return rows.Err()
		})
		if e != nil {
			return r, e
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			return r, errors.New("recall_parity_mismatch")
		}
		r.RecallParity++
		if err := timed("memory_recall", func() error {
			hits, e := memory.New(db).Recall(ctx, scope, memory.Query{Text: q, At: 1800000000, Limit: 12})
			r.Hits["memory_recall"] += len(hits)
			return e
		}); err != nil {
			return r, err
		}
		if err := timed("recall_match", func() error {
			found, e := recall.Match(ctx, db, recall.Scope{Person: person, Agent: agent, Kinds: []recall.Kind{recall.Event, recall.Memory}}, recall.Query{Text: q, Limit: coreexec.RecallItemLimit})
			r.Hits["recall_match"] += len(found)
			return e
		}); err != nil {
			return r, err
		}
		if err := timed("recall_person", func() error {
			answer, e := flow.RecallPerson(ctx, scope, coreexec.PersonRecall{Question: q, Query: q, At: 1800000000})
			r.Hits["recall_person"] += len(answer.Sources)
			return e
		}); err != nil {
			return r, err
		}
	}
	// A query that matches nothing must not pay for the person events it did
	// not match. The old ledger side scanned every person event per query, so
	// this cost grew with the person event count. Append as many unmatched
	// person events as the corpus already holds and measure it again.
	if err := sampleDepthZero(ctx, flow, timed, r.Hits, "recall_person_miss", []string{c.missQuery()}); err != nil {
		return r, err
	}
	for i := 0; i < len(c.Turns); i++ {
		at := time.Unix(1900000000+int64(i), 0)
		if _, err := events.Admit(ctx, guard.EntryOwner, ledger.Input{ID: fmt.Sprintf("extra-person-%06d", i), ConversationID: "extra-person", AgentID: agent, PersonID: person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: "unmatched filler body"}); err != nil {
			return r, err
		}
		r.PersonEventsExtra++
	}
	if err := sampleDepthZero(ctx, flow, timed, r.Hits, "recall_person_miss_more_person", []string{c.missQuery()}); err != nil {
		return r, err
	}
	// The packaged engine omits dbstat. Python's SQLite inspects the same
	// committed file read-only; it does not execute or time any store operation.
	if err := measurePages(ctx, root, &r); err != nil {
		return r, err
	}
	for table, size := range r.TableBytes {
		if strings.HasPrefix(table, "recall_text_") {
			r.RecallTextBytes += size
		}
		if strings.HasPrefix(table, "recall_pair_") {
			r.RecallPairBytes += size
		}
	}
	for _, id := range ids[:min(3, len(ids))] {
		if err := timed("forget", func() error {
			return removals.Forget(ctx, retract.Request{Person: person, Targets: []retract.Ref{{Kind: retract.Memory, ID: id}}})
		}); err != nil {
			return r, err
		}
		hits, err := memory.New(db).Recall(ctx, scope, memory.Query{ID: id, At: 1800000000})
		if err != nil {
			return r, err
		}
		if len(hits) != 0 {
			return r, errors.New("forget_still_visible")
		}
		r.ForgetHidden++
	}
	for name, values := range samples {
		r.Latency[name] = summarize(values)
	}
	return r, nil
}

// Same real asset adapter as Core's composition layer; no in-memory material
// substitute or direct insert bypasses the published write paths.
type materialStore struct {
	store  *assets.Store
	writer *assets.MaterialWriter
}
type materialWrite struct{ *assets.PreparedMaterial }

func (p materialWrite) Insert(ctx context.Context, tx *canon.Tx) (ledger.Material, error) {
	a, err := p.PreparedMaterial.Insert(ctx, tx)
	return ledger.Material{ID: a.ID, Revision: a.Revision}, err
}
func (m *materialStore) Prepare(ctx context.Context, body secrets.Masked) (ledger.MaterialWrite, error) {
	p, err := m.writer.Prepare(ctx, strings.NewReader(body.Text()), "conversation-body", assets.Actor{PersonID: person, AgentID: agent})
	if err != nil {
		return nil, err
	}
	return materialWrite{p}, nil
}
func (m *materialStore) Read(ctx context.Context, ref ledger.Material) (string, error) {
	f, err := m.store.OpenRead(ctx, ref.ID, ref.Revision)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	return string(data), err
}
