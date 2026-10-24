// SPDX-License-Identifier: Apache-2.0
package storebench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/recall"
)

// The question set is private and never committed. Only the format is public.
var questionsFlag = flag.String("recall-questions", "", "private recall question set, JSON lines (read-only)")

var (
	errEvalQuestions = errors.New("recall_eval_questions_invalid")
	errEvalEvidence  = errors.New("recall_eval_evidence_unknown")
)

// evalLimit is the largest k reported, so one search answers every recall
// number for a configuration.
const evalLimit = 20

type evalQuestion struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Short    string   `json:"short_query,omitempty"`
	Evidence []string `json:"evidence"`

	// keys are resolved after the corpus is loaded: the identical-derived-text
	// identity recall counts once.
	keys []string
}

type evalConfig struct {
	Name    string
	Short   bool
	Vectors recall.VectorQuery
}

// evalConfigs names one configuration per query shape. Fused rows appear when
// a vector source exists (#874); until then the lexical rows are the run.
func evalConfigs(vectors recall.VectorQuery) []evalConfig {
	configs := []evalConfig{{Name: "lexical-question"}, {Name: "lexical-short", Short: true}}
	if vectors != nil {
		configs = append(configs,
			evalConfig{Name: "fused-question", Vectors: vectors},
			evalConfig{Name: "fused-short", Short: true, Vectors: vectors})
	}
	return configs
}

type evalRow struct {
	Name             string       `json:"name"`
	Questions        int          `json:"questions"`
	Recall5          float64      `json:"recall_5"`
	Recall12         float64      `json:"recall_12"`
	Recall20         float64      `json:"recall_20"`
	EvidenceRecall12 float64      `json:"evidence_recall_12"`
	MRR20            float64      `json:"mrr_20"`
	Empty            int          `json:"empty"`
	Latency          distribution `json:"latency"`
}

type evalPair struct {
	A     string  `json:"a"`
	B     string  `json:"b"`
	OnlyA int     `json:"only_a"`
	OnlyB int     `json:"only_b"`
	Both  int     `json:"both"`
	P     float64 `json:"p"`
}

type evalReport struct {
	Version int    `json:"report_version"`
	Schema  int    `json:"schema_version"`
	Corpus  counts `json:"corpus"`
	// Questions counts the file; QuestionsRun counts those with evidence in
	// this snapshot. A reference the snapshot does not hold is counted, not
	// guessed, because an import may legitimately lack a source row.
	Questions       int        `json:"questions"`
	QuestionsRun    int        `json:"questions_run"`
	Evidence        int        `json:"evidence"`
	EvidenceUnknown int        `json:"evidence_unknown"`
	Configurations  []evalRow  `json:"configurations"`
	Pairs           []evalPair `json:"pairs"`
}

// readQuestionFile reads JSON lines without retaining or echoing their text.
// Errors carry a line or question position only.
func readQuestionFile(path string) ([]evalQuestion, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errEvalQuestions
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	var questions []evalQuestion
	seen := map[string]bool{}
	for line := 1; scanner.Scan(); line++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var q evalQuestion
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, fmt.Errorf("%w at line %d", errEvalQuestions, line)
		}
		q.Short = strings.TrimSpace(q.Short)
		if strings.TrimSpace(q.ID) == "" || strings.TrimSpace(q.Question) == "" || len(q.Evidence) == 0 || seen[q.ID] {
			return nil, fmt.Errorf("%w at line %d", errEvalQuestions, line)
		}
		seen[q.ID] = true
		questions = append(questions, q)
	}
	if err := scanner.Err(); err != nil || len(questions) == 0 {
		return nil, errEvalQuestions
	}
	return questions, nil
}

// resolveEvidence accepts a LINA event id or the snapshot's legacy reference:
// a decimal source row, optionally prefixed with "message:".
func resolveEvidence(c corpus, ref string) (string, bool) {
	for _, turn := range c.Turns {
		for _, m := range turn {
			if m.ID == ref {
				return m.ID, true
			}
		}
	}
	key := strings.TrimPrefix(ref, "message:")
	if n, err := strconv.ParseInt(key, 10, 64); err == nil {
		if id, ok := c.SourceEvents[n]; ok {
			return id, true
		}
	}
	return "", false
}

// resolveQuestions maps every evidence reference to an event and then to the
// identical-derived-text identity recall returns. Two references with the same
// text count once. A reference the snapshot does not hold is dropped and
// counted; a question left without evidence is skipped.
func resolveQuestions(questions []evalQuestion, c corpus, identities map[string]string) (evidence, unknown, run int) {
	for i := range questions {
		q := &questions[i]
		keys := map[string]bool{}
		q.keys = q.keys[:0]
		for _, ref := range q.Evidence {
			id, ok := resolveEvidence(c, ref)
			if !ok {
				unknown++
				continue
			}
			key := identities[id]
			if key == "" {
				key = "id:" + id
			}
			if !keys[key] {
				keys[key] = true
				q.keys = append(q.keys, key)
			}
		}
		if len(q.keys) == 0 {
			continue
		}
		run++
		evidence += len(q.keys)
	}
	return evidence, unknown, run
}

// signTest is the exact two-sided sign test over discordant pairs. Identical
// answers contribute nothing, as in the research method.
func signTest(onlyA, onlyB int) float64 {
	n := onlyA + onlyB
	if n == 0 {
		return 1
	}
	k := min(onlyA, onlyB)
	sum := new(big.Int)
	choose := big.NewInt(1)
	for i := range k + 1 {
		sum.Add(sum, choose)
		choose.Mul(choose, big.NewInt(int64(n-i)))
		choose.Div(choose, big.NewInt(int64(i+1)))
	}
	total := new(big.Int).Lsh(big.NewInt(1), uint(n))
	p := new(big.Rat).SetFrac(sum, total)
	p.Mul(p, big.NewRat(2, 1))
	if p.Cmp(big.NewRat(1, 1)) > 0 {
		return 1
	}
	value, _ := p.Float64()
	return value
}

type evalStore struct {
	db     *canon.Store
	events *ledger.Store
	index  *recall.Store
}

// openEvalStore builds the same store the benchmark uses, confined to root.
func openEvalStore(ctx context.Context, root string) (*evalStore, func() error, error) {
	if err := os.Chmod(root, 0700); err != nil {
		return nil, nil, err
	}
	previousTemp, hadTemp := os.LookupEnv("SQLITE_TMPDIR")
	if err := os.Setenv("SQLITE_TMPDIR", root); err != nil {
		return nil, nil, err
	}
	var cleanups []func() error
	cleanups = append(cleanups, func() error {
		if hadTemp {
			return os.Setenv("SQLITE_TMPDIR", previousTemp)
		}
		return os.Unsetenv("SQLITE_TMPDIR")
	})
	fail := func(err error) (*evalStore, func() error, error) {
		for i := len(cleanups) - 1; i >= 0; i-- {
			err = errors.Join(err, cleanups[i]())
		}
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "canon"), 0700); err != nil {
		return fail(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		return fail(err)
	}
	cleanups = append(cleanups, controls.Close)
	if err := controls.Acquire(); err != nil {
		return fail(err)
	}
	db, err := canon.Open(ctx, canon.Config{Path: filepath.Join(root, "canon/canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		return fail(err)
	}
	cleanups = append(cleanups, func() error { return db.Close(context.Background()) })
	a, err := assets.Open(ctx, root, db)
	if err != nil {
		return fail(err)
	}
	cleanups = append(cleanups, a.Close)
	w, err := assets.OpenMaterialWriter(ctx, a)
	if err != nil {
		return fail(err)
	}
	events := ledger.New(db, &materialStore{a, w})
	index := recall.New(db, func(ctx context.Context, id string) (string, error) {
		e, err := events.Get(ctx, id)
		if err != nil {
			return "", err
		}
		if e.BytesUnavailable {
			return "", ledger.ErrBytesUnavailable
		}
		return e.Body.DerivedText(), nil
	})
	return &evalStore{db: db, events: events, index: index}, func() error {
		var err error
		for i := len(cleanups) - 1; i >= 0; i-- {
			err = errors.Join(err, cleanups[i]())
		}
		return err
	}, nil
}

// admit loads the history through the ledger's own write path and drains the
// shared index before any query runs.
func (s *evalStore) admit(ctx context.Context, c corpus) error {
	index := 0
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
			index++
		}
		if _, err := s.events.AdmitBatch(ctx, batch); err != nil {
			return err
		}
	}
	for {
		n, err := s.index.DrainBatch(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
}

// identities reads the text identity of every indexed event. It is the value
// recall uses to count identical derived text once.
func (s *evalStore) identities(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT source_id,conversation,coalesce(text_hash,'') FROM recall_doc WHERE kind='event'")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, conversation, hash string
			if err := rows.Scan(&id, &conversation, &hash); err != nil {
				return err
			}
			if hash != "" {
				out[id] = conversation + "\x00" + hash
			}
		}
		return rows.Err()
	})
	return out, err
}

// evaluateRecall runs every named configuration over the same questions and
// returns numbers only. It writes nothing outside root and prints nothing.
func evaluateRecall(ctx context.Context, root string, c corpus, questions []evalQuestion, vectors recall.VectorQuery) (report evalReport, err error) {
	report = evalReport{Version: 1, Schema: len(canon.Migrations()), Corpus: c.stats(), Questions: len(questions)}
	if len(c.Turns) == 0 || len(questions) == 0 {
		return report, errEvalQuestions
	}
	s, closeStore, err := openEvalStore(ctx, root)
	if err != nil {
		return report, err
	}
	defer func() { err = errors.Join(err, closeStore()) }()
	if err := s.admit(ctx, c); err != nil {
		return report, err
	}
	identities, err := s.identities(ctx)
	if err != nil {
		return report, err
	}
	report.Evidence, report.EvidenceUnknown, report.QuestionsRun = resolveQuestions(questions, c, identities)
	if report.QuestionsRun == 0 {
		return report, errEvalEvidence
	}
	readEvent := func(ctx context.Context, id string) (string, error) {
		e, err := s.events.Get(ctx, id)
		if err != nil {
			return "", err
		}
		if e.BytesUnavailable {
			return "", ledger.ErrBytesUnavailable
		}
		return e.Body.DerivedText(), nil
	}
	scope := recall.Scope{Person: person, Agent: agent, Kinds: []recall.Kind{recall.Event}, At: 1800000000}
	configs := evalConfigs(vectors)
	found12 := make([]map[string]bool, len(configs))
	for i := range configs {
		found12[i] = map[string]bool{}
	}
	for ci, cfg := range configs {
		row := evalRow{Name: cfg.Name}
		var samples []int64
		var recall5, recall12, recall20, evidence12, evidenceTotal int
		var mrr float64
		for _, q := range questions {
			if len(q.keys) == 0 {
				continue
			}
			text := q.Question
			if cfg.Short {
				text = q.Short
				if text == "" {
					continue
				}
			}
			start := time.Now()
			results, err := recall.Search(ctx, s.db, readEvent, scope, recall.Query{Text: text, Limit: evalLimit, Vectors: cfg.Vectors})
			samples = append(samples, time.Since(start).Nanoseconds())
			if err != nil {
				return report, err
			}
			row.Questions++
			evidenceTotal += len(q.keys)
			if len(results) == 0 {
				row.Empty++
			}
			want := make(map[string]bool, len(q.keys))
			for _, key := range q.keys {
				want[key] = true
			}
			rank := 0
			in12 := map[string]bool{}
			for i, result := range results {
				key := identities[result.ID]
				if key == "" {
					key = "id:" + result.ID
				}
				if !want[key] {
					continue
				}
				if rank == 0 {
					rank = i + 1
				}
				if i < 12 {
					in12[key] = true
				}
			}
			if rank > 0 && rank <= 5 {
				recall5++
			}
			if rank > 0 && rank <= 12 {
				recall12++
			}
			if rank > 0 && rank <= evalLimit {
				recall20++
				mrr += 1 / float64(rank)
			}
			evidence12 += len(in12)
			found12[ci][q.ID] = rank > 0 && rank <= 12
		}
		row.Latency = summarize(samples)
		if row.Questions > 0 {
			row.Recall5 = float64(recall5) / float64(row.Questions)
			row.Recall12 = float64(recall12) / float64(row.Questions)
			row.Recall20 = float64(recall20) / float64(row.Questions)
			row.MRR20 = mrr / float64(row.Questions)
			if evidenceTotal > 0 {
				row.EvidenceRecall12 = float64(evidence12) / float64(evidenceTotal)
			}
		}
		report.Configurations = append(report.Configurations, row)
	}
	for i := range configs {
		for j := i + 1; j < len(configs); j++ {
			pair := evalPair{A: configs[i].Name, B: configs[j].Name}
			for id, a := range found12[i] {
				b, ok := found12[j][id]
				if !ok {
					continue
				}
				switch {
				case a && b:
					pair.Both++
				case a:
					pair.OnlyA++
				case b:
					pair.OnlyB++
				}
			}
			pair.P = signTest(pair.OnlyA, pair.OnlyB)
			report.Pairs = append(report.Pairs, pair)
		}
	}
	return report, nil
}

// BenchmarkRecallEval runs the private question set over the private snapshot.
// Fused configurations join when the vector source of #874 is supplied.
func BenchmarkRecallEval(b *testing.B) {
	if *questionsFlag == "" || *snapshotFlag == "" {
		b.Skip("recall eval needs -storebench-hermes and -recall-questions")
	}
	c, err := readSnapshot(b.Context(), *snapshotFlag)
	if err != nil {
		b.Fatal("storebench_snapshot_invalid")
	}
	questions, err := readQuestionFile(*questionsFlag)
	if err != nil {
		b.Fatal("recall_eval_questions_invalid")
	}
	var roots []string
	defer func() {
		for _, root := range roots {
			os.RemoveAll(root)
		}
	}()
	var r evalReport
	for b.Loop() {
		root, err := os.MkdirTemp("", "lina-recall-eval-")
		if err != nil {
			b.Fatal("recall_eval_scratch_failed")
		}
		roots = append(roots, root)
		r, err = evaluateRecall(b.Context(), root, c, questions, nil)
		if err != nil {
			b.Fatalf("recall_eval_failed: %v", err)
		}
	}
	b.ReportMetric(float64(r.Configurations[0].Latency.P50NS), "lexical-p50-ns")
	b.ReportMetric(r.Configurations[0].Recall12, "lexical-recall-12")
	data, err := json.Marshal(r)
	if err != nil {
		b.Fatal("recall_eval_report_failed")
	}
	fmt.Fprintln(os.Stdout, string(data))
}

// TestRecallEvalGenerated checks the report's shape, never its values. The
// questions come from the benchmark's public word list.
func TestRecallEvalGenerated(t *testing.T) {
	c := generate(4)
	questions := generatedQuestions(t, c, 4)
	// A private root inside a watched parent: anything outside the scratch
	// directory fails the run.
	parent := t.TempDir()
	root := filepath.Join(parent, "scratch")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	r, err := evaluateRecall(t.Context(), root, c, questions, testVectors(c))
	if err != nil {
		t.Fatal(err)
	}
	if r.Questions != 4 || r.QuestionsRun != 4 || r.Evidence == 0 || r.EvidenceUnknown != 0 || r.Corpus.Events == 0 {
		t.Fatalf("counts: %+v", r)
	}
	names := []string{"lexical-question", "lexical-short", "fused-question", "fused-short"}
	if len(r.Configurations) != len(names) {
		t.Fatalf("configurations: %d", len(r.Configurations))
	}
	for i, row := range r.Configurations {
		if row.Name != names[i] {
			t.Fatalf("configuration %d: %s", i, row.Name)
		}
		if row.Questions == 0 || row.Questions > len(questions) {
			t.Fatalf("%s questions: %d", row.Name, row.Questions)
		}
		if row.Latency.Count != row.Questions || row.Latency.P50NS > row.Latency.P95NS || row.Latency.P95NS > row.Latency.MaxNS {
			t.Fatalf("%s latency: %+v", row.Name, row.Latency)
		}
		for _, value := range []float64{row.Recall5, row.Recall12, row.Recall20, row.EvidenceRecall12, row.MRR20} {
			if value < 0 || value > 1 {
				t.Fatalf("%s out of range: %v", row.Name, value)
			}
		}
		if row.Recall5 > row.Recall12 || row.Recall12 > row.Recall20 {
			t.Fatalf("%s recall not monotonic", row.Name)
		}
		if row.Empty < 0 || row.Empty > row.Questions {
			t.Fatalf("%s empty: %d", row.Name, row.Empty)
		}
	}
	if len(r.Pairs) != 6 {
		t.Fatalf("pairs: %d", len(r.Pairs))
	}
	for _, pair := range r.Pairs {
		if pair.A == pair.B || pair.P <= 0 || pair.P > 1 {
			t.Fatalf("pair: %+v", pair)
		}
		if pair.OnlyA+pair.OnlyB+pair.Both > len(questions) {
			t.Fatalf("pair overcounts: %+v", pair)
		}
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range questions {
		if bytes.Contains(data, []byte(q.Question)) {
			t.Fatal("report contains question text")
		}
		if q.Short != "" && bytes.Contains(data, []byte(q.Short)) {
			t.Fatal("report contains query text")
		}
		if bytes.Contains(data, []byte(q.ID)) {
			t.Fatal("report contains a question id")
		}
	}
	for _, turn := range c.Turns {
		for _, m := range turn {
			if bytes.Contains(data, []byte(m.ID)) {
				t.Fatal("report contains an event id")
			}
		}
	}
	files, err := os.ReadDir(parent)
	if err != nil || len(files) != 1 || files[0].Name() != "scratch" {
		t.Fatal("eval wrote outside its scratch directory")
	}
}

// TestRecallEvalQuestionFile checks the private format, legacy references and
// that neither the file nor its directory is written.
func TestRecallEvalQuestionFile(t *testing.T) {
	c := generate(2)
	first := c.Turns[0][0]
	second := c.Turns[1][0]
	c.SourceEvents = map[int64]string{7001: second.ID}
	dir := t.TempDir()
	path := filepath.Join(dir, "questions.jsonl")
	body := fmt.Sprintf("{\"id\":\"q1\",\"question\":%q,\"evidence\":[%q]}\n{\"id\":\"q2\",\"question\":%q,\"short_query\":%q,\"evidence\":[\"message:7001\"]}\n",
		head(first.Text, 40), first.ID, head(second.Text, 40), head(second.Text, 10))
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := digest(path)
	if err != nil {
		t.Fatal(err)
	}
	questions, err := readQuestionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(questions) != 2 || questions[1].Short == "" {
		t.Fatalf("questions: %d", len(questions))
	}
	after, err := digest(path)
	if err != nil || before != after {
		t.Fatal("question file changed")
	}
	r, err := evaluateRecall(t.Context(), t.TempDir(), c, questions, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Questions != 2 || r.QuestionsRun != 2 || r.Configurations[0].Questions != 2 || r.Configurations[1].Questions != 1 {
		t.Fatalf("rows: %+v", r.Configurations)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 || files[0].Name() != filepath.Base(path) {
		t.Fatal("eval wrote beside the question file")
	}
	// A malformed line, a duplicate id and a question without evidence are
	// refused before any store is built.
	for _, bad := range []string{
		"{\"id\":\"q1\",\"question\":\"a\",\"evidence\":[\"1\"]}\n{\"id\":\"q1\",\"question\":\"b\",\"evidence\":[\"2\"]}\n",
		"{\"id\":\"q1\",\"question\":\"a\"}\n",
		"not json\n",
	} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readQuestionFile(path); !errors.Is(err, errEvalQuestions) {
			t.Fatal("malformed question file accepted")
		}
	}
	// Evidence the snapshot does not hold is counted and its question skipped;
	// a set with no usable evidence fails instead of reporting empty rows.
	if err := os.WriteFile(path, []byte("{\"id\":\"q1\",\"question\":\"a\",\"evidence\":[\"unknown\"]}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	parsed, err := readQuestionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluateRecall(t.Context(), t.TempDir(), c, parsed, nil); !errors.Is(err, errEvalEvidence) {
		t.Fatal("question set without usable evidence accepted")
	}
	if err := os.WriteFile(path, []byte("{\"id\":\"q1\",\"question\":\"a\",\"evidence\":[\"unknown\",\""+first.ID+"\"]}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if parsed, err = readQuestionFile(path); err != nil {
		t.Fatal(err)
	}
	r, err = evaluateRecall(t.Context(), t.TempDir(), c, parsed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.QuestionsRun != 1 || r.EvidenceUnknown != 1 || r.Evidence != 1 {
		t.Fatalf("unknown evidence counted wrong: %+v", r)
	}
}

// head returns the first n runes, never a split rune.
func head(text string, n int) string {
	runes := []rune(text)
	return string(runes[:min(n, len(runes))])
}

// generatedQuestions takes literal windows of generated person events, as the
// research method did, so the lexical configurations can find them.
func generatedQuestions(t *testing.T, c corpus, n int) []evalQuestion {
	t.Helper()
	var people []message
	for _, turn := range c.Turns {
		for _, m := range turn {
			if m.Entry == guard.EntryOwner && len(strings.Fields(m.Text)) > 8 {
				people = append(people, m)
			}
		}
	}
	var questions []evalQuestion
	for i := range min(n, len(people)) {
		words := strings.Fields(people[i].Text)
		start := (i * 7) % (len(words) - 5)
		short := ""
		if i%3 != 2 {
			short = words[start]
		}
		questions = append(questions, evalQuestion{
			ID:       fmt.Sprintf("question-%02d", i),
			Question: strings.Join(words[start:start+5], " "),
			Short:    short,
			Evidence: []string{people[i].ID},
		})
	}
	if len(questions) != n {
		t.Fatalf("generated %d questions", len(questions))
	}
	return questions
}

// testVectors is the test endpoint: it orders the newest events first so the
// fused path runs without a model or network call.
func testVectors(c corpus) recall.VectorQuery {
	var ids []string
	for _, turn := range c.Turns {
		for _, m := range turn {
			ids = append(ids, m.ID)
		}
	}
	return func(context.Context, recall.Scope, string) ([]string, error) {
		out := make([]string, 0, evalLimit)
		for i := len(ids) - 1; i >= 0 && len(out) < evalLimit; i-- {
			out = append(out, ids[i])
		}
		return out, nil
	}
}
