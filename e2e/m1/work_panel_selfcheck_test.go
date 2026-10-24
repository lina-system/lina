// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/kit/protocol"
)

// TestM3WorkPanelChecksRejectWrongFrames builds a recording that passes every
// T-16 check, then edits one thing per check and requires exactly that check to
// fail. Without this, a check that read nothing would look green.
func TestM3WorkPanelChecksRejectWrongFrames(t *testing.T) {
	base := syntheticPanelRecording(t)
	for _, verdict := range evaluateWorkPanel(base) {
		if !verdict.Input || !verdict.Passed {
			t.Fatalf("the baseline recording does not pass %s: %s", verdict.Check, verdict.Detail)
		}
	}
	// A wrong frame can legitimately fail a second check when both read the
	// same evidence. The pairs below are those overlaps; every other check must
	// stay green, so one check cannot stand in for another.
	overlaps := map[string][]string{
		"E4 now moves": {"E8 latency"},
		"E10 views":    {"E5 counts"},
		"E1 visible":   {"E5 counts"},
		"E5 counts":    {"E10 views"},
		"E2 two rows":  {"E5 counts"},
	}
	for _, wrong := range []struct {
		check  string
		break_ func(*panelRecording)
	}{
		{"E1 visible", func(r *panelRecording) {
			// Keep the row count, lose the card: the waiting card's name is
			// replaced by one the corpus does not hold.
			c := &r.Cases[0]
			c.Frames[0].Panel = renameCard(c.Frames[0].Panel, c.Cards[len(c.Cards)-1], "다른 일")
		}},
		{"E2 two rows", func(r *panelRecording) {
			c := &r.Cases[1]
			c.Frames[1].Panel = withoutNowRow(c.Frames[1].Panel, c.Cards[0])
		}},
		{"E3 elapsed", func(r *panelRecording) {
			r.Cases[0].Frames[1].Panel = replaceElapsed(r.Cases[0].Frames[1].Panel, "59m")
		}},
		{"E4 now moves", func(r *panelRecording) {
			c := &r.Cases[3]
			c.Frames[c.Moves[0].Frame].Panel = replaceNow(c.Frames[c.Moves[0].Frame].Panel, "Wrong activity")
		}},
		{"E5 counts", func(r *panelRecording) {
			c := &r.Cases[3]
			c.Frames[0].Panel = replaceOverflowCount(c.Frames[0].Panel, 5)
		}},
		{"E6 no graph", func(r *panelRecording) {
			c := &r.Cases[1]
			c.Views[0].Rows = append(c.Views[0].Rows, "    └ 연쇄")
		}},
		{"E7 workers", func(r *panelRecording) {
			r.Cases[1].Views[0].Entries[0].Open = false
		}},
		{"E8 latency", func(r *panelRecording) {
			move := &r.Cases[3].Moves[0]
			move.Commit = move.Commit.Add(-2 * time.Second)
		}},
		{"E9 calm", func(r *panelRecording) {
			c := &r.Cases[3]
			c.Frames[c.Moves[5].Frame].Composer = "typed during transitions"
		}},
		{"E10 views", func(r *panelRecording) {
			for i := range r.Cases[2].Frames {
				if r.Cases[2].Frames[i].Filter == string(protocol.RunningStateDone) {
					r.Cases[2].Frames[i].Filter = string(protocol.RunningStateInProgress)
					return
				}
			}
		}},
		// The compact row must still carry Now; a row that keeps only its name
		// and its elapsed time does not satisfy the view.
		{"E10 views", func(r *panelRecording) {
			c := &r.Cases[2]
			card := c.Cards[1]
			for i := range c.Frames {
				if c.Frames[i].View != "compact" || c.Frames[i].Filter != "" {
					continue
				}
				for y, row := range c.Frames[i].Panel {
					if strings.Contains(row, panelClipCells(card.Name, 4)) && strings.Contains(row, card.Now) {
						c.Frames[i].Panel[y] = strings.Replace(row, card.Now, "", 1)
						return
					}
				}
			}
		}},
		{"E11 order", func(r *panelRecording) {
			pass := &r.Cases[2].Passes[1]
			pass.Rank = reversed(pass.Rank)
		}},
		{"E12 pause", func(r *panelRecording) {
			for i := range r.Cases[2].Passes {
				if r.Cases[2].Passes[i].Command == "pause" {
					r.Cases[2].Passes[i].Started = []string{"일-00"}
					return
				}
			}
		}},
		{"E13 limit", func(r *panelRecording) {
			for i := range r.Cases[2].Passes {
				if r.Cases[2].Passes[i].Command == "limit" && r.Cases[2].Passes[i].Limit == 1 {
					r.Cases[2].Passes[i].Started = []string{"일-09"}
					return
				}
			}
		}},
		{"E14 shortage", func(r *panelRecording) {
			for i := range r.Cases[2].Passes {
				if r.Cases[2].Passes[i].Shortage != "" {
					r.Cases[2].Passes[i].Limit = 0
					return
				}
			}
		}},
	} {
		t.Run(wrong.check, func(t *testing.T) {
			broken := cloneRecording(base)
			wrong.break_(&broken)
			found := false
			for _, verdict := range evaluateWorkPanel(&broken) {
				if verdict.Check != wrong.check {
					if verdict.Input && !verdict.Passed && !contains(overlaps[wrong.check], verdict.Check) {
						t.Fatalf("%s also broke: %s", verdict.Check, verdict.Detail)
					}
					continue
				}
				found = true
				if !verdict.Input || verdict.Passed {
					t.Fatalf("%s passed a deliberately wrong recording", wrong.check)
				}
			}
			if !found {
				t.Fatalf("%s was not evaluated", wrong.check)
			}
		})
	}
}

// TestM3WorkPanelChecksAllowRunningStepsToDrain covers the other half of E13
// and E14: lowering a limit never stops steps that already run, so a pass that
// starts nothing may hold more running 일 than the new limit.
func TestM3WorkPanelChecksAllowRunningStepsToDrain(t *testing.T) {
	base := syntheticPanelRecording(t)
	// A pass that starts nothing may hold more running 일 than a limit lowered
	// underneath it; a pass that starts a step may not.
	for _, tc := range []struct {
		name     string
		check    string
		shortage string
		started  []string
		want     bool
	}{
		{"limit drains", "E13 limit", "", nil, true},
		{"limit starts past it", "E13 limit", "", []string{"extra"}, false},
		{"quota drains", "E14 shortage", "quota", nil, true},
		{"quota starts", "E14 shortage", "quota", []string{"extra"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := cloneRecording(base)
			c := &r.Cases[2]
			ids := rankOfIDs(c)
			started := []string{}
			for range tc.started {
				started = append(started, ids[3])
			}
			pass := panelSchedulePass{Seq: 99, Command: "limit", Limit: 1, Rank: ids, Running: ids[:3], Started: started, Draining: tc.started == nil}
			if tc.shortage != "" {
				pass.Command, pass.Shortage = "", tc.shortage
				pass.Reasons = map[string]string{ids[3]: tc.shortage}
				pass.Places = map[string]int{ids[3]: 1}
			}
			c.Passes = append(c.Passes, pass)
			checked := false
			for _, verdict := range evaluateWorkPanel(&r) {
				if verdict.Check != tc.check {
					if verdict.Input && !verdict.Passed {
						t.Fatalf("%s broke as well: %s", verdict.Check, verdict.Detail)
					}
					continue
				}
				checked = true
				if !verdict.Input || verdict.Passed != tc.want {
					t.Fatalf("%s: input=%t passed=%t, want pass=%t (%s)", verdict.Check, verdict.Input, verdict.Passed, tc.want, verdict.Detail)
				}
			}
			if !checked {
				t.Fatalf("%s was not evaluated", tc.check)
			}
		})
	}
}

// TestM3WorkPanelVisibleMatchesTheWaitingCard covers the fixed corpus's
// repeated names: at N = 50 a waiting card shares its name with earlier cards,
// so E1 must find the waiting card itself, not another card with its name.
func TestM3WorkPanelVisibleMatchesTheWaitingCard(t *testing.T) {
	base := syntheticPanelRecording(t)
	for _, tc := range []struct {
		name   string
		hidden bool
		want   bool
	}{
		{"waiting cards shown", false, true},
		{"waiting cards hidden behind namesakes", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := cloneRecording(base)
			c := &r.Cases[3]
			for i := range c.Cards {
				var admitted int
				if _, err := fmt.Sscanf(c.Cards[i].ID, "일-%d", &admitted); err != nil {
					t.Fatal(err)
				}
				c.Cards[i].Name = fmt.Sprintf("이름 %d", admitted%10)
			}
			drawn := *c
			if tc.hidden {
				// The frame keeps the waiting cards' namesakes and pushes the
				// waiting cards themselves past the overflow row.
				drawn.Cards = append(slices.Clone(c.Cards[c.Waiting:]), c.Cards[:c.Waiting]...)
			}
			first := &c.Frames[0]
			first.Panel, first.Rows = renderSyntheticPanel(drawn, first.View, first.Filter, first.At)
			for _, verdict := range evaluateWorkPanel(&r) {
				if verdict.Check != "E1 visible" {
					continue
				}
				if !verdict.Input || verdict.Passed != tc.want {
					t.Fatalf("E1: input=%t passed=%t, want pass=%t (%s)", verdict.Input, verdict.Passed, tc.want, verdict.Detail)
				}
				return
			}
			t.Fatal("E1 was not evaluated")
		})
	}
}

// TestM3WorkPanelOrderFollowsAdjacentMoves covers E11 for every move a key can
// make: Shift+Up and Shift+Down move an 일 one place, from anywhere in the rank.
func TestM3WorkPanelOrderFollowsAdjacentMoves(t *testing.T) {
	base := syntheticPanelRecording(t)
	for _, tc := range []struct {
		name     string
		from, to int
		want     bool
	}{
		{"up to the top", 1, 0, true},
		{"down from the top", 0, 1, true},
		{"up from below second place", 5, 4, true},
		{"down from the middle", 5, 6, true},
		{"down from second place", 1, 2, true},
		{"jump to the top", 5, 0, false},
		{"jump down", 2, 4, false},
		{"unchanged", 5, 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := cloneRecording(base)
			c := &r.Cases[2]
			before := c.Passes[0].Rank
			moved := before[tc.from]
			rank := slices.Delete(slices.Clone(before), tc.from, tc.from+1)
			rank = slices.Insert(rank, tc.to, moved)
			pass := &c.Passes[1]
			pass.Moved, pass.Rank, pass.Running, pass.Started = moved, rank, rank[:3], []string{rank[0]}
			for _, verdict := range evaluateWorkPanel(&r) {
				if verdict.Check != "E11 order" {
					continue
				}
				if !verdict.Input || verdict.Passed != tc.want {
					t.Fatalf("E11: input=%t passed=%t, want pass=%t (%s)", verdict.Input, verdict.Passed, tc.want, verdict.Detail)
				}
				return
			}
			t.Fatal("E11 was not evaluated")
		})
	}
}

func rankOfIDs(c *panelCase) []string {
	out := []string{}
	for _, card := range c.Cards {
		out = append(out, card.ID)
	}
	return out
}

func cloneRecording(r *panelRecording) panelRecording {
	out := panelRecording{Cases: make([]panelCase, len(r.Cases))}
	for i, c := range r.Cases {
		cloned := c
		cloned.Cards = append([]panelCard{}, c.Cards...)
		cloned.Frames = append([]panelFrame{}, c.Frames...)
		for j := range cloned.Frames {
			cloned.Frames[j].Panel = append([]string{}, c.Frames[j].Panel...)
			cloned.Frames[j].Rows = append([]string{}, c.Frames[j].Rows...)
		}
		cloned.Views = make([]panelStageView, len(c.Views))
		for j, view := range c.Views {
			view.Rows = append([]string{}, view.Rows...)
			view.Entries = append([]panelStageEntry{}, view.Entries...)
			for k := range view.Entries {
				view.Entries[k].Steps = append([]panelStageStep{}, view.Entries[k].Steps...)
			}
			view.Workers = append([]panelWorkerRow{}, view.Workers...)
			cloned.Views[j] = view
		}
		cloned.Moves = append([]panelTransition{}, c.Moves...)
		cloned.Passes = append([]panelSchedulePass{}, c.Passes...)
		for j := range cloned.Passes {
			pass := cloned.Passes[j]
			pass.Rank = append([]string{}, pass.Rank...)
			pass.Running = append([]string{}, pass.Running...)
			pass.Started = append([]string{}, pass.Started...)
			pass.Reasons = copyStringMap(pass.Reasons)
			pass.Places = copyIntMap(pass.Places)
			cloned.Passes[j] = pass
		}
		out.Cases[i] = cloned
	}
	return out
}

func copyStringMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyIntMap(in map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

// syntheticPanelRecording is a well-formed recording built from the same fixed
// corpus: four cases, both views, every filter, an opened view with a worker
// row, and recorded passes for a move, pause, resume, the three limits and a
// shortage.
func syntheticPanelRecording(t *testing.T) *panelRecording {
	t.Helper()
	fixture := loadPanelFixture(t)
	r := &panelRecording{}
	base := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	for _, scenario := range []struct{ N, Waiting int }{{1, 1}, {5, 1}, {20, 1}, {50, 3}} {
		c := panelCase{
			Name:     fmt.Sprintf("N=%d/waiting=%d", scenario.N, scenario.Waiting),
			N:        scenario.N,
			Waiting:  scenario.Waiting,
			Language: design.English,
			Width:    fixture.Widths[0],
			Height:   fixture.Height,
		}
		for i := range scenario.N {
			state := fixture.States[i%len(fixture.States)]
			waiting := 0
			if i >= scenario.N-scenario.Waiting {
				state, waiting = "needs_input", 1
			}
			c.Cards = append(c.Cards, panelCard{
				ID: fmt.Sprintf("일-%02d", i), Name: fmt.Sprintf("일 %02d", i),
				State: state, Waiting: waiting, Graph: fixture.Graphs[i%len(fixture.Graphs)].ID,
				Started: base.Add(-time.Duration(42+i) * time.Minute), Now: "자료 조사",
			})
		}
		// Core orders cards: what waits on the person, then running, then work
		// that waits outside, then done.
		order := func(card panelCard) int {
			if card.Waiting > 0 {
				return 0
			}
			switch card.State {
			case "in_progress":
				return 1
			case "waiting":
				return 2
			}
			return 3
		}
		slices.SortStableFunc(c.Cards, func(a, b panelCard) int { return order(a) - order(b) })
		at := base.Add(time.Duration(scenario.N) * time.Minute)
		addFrameAt := func(view, filter string, seq int64, frameAt time.Time) panelFrame {
			f := panelFrame{View: view, Filter: filter, Seq: seq, At: frameAt, Composer: "› ", Anchor: "reading", CursorHidden: view != ""}
			if filter == "" {
				f.Label = view
			} else {
				f.Label = view + "/" + filter
			}
			f.Panel, f.Rows = renderSyntheticPanel(c, view, filter, frameAt)
			f.Rows = f.Panel
			f.Commit = frameAt.Add(-time.Second)
			c.Frames = append(c.Frames, f)
			return f
		}
		addFrame := func(view, filter string, seq int64) panelFrame { return addFrameAt(view, filter, seq, at) }
		addFrame("cards", "", 0)
		addFrame("cards", "", 0)
		addFrame("compact", "", 0)
		for _, filter := range []string{"needs_input", "in_progress", "waiting", "done"} {
			addFrame("compact", filter, 0)
		}
		addFrame("cards", "", 0)
		for _, filter := range []string{"needs_input", "in_progress", "waiting", "done"} {
			addFrame("cards", filter, 0)
		}
		c.Views = append(c.Views, syntheticStageView(t, fixture, c.Cards[0]))
		if scenario.N == 50 {
			// Fifty injected transitions, each with the frame that first shows it.
			for i := range 50 {
				c.Cards[0].Now = fmt.Sprintf("진행 %02d", i)
				frameAt := base.Add(time.Duration(i) * time.Second)
				addFrameAt("cards", "", 0, frameAt)
				c.Moves = append(c.Moves, panelTransition{Card: c.Cards[0].Name, To: c.Cards[0].Now, Commit: frameAt.Add(-120 * time.Millisecond), Frame: len(c.Frames) - 1})
			}
		}
		if scenario.N == 20 {
			c.Passes = syntheticPasses(c, at)
			// Space pauses the selected 일; the next pass dispatches nothing and
			// its card shows the paused word. Space again resumes it.
			paused := c.Cards[0].Now
			c.Cards[0].Now = panelPausedWord(c.Language)
			c.Passes = append(c.Passes, panelSchedulePass{Seq: int64(len(c.Frames) + 1), At: at, Command: "pause", Rank: c.Passes[1].Rank})
			addFrame("cards", "", int64(len(c.Frames)+1))
			c.Cards[0].Now = paused
			c.Passes = append(c.Passes, panelSchedulePass{Seq: int64(len(c.Frames) + 1), At: at, Command: "resume", Rank: c.Passes[1].Rank, Started: []string{c.Cards[0].ID}})
			addFrame("cards", "", int64(len(c.Frames)+1))
			// Delete and Enter stop the 일; Delete and any other key change nothing.
			c.Passes = append(c.Passes,
				panelSchedulePass{Seq: int64(len(c.Frames) + 1), At: at, Command: "stop", State: "stopped", Rank: c.Passes[1].Rank},
				panelSchedulePass{Seq: int64(len(c.Frames) + 1), At: at, Command: "stop-cancel", State: "in_progress", Rank: c.Passes[1].Rank},
			)
		}
		r.Cases = append(r.Cases, c)
	}
	return r
}

// syntheticPasses records what the design says each command does on the next
// pass: a move changes rank, a pause dispatches nothing, a resume dispatches
// again, the limit holds the running count and a shortage lowers the automatic
// limit.
func syntheticPasses(c panelCase, at time.Time) []panelSchedulePass {
	ids := make([]string, 0, len(c.Cards))
	for _, card := range c.Cards {
		ids = append(ids, card.ID)
	}
	rank := append([]string{}, ids...)
	moved := append([]string{ids[1], ids[0]}, ids[2:]...)
	// A queued 일 knows its place in rank order and its one reason.
	queued := func(order []string, running []string, reason string, count int) (map[string]string, map[string]int) {
		reasons, places := map[string]string{}, map[string]int{}
		place := 0
		for _, id := range order {
			if contains(running, id) || place == count {
				continue
			}
			place++
			reasons[id] = reason
			places[id] = place
		}
		return reasons, places
	}
	limitPass := func(seq int64, limit int, order []string, started []string) panelSchedulePass {
		running := append([]string{}, order[:min(limit, len(order))]...)
		short := len(running) < limit
		reasons, places := queued(order, running, "slot", 2)
		var start []string
		if short && len(started) > 0 {
			start = started
			reasons, places = queued(order, append(running, start...), "slot", 2)
		}
		return panelSchedulePass{Seq: seq, At: at, Command: "limit", Limit: limit, Rank: order, Running: running, Started: start, Reasons: reasons, Places: places}
	}
	// Each pass records what the person's last choice did on it: the move
	// applies here, not on the pass that recorded the key.
	passes := []panelSchedulePass{
		{Seq: 1, At: at, Rank: rank},
		{Seq: 2, At: at, Command: "move", Moved: ids[1], Rank: moved, Running: moved[:3], Started: []string{moved[0]}},
	}
	passes = append(passes,
		limitPass(3, 1, moved, nil),
		limitPass(4, 3, moved, []string{moved[2]}),
		panelSchedulePass{Seq: 5, At: at, Command: "limit", Limit: 0, Rank: moved, Running: moved[:5]},
	)
	reasons, places := queued(moved, moved[:2], "quota", 2)
	passes = append(passes, panelSchedulePass{Seq: 6, At: at, Limit: 2, Shortage: "quota", Rank: moved, Running: moved[:2], Reasons: reasons, Places: places})
	return passes
}

// syntheticStages groups the diamond graph by blocker depth, as the stage list
// does: one entry per depth, and a depth with several steps is one entry.
func syntheticStages(t *testing.T, fixture panelFixture, graph string) []panelStageEntry {
	t.Helper()
	var steps []panelStep
	for _, g := range fixture.Graphs {
		if g.ID == graph {
			var err error
			if steps, err = expandPanelGraph(g); err != nil {
				t.Fatal(err)
			}
		}
	}
	depth := map[int]int{}
	byDepth := map[int][]panelStep{}
	highest := 0
	for _, step := range steps {
		number := 1
		for _, after := range step.After {
			number = max(number, depth[after]+1)
		}
		depth[step.Index] = number
		byDepth[number] = append(byDepth[number], step)
		highest = max(highest, number)
	}
	out := []panelStageEntry{}
	for number := 1; number <= highest; number++ {
		group := byDepth[number]
		entry := panelStageEntry{Number: number, Kind: "stage", State: "next", Label: group[0].Name}
		for _, step := range group {
			entry.Steps = append(entry.Steps, panelStageStep{Name: step.Name, State: "next"})
		}
		if len(group) > 1 {
			entry.Label = fmt.Sprintf("%s 외 %d", group[0].Name, len(group)-1)
		}
		out = append(out, entry)
	}
	return append(out, panelStageEntry{Kind: "checking", Label: "checking", State: "next"})
}

func syntheticStageView(t *testing.T, fixture panelFixture, card panelCard) panelStageView {
	t.Helper()
	entries := syntheticStages(t, fixture, "diamond")
	for i := range entries {
		entries[i].Open = i == 0
		if entries[i].Open {
			entries[i].State = "working"
			for j := range entries[i].Steps {
				entries[i].Steps[j].State = "working"
			}
		}
	}
	view := panelStageView{Card: card.ID, Entries: entries, Rows: syntheticStageRows(entries)}
	if len(entries) > 0 && len(entries[0].Steps) > 0 {
		worker := "[Codex] In progress"
		view.Workers = append(view.Workers, panelWorkerRow{Text: worker, Entry: entries[0].Number, Step: entries[0].Steps[0].Name, Expanded: true})
		view.Rows = append(view.Rows, "        "+worker)
	}
	return view
}

func syntheticStageRows(entries []panelStageEntry) []string {
	rows := []string{}
	for _, entry := range entries {
		mark := "·"
		switch entry.State {
		case "done":
			mark = "✓"
		case "working":
			mark = "●"
		case "waiting":
			mark = "○"
		case "needs_input":
			mark = "!"
		}
		if entry.Kind == "stage" {
			rows = append(rows, fmt.Sprintf("%d %s %s", entry.Number, mark, entry.Label))
			continue
		}
		rows = append(rows, fmt.Sprintf("%s %s", mark, entry.Label))
	}
	return rows
}

// renderSyntheticPanel draws the sidebar the corpus expects, in the same form
// the work panel uses. It gives the self-test a well-formed baseline; the
// recorded frames come from the real TUI.
func renderSyntheticPanel(c panelCase, view, filter string, now time.Time) ([]string, []string) {
	title := design.Text(c.Language, "work.title")
	if filter != "" {
		title += " · " + design.Text(c.Language, "work.filter."+filter)
	}
	rows := []string{"", title}
	active, done := []panelCard{}, 0
	for _, card := range c.Cards {
		if !panelMatchesFilter(card, filter) {
			continue
		}
		if card.State == "done" && card.Waiting == 0 {
			done++
			continue
		}
		active = append(active, card)
	}
	// The sidebar starts under the header and ends above the composer rule.
	panelHeight := c.Height - 4
	available := panelHeight - 1
	if done > 0 {
		available--
	}
	cardRows := 2
	if view == "compact" {
		cardRows = 1
	}
	shown := min(len(active), max(0, available/cardRows))
	if shown < len(active) {
		shown = min(len(active), max(0, (available-1)/cardRows))
	}
	const panelWidth = 40
	u := render.Unicode{}
	for _, card := range active[:shown] {
		elapsed := design.Text(c.Language, "unknown")
		if !card.Started.IsZero() {
			elapsed = panelElapsed(now.Sub(card.Started))
		}
		budget := panelWidth - u.Width(elapsed)
		name := render.Truncate(card.Name, max(1, budget), u)
		body := name
		if view == "compact" {
			activity := render.Truncate(card.Now, max(0, budget-u.Width(name)-1), u)
			body = name + " " + activity
		}
		rows = append(rows, body+strings.Repeat(" ", max(1, panelWidth-u.Width(body)-u.Width(elapsed)))+elapsed)
		if view != "compact" {
			rows = append(rows, "  "+render.Truncate(card.Now, panelWidth-2, u))
		}
	}
	if done > 0 {
		rows = append(rows, fmt.Sprintf(design.Text(c.Language, "work.done"), done))
	}
	if shown < len(active) {
		waiting := 0
		for _, card := range active[shown:] {
			if card.Waiting > 0 || card.State == "needs_input" {
				waiting++
			}
		}
		more := fmt.Sprintf(design.Text(c.Language, "work.more"), panelEllipsis(c.ASCII), len(active)-shown)
		if waiting > 0 {
			more += " · " + fmt.Sprintf(design.Text(c.Language, "work.needs"), waiting)
		}
		rows = append(rows, more)
	}
	running, waiting := 0, 0
	for _, card := range c.Cards {
		if card.Waiting > 0 {
			waiting++
		} else if card.State == "in_progress" {
			running++
		}
	}
	status := ""
	if running+waiting > 0 {
		status = fmt.Sprintf(design.Text(c.Language, "work.count"), running, waiting)
	}
	// The panel occupies the right forty cells of every row, so its rows are
	// the whole frame's rows sliced to that width; the status line shares the
	// last one.
	full := make([]string, c.Height)
	for i := range full {
		if i < len(rows) {
			full[i] = rows[i]
		}
	}
	full[c.Height-3] = strings.Repeat("─", 40)
	full[c.Height-1] = status
	return full, full
}

// overflowCount finds the hidden-card number after the ellipsis.
var overflowCount = regexp.MustCompile(`(?:…|\.\.\.) (\d+)`)

// renameCard shows a different name on a card's first row, keeping its width.
func renameCard(panel []string, card panelCard, name string) []string {
	out := append([]string{}, panel...)
	prefix := panelClipCells(card.Name, 6)
	for i, row := range out {
		if strings.Contains(row, prefix) {
			out[i] = strings.Replace(row, prefix, name, 1)
			return out
		}
	}
	return out
}

// withoutNowRow blanks a card's second row, so it reads as one row.
func withoutNowRow(panel []string, card panelCard) []string {
	out := append([]string{}, panel...)
	prefix := panelClipCells(card.Name, 6)
	for i, row := range out {
		if strings.Contains(row, prefix) && i+1 < len(out) && strings.HasPrefix(out[i+1], "  ") {
			out[i+1] = ""
			return out
		}
	}
	return out
}

func replaceElapsed(panel []string, elapsed string) []string {
	out := append([]string{}, panel...)
	for i, row := range out {
		if m := elapsedTail.FindStringSubmatchIndex(row); m != nil {
			out[i] = row[:m[2]] + elapsed
			return out
		}
	}
	return out
}

func replaceNow(panel []string, now string) []string {
	out := append([]string{}, panel...)
	for i := 2; i < len(out); i++ {
		if strings.HasPrefix(out[i], "  ") && strings.TrimSpace(out[i]) != "" {
			out[i] = "  " + now
			return out
		}
	}
	return out
}

func replaceOverflowCount(panel []string, count int) []string {
	out := append([]string{}, panel...)
	for i, row := range out {
		if m := overflowCount.FindStringSubmatchIndex(row); m != nil {
			out[i] = row[:m[2]] + fmt.Sprint(count) + row[m[3]:]
			return out
		}
	}
	return out
}

func reversed(values []string) []string {
	out := append([]string{}, values...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
