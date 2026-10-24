// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
)

// The panel's own keys drive these scenarios at 120x34: a key is sent to the
// real TUI, the pass Core records next is read from the ledger, and the frame
// that follows it is recorded beside that pass.

// adjustRun is one live scenario: a child Core, its stub workers, the real TUI
// and the ledger the recorder reads.
type adjustRun struct {
	t       *testing.T
	h       *harness
	fixture panelFixture
	child   *exec.Cmd
	exited  chan error
	person  string
	agent   string
	goals   []string
	card    *panelCase
	term    *terminal
	control adjustControl
}

// adjustMark is what a key asked of one pass: the command the pass applied and
// the goal it applied to.
type adjustMark struct {
	Command string
	Moved   string
	State   string
}

// startAdjustCore installs the bundle, creates the state root with one Core
// run, writes the fixture through the goal engine, then starts Core over it.
func startAdjustCore(t *testing.T, fixture panelFixture, graphs []string, limit goal.RunLimit) *adjustRun {
	t.Helper()
	h := newHarness(t, buildLina(t))
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	t.Cleanup(cancel)
	h.ctx = ctx
	// The installed bundle is the real lina; the child Core is this test binary,
	// which hosts the fixture driver in its own process tree.
	bundle := makeInstallBundle(t, t.TempDir(), h.binary, buildCommand(t, "lina-node"))
	installConversationBundle(t, h, bundle, "http://127.0.0.1:1")
	r := &adjustRun{t: t, h: h, fixture: fixture}
	r.boot()
	r.stopCore()
	r.seed(graphs, limit)
	r.boot()
	return r
}

// boot starts the child Core and waits until it admits requests.
func (r *adjustRun) boot() {
	r.t.Helper()
	executable, err := os.Executable()
	if err != nil {
		r.t.Fatal(err)
	}
	r.child = exec.CommandContext(r.h.ctx, executable, "-test.run=^TestM3WorkPanelAdjustChild$")
	r.child.Env = append(os.Environ(), adjustRootEnv+"="+r.h.root, adjustSocketEnv+"="+r.h.socket)
	r.child.Stdout, r.child.Stderr = os.Stdout, os.Stderr
	if err = r.child.Start(); err != nil {
		r.t.Fatal(err)
	}
	r.exited = make(chan error, 1)
	go func() { r.exited <- r.child.Wait() }()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(r.h.socket); err == nil {
			break
		}
		select {
		case err := <-r.exited:
			r.t.Fatalf("child Core exited before listening: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			r.t.Fatal("Core did not listen")
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitCoreReady(r.t, r.h)
}

// stopCore ends the child Core and waits for it to exit.
func (r *adjustRun) stopCore() {
	r.t.Helper()
	if r.child == nil {
		return
	}
	_ = r.child.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-r.exited:
		if err != nil {
			r.t.Fatalf("child Core exit: %v", err)
		}
	case <-time.After(30 * time.Second):
		_ = r.child.Process.Kill()
		<-r.exited
		r.t.Fatal("child Core did not stop")
	}
	r.child, r.exited = nil, nil
}

// seed writes the fixture's 일 and the person's limit through the goal engine
// before the run that exercises the panel, as the fixture manager does.
func (r *adjustRun) seed(graphs []string, limit goal.RunLimit) {
	r.t.Helper()
	db, closeStore := installedAcceptanceStore(r.t, r.h)
	defer closeStore()
	if err := db.Read(r.h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(r.h.ctx, "SELECT r.person_id,f.agent_id FROM agent_first_run f JOIN agent_revisions r ON r.agent_id=f.agent_id AND r.revision=1").Scan(&r.person, &r.agent)
	}); err != nil {
		r.t.Fatal(err)
	}
	engine, store := panelManager(r.t, r.h, db)
	defer store.Close()
	if _, _, err := engine.SetRunLimit(r.h.ctx, r.person, limit, "fixture-start-limit"); err != nil {
		r.t.Fatal(err)
	}
	for i, graph := range graphs {
		id := fmt.Sprintf("adjust-%03d", i)
		if err := adjustAdmit(r.h.ctx, engine, id, r.fixture.Names[i%len(r.fixture.Names)], r.person, r.agent, r.fixture.Graphs[indexOfGraph(r.fixture, graph)]); err != nil {
			r.t.Fatalf("admit %s: %v", id, err)
		}
		r.goals = append(r.goals, "goal-"+id)
	}
}

func indexOfGraph(fixture panelFixture, id string) int {
	for i, g := range fixture.Graphs {
		if g.ID == id {
			return i
		}
	}
	panic("unknown graph " + id)
}

// adjustAdmit writes one 일 of the corpus as a goal with its whole graph.
func adjustAdmit(ctx context.Context, e *goal.Engine, id, name, person, agent string, graph panelGraphFixture) error {
	if err := e.Admit(ctx, goal.Goal{ID: "goal-" + id, Person: person, Agent: agent, Conversation: "main:" + agent, Summary: name, Done: "The sample is verified", NoServiceReason: "plan", Scope: goal.GoalScope{Repositories: []string{"fixture"}, Includes: graph.ID}, Criteria: []goal.Criterion{{ID: "outcome", Statement: "The sample is verified", Check: goal.CriterionCheck{Kind: goal.RunEvidence, Method: "acceptance", Target: "fixture"}}}}); err != nil {
		return fmt.Errorf("goal: %w", err)
	}
	expanded, err := expandPanelGraph(graph)
	if err != nil {
		return err
	}
	steps := make([]goal.Step, 0, len(expanded))
	for _, step := range expanded {
		item := goal.Step{ID: fmt.Sprintf("s%d", step.Index), Name: step.Name, Kind: goal.BackgroundStep, Done: "Produce " + step.Name, Repository: "fixture", Resources: goal.Resources{CPU: 1, MemoryMB: 1, DiskMB: 1}, Background: &goal.BackgroundBrief{Goal: step.Name, Scope: graph.ID, NotTouch: "other work", Done: "Produce " + step.Name, Result: "notes", Checks: []goal.BackgroundCheck{{Name: "result", Kind: "claimed"}}}}
		for _, after := range step.After {
			item.DependsOn = append(item.DependsOn, fmt.Sprintf("s%d", after))
		}
		steps = append(steps, item)
	}
	if _, err = e.SubmitStepGraph(ctx, "goal-"+id, person, agent, "main:"+agent, 0, steps); err != nil {
		return fmt.Errorf("graph: %w", err)
	}
	return nil
}

// setLoad writes the host load the child reports, as a host monitor would.
func (r *adjustRun) setLoad(load float64) {
	r.t.Helper()
	r.control.Load = load
	writeAdjustControl(r.t, r.h.root, r.control)
}

// wake reports a small host change, so Core commits a pass without the person
// asking for anything. The person's own limit is a number here, so the load
// never changes what may start.
func (r *adjustRun) wake() {
	r.t.Helper()
	r.control.Load += 0.25
	writeAdjustControl(r.t, r.h.root, r.control)
}

// release lets the goal's running step finish in the child.
func (r *adjustRun) release(goalID string) {
	r.t.Helper()
	for _, id := range r.control.Release {
		if id == goalID {
			return
		}
	}
	r.control.Release = append(r.control.Release, goalID)
	writeAdjustControl(r.t, r.h.root, r.control)
}

func (r *adjustRun) press(keys string) int {
	r.t.Helper()
	writes := r.term.screen.Snapshot().Writes
	r.h.keys(r.term, keys)
	return writes
}

// passes reads the ledger's committed passes, newest last.
func (r *adjustRun) passes() []adjustPass {
	r.t.Helper()
	return adjustLedger(r.t, r.h.ctx, r.h.root)
}

func (r *adjustRun) lastSeq() int64 {
	r.t.Helper()
	passes := r.passes()
	if len(passes) == 0 {
		return 0
	}
	return passes[len(passes)-1].Seq
}

// waitPass blocks until Core commits a pass after after that matches, so a
// command is read from the ledger instead of assumed from a delay.
func (r *adjustRun) waitPass(after int64, want func(adjustPass) bool) adjustPass {
	r.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, pass := range r.passes() {
			if pass.Seq > after && want(pass) {
				return pass
			}
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("no recorded pass matched after %d; recorded: %s", after, r.describe(after))
		}
		select {
		case <-r.h.ctx.Done():
			r.t.Fatalf("waiting for a pass: %v", r.h.ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// describe prints the passes around one, the control file and the child's
// worker log, for a failed wait.
func (r *adjustRun) describe(after int64) string {
	var b strings.Builder
	all := r.passes()
	fmt.Fprintf(&b, "\n  %d passes, last seq %d", len(all), all[len(all)-1].Seq)
	for _, pass := range all {
		if pass.Seq <= after-20 {
			continue
		}
		fmt.Fprintf(&b, "\n  seq=%d auto=%t limit=%d/%s rank=%d run=%v started=%v places=%v", pass.Seq, pass.Auto, pass.Limit, pass.LimitKey, len(pass.Rank), pass.Running, pass.Started, pass.Places)
	}
	if raw, err := os.ReadFile(adjustControlPath(r.h.root)); err == nil {
		b.WriteString("\n  control=" + string(raw))
	}
	if raw, err := os.ReadFile(filepath.Join(r.h.root, "adjust", "child.log")); err == nil {
		b.WriteString("\n  child log:\n" + string(raw))
	}
	return b.String()
}

// frame waits for the settled frame the key produced and records it against the
// pass that applied the key.
func (r *adjustRun) frame(seq int64, label string, writes int, want func([]string) bool) panelFrame {
	r.t.Helper()
	const x, width = 80, 40
	ctx, cancel := context.WithTimeout(r.h.ctx, operationTimeout)
	defer cancel()
	snap, err := r.term.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
		rows := panelCellRows(s, x, width)
		return s.Writes > writes && s.AltScreen && len(rows) == r.card.Height && want(rows)
	})
	if err != nil {
		r.t.Fatalf("%s frame: %v\n%s", label, err, screenText(snap))
	}
	rows := screenRows(snap)
	frame := panelFrame{Label: label, View: "cards", Seq: seq, At: time.Now(), Commit: time.Now(), Rows: rows, Panel: panelCellRows(snap, x, width), Composer: rows[r.card.Height-2], Anchor: rows[max(0, r.card.Height-3)], CursorHidden: snap.CursorHidden, CursorX: snap.CursorX, CursorY: snap.CursorY}
	r.card.Frames = append(r.card.Frames, frame)
	return frame
}

// connectPanel starts the real TUI at 120x34 and focuses the work panel.
func (r *adjustRun) connectPanel(language design.Language) {
	r.t.Helper()
	r.card = &panelCase{Name: "adjust", N: len(r.goals), Language: language, Width: 120, Height: 34}
	env := []string{"TERM=xterm-256color", "LANG=" + string(language) + "_US.UTF-8", "LC_ALL=" + string(language) + "_US.UTF-8"}
	r.term = r.h.startTerminalSize(env, false, true, r.card.Width, r.card.Height)
	title := design.Text(language, "work.title")
	ctx, cancel := context.WithTimeout(r.h.ctx, operationTimeout)
	defer cancel()
	if _, err := r.term.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
		rows := panelCellRows(s, 80, 40)
		return s.AltScreen && len(rows) == r.card.Height && strings.HasPrefix(rows[1], title)
	}); err != nil {
		r.t.Fatalf("panel title: %v", err)
	}
	// Ctrl+P focuses the panel, so the adjust keys act on the selection.
	writes := r.press("\x10")
	if _, err := r.term.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
		return s.Writes > writes && s.CursorHidden
	}); err != nil {
		r.t.Fatalf("panel focus: %v", err)
	}
}

// cards reads Core's card projection for the panel.
func (r *adjustRun) cards() []panelCard {
	r.t.Helper()
	out := []panelCard{}
	for _, row := range panelProjection(r.t, r.h) {
		card := panelCard{ID: row.Ref, Name: row.Line.Name, State: "waiting"}
		if row.Line.Now != nil {
			card.Now = *row.Line.Now
		}
		if row.Line.StartedAt != nil {
			card.Started, _ = time.Parse(time.RFC3339Nano, *row.Line.StartedAt)
		}
		if row.Line.State != nil {
			card.State = string(*row.Line.State)
		}
		fmt.Sscanf(string(row.Line.Waiting), "%d", &card.Waiting)
		out = append(out, card)
	}
	return out
}

// closePanel quits the TUI and checks the terminal was restored.
func (r *adjustRun) closePanel() {
	r.t.Helper()
	r.h.keys(r.term, "\x04")
	r.term.process.wait(r.t, 0)
	r.h.expectTerminalRestored(r.term)
	r.stopCore()
}

// recordAdjustPause records E12's keys: pause, resume, a confirmed stop and a
// cancelled one. The 일 that runs holds the only slot, so the pause pass has
// nothing to start and the resume pass has the slot it needs.
func recordAdjustPause(t *testing.T, fixture panelFixture) panelCase {
	t.Helper()
	r := startAdjustCore(t, fixture, []string{"chain", "single"}, goal.RunLimit{Value: 1})
	// Core localizes a card's Now from the 일's own name language, and the
	// corpus names are Korean, so the case is Korean too.
	r.connectPanel(design.Korean)
	run, other := r.goals[0], r.goals[1]
	// The first pass that holds the first 일's step is the scenario's start.
	first := r.waitPass(0, func(p adjustPass) bool { return contains(p.Started, run) || contains(p.Running, run) })
	r.card.Cards = r.cards()
	r.frame(first.Seq, "cards", 0, func([]string) bool { return true })
	marks := map[int64]adjustMark{}
	// The second card is the queued 일; Space pauses it and its card says so.
	writes := r.press("\x1b[B")
	r.frame(r.lastSeq(), "select", writes, func([]string) bool { return true })
	before := r.lastSeq()
	writes = r.press(" ")
	paused := r.waitPass(before, func(p adjustPass) bool { return p.Paused[other] })
	marks[paused.Seq] = adjustMark{Command: "pause"}
	r.frame(paused.Seq, "paused", writes, func(rows []string) bool {
		return strings.Contains(strings.Join(rows, "\n"), panelPausedWord(design.Korean))
	})
	// The running 일 finishes its chain, which frees the only slot while the
	// paused 일 is fenced off. No pass of that run starts the paused 일.
	for contains(r.lastPass().Running, run) || contains(r.lastPass().Started, run) {
		before = r.lastSeq()
		r.release(run)
		next := r.waitPass(before, func(p adjustPass) bool {
			return contains(p.Started, run) || !contains(p.Running, run) && !contains(p.Started, run)
		})
		if contains(next.Started, other) {
			t.Fatalf("the paused 일 started on pass %d", next.Seq)
		}
	}
	// Space again resumes it, and its step dispatches on the next pass.
	before = r.lastSeq()
	writes = r.press(" ")
	resumed := r.waitPass(before, func(p adjustPass) bool { return contains(p.Started, other) })
	marks[resumed.Seq] = adjustMark{Command: "resume"}
	r.frame(resumed.Seq, "resumed", writes, func([]string) bool { return true })
	// Delete asks first; Enter confirms and Core records the stop.
	before = r.lastSeq()
	r.press("\x1b[3~")
	if r.lastSeq() != before {
		t.Fatal("Delete stopped the 일 before the person confirmed")
	}
	r.press("\r")
	// A stopped 일 leaves the lines, so the limit change commits the pass that
	// carries its state.
	r.press("+")
	stopped := r.waitPass(before, func(p adjustPass) bool { return p.States[other] == "stopped" })
	marks[stopped.Seq] = adjustMark{Command: "stop", State: "stopped"}
	// Delete and another key change nothing, which the next pass shows. The
	// stopped 일 left the panel, so the selection moves back to the first card.
	before = r.lastSeq()
	r.press("\x1b[A")
	r.press("\x1b[3~")
	r.press("x")
	// A limit change commits a pass that shows the 일 was not stopped.
	r.press("+")
	cancelled := r.waitPass(before, func(p adjustPass) bool { return true })
	marks[cancelled.Seq] = adjustMark{Command: "stop-cancel", State: cancelled.States[run]}
	if cancelled.States[run] == "stopped" {
		t.Fatal("a cancelled stop stopped the 일")
	}
	r.closePanel()
	return r.finish(marks)
}

// recordAdjustLimit records E11, E13 and E14: a move, limits 1, 3 and
// automatic, and a host that carries nothing.
func recordAdjustLimit(t *testing.T, fixture panelFixture) panelCase {
	t.Helper()
	graphs := make([]string, 20)
	for i := range graphs {
		graphs[i] = fixture.Graphs[i%len(fixture.Graphs)].ID
	}
	r := startAdjustCore(t, fixture, graphs, goal.RunLimit{Value: 1})
	r.connectPanel(design.Korean)
	marks := map[int64]adjustMark{}
	// One 일 runs under the person's limit of one; that pass is the first the
	// check reads for the limit of one.
	start := r.waitPass(0, func(p adjustPass) bool { return len(p.Started) == 1 && len(p.Rank) == 20 })
	marks[start.Seq] = adjustMark{Command: "limit"}
	// The person raises the limit one step at a time; each pass starts the next
	// 일 in rank order.
	for _, want := range []int{2, 3} {
		before := r.lastSeq()
		r.press("+")
		pass := r.waitPass(before, func(p adjustPass) bool { return !p.Auto && p.Limit == want })
		marks[pass.Seq] = adjustMark{Command: "limit"}
	}
	r.card.Cards = r.cards()
	r.frame(r.lastSeq(), "cards", 0, func([]string) bool { return true })
	// A move key reorders one place, and the pass Core records carries it.
	moved := r.goals[11]
	rank := r.lastPass().Rank
	from := rankOf(rank, moved)
	if from < 1 {
		t.Fatalf("the moved 일 is at rank %d", from+1)
	}
	for range 11 {
		r.press("\x1b[B")
	}
	before := r.lastSeq()
	writes := r.press("\x1b[a")
	pass := r.waitPass(before, func(p adjustPass) bool { return rankOf(p.Rank, moved) == from-1 })
	marks[pass.Seq] = adjustMark{Command: "move", Moved: moved}
	r.frame(pass.Seq, "moved", writes, func(rows []string) bool {
		card := r.card.Cards[cardIndexOf(r.card.Cards, moved)]
		return panelShowsCard(parsePanelFrame(panelFrame{Panel: rows}, *r.card), card)
	})
	// A host that carries nothing lowers the automatic limit and starts nothing
	// past it; the waiting 일 keep their place and the load reason.
	r.setLoad(20)
	before = r.lastSeq()
	r.press("0")
	short := r.waitPass(before, func(p adjustPass) bool { return p.Auto && p.LimitKey == "load" && len(p.Places) > 0 })
	marks[short.Seq] = adjustMark{Command: "limit"}
	r.closePanel()
	return r.finish(marks)
}

// lastPass is the newest committed pass.
func (r *adjustRun) lastPass() adjustPass {
	r.t.Helper()
	passes := r.passes()
	if len(passes) == 0 {
		r.t.Fatal("no pass was recorded")
	}
	return passes[len(passes)-1]
}

// finish applies the marks to the ledger's passes and returns the case.
func (r *adjustRun) finish(marks map[int64]adjustMark) panelCase {
	r.t.Helper()
	for _, pass := range r.passes() {
		recorded := panelSchedulePass{Seq: pass.Seq, At: pass.At, Rank: pass.Rank, Running: pass.Running, Started: pass.Started, Reasons: pass.Reasons, Places: pass.Places, Limit: pass.Limit, Automatic: pass.Auto}
		// An automatic pass that fell for a quota or a loaded host is the
		// shortage the person sees as each waiting 일's reason.
		if pass.Auto && (pass.LimitKey == "quota" || pass.LimitKey == "load") && len(pass.Places) > 0 {
			recorded.Shortage = pass.LimitKey
		}
		if mark, ok := marks[pass.Seq]; ok {
			recorded.Command = mark.Command
			recorded.Moved = mark.Moved
			recorded.State = mark.State
		}
		r.card.Passes = append(r.card.Passes, recorded)
	}
	return *r.card
}

// The work panel's adjust keys at 120x34: E11 to E14 of T-16.
func TestM3WorkPanelAdjust(t *testing.T) {
	fixture := loadPanelFixture(t)
	paused := recordAdjustPause(t, fixture)
	limits := recordAdjustLimit(t, fixture)
	recording := &panelRecording{Cases: []panelCase{paused, limits}}
	for _, verdict := range evaluateWorkPanel(recording) {
		switch verdict.Check {
		case "E11 order", "E12 pause", "E13 limit", "E14 shortage":
		default:
			continue
		}
		if !verdict.Input || !verdict.Passed {
			t.Errorf("%s: %s", verdict.Check, verdict.Detail)
			continue
		}
		t.Logf("%s pass: %s", verdict.Check, verdict.Detail)
	}
}
