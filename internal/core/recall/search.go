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
	at := scope.At
	if at == 0 {
		at = time.Now().Unix()
	}
	restricted := scope.Projection || scope.ExternalModel
	var with []string
	var withArgs []any
	if restricted {
		with = append(with, `restricted AS (
  SELECT DISTINCT c.target_id AS id FROM cite c JOIN memory_item m ON m.id=c.view_id
  WHERE c.view_kind='memory' AND c.target_kind='event' AND c.dropped_revision IS NULL AND m.person=?
   AND ((?=1 AND m.scope IN ('private','never-leaves')) OR (?=1 AND m.scope='never-leaves')))`)
		withArgs = append(withArgs, scope.Person, boolInt(scope.Projection), boolInt(scope.ExternalModel))
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
	where := []string{"d.person=?", holderCondition(scope)}
	var whereArgs []any
	whereArgs = append(whereArgs, scope.Person)
	if !scope.CommonOnly && scope.Agent != "" {
		whereArgs = append(whereArgs, scope.Agent)
	}
	where = append(where, "d.kind IN ("+placeholders(len(kinds))+")")
	for _, kind := range kinds {
		whereArgs = append(whereArgs, string(kind))
	}
	if scope.Conversation != "" {
		where = append(where, "d.conversation=?")
		whereArgs = append(whereArgs, scope.Conversation)
	}
	if !scope.Since.IsZero() {
		where = append(where, "julianday(d.occurred_at)>=julianday(?)")
		whereArgs = append(whereArgs, scope.Since.UTC().Format(time.RFC3339Nano))
	}
	if !scope.Until.IsZero() {
		where = append(where, "julianday(d.occurred_at)<julianday(?)")
		whereArgs = append(whereArgs, scope.Until.UTC().Format(time.RFC3339Nano))
	}
	if scope.SpeakerKind != "" {
		where = append(where, "d.speaker_kind=?")
		whereArgs = append(whereArgs, string(scope.SpeakerKind))
	}
	if scope.Origin != "" {
		where = append(where, "json_extract(d.origin,'$.kind')=?")
		whereArgs = append(whereArgs, scope.Origin)
	}
	if !scope.IncludeReplay {
		where = append(where, "d.replay=0")
	}
	lineageEvents := containsKind(kinds, Event)
	if lineageEvents {
		where = append(where, `(d.kind<>'event' OR EXISTS(SELECT 1 FROM conv_event e WHERE e.id=d.source_id
   AND coalesce(e.scaffold_kind,'')<>'reply_check'))`)
	}
	var match []string
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
			whereArgs = append(whereArgs, short, short)
		}
		match = append(match, "("+strings.Join(literal, " OR ")+")")
	}
	where = append(where, "("+strings.Join(match, " OR ")+")")
	if containsKind(kinds, Memory) {
		where = append(where, `(d.kind<>'memory' OR EXISTS(SELECT 1 FROM memory_item v WHERE v.id=d.source_id AND v.valid_from<=?))`)
		whereArgs = append(whereArgs, at)
		if restricted {
			where = append(where, `(d.kind<>'memory' OR NOT EXISTS(SELECT 1 FROM memory_item s WHERE s.id=d.source_id
   AND ((?=1 AND s.scope IN ('private','never-leaves')) OR (?=1 AND s.scope='never-leaves'))))`)
			whereArgs = append(whereArgs, boolInt(scope.Projection), boolInt(scope.ExternalModel))
			where = append(where, `(d.kind<>'memory' OR NOT EXISTS(SELECT 1 FROM cite c2 JOIN restricted r2 ON r2.id=c2.target_id
   WHERE c2.view_kind='memory' AND c2.view_id=d.source_id AND c2.dropped_revision IS NULL))`)
		}
	}
	if restricted {
		where = append(where, `(d.kind<>'event' OR NOT EXISTS(SELECT 1 FROM restricted r WHERE r.id=d.source_id))`)
		where = append(where, `(d.kind NOT IN ('summary','record') OR NOT EXISTS(SELECT 1 FROM cite c3 JOIN restricted r3 ON r3.id=c3.target_id
   WHERE c3.view_kind=d.kind AND c3.view_id=d.source_id AND c3.dropped_revision IS NULL))`)
	}
	var older = "0"
	var olderArgs []any
	if containsKind(kinds, Memory) {
		older = `CASE WHEN d.kind='memory' THEN (SELECT CASE WHEN (m.valid_until<>0 AND m.valid_until<=?) OR EXISTS(SELECT 1 FROM memory_item n WHERE n.supersedes=m.id AND n.valid_from<=?) THEN 1 ELSE 0 END FROM memory_item m WHERE m.id=d.source_id) ELSE 0 END`
		olderArgs = append(olderArgs, at, at)
	}
	args := append(append(withArgs, olderArgs...), whereArgs...)
	statement := `WITH ` + strings.Join(with, ",\n") + `,
candidates AS MATERIALIZED (
 SELECT d.id,d.kind,d.source_id,d.revision,d.occurred_at,d.holder,d.conversation,d.text_hash,d.replay,
  p.score AS score,(p.doc IS NOT NULL) AS paired,(t.doc IS NOT NULL) AS trigram,` + older + ` AS older
 FROM recall_doc d LEFT JOIN pair p ON p.doc=d.id LEFT JOIN trig t ON t.doc=d.id LEFT JOIN pairhit ph ON ph.doc=d.id
 WHERE ` + strings.Join(where, "\n AND ") + `
),
` + lineageCTE(lineageEvents) + `
dedup AS (
 SELECT c.id,c.kind,c.source_id,c.revision,c.occurred_at,c.holder,c.conversation,c.score,c.paired,c.trigram,c.older,
  CASE WHEN c.kind='event' AND c.text_hash IS NOT NULL AND c.replay=0 THEN row_number() OVER (
   PARTITION BY c.conversation,c.text_hash ORDER BY julianday(c.occurred_at),c.source_id) ELSE 1 END AS twin
 FROM candidates c` + lineageFilter(lineageEvents) + `)
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
	if query.Vectors != nil {
		return fuse(ctx, db, scope, query, hits)
	}
	return lineage(ctx, db, scope, hits)
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
	return ` WHERE c.kind<>'event' OR c.id NOT IN (SELECT d.id FROM candidates d JOIN derived x ON x.id=d.source_id)`
}

func holderCondition(scope Scope) string {
	if scope.CommonOnly || scope.Agent == "" {
		return "d.holder='all'"
	}
	return "d.holder IN (?,'all')"
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

func byIDs(ctx context.Context, db Canon, scope Scope, ids []string) ([]Hit, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	encoded, err := jsonArray(ids)
	if err != nil {
		return nil, err
	}
	kinds := kindsOf(scope)
	where := []string{"d.person=?", holderCondition(scope), "d.source_id IN (SELECT value FROM json_each(?))", "d.kind IN (" + placeholders(len(kinds)) + ")"}
	args := []any{scope.Person}
	if !scope.CommonOnly && scope.Agent != "" {
		args = append(args, scope.Agent)
	}
	args = append(args, encoded)
	for _, kind := range kinds {
		args = append(args, string(kind))
	}
	order := map[string]int{}
	for i, id := range ids {
		if _, ok := order[id]; !ok {
			order[id] = i
		}
	}
	var hits []Hit
	err = db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT d.source_id,d.kind,d.revision,d.occurred_at,d.holder FROM recall_doc d
   WHERE `+strings.Join(where, " AND "), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h Hit
			var occurred string
			if err := rows.Scan(&h.ID, &h.Kind, &h.Revision, &occurred, &h.Holder); err != nil {
				return err
			}
			h.OccurredAt, _ = time.Parse(time.RFC3339Nano, occurred)
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
		var body string
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
		if material.Valid {
			if readEvent == nil {
				return "", ErrBytesUnavailable
			}
			text, err := readEvent(ctx, hit.ID)
			if err != nil {
				return "", ErrBytesUnavailable
			}
			return text, nil
		}
		return body, nil
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
