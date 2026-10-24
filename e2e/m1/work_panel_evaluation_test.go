// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/progress"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

// panelFixture is the fixed T-16 input. Counts, names and graphs never come
// from the run, so two recordings compare.
type panelFixture struct {
	Counts []int
	Widths []int
	Height int
	Cases  []struct{ N, Waiting int }
	States []string
	Names  []string
	Graphs []panelGraphFixture
}

type panelGraphFixture struct {
	ID    string
	Steps []struct {
		Name     string `json:"name"`
		After    []int  `json:"after"`
		Count    int    `json:"count"`
		AfterAll bool   `json:"after_all"`
	} `json:"steps"`
}

// panelStep is one expanded step of the corpus. Index is its position in the
// submitted graph, so After can only name earlier steps.
type panelStep struct {
	Index int
	Name  string
	After []int
}

// panelCard is one 일 as Core projects it. Goal is the goal record that owns
// the 일's graph, Stages is Core's own stage list for it.
type panelCard struct {
	ID      string
	Name    string
	Now     string
	State   string
	Waiting int
	Started time.Time
	Commit  time.Time
	Graph   string
	Goal    string
	Stages  []panelStageEntry
}

// panelFrame is one settled 120x34 frame. Commit is the latest work-ledger
// commit at or before the frame; a frame produced by a local key carries the
// same commit as the last Core-driven frame.
type panelFrame struct {
	Label        string
	View         string
	Filter       string
	Seq          int64
	At           time.Time
	Commit       time.Time
	Rows         []string
	Panel        []string
	Composer     string
	Anchor       string
	CursorHidden bool
	CursorX      int
	CursorY      int
}

// panelCase is one fixed case in one language and glyph mode.
type panelCase struct {
	Name     string
	N        int
	Waiting  int
	Language design.Language
	ASCII    bool
	Width    int
	Height   int
	Cards    []panelCard
	Frames   []panelFrame
	Views    []panelStageView
	Passes   []panelSchedulePass
	Moves    []panelTransition
}

// panelStageView is one opened 일 view. A build without the view records none,
// so its check reports the missing input instead of a pass.
type panelStageView struct {
	Card    string
	Rows    []string
	Entries []panelStageEntry
	Workers []panelWorkerRow
}

type panelStageEntry struct {
	Number int
	Kind   string
	Label  string
	State  string
	Open   bool
	Steps  []panelStageStep
}

type panelStageStep struct {
	Name  string
	State string
}

// panelWorkerRow is a bracketed live-session row and the entry and step it sits
// under.
type panelWorkerRow struct {
	Text     string
	Entry    int
	Step     string
	Expanded bool
}

// panelSchedulePass is one scheduling pass Core recorded. A build without
// recorded passes leaves the list empty.
type panelSchedulePass struct {
	Seq      int64
	At       time.Time
	Command  string
	Moved    string
	State    string
	Limit    int
	// Automatic marks a pass that applied the automatic limit, so "the limit
	// set to automatic" is an input the check can read.
	Automatic bool
	Running   []string
	Rank     []string
	Started  []string
	Reasons  map[string]string
	Places   map[string]int
	Shortage string
	// Draining marks a pass whose running steps predate a lowered limit and
	// are finishing; it starts nothing.
	Draining bool
}

// panelTransition is one injected step change with its commit and the frame
// that first showed it.
type panelTransition struct {
	Card   string
	From   string
	To     string
	Commit time.Time
	Frame  int
}

type panelRecording struct {
	Cases []panelCase
}

func loadPanelFixture(t *testing.T) panelFixture {
	t.Helper()
	raw, err := os.ReadFile("../m3/work-panel.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture panelFixture
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) != 5 || len(fixture.Names) < 4 || len(fixture.Graphs) != 5 {
		t.Fatalf("fixed corpus incomplete: %+v", fixture)
	}
	want := []int{1, 6, 4, 84, 6}
	// The expanded edges are the corpus, not just its step count: a graph that
	// lost its blockers still has the right length.
	// single; chain; diamond; fan-out of 1/82/1 with the last step after all
	// 83 that precede it; two independent chains of three.
	carets := []int{0, 5, 4, 165, 4}
	for i, graph := range fixture.Graphs {
		steps, err := expandPanelGraph(graph)
		if err != nil {
			t.Fatalf("graph %s: %v", graph.ID, err)
		}
		if len(steps) != want[i] {
			t.Fatalf("graph %s has %d steps, want %d", graph.ID, len(steps), want[i])
		}
		edges := 0
		for _, step := range steps {
			edges += len(step.After)
		}
		if edges != carets[i] {
			t.Fatalf("graph %s has %d blockers, want %d", graph.ID, edges, carets[i])
		}
	}
	for _, c := range fixture.Cases {
		if c.N != 1 && c.N != 5 && c.N != 20 && c.N != 50 {
			t.Fatalf("case count %d is outside the fixed corpus", c.N)
		}
		if c.Waiting < 1 || c.Waiting > c.N {
			t.Fatalf("case waiting %d is outside its count", c.Waiting)
		}
	}
	return fixture
}

// expandPanelGraph turns the compact fixed corpus into the exact step list.
// Count duplicates a step as parallel work; AfterAll names every earlier step.
func expandPanelGraph(g panelGraphFixture) ([]panelStep, error) {
	out := []panelStep{}
	for _, step := range g.Steps {
		after := append([]int{}, step.After...)
		for _, index := range after {
			if index < 0 || index >= len(out) {
				return nil, fmt.Errorf("step %q names a later or absent blocker", step.Name)
			}
		}
		if step.AfterAll {
			after = after[:0]
			for i := range out {
				after = append(after, i)
			}
		}
		count := max(1, step.Count)
		for i := range count {
			name := step.Name
			if count > 1 {
				name = fmt.Sprintf("%s %d", step.Name, i+1)
			}
			out = append(out, panelStep{Index: len(out), Name: name, After: append([]int{}, after...)})
		}
	}
	for _, step := range out {
		if (render.Unicode{}).Width(step.Name) > 24 {
			return nil, fmt.Errorf("step name %q exceeds the 24-cell bound", step.Name)
		}
	}
	return out, nil
}

// TestM3WorkPanelEvaluation records the fixed corpus through installed Core and
// the real TUI, then reports every T-16 check. Checks whose input this build
// does not record yet are reported as missing, never as passing; the
// deliberate-wrong-frame self-test covers all fourteen evaluators.
func TestM3WorkPanelEvaluation(t *testing.T) {
	fixture := loadPanelFixture(t)
	recording := recordWorkPanel(t, fixture)
	// The scheduling controls of E11 to E14 are recorded by the panel's own
	// keys against a Core in a child process, which can grant the person's
	// choices; TestM3WorkPanelAdjust reads those recordings.
	for _, verdict := range evaluateWorkPanel(recording) {
		word := "pending"
		if verdict.Input {
			word = "pass"
			if !verdict.Passed {
				word = "FAIL"
				t.Errorf("%s: %s", verdict.Check, verdict.Detail)
			}
		}
		t.Logf("%s %s: %s", verdict.Check, word, verdict.Detail)
	}
}

func recordWorkPanel(t *testing.T, fixture panelFixture) *panelRecording {
	t.Helper()
	recording := &panelRecording{}
	for _, scenario := range fixture.Cases {
		t.Run(fmt.Sprintf("N=%d/waiting=%d", scenario.N, scenario.Waiting), func(t *testing.T) {
			h, start := installedConversationHarness(t, "http://127.0.0.1:1")
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
			defer cancel()
			h.ctx = ctx
			core := start(true)
			h.stopCore(core)
			cards := seedPanelCorpus(t, h, fixture, scenario.N, scenario.Waiting)
			core = start(true)
			defer h.stopCore(core)
			// The panel keeps the order Core publishes beside the rows, so the
			// recording's sequence is that order, not the rows' own.
			seeded := map[string]panelCard{}
			for _, card := range cards {
				seeded[card.ID] = card
			}
			projection := panelProjection(t, h)
			if len(projection) != scenario.N {
				t.Fatalf("Core projected %d cards, want %d", len(projection), scenario.N)
			}
			cards = make([]panelCard, 0, len(projection))
			waiting := 0
			for _, row := range projection {
				line := row.Line
				card, ok := seeded[row.Ref]
				if !ok {
					t.Fatalf("Core published an unknown card %q", row.Ref)
				}
				card.Name = line.Name
				card.Now = ""
				if line.Now != nil {
					card.Now = *line.Now
				}
				card.Started = time.Time{}
				if line.StartedAt != nil {
					card.Started, _ = time.Parse(time.RFC3339Nano, *line.StartedAt)
				}
				if n, err := fmt.Sscanf(string(line.Waiting), "%d", &card.Waiting); n != 1 || err != nil {
					t.Fatalf("Core waiting count %q: %v", line.Waiting, err)
				}
				card.State = ""
				if line.State != nil {
					card.State = string(*line.State)
				}
				if card.Waiting > 0 {
					waiting++
				}
				cards = append(cards, card)
			}
			if waiting != scenario.Waiting {
				t.Fatalf("Core shows %d waiting cards, want %d", waiting, scenario.Waiting)
			}
			for _, language := range []design.Language{design.English, design.Korean} {
				for _, ascii := range []bool{false, true} {
					c := panelCase{
						Name: fmt.Sprintf("N=%d/waiting=%d/%s/ascii=%t", scenario.N, scenario.Waiting, language, ascii),
						N:    scenario.N, Waiting: scenario.Waiting, Language: language, ASCII: ascii,
						Width: fixture.Widths[0], Height: fixture.Height, Cards: cards,
					}
					recordPanelTerminal(t, h, &c)
					recording.Cases = append(recording.Cases, c)
				}
			}
		})
	}
	return recording
}

// panelProjection reads the rows the progress topic publishes for the panel,
// in the order the topic gives beside them. Cards only; a goal row has none.
func panelProjection(t *testing.T, h *harness) []protocol.ProgressRow {
	t.Helper()
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "work-panel-eval", []protocol.Capability{{Name: "progress", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	view := "overview"
	if _, err = c.Progress(h.ctx, protocol.ProgressGet{View: &view}); err != nil {
		t.Fatal(err)
	}
	e, err := c.Receive(h.ctx)
	if err != nil || e.Error != nil {
		t.Fatalf("progress: %+v %v", e, err)
	}
	p, ok := e.Payload.(protocol.Progress)
	if !ok || p.Rows == nil || p.CardOrder == nil {
		t.Fatalf("missing progress projection: %+v", e)
	}
	byRef := map[string]protocol.ProgressRow{}
	for _, row := range *p.Rows {
		if row.Line.Kind != "goal" {
			byRef[row.Ref] = row
		}
	}
	out := make([]protocol.ProgressRow, 0, len(*p.CardOrder))
	for _, ref := range *p.CardOrder {
		if row, ok := byRef[ref]; ok {
			out = append(out, row)
		}
	}
	return out
}

// recordPanelTerminal drives one PTY at 120x34. Every recorded frame is a
// settled snapshot the TUI produced, in Core's card order and in both views.
func recordPanelTerminal(t *testing.T, h *harness, c *panelCase) {
	t.Helper()
	env := []string{"TERM=xterm-256color", "LANG=" + string(c.Language) + "_US.UTF-8", "LC_ALL=" + string(c.Language) + "_US.UTF-8"}
	if c.ASCII {
		env = append(env, "LINA_GLYPHS=ascii")
	}
	terminal := h.startTerminalSize(env, false, true, c.Width, c.Height)
	const x, width = 80, 40
	title := design.Text(c.Language, "work.title")
	titleWith := func(filter string) string {
		if filter == "" {
			return title
		}
		separator := " · "
		if c.ASCII {
			separator = " - "
		}
		return title + separator + design.Text(c.Language, "work.filter."+filter)
	}
	// A settled frame is the TUI's own redraw after the key, not the frame that
	// was still on screen when the key arrived. Writes bound that redraw; no
	// sleep or renderer tick establishes it.
	settle := func(label, view, filter string, focused bool, after int) panelFrame {
		t.Helper()
		ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
		defer cancel()
		snap, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
			rows := panelCellRows(s, x, width)
			return s.Writes > after && s.AltScreen && len(rows) == c.Height && rows[1] == titleWith(filter) && s.CursorHidden == focused && panelViewMatches(rows, view, c, filter)
		})
		if err != nil {
			t.Fatalf("%s frame (filter %q, want %q): %v\n%s", label, filter, titleWith(filter), err, screenText(snap))
		}
		rows := screenRows(snap)
		c.Frames = append(c.Frames, panelFrame{Label: label, View: view, Filter: filter, At: time.Now(), Commit: panelFrameCommit(c.Cards), Rows: rows, Panel: panelCellRows(snap, x, width), Composer: rows[c.Height-2], Anchor: rows[max(0, c.Height-3)], CursorHidden: snap.CursorHidden, CursorX: snap.CursorX, CursorY: snap.CursorY})
		return c.Frames[len(c.Frames)-1]
	}
	// The first frame must carry the composer and every card that waits on the
	// person, with no key sent.
	ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
	defer cancel()
	first, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
		rows := panelCellRows(s, x, width)
		if !s.AltScreen || len(rows) != c.Height || rows[1] != title || s.CursorHidden {
			return false
		}
		joined := strings.Join(rows, "\n")
		for _, card := range c.Cards {
			if card.Waiting > 0 && !strings.Contains(joined, panelClipCells(card.Name, 6)) {
				return false
			}
		}
		return true
	})
	if err != nil {
		t.Fatalf("first frame: %v\n%s", err, screenText(first))
	}
	firstRows := screenRows(first)
	c.Frames = append(c.Frames, panelFrame{Label: "cards", View: "cards", At: time.Now(), Commit: panelFrameCommit(c.Cards), Rows: firstRows, Panel: panelCellRows(first, x, width), Composer: firstRows[c.Height-2], Anchor: firstRows[c.Height-3], CursorHidden: first.CursorHidden, CursorX: first.CursorX, CursorY: first.CursorY})

	press := func(keys string) int {
		t.Helper()
		writes := terminal.screen.Snapshot().Writes
		h.keys(terminal, keys)
		return writes
	}
	settle("cards", "cards", "", true, press("\x10"))
	filters := []string{"", string(protocol.RunningStateNeedsInput), string(protocol.RunningStateInProgress), string(protocol.RunningStateWaiting), string(protocol.RunningStateDone)}
	settle("compact", "compact", "", true, press("\x10"))
	for _, filter := range filters[1:] {
		settle("compact", "compact", filter, true, press("\x1b[C"))
	}
	settle("compact", "compact", "", true, press(strings.Repeat("\x1b[D", len(filters)-1)))
	settle("cards", "cards", "", true, press("\x10"))
	for _, filter := range filters[1:] {
		settle("cards", "cards", filter, true, press("\x1b[C"))
	}
	// Esc returns focus to the composer with the filter the person chose; the
	// draft stays, so Ctrl+D quits from there.
	settle("cards", "cards", "", true, press(strings.Repeat("\x1b[D", len(filters)-1)))
	settle("cards", "cards", "", false, press("\x1b"))
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
}

func panelFrameCommit(cards []panelCard) time.Time {
	var latest time.Time
	for _, card := range cards {
		if card.Commit.After(latest) {
			latest = card.Commit
		}
	}
	return latest
}

// seedPanelCorpus writes the fixed corpus through installed Core before startup,
// as the fixture manager's recorded work. Each 일 first gets its corpus graph as
// an admitted goal step graph; then the 일's task with a stub worker session is
// recorded against that goal, with the waiting cards last.
func seedPanelCorpus(t *testing.T, h *harness, fixture panelFixture, n, waiting int) []panelCard {
	t.Helper()
	db, closeStore := installedAcceptanceStore(t, h)
	defer closeStore()
	var person, agent string
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT r.person_id,f.agent_id FROM agent_first_run f JOIN agent_revisions r ON r.agent_id=f.agent_id AND r.revision=1").Scan(&person, &agent)
	}); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(h.root, "corpus.txt")
	content := []byte("fixed corpus result\n")
	if err := os.WriteFile(artifact, content, 0600); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(content))
	engine, store := panelManager(t, h, db)
	defer store.Close()
	// The fixture manager admits each 일's graph first, in its own transaction,
	// then the 일's work is recorded in one write.
	goals := make([]string, n)
	for i := range n {
		id := fmt.Sprintf("corpus-%03d", i)
		goalID, err := panelAdmitGraph(h.ctx, engine, id, fixture.Names[i%len(fixture.Names)], person, agent, fixture.Graphs[i%len(fixture.Graphs)])
		if err != nil {
			t.Fatalf("%s: admit graph: %v", id, err)
		}
		goals[i] = goalID
	}
	// Managed goals are the project cards; their worker tasks are detail rows.
	// Record the fixture's person wait on the owning step as well, so Core
	// derives the card's wait from the goal journal after startup/replay.
	journal := goal.NewJournal(db)
	for _, goalID := range goals[n-waiting:] {
		err := journal.Update(h.ctx, goal.Transition{ID: goalID + ":fixture-wait", Kind: "fixture.person_wait", At: time.Now().Unix()}, func(v *goal.View) error {
			r, ok := v.Get("goal", goalID)
			if !ok {
				return fmt.Errorf("fixture goal %s is missing", goalID)
			}
			var g goal.Goal
			if err := json.Unmarshal(r.Body, &g); err != nil {
				return err
			}
			if len(g.Tasks) == 0 {
				return fmt.Errorf("fixture goal %s has no steps", goalID)
			}
			g.Tasks[0].Stage = goal.TaskNeedsPerson
			raw, err := json.Marshal(g)
			if err != nil {
				return err
			}
			return v.Put("goal", goalID, raw)
		})
		if err != nil {
			t.Fatalf("%s: record person wait: %v", goalID, err)
		}
	}
	cards := make([]panelCard, 0, n)
	if err := db.Write(h.ctx, func(tx *canon.Tx) error {
		for i := range n {
			id := fmt.Sprintf("corpus-%03d", i)
			name := fixture.Names[i%len(fixture.Names)]
			state := fixture.States[i%len(fixture.States)]
			// The last admitted cards wait on the person.
			if i >= n-waiting {
				state = "needs_input"
			}
			graph := fixture.Graphs[i%len(fixture.Graphs)]
			goalID := goals[i]
			task := work.Task{ID: id, GoalID: goalID, PersonID: person, AgentID: agent, ConversationID: "main:" + agent, Title: name, Done: "Sample verified", ArtifactRoots: []string{h.root}}
			run := work.Run{ID: id + ":run", TaskID: id, Generation: 1, Worker: "fixture", ThreadID: id, TurnID: id, NodeID: "fixture"}
			at := time.Now().UTC().Add(-time.Duration(42+i*65) * time.Minute)
			if state == "waiting" {
				task.Schedule = &work.Schedule{Group: "corpus", Repository: "fixture", Worker: run.Worker, Host: run.NodeID}
			}
			events := []work.Event{{Key: id + ":create", TaskID: id, Kind: "created", Task: &task, RecordedAt: at.Format(time.RFC3339Nano)}}
			add := func(e work.Event) {
				e.Key = fmt.Sprintf("%s:%d", id, len(events))
				e.TaskID = id
				e.RecordedAt = at.Add(time.Minute).Format(time.RFC3339Nano)
				events = append(events, e)
			}
			if state == "needs_input" {
				add(work.Event{Kind: "waiting_opened", Wait: &work.Wait{Round: "corpus", Question: "Choose the sample label"}})
			} else {
				if task.Schedule != nil {
					add(work.Event{Kind: "schedule_reserved", Scheduling: &work.Scheduling{Reserved: true, CapacityHeld: true}})
				}
				add(work.Event{Kind: "started", Run: &run})
				receipt := work.Receipt{SchemaVersion: 1, RelationshipID: id, ExecutionGeneration: 1, Attempt: 1, Outcome: "ready_for_review", Producer: "child", TurnRef: work.TurnRef{ThreadID: id, TurnID: id, TurnStatus: "completed"}, EmittedAt: at, Manifest: []work.Artifact{{Path: artifact, SHA256: hash}}, Provenance: []work.Provenance{{ArtifactID: id, TaskID: id, NodeID: run.NodeID, Path: artifact, SHA256: hash, ObservedAt: at}}}
				receipt.RevisionHash = work.ManifestHash(receipt.Manifest)
				receipt.EventID = work.ReceiptID(receipt)
				add(work.Event{Kind: "receipt", RunID: run.ID, Receipt: &receipt})
				add(work.Event{Kind: "finished", RunID: run.ID, Value: "completed"})
				if state == "done" {
					add(work.Event{Kind: "judged", ReceiptID: receipt.EventID, Value: "verified", Evidence: "fixture content checked"})
				}
				if state == "waiting" {
					add(work.Event{Kind: "schedule_wait", Scheduling: &work.Scheduling{}, WaitReason: &work.ScheduleWait{Kind: "host_capacity", Target: "fixture"}})
				}
			}
			card, err := work.Project(events)
			if err != nil {
				return err
			}
			if card.Active {
				return fmt.Errorf("fixture cannot leave an unobserved live execution")
			}
			for _, event := range events {
				raw, err := json.Marshal(event)
				if err != nil {
					return err
				}
				if _, err = tx.ExecContext(h.ctx, "INSERT INTO work_ledger(task_id,event_key,record) VALUES(?,?,?)", id, event.Key, raw); err != nil {
					return err
				}
			}
			cards = append(cards, panelCard{ID: goalID, Name: name, State: card.State, Graph: graph.ID, Goal: goalID, Commit: cardCommit(events)})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Core's own projection of each 일's graph is the oracle the checks and the
	// opened view use. Reading it here also proves the corpus reached the
	// ledger whole: step count, names and blockers.
	for i := range cards {
		stages, err := panelGoalStages(t, h, db, cards[i].Goal, person)
		if err != nil {
			t.Fatalf("%s: Core has no stage list: %v", cards[i].ID, err)
		}
		cards[i].Stages = stages
		if err = panelGraphMatches(cards[i].Graph, fixture, stages); err != nil {
			t.Fatalf("%s: %v", cards[i].ID, err)
		}
	}
	return cards
}

// panelManager is the fixture manager: installed Core's goal engine on the
// installed store. Core runs the same engine after it starts.
func panelManager(t *testing.T, h *harness, db *canon.Store) (*goal.Engine, *records.Store) {
	t.Helper()
	repo, err := records.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := goal.NewEngine(goal.Config{DB: db, Records: panelDefinitions{repo}, Epoch: 1, Now: time.Now})
	if err != nil {
		repo.Close()
		t.Fatal(err)
	}
	return engine, repo
}

// panelDefinitions adapts the records store to the goal engine.
type panelDefinitions struct{ repo *records.Store }

func (d panelDefinitions) Read(ctx context.Context, id string) (goal.DefinitionRecord, string, error) {
	r, rev, err := d.repo.Read(ctx, id)
	if errors.Is(err, records.ErrNotFound) {
		err = goal.ErrDefinitionMissing
	}
	return goal.DefinitionRecord{ID: r.ID, Path: r.Path, Data: r.Data}, rev, err
}

func (d panelDefinitions) Commit(ctx context.Context, c goal.DefinitionChange, person, agent string) (string, error) {
	return d.repo.Commit(ctx, records.Change{ID: c.ID, Base: c.Base, Path: c.Path, Data: c.Data}, records.Actor{Person: person, Agent: agent})
}

// panelAdmitGraph writes one 일's whole fixed graph through Core's goal engine:
// the goal record, then the graph revision with its step names and blockers.
func panelAdmitGraph(ctx context.Context, engine *goal.Engine, id, name, person, agent string, graph panelGraphFixture) (string, error) {
	goalID := "goal-" + id
	g := goal.Goal{ID: goalID, Person: person, Agent: agent, Conversation: "main:" + agent, Summary: name, Done: "The sample is verified", NoServiceReason: "plan", Scope: goal.GoalScope{Repositories: []string{"fixture"}, Includes: graph.ID + ".md"}, Criteria: []goal.Criterion{{ID: "outcome", Statement: "The sample is verified", Check: goal.CriterionCheck{Kind: goal.RunEvidence, Method: "acceptance", Target: "fixture"}}}}
	if err := engine.Admit(ctx, g); err != nil {
		return "", err
	}
	steps, err := expandPanelGraph(graph)
	if err != nil {
		return "", err
	}
	admitted := make([]goal.Step, 0, len(steps))
	for _, step := range steps {
		item := goal.Step{ID: fmt.Sprintf("s%d", step.Index), Name: step.Name, Done: "Produce " + step.Name, Repository: "fixture", Base: "dev", Area: fmt.Sprintf("%s/s%d.txt", graph.ID, step.Index), Worker: "fixture", Resources: goal.Resources{CPU: 0.01, MemoryMB: 1, DiskMB: 1}}
		for _, after := range step.After {
			item.DependsOn = append(item.DependsOn, fmt.Sprintf("s%d", after))
		}
		admitted = append(admitted, item)
	}
	if _, err = engine.SubmitStepGraph(ctx, goalID, person, agent, "main:"+agent, 0, admitted); err != nil {
		return "", err
	}
	return goalID, nil
}

// panelGoalStages reads the stage list Core projects for one 일's graph.
func panelGoalStages(t *testing.T, h *harness, db *canon.Store, goalID, person string) ([]panelStageEntry, error) {
	t.Helper()
	var stages []progress.Stage
	err := db.Read(h.ctx, func(tx *canon.Tx) error {
		var err error
		stages, err = progress.GoalStages(h.ctx, tx, goalID, person, design.English)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]panelStageEntry, 0, len(stages))
	for _, stage := range stages {
		entry := panelStageEntry{Number: stage.Number, Kind: string(stage.Kind), Label: stage.Label, State: string(stage.State)}
		for _, step := range stage.Steps {
			entry.Steps = append(entry.Steps, panelStageStep{Name: step.Name, State: string(step.State)})
		}
		out = append(out, entry)
	}
	return out, nil
}

// panelGraphMatches checks that the ledger's stage list is the corpus graph:
// the same steps at the same depth, so a lost blocker moves a step and fails.
func panelGraphMatches(graph string, fixture panelFixture, stages []panelStageEntry) error {
	var want []panelStep
	for _, g := range fixture.Graphs {
		if g.ID == graph {
			var err error
			if want, err = expandPanelGraph(g); err != nil {
				return err
			}
		}
	}
	if want == nil {
		return fmt.Errorf("unknown graph %q", graph)
	}
	names := make([]string, 0, len(want))
	for _, step := range want {
		names = append(names, step.Name)
	}
	got := []string{}
	entries := 0
	for _, entry := range stages {
		if entry.Kind != "stage" {
			continue
		}
		entries++
		for _, step := range entry.Steps {
			if step.State == string(progress.StepSuperseded) {
				continue
			}
			got = append(got, step.Name)
		}
	}
	slices.Sort(names)
	slices.Sort(got)
	if !slices.Equal(names, got) {
		return fmt.Errorf("the ledger holds %d of the corpus graph %q's %d steps", len(got), graph, len(names))
	}
	// Each graph has its own depth: a chain of six numbers six stages, a diamond
	// three, and the 82-wide fan-out three.
	depth := map[string]int{"single": 1, "chain": 6, "diamond": 3, "fan": 3, "two-chains": 3}
	if entries != depth[graph] {
		return fmt.Errorf("graph %q numbers %d stages, want %d", graph, entries, depth[graph])
	}
	groups := 0
	for _, entry := range stages {
		if len(entry.Steps) > 1 {
			groups++
		}
	}
	// Two independent chains put two steps in each of their three stages.
	if groups != map[string]int{"diamond": 1, "fan": 1, "two-chains": 3}[graph] {
		return fmt.Errorf("graph %q has %d parallel groups", graph, groups)
	}
	return nil
}

// cardCommit is when the event that set the card's current state was recorded.
func cardCommit(events []work.Event) time.Time {
	for i := len(events) - 1; i >= 0; i-- {
		if at, err := time.Parse(time.RFC3339Nano, events[i].RecordedAt); err == nil {
			return at
		}
	}
	return time.Time{}
}
