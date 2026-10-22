// SPDX-License-Identifier: Apache-2.0

// Package layout assigns disjoint terminal rectangles without rendering or I/O.
package layout

type Size struct{ Width, Height int }
type Rect struct{ X, Y, Width, Height int }
type Tier string

const (
	TooSmall Tier = "too_small"
	Narrow   Tier = "narrow"
	Medium   Tier = "medium"
	Wide     Tier = "wide"
)

type Caps struct{ SidePane, Running, Cards bool }
type Shape struct {
	ActivityRows                      int
	ComposerRows                      int
	Attachments, SidePane             bool
	RunningCount, CardCount, CardRows int
}
type Layout struct {
	Tier                                                                                                          Tier
	Header, Conversation, SidePane, Running, Cards, Attachments, Activity, ComposerRule, Composer, Status, Notice Rect
	RunningCount, CardCount                                                                                       int
	RunningFolded, CardsFolded, CombinedWork                                                                      bool
	SideUnsupported, RunningUnsupported, CardsUnsupported                                                         bool
}

// Plan keeps counts even when a region folds or is unsupported. At usable sizes
// the transcript has at least four rows. Smaller sizes reserve a status row and
// give the remainder to a size notice. Negative dimensions are treated as zero.
func Plan(size Size, caps Caps, st Shape) Layout {
	w, h := max(0, size.Width), max(0, size.Height)
	p := Layout{Tier: TooSmall, RunningCount: max(0, st.RunningCount), CardCount: max(0, st.CardCount),
		SideUnsupported: st.SidePane && !caps.SidePane, RunningUnsupported: !caps.Running, CardsUnsupported: !caps.Cards}
	if w == 0 || h == 0 {
		return p
	}
	p.Status = Rect{0, h - 1, w, 1}
	if w < 40 || h < 10 {
		if h > 1 {
			p.Notice = Rect{0, 0, w, h - 1}
		}
		return p
	}
	p.Tier = Narrow
	if w >= 120 {
		p.Tier = Wide
	} else if w >= 80 {
		p.Tier = Medium
	}
	composer := min(6, max(1, st.ComposerRows))
	activity := min(2, max(0, st.ActivityRows))
	attachment := 0
	if st.Attachments {
		attachment = 1
	}
	// Only granted, populated lists consume rows. Missing capabilities remain
	// typed flags; their one-time explanation belongs to the local status notice.
	running := 0
	if caps.Running && p.RunningCount > 0 {
		running = min(3, p.RunningCount)
	}
	cards := 0
	if caps.Cards && p.CardCount > 0 {
		cards = min(5, max(1, st.CardRows))
	}
	side := st.SidePane && caps.SidePane && p.Tier == Wide
	rule := 1
	used := func() int { return 2 + 4 + composer + attachment + running + cards + activity + rule }
	if used() > h {
		side = false
	}
	if used() > h && running > 1 {
		running = 1
		p.RunningFolded = true
	}
	if used() > h && cards > 1 {
		cards = 1
		p.CardsFolded = true
	}
	if used() > h {
		rule = 0
	}
	if used() > h {
		composer = max(1, composer-(used()-h))
	}
	if used() > h {
		attachment = 0
	}
	if used() > h && running > 0 && cards > 0 {
		// At the ten-row minimum a wrapped activity and the transcript floor
		// leave one row for both lists. Preserve both counts in that summary.
		cards = 0
		p.CombinedWork, p.RunningFolded, p.CardsFolded = true, true, true
	}

	y := 0
	take := func(rows int) Rect {
		if rows == 0 {
			return Rect{}
		}
		r := Rect{0, y, w, rows}
		y += rows
		return r
	}
	p.Header = take(1)
	p.Conversation = take(h - 2 - composer - attachment - running - cards - activity - rule)
	if side {
		sideWidth := w / 3
		p.Conversation.Width = w - sideWidth
		p.SidePane = Rect{w - sideWidth, p.Conversation.Y, sideWidth, p.Conversation.Height}
	}
	p.Running = take(running)
	p.Cards = take(cards)
	p.Attachments = take(attachment)
	p.Activity = take(activity)
	p.ComposerRule = take(rule)
	p.Composer = take(composer)
	return p
}
