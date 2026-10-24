// SPDX-License-Identifier: Apache-2.0
package recall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// Kind names one searchable view of the shared store.
type Kind string

const (
	Event   Kind = "event"
	Summary Kind = "summary"
	Memory  Kind = "memory"
	Record  Kind = "record"
)

var ErrInvalid = errors.New("invalid_recall_query")

// ExcerptRunes bounds one result's excerpt. The full source is reached through
// expand; the excerpt exists so a caller can choose without reading everything.
const ExcerptRunes = 240

// FusionConstant is the reciprocal-rank constant used when vectors join the
// full-text list. The value is fixed by measurement (#1040, #874).
const FusionConstant = 60

// Scope is applied before text matching, ranking and the limit. Person and the
// agent's holder set come from Core, never from a model argument.
type Scope struct {
	Person string
	// Agent selects the agent's own holder and conversation set. An empty agent
	// sees only common items and records.
	Agent        string
	Kinds        []Kind
	Conversation string
	Since, Until time.Time
	SpeakerKind  guard.SpeakerKind
	Origin       string
	CommonOnly   bool
	// PersonOnly restricts person-model queries to person-authored events and
	// person-subject memory items, before ranking and the result limit.
	PersonOnly bool
	// IncludeReplay makes replay copies candidates. A copy still counts once
	// with its lineage, so it surfaces only when its original is not a
	// candidate: a copy appears only when asked, through its original.
	IncludeReplay bool
	// History keeps memory items past their validity or superseded by a newer
	// one, marking them Older; an item past valid_from is never returned.
	History bool
	// At is the instant used for memory validity, as unix seconds. Zero means
	// now.
	At int64
	// Projection and ExternalModel exclude private and never-leaves memory, and
	// every event their evidence protects, before ranking or limiting.
	Projection    bool
	ExternalModel bool
}

// VectorQuery is the fusion hook for the vector index (#874): it returns
// document ids ordered by distance. Nil keeps full-text order.
type VectorQuery func(context.Context, Scope, string) ([]string, error)

// Query is the text and limit of one search. Terms are literal, whitespace
// split, at most eight, and any term may match.
type Query struct {
	Text  string
	Limit int
	// Vectors is optional. When set, its list is fused with full-text ranking
	// by reciprocal rank before the limit.
	Vectors VectorQuery
	// EventText resolves the derived text of a queued event whose body lives in
	// material. Match calls it only for sources the index has not published yet.
	EventText ReadEvent
}

// Hit is one ranked document before its excerpt and citations are read.
type Hit struct {
	Kind       Kind
	ID         string
	Revision   string
	OccurredAt time.Time
	Holder     string
	Older      bool
}

// Result is one recall result: identity, time, a bounded masked excerpt and
// the citations in force at its revision.
type Result struct {
	Hit
	Excerpt   string
	Citations []canon.Citation
	// Unavailable reports that the source bytes cannot be read now. The result
	// keeps its identity, so a caller can report the omission.
	Unavailable bool
}

type term struct {
	text  string
	runes int
	pair  bool
}

func queryTerms(text string) []term {
	fields := strings.Fields(text)
	if len(fields) > 8 {
		fields = fields[:8]
	}
	terms := make([]term, 0, len(fields))
	for _, field := range fields {
		runes := []rune(field)
		terms = append(terms, term{text: field, runes: len(runes), pair: len(runes) == 2 && hangul(runes[0]) && hangul(runes[1])})
	}
	return terms
}

func hangul(r rune) bool { return r >= '\uac00' && r <= '\ud7a3' }

func validScope(scope Scope) error {
	if strings.TrimSpace(scope.Person) == "" || !utf8.ValidString(scope.Person) || strings.ContainsRune(scope.Person, 0) {
		return ErrInvalid
	}
	if !utf8.ValidString(scope.Agent) || strings.ContainsRune(scope.Agent, 0) {
		return ErrInvalid
	}
	for _, kind := range scope.Kinds {
		switch kind {
		case Event, Summary, Memory, Record:
		default:
			return ErrInvalid
		}
	}
	return nil
}

func kindsOf(scope Scope) []Kind {
	if len(scope.Kinds) > 0 {
		return scope.Kinds
	}
	return []Kind{Event, Summary, Memory, Record}
}

// Match ranks the documents a query matches without reading their text. It is
// the scope, match and rank phase of Search, shared by the readers that keep
// their own materialization. A zero limit means no limit.
func Match(ctx context.Context, db Canon, scope Scope, query Query) ([]Hit, error) {
	if db == nil || validScope(scope) != nil || !utf8.ValidString(query.Text) || strings.ContainsRune(query.Text, 0) || len(query.Text) > 512 || query.Limit < 0 {
		return nil, ErrInvalid
	}
	terms := queryTerms(query.Text)
	if len(terms) == 0 {
		return nil, nil
	}
	var pairPhrases, pairMatches, trigramPhrases []string
	var shortTerms []string
	for _, t := range terms {
		switch {
		case t.runes >= 3:
			trigramPhrases = append(trigramPhrases, canon.TrigramPhrase(t.text))
			if tokens := PairTokens(t.text); tokens != "" {
				pairPhrases = append(pairPhrases, canon.TrigramPhrase(tokens))
			}
		case t.pair:
			pairPhrases = append(pairPhrases, canon.TrigramPhrase(t.text))
			pairMatches = append(pairMatches, canon.TrigramPhrase(t.text))
		default:
			shortTerms = append(shortTerms, strings.ToLower(t.text))
		}
	}
	if len(pairPhrases) == 0 && len(trigramPhrases) == 0 && len(shortTerms) == 0 {
		return nil, nil
	}
	kinds := kindsOf(scope)
	withEvents := containsKind(kinds, Event)
	at := scope.At
	if at == 0 {
		at = time.Now().Unix()
	}
	restricted := scope.Projection || scope.ExternalModel
	var with []string
	var withArgs []any
	if restricted {
		fragment, args := restrictedSources(scope)
		with = append(with, fragment)
		withArgs = append(withArgs, args...)
	}
	if len(pairPhrases) > 0 {
		with = append(with, `pair AS MATERIALIZED (SELECT rowid AS doc, bm25(recall_pair) AS score FROM recall_pair WHERE recall_pair MATCH ?)`)
		withArgs = append(withArgs, strings.Join(pairPhrases, " OR "))
	} else {
		with = append(with, `pair AS (SELECT NULL AS doc, 0.0 AS score WHERE 0)`)
	}
	if len(trigramPhrases) > 0 {
		with = append(with, `trig AS MATERIALIZED (SELECT rowid AS doc FROM recall_text WHERE recall_text MATCH ?)`)
		withArgs = append(withArgs, strings.Join(trigramPhrases, " OR "))
	} else {
		with = append(with, `trig AS (SELECT NULL AS doc WHERE 0)`)
	}
	if len(pairMatches) > 0 {
		with = append(with, `pairhit AS MATERIALIZED (SELECT rowid AS doc FROM recall_pair WHERE recall_pair MATCH ?)`)
		withArgs = append(withArgs, strings.Join(pairMatches, " OR "))
	} else {
		with = append(with, `pairhit AS (SELECT NULL AS doc WHERE 0)`)
	}
	var match []string
	var matchArgs []any
	if len(pairMatches) > 0 {
		match = append(match, "ph.doc IS NOT NULL")
	}
	if len(trigramPhrases) > 0 {
		match = append(match, "t.doc IS NOT NULL")
	}
	if len(shortTerms) > 0 {
		var literal []string
		for _, short := range shortTerms {
			literal = append(literal, `(d.kind='memory' AND EXISTS(SELECT 1 FROM memory_item m WHERE m.id=d.source_id AND instr(lower(m.content),?)>0))
   OR (d.kind='record' AND EXISTS(SELECT 1 FROM records_text r WHERE r.id=d.source_id AND instr(lower(r.title||char(10)||r.note),?)>0))`)
			matchArgs = append(matchArgs, short, short)
		}
		match = append(match, "("+strings.Join(literal, " OR ")+")")
	}
	indexWhere, indexArgs := scopeWhere(scope, kinds, at, restricted, "d")
	indexWhere = append(indexWhere, "("+strings.Join(match, " OR ")+")")
	indexArgs = append(indexArgs, matchArgs...)
	// A source that is still queued keeps its text in canon. Matching it here
	// keeps search behavior independent of when the background drain runs.
	var pendingMatch []string
	var pendingArgs []any
	for _, t := range terms {
		if t.runes >= 3 || t.pair {
			pendingMatch = append(pendingMatch, "instr(lower(s.body),?)>0")
			pendingArgs = append(pendingArgs, strings.ToLower(t.text))
			continue
		}
		pendingMatch = append(pendingMatch, "(s.kind IN ('memory','record') AND instr(lower(s.body),?)>0)")
		pendingArgs = append(pendingArgs, strings.ToLower(t.text))
	}
	pendingWhere, queuedArgs := scopeWhere(scope, kinds, at, restricted, "s")
	pendingWhere = append(pendingWhere,
		"NOT EXISTS(SELECT 1 FROM recall_doc x WHERE x.kind=s.kind AND x.source_id=s.source_id)",
		"(s.body IS NOT NULL AND ("+strings.Join(pendingMatch, " OR ")+"))")
	queuedArgs = append(queuedArgs, pendingArgs...)
	olderD, olderDArgs := olderExpr(kinds, at, "d")
	olderS, olderSArgs := olderExpr(kinds, at, "s")
	args := append([]any{}, withArgs...)
	args = append(args, olderDArgs...)
	args = append(args, indexArgs...)
	args = append(args, olderSArgs...)
	args = append(args, queuedArgs...)
	var external []materialHit
	if query.EventText != nil && withEvents {
		var err error
		external, err = pendingEventText(ctx, db, scope, at, restricted, terms, query.EventText)
		if err != nil {
			return nil, err
		}
	}
	statement := `WITH RECURSIVE ` + strings.Join(with, ",\n") + `,
candidates AS MATERIALIZED (
 SELECT d.id,d.kind,d.source_id,d.revision,d.occurred_at,d.holder,d.conversation,d.text_hash,d.replay,
  p.score AS score,(p.doc IS NOT NULL) AS paired,(t.doc IS NOT NULL) AS trigram,` + olderD + ` AS older
 FROM recall_doc d LEFT JOIN pair p ON p.doc=d.id LEFT JOIN trig t ON t.doc=d.id LEFT JOIN pairhit ph ON ph.doc=d.id
 WHERE ` + strings.Join(indexWhere, "\n AND ") + `
 UNION ALL
 SELECT NULL AS id,s.kind,s.source_id,s.revision,s.occurred_at,s.holder,s.conversation,NULL AS text_hash,s.replay,
  NULL AS score,0 AS paired,0 AS trigram,` + olderS + ` AS older
 FROM recall_source s JOIN recall_pending q ON q.kind=s.kind AND q.source_id=s.source_id
 WHERE ` + strings.Join(pendingWhere, "\n AND ") + `
),
` + lineageCTE(withEvents) + `
dedup AS (
 SELECT c.id,c.kind,c.source_id,c.revision,c.occurred_at,c.holder,c.conversation,c.score,c.paired,c.trigram,c.older,
  CASE WHEN c.kind='event' AND c.text_hash IS NOT NULL AND c.replay=0 THEN row_number() OVER (
   PARTITION BY c.conversation,c.text_hash ORDER BY julianday(c.occurred_at),c.source_id) ELSE 1 END AS twin
 FROM candidates c` + lineageFilter(withEvents) + `)
SELECT kind,source_id,revision,occurred_at,holder,older FROM dedup WHERE twin=1 AND (?=1 OR older=0)
ORDER BY CASE WHEN paired THEN 0 WHEN trigram THEN 1 ELSE 2 END, score, julianday(occurred_at) DESC, source_id`
	args = append(args, boolInt(scope.History))
	limit := query.Limit
	if limit > 0 {
		statement += " LIMIT ?"
		args = append(args, limit)
	}
	var hits []Hit
	err := db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, statement, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h Hit
			var occurred string
			var older int
			if err := rows.Scan(&h.Kind, &h.ID, &h.Revision, &occurred, &h.Holder, &older); err != nil {
				return err
			}
			h.Older = older == 1
			if h.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred); err != nil {
				h.OccurredAt = time.Time{}
			}
			hits = append(hits, h)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	// Queued material hits are not in the FTS tables yet. Rank them with the
	// indexed matches, by how many query terms they hold and then by time,
	// before the limit. Placing every one after the indexed list drops them
	// once that list already fills the limit.
	if len(external) > 0 {
		cover, err := termCoverage(ctx, db, hits, terms)
		if err != nil {
			return nil, err
		}
		hits = mergeMaterial(hits, cover, external)
	}
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	if query.Vectors != nil {
		return fuse(ctx, db, scope, query, hits)
	}
	return lineage(ctx, db, scope, hits)
}

// pendingEventText reads the derived text of queued events whose body lives in
// material, so a caller with material access sees them before the background
// drain publishes them. The scan is bounded by the queue, never the corpus.
// materialHit is a queued material-backed event that matched in memory,
// with how many of the query's terms its text holds.
type materialHit struct {
	Hit
	coverage int
}

func pendingEventText(ctx context.Context, db Canon, scope Scope, at int64, restricted bool, terms []term, readEvent ReadEvent) ([]materialHit, error) {
	var lowered []string
	for _, t := range terms {
		if t.runes >= 3 || t.pair {
			lowered = append(lowered, strings.ToLower(t.text))
		}
	}
	if len(lowered) == 0 {
		return nil, nil
	}
	where, args := scopeWhere(scope, []Kind{Event}, at, restricted, "s")
	where = append(where,
		"NOT EXISTS(SELECT 1 FROM recall_doc x WHERE x.kind=s.kind AND x.source_id=s.source_id)",
		"s.body IS NULL")
	prefix := ""
	var prefixArgs []any
	if restricted {
		fragment, values := restrictedSources(scope)
		prefix = "WITH RECURSIVE " + fragment + " "
		prefixArgs = values
	}
	args = append(prefixArgs, args...)
	var queued []Hit
	err := db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, prefix+`SELECT s.source_id,s.revision,s.occurred_at,s.holder
 FROM recall_source s JOIN recall_pending q ON q.kind=s.kind AND q.source_id=s.source_id
 WHERE `+strings.Join(where, "\n AND "), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h Hit
			var occurred string
			if err := rows.Scan(&h.ID, &h.Revision, &occurred, &h.Holder); err != nil {
				return err
			}
			h.Kind = Event
			h.OccurredAt, _ = time.Parse(time.RFC3339Nano, occurred)
			queued = append(queued, h)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	out := make([]materialHit, 0, len(queued))
	for _, h := range queued {
		text, err := readEvent(ctx, h.ID)
		if err != nil {
			continue
		}
		text = strings.ToLower(text)
		n := 0
		for _, term := range lowered {
			if strings.Contains(text, term) {
				n++
			}
		}
		if n > 0 {
			out = append(out, materialHit{Hit: h, coverage: n})
		}
	}
	return out, nil
}

func coverageKey(kind Kind, id string) string { return string(kind) + "\x00" + id }

// termCoverage counts how many query terms each already-ranked hit holds.
// Indexed rows use the same FTS tables as the rank query. Queued inline rows
// still have a body, so they use that text.
func termCoverage(ctx context.Context, db Canon, hits []Hit, terms []term) (map[string]int, error) {
	cover := map[string]int{}
	var counted []term
	for _, term := range terms {
		if term.runes >= 3 || term.pair {
			counted = append(counted, term)
		}
	}
	if len(hits) == 0 || len(counted) == 0 {
		return cover, nil
	}
	want := map[string]bool{}
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		want[coverageKey(hit.Kind, hit.ID)] = true
		ids = append(ids, hit.ID)
	}
	encoded, err := jsonArray(ids)
	if err != nil {
		return nil, err
	}
	err = db.Read(ctx, func(tx *canon.Tx) error {
		indexed := map[string]bool{}
		rows, err := tx.QueryContext(ctx, `SELECT kind, source_id FROM recall_doc WHERE source_id IN (SELECT value FROM json_each(?))`, encoded)
		if err != nil {
			return err
		}
		for rows.Next() {
			var kind, id string
			if err := rows.Scan(&kind, &id); err != nil {
				rows.Close()
				return err
			}
			indexed[coverageKey(Kind(kind), id)] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, term := range counted {
			keys, err := docsHolding(ctx, tx, encoded, term)
			if err != nil {
				return err
			}
			for _, key := range keys {
				if want[key] && indexed[key] {
					cover[key]++
				}
			}
		}
		rows, err = tx.QueryContext(ctx, `SELECT kind, source_id, body FROM recall_source WHERE body IS NOT NULL AND source_id IN (SELECT value FROM json_each(?))`, encoded)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind, id, body string
			if err := rows.Scan(&kind, &id, &body); err != nil {
				return err
			}
			key := coverageKey(Kind(kind), id)
			if !want[key] || indexed[key] {
				continue
			}
			lower := strings.ToLower(body)
			for _, term := range counted {
				if strings.Contains(lower, strings.ToLower(term.text)) {
					cover[key]++
				}
			}
		}
		return rows.Err()
	})
	return cover, err
}

func docsHolding(ctx context.Context, tx *canon.Tx, encoded string, term term) ([]string, error) {
	table, match := "recall_text", "recall_text"
	if term.pair {
		table, match = "recall_pair", "recall_pair"
	}
	rows, err := tx.QueryContext(ctx, `SELECT d.kind, d.source_id FROM recall_doc d JOIN `+table+` f ON f.rowid=d.id WHERE `+match+` MATCH ? AND d.source_id IN (SELECT value FROM json_each(?))`, canon.TrigramPhrase(term.text), encoded)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var kind, id string
		if err := rows.Scan(&kind, &id); err != nil {
			return nil, err
		}
		keys = append(keys, coverageKey(Kind(kind), id))
	}
	return keys, rows.Err()
}

// mergeMaterial inserts queued material hits into the ranked list before the
// limit is applied. More of the query's terms come first; equal coverage
// keeps the newer event. Indexed hits keep the order BM25 already gave them.
func mergeMaterial(hits []Hit, cover map[string]int, external []materialHit) []Hit {
	out := append([]Hit(nil), hits...)
	for _, ext := range external {
		at := len(out)
		for i, hit := range out {
			if materialOutranks(ext, cover[coverageKey(hit.Kind, hit.ID)], hit) {
				at = i
				break
			}
		}
		out = append(out, Hit{})
		copy(out[at+1:], out[at:])
		out[at] = ext.Hit
		cover[coverageKey(ext.Kind, ext.ID)] = ext.coverage
	}
	return out
}

func materialOutranks(ext materialHit, otherCoverage int, other Hit) bool {
	if ext.coverage != otherCoverage {
		return ext.coverage > otherCoverage
	}
	if !ext.OccurredAt.Equal(other.OccurredAt) {
		return ext.OccurredAt.After(other.OccurredAt)
	}
	return ext.ID < other.ID
}

// lineageCTE walks each candidate event's replay_of and view_of chain. A
// derivation of another candidate is represented by that candidate, so one
// lineage counts once (conversation-and-memory.md, One recall entry).
func lineageCTE(enabled bool) string {
	if !enabled {
		return `derived AS (SELECT NULL AS id WHERE 0),`
	}
	return `lineage(origin,node,depth) AS (
  SELECT c.source_id,c.source_id,0 FROM candidates c WHERE c.kind='event'
  UNION
  SELECT a.origin,coalesce(nullif(e.replay_of,''),v.target),a.depth+1 FROM lineage a
   JOIN conv_event e ON e.id=a.node
   LEFT JOIN (SELECT event_id,min(target_id) AS target FROM ev_rel WHERE kind='view_of' GROUP BY event_id) v ON v.event_id=a.node
   WHERE a.depth<64 AND coalesce(nullif(e.replay_of,''),v.target) IS NOT NULL),
derived AS (SELECT DISTINCT a.origin AS id FROM lineage a WHERE a.depth>0
  AND a.node IN (SELECT c.source_id FROM candidates c WHERE c.kind='event')),
`
}

func lineageFilter(enabled bool) string {
	if !enabled {
		return ""
	}
	return ` WHERE c.kind<>'event' OR c.source_id NOT IN (SELECT x.id FROM derived x)`
}

func holderCondition(scope Scope, alias string) string {
	if scope.CommonOnly || scope.Agent == "" {
		return alias + ".holder='all'"
	}
	return alias + ".holder IN (?,'all')"
}

// Follow protected memory to its evidence, then protect every current view
// that depends on any of those sources. A replay copy or view_of event carries
// its original's text, so protection also spreads both ways along those links
// before anything cites them. Typed identities and UNION terminate cycles
// without confusing equal ids belonging to different source kinds.
func restrictedSources(scope Scope) (string, []any) {
	return `sensitive(kind,id) AS (
 SELECT 'memory',m.id FROM memory_item m WHERE m.person=?
  AND ((?=1 AND m.scope IN ('private','never-leaves')) OR (?=1 AND m.scope='never-leaves'))
 UNION
 SELECT c.target_kind,c.target_id FROM cite c JOIN sensitive s ON c.view_kind=s.kind AND c.view_id=s.id
  WHERE c.dropped_revision IS NULL
), copied(child,parent) AS MATERIALIZED (
 SELECT e.id,e.replay_of FROM recall_doc d JOIN conv_event e ON e.id=d.source_id
  WHERE d.person=? AND d.kind='event' AND d.replay=1 AND coalesce(e.replay_of,'')<>''
 UNION ALL
 SELECT e.id,e.replay_of FROM recall_pending q JOIN conv_event e ON e.id=q.source_id
  WHERE q.kind='event' AND coalesce(e.replay_of,'')<>''
 UNION ALL
 SELECT event_id,target_id FROM ev_rel WHERE kind='view_of'
), restricted(kind,id) AS (
 SELECT kind,id FROM sensitive
 UNION
 SELECT c.view_kind,c.view_id FROM cite c JOIN restricted r ON c.target_kind=r.kind AND c.target_id=r.id
  WHERE c.dropped_revision IS NULL
 UNION
 SELECT 'event',l.child FROM copied l JOIN restricted r ON r.kind='event' AND r.id=l.parent
 UNION
 SELECT 'event',l.parent FROM copied l JOIN restricted r ON r.kind='event' AND r.id=l.child
)`, []any{scope.Person, boolInt(scope.Projection), boolInt(scope.ExternalModel), scope.Person}
}

// scopeWhere applies the scope before matching. It is shared by the published
// index and the still-queued sources so both see the same filters.
func scopeWhere(scope Scope, kinds []Kind, at int64, restricted bool, alias string) ([]string, []any) {
	where := []string{alias + ".person=?", holderCondition(scope, alias)}
	var args []any
	args = append(args, scope.Person)
	if !scope.CommonOnly && scope.Agent != "" {
		args = append(args, scope.Agent)
	}
	where = append(where, alias+".kind IN ("+placeholders(len(kinds))+")")
	for _, kind := range kinds {
		args = append(args, string(kind))
	}
	if scope.Conversation != "" {
		where = append(where, alias+".conversation=?")
		args = append(args, scope.Conversation)
	}
	if !scope.Since.IsZero() {
		where = append(where, "julianday("+alias+".occurred_at)>=julianday(?)")
		args = append(args, scope.Since.UTC().Format(time.RFC3339Nano))
	}
	if !scope.Until.IsZero() {
		where = append(where, "julianday("+alias+".occurred_at)<julianday(?)")
		args = append(args, scope.Until.UTC().Format(time.RFC3339Nano))
	}
	if scope.SpeakerKind != "" {
		where = append(where, alias+".speaker_kind=?")
		args = append(args, string(scope.SpeakerKind))
	}
	if scope.PersonOnly {
		where = append(where, `((`+alias+`.kind='event' AND `+alias+`.speaker_kind='person')
 OR (`+alias+`.kind='memory' AND EXISTS(SELECT 1 FROM memory_item m WHERE m.id=`+alias+`.source_id AND m.subject_kind='person')))`)
	}
	if scope.Origin != "" {
		where = append(where, "json_extract("+alias+".origin,'$.kind')=?")
		args = append(args, scope.Origin)
	}
	if !scope.IncludeReplay {
		where = append(where, alias+".replay=0")
	}
	if containsKind(kinds, Event) {
		where = append(where, `(`+alias+`.kind<>'event' OR EXISTS(SELECT 1 FROM conv_event e WHERE e.id=`+alias+`.source_id
   AND coalesce(e.scaffold_kind,'')<>'reply_check'))`)
	}
	if containsKind(kinds, Memory) {
		where = append(where, `(`+alias+`.kind<>'memory' OR EXISTS(SELECT 1 FROM memory_item v WHERE v.id=`+alias+`.source_id AND v.valid_from<=?))`)
		args = append(args, at)
	}
	if restricted {
		where = append(where, "NOT EXISTS(SELECT 1 FROM restricted r WHERE r.kind="+alias+".kind AND r.id="+alias+".source_id)")
	}
	return where, args
}

func olderExpr(kinds []Kind, at int64, alias string) (string, []any) {
	if !containsKind(kinds, Memory) {
		return "0", nil
	}
	return `CASE WHEN ` + alias + `.kind='memory' THEN (SELECT CASE WHEN (m.valid_until<>0 AND m.valid_until<=?) OR EXISTS(SELECT 1 FROM memory_item n WHERE n.supersedes=m.id AND n.valid_from<=?) THEN 1 ELSE 0 END FROM memory_item m WHERE m.id=` + alias + `.source_id) ELSE 0 END`, []any{at, at}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func containsKind(kinds []Kind, want Kind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

// lineage drops an event that is the evidence of a returned memory item: the
// item already carries it as its citation and counts once. Replay copies are
// excluded by scope unless asked for.
func lineage(ctx context.Context, db Canon, scope Scope, hits []Hit) ([]Hit, error) {
	var cited map[string]bool
	for _, h := range hits {
		if h.Kind != Memory {
			continue
		}
		revision, err := strconv.ParseInt(h.Revision, 10, 64)
		if err != nil {
			continue
		}
		var citations []canon.Citation
		err = db.Read(ctx, func(tx *canon.Tx) error {
			var err error
			citations, err = canon.CitationsAt(ctx, tx, "memory", h.ID, revision)
			return err
		})
		if err != nil {
			return nil, err
		}
		for _, c := range citations {
			if c.TargetKind != "event" {
				continue
			}
			if cited == nil {
				cited = map[string]bool{}
			}
			cited[c.TargetID] = true
		}
	}
	if len(cited) == 0 {
		return hits, nil
	}
	out := hits[:0]
	for _, h := range hits {
		if h.Kind == Event && cited[h.ID] {
			continue
		}
		out = append(out, h)
	}
	return out, nil
}

// fuse merges the full-text list and the vector hook by reciprocal rank.
func fuse(ctx context.Context, db Canon, scope Scope, query Query, text []Hit) ([]Hit, error) {
	ids, err := query.Vectors(ctx, scope, query.Text)
	if err != nil {
		return nil, err
	}
	type key struct {
		kind Kind
		id   string
	}
	scores := map[key]float64{}
	keep := map[key]Hit{}
	for rank, h := range text {
		k := key{h.Kind, h.ID}
		scores[k] += 1 / float64(FusionConstant+rank+1)
		keep[k] = h
	}
	extra, err := byIDs(ctx, db, scope, ids)
	if err != nil {
		return nil, err
	}
	for rank, h := range extra {
		k := key{h.Kind, h.ID}
		scores[k] += 1 / float64(FusionConstant+rank+1)
		if _, ok := keep[k]; !ok {
			keep[k] = h
		}
	}
	out := make([]Hit, 0, len(keep))
	for k := range keep {
		out = append(out, keep[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := key{out[i].Kind, out[i].ID}, key{out[j].Kind, out[j].ID}
		if scores[a] != scores[b] {
			return scores[a] > scores[b]
		}
		if !out[i].OccurredAt.Equal(out[j].OccurredAt) {
			return out[i].OccurredAt.After(out[j].OccurredAt)
		}
		return out[i].ID < out[j].ID
	})
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return lineage(ctx, db, scope, out)
}

// byIDs loads vector candidates. They pass the same scope and validity
// filters as a full-text match, then keep the vector order.
func byIDs(ctx context.Context, db Canon, scope Scope, ids []string) ([]Hit, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	encoded, err := jsonArray(ids)
	if err != nil {
		return nil, err
	}
	kinds := kindsOf(scope)
	at := scope.At
	if at == 0 {
		at = time.Now().Unix()
	}
	restricted := scope.Projection || scope.ExternalModel
	where, whereArgs := scopeWhere(scope, kinds, at, restricted, "d")
	where = append(where, "d.source_id IN (SELECT value FROM json_each(?))")
	whereArgs = append(whereArgs, encoded)
	// History keeps a past or superseded item and marks it Older. Otherwise the
	// same predicate drops it, matching a full-text match.
	older, olderArgs := olderExpr(kinds, at, "d")
	if !scope.History {
		where = append(where, "("+older+")=0")
		whereArgs = append(whereArgs, olderArgs...)
	}
	prefix := ""
	var args []any
	if restricted {
		fragment, prefixArgs := restrictedSources(scope)
		prefix = "WITH RECURSIVE " + fragment + " "
		args = append(args, prefixArgs...)
	}
	args = append(args, olderArgs...)
	args = append(args, whereArgs...)
	order := map[string]int{}
	for i, id := range ids {
		if _, ok := order[id]; !ok {
			order[id] = i
		}
	}
	var hits []Hit
	err = db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, prefix+`SELECT d.source_id,d.kind,d.revision,d.occurred_at,d.holder,`+older+` FROM recall_doc d
   WHERE `+strings.Join(where, " AND "), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h Hit
			var occurred string
			var past int
			if err := rows.Scan(&h.ID, &h.Kind, &h.Revision, &occurred, &h.Holder, &past); err != nil {
				return err
			}
			h.OccurredAt, _ = time.Parse(time.RFC3339Nano, occurred)
			h.Older = past == 1
			hits = append(hits, h)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(hits, func(i, j int) bool { return order[hits[i].ID] < order[hits[j].ID] })
	return hits, nil
}

func jsonArray(values []string) (string, error) {
	raw, err := json.Marshal(values)
	return string(raw), err
}

// Search is the one read entry: scope, match and rank (Match), then a bounded
// masked excerpt and the citations in force at each result's revision.
func Search(ctx context.Context, db Canon, readEvent ReadEvent, scope Scope, query Query) ([]Result, error) {
	if query.Limit == 0 {
		query.Limit = 20
	}
	hits, err := Match(ctx, db, scope, query)
	if err != nil {
		return nil, err
	}
	terms := queryTerms(query.Text)
	results := make([]Result, 0, len(hits))
	for _, hit := range hits {
		result := Result{Hit: hit}
		text, err := sourceText(ctx, db, readEvent, hit)
		if errors.Is(err, ErrBytesUnavailable) {
			result.Unavailable = true
		} else if err != nil {
			return nil, err
		} else if masked, err := maskDerived(text, nil); err != nil {
			return nil, err
		} else {
			result.Excerpt = Excerpt(masked, terms)
		}
		if result.Citations, err = citations(ctx, db, hit); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

// Excerpt centers a bounded window on the first query term it finds.
func Excerpt(text string, terms []term) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= ExcerptRunes {
		return string(runes)
	}
	lower := []rune(strings.ToLower(string(runes)))
	position := -1
	for _, term := range terms {
		needle := []rune(strings.ToLower(term.text))
		if i := indexRunes(lower, needle); i >= 0 && (position < 0 || i < position) {
			position = i
		}
	}
	start := 0
	if position > ExcerptRunes/4 {
		start = position - ExcerptRunes/4
	}
	end := start + ExcerptRunes
	if end > len(runes) {
		end = len(runes)
		start = end - ExcerptRunes
	}
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

func indexRunes(haystack, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func maskDerived(text string, spans []secrets.Span) (string, error) {
	masked, err := secrets.Mask(text, spans...)
	if err != nil {
		return "", err
	}
	return masked.DerivedText(), nil
}

func sourceText(ctx context.Context, db Canon, readEvent ReadEvent, hit Hit) (string, error) {
	switch hit.Kind {
	case Event:
		var body sql.NullString
		var material sql.NullString
		var spans []secrets.Span
		err := db.Read(ctx, func(tx *canon.Tx) error {
			var revision sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT body,material_id,material_revision FROM retract_visible_event WHERE id=?`, hit.ID).Scan(&body, &material, &revision); err != nil {
				return err
			}
			rows, err := tx.QueryContext(ctx, "SELECT start,end,sensitivity FROM ev_span WHERE event_id=? AND sensitivity='secret' ORDER BY start,end", hit.ID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var span secrets.Span
				if err := rows.Scan(&span.Start, &span.End, &span.Sensitivity); err != nil {
					return err
				}
				spans = append(spans, span)
			}
			return rows.Err()
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", ErrBytesUnavailable
			}
			return "", err
		}
		if material.Valid || !body.Valid {
			if readEvent == nil {
				return "", ErrBytesUnavailable
			}
			text, err := readEvent(ctx, hit.ID)
			if err != nil {
				return "", ErrBytesUnavailable
			}
			return text, nil
		}
		// Stored spans address the display body. The excerpt is derived text:
		// flagged secrets are removed, not left as the display placeholder.
		return maskDerived(body.String, spans)
	case Memory:
		var text string
		err := db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT content FROM memory_item WHERE id=?", hit.ID).Scan(&text)
		})
		return text, err
	case Summary:
		var text string
		err := db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT text FROM sum_node WHERE id=?", hit.ID).Scan(&text)
		})
		return text, err
	case Record:
		var text string
		err := db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT title||char(10)||note FROM records_text WHERE id=?", hit.ID).Scan(&text)
		})
		return text, err
	}
	return "", ErrInvalid
}

func citations(ctx context.Context, db Canon, hit Hit) ([]canon.Citation, error) {
	var out []canon.Citation
	switch hit.Kind {
	case Event:
		return nil, nil
	case Memory, Summary:
		revision, err := strconv.ParseInt(hit.Revision, 10, 64)
		if err != nil {
			return nil, ErrInvalid
		}
		err = db.Read(ctx, func(tx *canon.Tx) error {
			var err error
			out, err = canon.CitationsAt(ctx, tx, string(hit.Kind), hit.ID, revision)
			return err
		})
		return out, err
	case Record:
		err := db.Read(ctx, func(tx *canon.Tx) error {
			rows, err := tx.QueryContext(ctx, `SELECT ordinal,target_kind,target_id,target_revision,span_start,span_end,precision,role FROM cite
   WHERE view_kind='record' AND view_id=? AND dropped_revision IS NULL ORDER BY ordinal`, hit.ID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var c canon.Citation
				if err := rows.Scan(&c.Ordinal, &c.TargetKind, &c.TargetID, &c.TargetRevision, &c.SpanStart, &c.SpanEnd, &c.Precision, &c.Role); err != nil {
					return err
				}
				out = append(out, c)
			}
			return rows.Err()
		})
		return out, err
	}
	return nil, nil
}
