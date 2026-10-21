// SPDX-License-Identifier: Apache-2.0

package layout

import "testing"

func TestPlanHidesEmptyAndUnavailableWorkLists(t *testing.T) {
	for _, caps := range []Caps{{}, {Running: true, Cards: true}} {
		for _, shape := range []Shape{{}, {ComposerRows: 6, Attachments: true}} {
			p := Plan(Size{80, 10}, caps, shape)
			if p.Running.Height != 0 || p.Cards.Height != 0 || p.RunningFolded || p.CardsFolded {
				t.Fatalf("empty work lists consume rows: %+v", p)
			}
		}
	}
	p := Plan(Size{80, 24}, Caps{}, Shape{RunningCount: 3, CardCount: 2, CardRows: 2})
	if p.Running.Height != 0 || p.Cards.Height != 0 || !p.RunningUnsupported || !p.CardsUnsupported {
		t.Fatalf("unavailable work lists must stay hidden with typed capability flags: %+v", p)
	}
}

func TestPlanEveryTerminalSize(t *testing.T) {
	for width := 0; width <= 240; width++ {
		for height := 0; height <= 100; height++ {
			for _, shape := range []Shape{{}, {ComposerRows: 6, Attachments: true, SidePane: true, RunningCount: 12, CardCount: 8, CardRows: 5}} {
				for _, caps := range []Caps{{}, {SidePane: true, Running: true, Cards: true}} {
					p := Plan(Size{width, height}, caps, shape)
					regions := []Rect{p.Header, p.Conversation, p.SidePane, p.Running, p.Cards, p.Attachments, p.Composer, p.Status, p.Notice}
					for i, a := range regions {
						if a.X < 0 || a.Y < 0 || a.Width < 0 || a.Height < 0 || a.X+a.Width > width || a.Y+a.Height > height {
							t.Fatalf("bounds %dx%d: %+v", width, height, p)
						}
						for _, b := range regions[i+1:] {
							if a.Width > 0 && a.Height > 0 && b.Width > 0 && b.Height > 0 && a.X < b.X+b.Width && b.X < a.X+a.Width && a.Y < b.Y+b.Height && b.Y < a.Y+a.Height {
								t.Fatalf("overlap %dx%d: %+v %+v", width, height, a, b)
							}
						}
					}
					if width >= 40 && height >= 10 {
						if p.Conversation.Height < 4 || p.Composer.Height < 1 || p.Composer.Height > 6 || p.Header.Height != 1 || p.Status.Height != 1 {
							t.Fatalf("floor %dx%d: %+v", width, height, p)
						}
						if p.RunningCount != shape.RunningCount || p.CardCount != shape.CardCount {
							t.Fatal("lost count")
						}
					} else if p.Conversation.Height != 0 || p.Composer.Height != 0 {
						t.Fatal("small terminal did not show size notice")
					}
				}
			}
		}
	}
}

func TestSheddingAndUnsupported(t *testing.T) {
	shape := Shape{ComposerRows: 6, Attachments: true, SidePane: true, RunningCount: 10, CardCount: 7, CardRows: 5}
	caps := Caps{SidePane: true, Running: true, Cards: true}
	roomy := Plan(Size{140, 40}, caps, shape)
	if roomy.SidePane.Width == 0 || roomy.RunningFolded || roomy.CardsFolded {
		t.Fatal("unnecessary shedding")
	}
	tight := Plan(Size{140, 10}, caps, shape)
	if tight.SidePane.Width != 0 || !tight.RunningFolded || !tight.CardsFolded || tight.Cards.Height != 1 {
		t.Fatal("wrong shedding")
	}
	p := Plan(Size{100, 24}, Caps{}, shape)
	if !p.RunningUnsupported || !p.CardsUnsupported || !p.SideUnsupported {
		t.Fatal("missing unsupported state")
	}
	if p.SidePane.Width != 0 {
		t.Fatal("medium terminal has side pane")
	}
}

func TestExtremeSizes(t *testing.T) {
	largest := int(^uint(0) >> 1)
	for _, size := range []Size{{-1, -1}, {largest, largest}, {40, 10}, {39, 10}, {40, 9}, {80, 10}, {119, 10}, {120, 10}} {
		p := Plan(size, Caps{true, true, true}, Shape{ComposerRows: largest, Attachments: true, SidePane: true, RunningCount: largest, CardCount: largest, CardRows: largest})
		for _, r := range []Rect{p.Header, p.Conversation, p.SidePane, p.Running, p.Cards, p.Attachments, p.Composer, p.Status, p.Notice} {
			if r.X < 0 || r.Y < 0 || r.Width < 0 || r.Height < 0 || r.Width > max(0, size.Width)-r.X || r.Height > max(0, size.Height)-r.Y {
				t.Fatalf("invalid rectangle at %+v: %+v", size, r)
			}
		}
		if size.Width >= 40 && size.Height >= 10 && p.Conversation.Height < 4 {
			t.Fatal("lost floor")
		}
	}
}
