// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
)

// panelVerdict is one T-16 check's result. Input is false when the recording
// holds no frame, view or pass for the check to read; that is reported as a
// missing input, never as a pass.
type panelVerdict struct {
	Check  string
	Input  bool
	Passed bool
	Detail string
}

// shownCard is one card the panel drew, as an independent line reader sees it.
type shownCard struct {
	Name    string
	Elapsed string
	Now     string
	Body    string
	Row     int
	Rows    int
}

// frameFacts is a frame's panel read back into what it shows.
type frameFacts struct {
	Title      string
	Cards      []shownCard
	Fold       int
	FoldSet    bool
	More       int
	MoreSet    bool
	Needs      int
	NeedsSet   bool
	StatusRun  int
	StatusWait int
	StatusSet  bool
	GraphMark  bool
}

var (
	elapsedTail = regexp.MustCompile(`(?:^|\s)((?:\d+d \d{2}h)|(?:\d+h \d{2}m)|(?:\d+m))\s*$`)
	actionMark  = regexp.MustCompile(`^[›>]\s`)
	entryRow    = regexp.MustCompile(`^\s*(?:[›>]\s+)?(?:(\d+)\s+)?([✓●○!×?·+*ox.])\s+(.*)$`)
)

// templateRegex matches a design format, capturing each %d value in order.
func templateRegex(template string, args ...string) *regexp.Regexp {
	return templateAt(template, true, args...)
}

// templateSearch finds a design format anywhere in a row, as the status line
// and an overflow row carry it beside other text.
func templateSearch(template string, args ...string) *regexp.Regexp {
	return templateAt(template, false, args...)
}

func templateAt(template string, anchored bool, args ...string) *regexp.Regexp {
	var b strings.Builder
	if anchored {
		b.WriteString("^")
	}
	arg := 0
	for {
		i := strings.Index(template, "%")
		if i < 0 || i+1 == len(template) {
			b.WriteString(regexp.QuoteMeta(template))
			break
		}
		b.WriteString(regexp.QuoteMeta(template[:i]))
		switch template[i+1] {
		case 'd':
			b.WriteString(`(\d+)`)
		case 's':
			if arg < len(args) {
				b.WriteString(regexp.QuoteMeta(args[arg]))
				arg++
			}
		}
		template = template[i+2:]
	}
	if anchored {
		b.WriteString("$")
	}
	return regexp.MustCompile(b.String())
}

func panelEllipsis(ascii bool) string {
	if ascii {
		return "..."
	}
	return "…"
}

// panelPausedWord is the Now word the design gives a paused 일.
func panelPausedWord(language design.Language) string {
	if language == design.Korean {
		return "일시정지"
	}
	return "Paused"
}

// parsePanelFrame reads a frame back from its cells. It never trusts the
// recorder's own bookkeeping, so a wrongly rendered frame fails the same way
// whether Core or the self-test produced it. The panel's own rows run from the
// line under its title to the boundary rule above the composer; the status
// line sits on the left of the last screen row, outside that column.
func parsePanelFrame(f panelFrame, c panelCase) frameFacts {
	out := frameFacts{}
	if len(f.Panel) < 2 {
		return out
	}
	out.Title = f.Panel[1]
	done := templateRegex(design.Text(c.Language, "work.done"))
	more := templateSearch(design.Text(c.Language, "work.more"), panelEllipsis(c.ASCII))
	needs := templateSearch(design.Text(c.Language, "work.needs"))
	status := templateSearch(design.Text(c.Language, "work.count"))
	unknown := design.Text(c.Language, "unknown")
	// The panel ends one row above the composer rule.
	end := len(f.Panel) - 3
	for i := 2; i < end; i++ {
		if strings.TrimSpace(f.Panel[i]) == "" {
			end = i
			break
		}
	}
	rows := f.Panel[2:end]
	for _, row := range rows {
		if containsBetween(row, 0x2190, 0x21ff) || containsBetween(row, 0x2500, 0x257f) {
			out.GraphMark = true
		}
	}
	for y := 0; y < len(rows); y++ {
		row := strings.TrimRight(rows[y], " ")
		if row == "" {
			continue
		}
		if m := done.FindStringSubmatch(row); m != nil {
			out.Fold, _ = strconv.Atoi(m[1])
			out.FoldSet = true
			continue
		}
		if m := more.FindStringSubmatch(row); m != nil {
			out.More, _ = strconv.Atoi(m[1])
			out.MoreSet = true
			if n := needs.FindStringSubmatch(row); n != nil {
				out.Needs, _ = strconv.Atoi(n[1])
				out.NeedsSet = true
			}
			continue
		}
		if m := status.FindStringSubmatch(row); m != nil {
			out.StatusRun, _ = strconv.Atoi(m[1])
			out.StatusWait, _ = strconv.Atoi(m[2])
			out.StatusSet = true
			continue
		}
		card := shownCard{Row: y + 2, Rows: 1}
		if f.View != "compact" && y+1 < len(rows) && strings.HasPrefix(rows[y+1], "  ") && strings.TrimSpace(rows[y+1]) != "" {
			card.Rows = 2
			card.Now = strings.TrimSpace(rows[y+1])
			y++
		}
		body := actionMark.ReplaceAllString(row, "")
		if m := elapsedTail.FindStringSubmatchIndex(body); m != nil {
			card.Elapsed = body[m[2]:m[3]]
			body = strings.TrimRight(body[:m[0]], " ")
		} else if strings.HasSuffix(body, unknown) {
			card.Elapsed = unknown
			body = strings.TrimRight(strings.TrimSuffix(body, unknown), " ")
		}
		// Body is the card's content without its elapsed column, so a reader
		// cannot mistake a time for activity.
		card.Body = body
		card.Name = strings.TrimSpace(body)
		out.Cards = append(out.Cards, card)
	}
	if len(f.Rows) > 0 {
		if m := status.FindStringSubmatch(f.Rows[len(f.Rows)-1]); m != nil {
			out.StatusRun, _ = strconv.Atoi(m[1])
			out.StatusWait, _ = strconv.Atoi(m[2])
			out.StatusSet = true
		}
	}
	return out
}

func containsBetween(s string, lo, hi rune) bool {
	for _, r := range s {
		if r >= lo && r <= hi {
			return true
		}
	}
	return false
}

// panelElapsedMinutes reads the panel's elapsed format.
func panelElapsedMinutes(text string) (int64, bool) {
	var d, h, m int64
	switch {
	case strings.HasSuffix(text, "m") && !strings.Contains(text, "h"):
		if _, err := fmt.Sscanf(text, "%dm", &m); err == nil {
			return m, true
		}
	case strings.HasSuffix(text, "m"):
		if _, err := fmt.Sscanf(text, "%dh %02dm", &h, &m); err == nil {
			return h*60 + m, true
		}
	case strings.HasSuffix(text, "h"):
		if _, err := fmt.Sscanf(text, "%dd %02dh", &d, &h); err == nil {
			return d*1440 + h*60, true
		}
	}
	return 0, false
}

// panelActive is the card sequence the panel scrolls: everything but folded
// done cards.
func panelActive(c panelCase) (active []panelCard, done int) {
	for _, card := range c.Cards {
		if card.State == "done" && card.Waiting == 0 {
			done++
			continue
		}
		active = append(active, card)
	}
	return active, done
}

// panelFiltered is the sequence a frame shows: the cards in its filter, with
// done cards folded out.
func panelFiltered(c panelCase, filter string) (active []panelCard, done int) {
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
	return active, done
}

func panelMatchesFilter(card panelCard, filter string) bool {
	if filter == "" {
		return true
	}
	if card.Waiting > 0 || card.State == "needs_input" {
		return filter == "needs_input"
	}
	return card.State == filter
}

// panelViewMatches tells the two views apart by their own rows. A card view
// gives each card a second row that carries no elapsed time; the compact view
// keeps one row per card and each of those rows ends with the elapsed time.
func panelViewMatches(rows []string, view string, c *panelCase, filter string) bool {
	if active, _ := panelFiltered(*c, filter); len(active) == 0 {
		// An empty filter shows only its title, so the two views read alike.
		return true
	}
	end := min(len(rows)-3, len(rows))
	withElapsed, withoutElapsed := 0, 0
	for i := 2; i < end; i++ {
		row := strings.TrimRight(rows[i], " ")
		// A card row starts with the selection mark or its blank stand-in.
		if !strings.HasPrefix(row, "  ") && !actionMark.MatchString(row) {
			continue
		}
		if elapsedTail.MatchString(row) {
			withElapsed++
			continue
		}
		withoutElapsed++
	}
	if view == "compact" {
		return withoutElapsed == 0 && withElapsed > 0
	}
	return withoutElapsed > 0
}

func firstPanelFrame(c panelCase, label, view, filter string) (panelFrame, bool) {
	for _, f := range c.Frames {
		if f.Label == label && f.View == view && f.Filter == filter {
			return f, true
		}
	}
	return panelFrame{}, false
}

func panelClipCells(text string, cells int) string {
	return render.Truncate(text, cells, render.Unicode{})
}

func panelShowsCard(facts frameFacts, card panelCard) bool {
	for _, shown := range facts.Cards {
		if panelCardIs(shown, card) {
			return true
		}
	}
	return false
}

func panelCardIs(shown shownCard, card panelCard) bool {
	return strings.Contains(shown.Body, panelClipCells(card.Name, 6)) || panelClipEquals(shown.Name, card.Name)
}

func panelClipEquals(shown, full string) bool {
	trimmed := strings.TrimSuffix(strings.TrimSuffix(shown, "…"), "...")
	return trimmed != "" && strings.HasPrefix(full, trimmed)
}

// evaluateWorkPanel runs E1 to E14 over one recording.
func evaluateWorkPanel(r *panelRecording) []panelVerdict {
	return []panelVerdict{
		checkVisible(r), checkTwoRows(r), checkElapsed(r), checkNowMoves(r), checkCounts(r),
		checkNoGraph(r), checkWorkers(r), checkLatency(r), checkCalm(r), checkViews(r),
		checkOrder(r), checkPause(r), checkLimit(r), checkShortage(r),
	}
}

func checkVisible(r *panelRecording) panelVerdict {
	v := panelVerdict{Check: "E1 visible"}
	if len(r.Cases) == 0 {
		v.Detail = "no recorded case"
		return v
	}
	v.Input = true
	for _, c := range r.Cases {
		frame, ok := firstPanelFrame(c, "cards", "cards", "")
		if !ok {
			v.Detail = c.Name + ": no first settled frame"
			return v
		}
		facts := parsePanelFrame(frame, c)
		// Names repeat in the corpus, so each waiting card is read at its own
		// place in Core's order; the first window starts at the top of it.
		active, _ := panelActive(c)
		for i, card := range active {
			if card.Waiting == 0 {
				continue
			}
			if i >= len(facts.Cards) || !panelCardIs(facts.Cards[i], card) {
				v.Detail = fmt.Sprintf("%s: the card that waits on the person is not in the first frame (%q)", c.Name, panelClipCells(card.Name, 6))
				return v
			}
		}
	}
	v.Passed = true
	v.Detail = fmt.Sprintf("%d cases: every card that waits on the person is in the first settled frame", len(r.Cases))
	return v
}

func checkTwoRows(r *panelRecording) panelVerdict {
	v := panelVerdict{Check: "E2 two rows"}
	v.Input = true
	seen := 0
	u := render.Unicode{}
	for _, c := range r.Cases {
		if c.N != 1 && c.N != 5 && c.N != 20 {
			continue
		}
		for _, f := range c.Frames {
			if f.View != "cards" {
				continue
			}
			facts := parsePanelFrame(f, c)
			for _, shown := range facts.Cards {
				seen++
				if shown.Rows != 2 {
					v.Detail = fmt.Sprintf("%s/%s: card %q drew %d rows", c.Name, f.Label, shown.Name, shown.Rows)
					return v
				}
				if shown.Now == "" {
					v.Detail = fmt.Sprintf("%s/%s: card %q has no Now line", c.Name, f.Label, shown.Name)
					return v
				}
				if u.Width(shown.Body) > c.Width-40 {
					v.Detail = fmt.Sprintf("%s/%s: card %q is wider than the panel", c.Name, f.Label, shown.Name)
					return v
				}
			}
		}
	}
	if seen == 0 {
		v.Input = false
		v.Detail = "no card frame at N = 1, 5 or 20"
		return v
	}
	v.Passed = true
	v.Detail = fmt.Sprintf("%d shown cards are two rows each", seen)
	return v
}

func checkElapsed(r *panelRecording) panelVerdict {
	v := panelVerdict{Check: "E3 elapsed"}
	v.Input = true
	checked := 0
	for _, c := range r.Cases {
		active, _ := panelActive(c)
		for _, f := range c.Frames {
			if f.View != "cards" || f.Filter != "" {
				continue
			}
			facts := parsePanelFrame(f, c)
			for i, shown := range facts.Cards {
				if i >= len(active) || active[i].Started.IsZero() {
					continue
				}
				want, ok := panelElapsedMinutes(panelElapsed(f.At.Sub(active[i].Started)))
				if !ok {
					continue
				}
				got, ok := panelElapsedMinutes(shown.Elapsed)
				if !ok {
					v.Detail = fmt.Sprintf("%s/%s: card %q has no elapsed time (%q)", c.Name, f.Label, shown.Name, shown.Elapsed)
					return v
				}
				// A minute boundary inside the frame cannot be told from a wrong
				// value, so one minute is tolerated.
				if got < want-1 || got > want+1 {
					v.Detail = fmt.Sprintf("%s/%s: card %q shows %s, Core's start gives %s", c.Name, f.Label, shown.Name, shown.Elapsed, panelElapsed(f.At.Sub(active[i].Started)))
					return v
				}
				checked++
			}
		}
	}
	if checked == 0 {
		v.Input = false
		v.Detail = "no card with a Core start time"
		return v
	}
	v.Passed = true
	v.Detail = fmt.Sprintf("%d elapsed values match Core's start to the minute", checked)
	return v
}

func checkNowMoves(r *panelRecording) panelVerdict {
	v := panelVerdict{Check: "E4 now moves"}
	moves := 0
	for _, c := range r.Cases {
		moves += len(c.Moves)
	}
	if moves == 0 {
		v.Detail = "no injected step transition was recorded"
		return v
	}
	v.Input = true
	for _, c := range r.Cases {
		byFrame := map[int][]panelTransition{}
		for _, move := range c.Moves {
			if move.Frame < 0 || move.Frame >= len(c.Frames) {
				v.Detail = fmt.Sprintf("%s: the transition to %q has no frame", c.Name, move.To)
				return v
			}
			byFrame[move.Frame] = append(byFrame[move.Frame], move)
		}
		// The calm rule covers the run of injected transitions, not the frames
		// a pause or resume produces on purpose.
		first, last := len(c.Frames), -1
		for i := range c.Frames {
			for _, move := range c.Moves {
				if move.Frame == i {
					first, last = min(first, i), max(last, i)
				}
			}
		}
		var previous []shownCard
		for i, f := range c.Frames {
			if f.View != "cards" || i < first || i > last {
				continue
			}
			facts := parsePanelFrame(f, c)
			moved := byFrame[i]
			for _, move := range moved {
				if !panelHasNow(facts, move.Card, move.To) {
					v.Detail = fmt.Sprintf("%s/%s: the transition to %q is not on the card", c.Name, f.Label, move.To)
					return v
				}
			}
			if len(moved) == 0 && previous != nil {
				for j, shown := range facts.Cards {
					if j < len(previous) && previous[j].Body == shown.Body && previous[j].Now != shown.Now {
						v.Detail = fmt.Sprintf("%s/%s: Now changed with no transition (%q -> %q)", c.Name, f.Label, previous[j].Now, shown.Now)
						return v
					}
				}
			}
			previous = facts.Cards
		}
	}
	v.Passed = true
	v.Detail = fmt.Sprintf("%d injected transitions appear in their next frame", moves)
	return v
}

func panelHasNow(facts frameFacts, name, now string) bool {
	for _, shown := range facts.Cards {
		if panelClipEquals(shown.Name, name) && strings.Contains(shown.Now, panelClipCells(now, 4)) {
			return true
		}
	}
	return false
}

// panelElapsedOnly reports a string that is nothing but an elapsed value, which
// is what a compact row truncated after its name would be left with.
func panelElapsedOnly(text string) bool {
	_, ok := panelElapsedMinutes(strings.TrimSpace(text))
	return ok
}

func checkCounts(r *panelRecording) panelVerdict {
	v := panelVerdict{Check: "E5 counts"}
	v.Input = true
	frames := 0
	for _, c := range r.Cases {
		for _, f := range c.Frames {
			if f.View != "cards" {
				continue
			}
			frames++
			active, done := panelFiltered(c, f.Filter)
			facts := parsePanelFrame(f, c)
			// The panel keeps Core's order, so the cards outside the window are
			// the tail of the sequence it scrolls.
			shown := min(len(facts.Cards), len(active))
			if facts.FoldSet != (done > 0) || facts.FoldSet && facts.Fold != done {
				v.Detail = fmt.Sprintf("%s/%s: fold row counts %d, Core has %d done cards", c.Name, f.Label, facts.Fold, done)
				return v
			}
			hiddenWaiting := 0
			for _, card := range active[shown:] {
				if card.Waiting > 0 || card.State == "needs_input" {
					hiddenWaiting++
				}
			}
			if shown < len(active) {
				if !facts.MoreSet || facts.More != len(active)-shown {
					v.Detail = fmt.Sprintf("%s/%s: overflow row counts %d, %d cards are hidden", c.Name, f.Label, facts.More, len(active)-shown)
					return v
				}
				if hiddenWaiting > 0 && (!facts.NeedsSet || facts.Needs != hiddenWaiting) {
					v.Detail = fmt.Sprintf("%s/%s: overflow row counts %d waiting cards, %d are hidden", c.Name, f.Label, facts.Needs, hiddenWaiting)
					return v
				}
			} else if facts.MoreSet {
				v.Detail = fmt.Sprintf("%s/%s: an overflow row appears with nothing hidden", c.Name, f.Label)
				return v
			}
			running, waiting := 0, 0
			for _, card := range c.Cards {
				if card.Waiting > 0 {
					waiting++
				} else if card.State == "in_progress" {
					running++
				}
			}
			if running+waiting > 0 {
				if !facts.StatusSet || facts.StatusRun != running || facts.StatusWait != waiting {
					v.Detail = fmt.Sprintf("%s/%s: the status line counts %d running and %d waiting, Core has %d and %d", c.Name, f.Label, facts.StatusRun, facts.StatusWait, running, waiting)
					return v
				}
			}
		}
	}
	if frames == 0 {
		v.Input = false
		v.Detail = "no card frame"
		return v
	}
	v.Passed = true
	v.Detail = fmt.Sprintf("%d frames: fold, overflow and status numbers equal the hidden and waiting cards", frames)
	return v
}

func checkNoGraph(r *panelRecording) panelVerdict {
	v := panelVerdict{Check: "E6 no graph"}
	for _, c := range r.Cases {
		for _, f := range c.Frames {
			if parsePanelFrame(f, c).GraphMark {
				v.Input = true
				v.Detail = fmt.Sprintf("%s/%s: a panel row holds an arrow or box-drawing character", c.Name, f.Label)
				return v
			}
		}
	}
	views := 0
	for _, c := range r.Cases {
		for _, view := range c.Views {
			views++
			entries := 0
			for _, row := range view.Rows {
				if containsBetween(row, 0x2190, 0x21ff) || containsBetween(row, 0x2500, 0x257f) {
					v.Input = true
					v.Detail = c.Name + ": the stage view holds an arrow or box-drawing character"
					return v
				}
				if panelEntryLine(row) {
					entries++
				}
			}
			if entries != len(view.Entries) {
				v.Input = true
				v.Detail = fmt.Sprintf("%s: %d stage rows for %d entries", c.Name, entries, len(view.Entries))
				return v
			}
			for _, row := range view.Rows {
				if panelEntryLine(row) {
					continue
				}
				// An indented row expands an entry into its steps and sessions.
				// Anything else at the margin is a fold row or a violation.
				if strings.HasPrefix(row, " ") {
					continue
				}
				if !strings.Contains(row, "…") && !strings.Contains(row, "...") && !strings.Contains(row, design.Text(c.Language, "progress.done")) {
					v.Input = true
					v.Detail = fmt.Sprintf("%s: row %q is neither a stage entry nor a fold row", c.Name, strings.TrimSpace(row))
					return v
				}
			}
			entryLines := entryRows(view.Rows)
			for i, entry := range view.Entries {
				if len(entry.Steps) < 2 || i >= len(entryLines) {
					continue
				}
				// A parallel group is one row: its first step, then the others.
				row := entryLines[i]
				if !strings.Contains(row, panelClipCells(entry.Steps[0].Name, 4)) || !strings.Contains(row, fmt.Sprint(len(entry.Steps)-1)) {
					v.Input = true
					v.Detail = fmt.Sprintf("%s: the parallel group of %q does not name its first step and %d others", c.Name, entry.Steps[0].Name, len(entry.Steps)-1)
					return v
				}
			}
		}
	}
	if views == 0 {
		v.Detail = "no opened 일 view was recorded"
		return v
	}
	v.Input = true
	v.Passed = true
	v.Detail = fmt.Sprintf("%d opened views keep one row per stage entry and no graph mark", views)
	return v
}

// Stage rows have at most the two-cell selection prefix. Deeper state
// marks belong to expanded steps; the optional action mark is presentation.
func panelEntryLine(row string) bool {
	indent := len(row) - len(strings.TrimLeft(row, " "))
	if indent > 2 || strings.HasPrefix(row, "  › ") || strings.HasPrefix(row, "  > ") {
		return false
	}
	return entryRow.MatchString(strings.TrimSpace(row))
}

func entryRows(rows []string) []string {
	out := []string{}
	for _, row := range rows {
		if panelEntryLine(row) {
			out = append(out, row)
		}
	}
	return out
}

func checkWorkers(r *panelRecording) panelVerdict {
	v := panelVerdict{Check: "E7 workers"}
	views := 0
	for _, c := range r.Cases {
		for _, view := range c.Views {
			views++
			for _, worker := range view.Workers {
				v.Input = true
				open := false
				stepInProgress := false
				for _, entry := range view.Entries {
					if entry.Number == worker.Entry && entry.Open {
						open = true
						for _, step := range entry.Steps {
							if step.Name == worker.Step && (step.State == "working" || step.State == "needs_input") {
								stepInProgress = true
							}
						}
					}
				}
				if !worker.Expanded || worker.Step == "" || !open || !stepInProgress {
					v.Detail = fmt.Sprintf("%s: worker row %q is not under an expanded step of an open entry", c.Name, worker.Text)
					return v
				}
				if !strings.Contains(strings.Join(view.Rows, "\n"), worker.Text) && worker.Text != "" {
					v.Detail = fmt.Sprintf("%s: recorded worker row %q is not in the view", c.Name, worker.Text)
					return v
				}
			}
			for _, row := range view.Rows {
				if !strings.Contains(row, "[") || !strings.Contains(row, "]") || panelEntryLine(row) {
					continue
				}
				matched := false
				for _, worker := range view.Workers {
					matched = matched || strings.Contains(row, worker.Text)
				}
				if !matched {
					v.Input = true
					v.Detail = fmt.Sprintf("%s: bracketed row %q has no recorded open expanded step", c.Name, strings.TrimSpace(row))
					return v
				}
			}
		}
	}
	if views == 0 {
		v.Detail = "no opened 일 view was recorded"
		return v
	}
	v.Input = true
	v.Passed = true
	v.Detail = "bracketed worker rows appear only under expanded steps of an open entry"
	return v
}

func checkLatency(r *panelRecording) panelVerdict {
	v := panelVerdict{Check: "E8 latency"}
	samples := []time.Duration{}
	for _, c := range r.Cases {
		if c.N != 50 {
			continue
		}
		for _, move := range c.Moves {
			if move.Frame < 0 || move.Frame >= len(c.Frames) {
				continue
			}
			frame := c.Frames[move.Frame]
			if !panelHasNow(parsePanelFrame(frame, c), move.Card, move.To) {
				v.Input = true
				v.Detail = fmt.Sprintf("%s: the transition to %q never appeared", c.Name, move.To)
				return v
			}
			samples = append(samples, frame.At.Sub(move.Commit))
		}
	}
	if len(samples) < 50 {
		v.Detail = fmt.Sprintf("%d of 50 step transitions at N = 50 were recorded", len(samples))
		return v
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p95 := samples[(len(samples)*95+99)/100-1]
	if samples[len(samples)-1] > time.Second || p95 > 500*time.Millisecond {
		v.Input = true
		v.Detail = fmt.Sprintf("commit to frame: p95 %s, max %s", p95, samples[len(samples)-1])
		return v
	}
	v.Input = true
	v.Passed = true
	v.Detail = fmt.Sprintf("%d transitions: p95 %s, max %s", len(samples), p95, samples[len(samples)-1])
	return v
}

func checkCalm(r *panelRecording) panelVerdict {
	v := panelVerdict{Check: "E9 calm"}
	for _, c := range r.Cases {
		if c.N != 50 || len(c.Moves) == 0 {
			continue
		}
		first := c.Frames[c.Moves[0].Frame]
		for _, f := range c.Frames[c.Moves[0].Frame:] {
			if f.Composer != first.Composer || f.CursorHidden != first.CursorHidden || f.Anchor != first.Anchor {
				v.Input = true
				v.Detail = fmt.Sprintf("%s/%s: the draft, focus or reading anchor changed during the transitions", c.Name, f.Label)
				return v
			}
		}
		v.Input = true
		v.Passed = true
		v.Detail = "the draft, focus and reading anchor held through the transition frames"
		return v
	}
	v.Detail = "no transition frames at N = 50"
	return v
}

func checkViews(r *panelRecording) panelVerdict {
	v := panelVerdict{Check: "E10 views"}
	v.Input = true
	frames := 0
	for _, c := range r.Cases {
		if c.N != 20 && c.N != 50 {
			continue
		}
		for _, f := range c.Frames {
			frames++
			facts := parsePanelFrame(f, c)
			expected, done := panelFiltered(c, f.Filter)
			for _, shown := range facts.Cards {
				if f.View == "compact" && shown.Rows != 1 {
					v.Detail = fmt.Sprintf("%s/%s: compact card %q drew %d rows", c.Name, f.Label, shown.Name, shown.Rows)
					return v
				}
				matched := false
				for _, card := range expected {
					if !strings.Contains(shown.Body, panelClipCells(card.Name, 6)) {
						continue
					}
					matched = true
					// The compact row carries the name and Now together; the card
					// view's Now line is E2's check.
					if f.View == "compact" {
						// The compact row holds the name, then Now. A row that
						// holds only the name has lost its activity.
						activity := strings.TrimSpace(strings.TrimPrefix(shown.Body, panelClipCells(card.Name, 6)))
						if activity == "" || strings.HasPrefix(activity, "${") || panelElapsedOnly(activity) {
							v.Detail = fmt.Sprintf("%s/%s: the compact row for %q has no Now", c.Name, f.Label, shown.Name)
							return v
						}
					}
					if shown.Elapsed == "" {
						v.Detail = fmt.Sprintf("%s/%s: the row for %q has no elapsed time", c.Name, f.Label, shown.Name)
						return v
					}
				}
				if !matched {
					v.Detail = fmt.Sprintf("%s/%s: %q is not a card in the %q filter", c.Name, f.Label, shown.Name, f.Filter)
					return v
				}
			}
			shown := min(len(facts.Cards), len(expected))
			if facts.FoldSet != (done > 0) || facts.FoldSet && facts.Fold != done {
				v.Detail = fmt.Sprintf("%s/%s: the fold row counts %d in the %q filter, %d cards are done", c.Name, f.Label, facts.Fold, f.Filter, done)
				return v
			}
			if shown < len(expected) {
				if !facts.MoreSet || facts.More != len(expected)-shown {
					v.Detail = fmt.Sprintf("%s/%s: the overflow row counts %d in the %q filter, %d are hidden", c.Name, f.Label, facts.More, f.Filter, len(expected)-shown)
					return v
				}
			} else if facts.MoreSet {
				v.Detail = fmt.Sprintf("%s/%s: an overflow row appears in the %q filter with nothing hidden", c.Name, f.Label, f.Filter)
				return v
			}
		}
	}
	if frames == 0 {
		v.Input = false
		v.Detail = "no view at N = 20 or 50"
		return v
	}
	v.Passed = true
	v.Detail = fmt.Sprintf("%d frames across both views and every filter", frames)
	return v
}

func checkOrder(r *panelRecording) panelVerdict {
	v := panelVerdict{Check: "E11 order"}
	for _, c := range r.Cases {
		for i, pass := range c.Passes {
			if pass.Command != "move" {
				continue
			}
			v.Input = true
			if pass.Moved == "" || len(pass.Rank) == 0 {
				v.Detail = c.Name + ": the pass after a move records no moved 일 or rank"
				return v
			}
			if i == 0 || rankOf(c.Passes[i-1].Rank, pass.Moved) < 0 || rankOf(pass.Rank, pass.Moved) < 0 {
				v.Detail = c.Name + ": no pass before the move ranks the moved 일"
				return v
			}
			// A move key shifts the 일 one place up or down. 일 admitted or
			// finished between the passes are not part of the move.
			before := rankWithin(c.Passes[i-1].Rank, pass.Rank)
			after := rankWithin(pass.Rank, c.Passes[i-1].Rank)
			from, to := rankOf(before, pass.Moved), rankOf(after, pass.Moved)
			if to != from-1 && to != from+1 {
				v.Detail = fmt.Sprintf("%s: the moved 일 went from rank %d to %d, not one place", c.Name, from+1, to+1)
				return v
			}
			if !slices.Equal(withoutID(before, pass.Moved), withoutID(after, pass.Moved)) {
				v.Detail = fmt.Sprintf("%s: a move changed the order of other 일", c.Name)
				return v
			}
			// Started is the dispatch order, so each start must outrank the next.
			for i := 1; i < len(pass.Started); i++ {
				if rankOf(pass.Rank, pass.Started[i-1]) > rankOf(pass.Rank, pass.Started[i]) {
					v.Detail = fmt.Sprintf("%s: pass %d started a lower-ranked 일 first", c.Name, pass.Seq)
					return v
				}
			}
			// The next frame carries the moved 일 in its new place.
			if frame, ok := panelFrameFor(c, pass.Seq); ok && cardIndexOf(c.Cards, pass.Moved) > 0 {
				facts := parsePanelFrame(frame, c)
				moved := c.Cards[cardIndexOf(c.Cards, pass.Moved)]
				if len(facts.Cards) > 0 && !panelShowsCard(facts, moved) {
					v.Detail = fmt.Sprintf("%s: the next frame does not show the moved 일", c.Name)
					return v
				}
			}
		}
	}
	if !v.Input {
		v.Detail = "no recorded scheduling pass follows a move key"
		return v
	}
	v.Passed = true
	v.Detail = "ready steps follow the new rank on the next recorded pass"
	return v
}

func cardIndexOf(cards []panelCard, id string) int {
	for i, card := range cards {
		if card.ID == id {
			return i
		}
	}
	return -1
}

func rankOf(rank []string, id string) int {
	for i, other := range rank {
		if other == id {
			return i
		}
	}
	return -1
}

// rankWithin keeps the rank's 일 that the other rank also holds, in order.
func rankWithin(rank, other []string) []string {
	out := []string{}
	for _, id := range rank {
		if contains(other, id) {
			out = append(out, id)
		}
	}
	return out
}

func withoutID(rank []string, id string) []string {
	return slices.DeleteFunc(slices.Clone(rank), func(other string) bool { return other == id })
}

func panelFrameFor(c panelCase, seq int64) (panelFrame, bool) {
	for _, f := range c.Frames {
		if f.Seq == seq {
			return f, true
		}
	}
	return panelFrame{}, false
}

func checkPause(r *panelRecording) panelVerdict {
	v := panelVerdict{Check: "E12 pause"}
	for _, c := range r.Cases {
		for _, pass := range c.Passes {
			switch pass.Command {
			case "pause", "resume", "stop", "stop-cancel":
			default:
				continue
			}
			v.Input = true
			switch pass.Command {
			case "pause":
				if len(pass.Started) != 0 {
					v.Detail = fmt.Sprintf("%s: a paused 일 dispatched on the next pass", c.Name)
					return v
				}
				frame, ok := panelFrameFor(c, pass.Seq)
				if !ok {
					v.Detail = c.Name + ": no frame recorded for the pass after the key"
					return v
				}
				if !strings.Contains(strings.Join(frame.Panel, "\n"), panelPausedWord(c.Language)) {
					v.Detail = c.Name + ": the card does not show the paused word"
					return v
				}
			case "resume":
				if len(pass.Started) == 0 {
					v.Detail = fmt.Sprintf("%s: a resumed 일 did not dispatch on the next pass", c.Name)
					return v
				}
			case "stop":
				if pass.State != "stopped" {
					v.Detail = fmt.Sprintf("%s: a confirmed stop left the 일 %q", c.Name, pass.State)
					return v
				}
			case "stop-cancel":
				if pass.State == "stopped" {
					v.Detail = fmt.Sprintf("%s: a cancelled stop still stopped the 일", c.Name)
					return v
				}
			}
		}
	}
	if !v.Input {
		v.Detail = "no recorded pass follows a pause, resume or stop key"
		return v
	}
	v.Passed = true
	v.Detail = "pause, resume and stop take effect on the next recorded pass"
	return v
}

func checkLimit(r *panelRecording) panelVerdict {
	v := panelVerdict{Check: "E13 limit"}
	limits := map[int]bool{}
	automatic := false
	for _, c := range r.Cases {
		if c.N != 20 {
			continue
		}
		for _, pass := range c.Passes {
			if pass.Command != "limit" {
				continue
			}
			v.Input = true
			if pass.Automatic {
				automatic = true
			} else {
				limits[pass.Limit] = true
			}
			// Steps already running when the limit falls finish; nothing new
			// starts past it, so only a pass that starts a step must stay within
			// the limit.
			if pass.Limit > 0 && len(pass.Started) > 0 && len(pass.Running)+len(pass.Started) > pass.Limit {
				v.Detail = fmt.Sprintf("%s: pass %d starts a step with %d running under the limit %d", c.Name, pass.Seq, len(pass.Running), pass.Limit)
				return v
			}
			for id, reason := range pass.Reasons {
				if reason != "slot" && reason != "quota" && reason != "load" {
					v.Detail = fmt.Sprintf("%s: waiting 일 %q has reason %q", c.Name, id, reason)
					return v
				}
				if _, ok := pass.Places[id]; !ok {
					v.Detail = fmt.Sprintf("%s: waiting 일 %q has no place", c.Name, id)
					return v
				}
			}
			for id, place := range pass.Places {
				for other, otherPlace := range pass.Places {
					if id != other && rankOf(pass.Rank, id) < rankOf(pass.Rank, other) && place > otherPlace {
						v.Detail = fmt.Sprintf("%s: places are not in rank order (%q %d, %q %d)", c.Name, id, place, other, otherPlace)
						return v
					}
				}
			}
		}
	}
	if !v.Input || !limits[1] || !limits[3] || !automatic {
		v.Detail = fmt.Sprintf("limits 1, 3 and automatic were not all recorded (%v, automatic %t)", limits, automatic)
		return v
	}
	v.Passed = true
	v.Detail = "limits 1, 3 and automatic hold the running count and keep each place in rank order"
	return v
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func checkShortage(r *panelRecording) panelVerdict {
	v := panelVerdict{Check: "E14 shortage"}
	for _, c := range r.Cases {
		for _, pass := range c.Passes {
			if pass.Shortage == "" {
				continue
			}
			v.Input = true
			if pass.Limit <= 0 {
				v.Detail = fmt.Sprintf("%s: the automatic limit was not lowered after %s", c.Name, pass.Shortage)
				return v
			}
			// Healthy running steps continue; the lowered limit only stops new
			// starts and holds the running count once they end.
			if len(pass.Started) > 0 || len(pass.Running) > pass.Limit && !pass.Draining {
				v.Detail = fmt.Sprintf("%s: pass %d started or ran past the lowered limit", c.Name, pass.Seq)
				return v
			}
			for id, reason := range pass.Reasons {
				if reason != pass.Shortage {
					v.Detail = fmt.Sprintf("%s: waiting 일 %q gives %q, want %q", c.Name, id, reason, pass.Shortage)
					return v
				}
				if _, ok := pass.Places[id]; !ok {
					v.Detail = fmt.Sprintf("%s: waiting 일 %q has no place", c.Name, id)
					return v
				}
			}
		}
	}
	if !v.Input {
		v.Detail = "no recorded pass follows a quota or host-report shortage"
		return v
	}
	v.Passed = true
	v.Detail = "a shortage lowers the automatic limit and keeps each card's reason and place"
	return v
}
