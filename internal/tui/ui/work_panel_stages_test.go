// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/kit/protocol"
)

// This is the first interaction of T-16 E6: opening the selected work.
// A card with an unknown plan must still open; the client must not invent stages.
func TestWorkPanelOpenStageList(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		for _, ascii := range []bool{false, true} {
			for _, width := range []int{120, 100} {
				t.Run(fmt.Sprintf("%s/ascii=%t/%dx34", language, ascii, width), func(t *testing.T) {
					cards := navigationCards(2)
					m := navigationShell(width, cards)
					m.styles = NewStyles(Options{Profile: TrueColor, Language: language, ASCII: ascii})
					m.View()
					m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
					m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
					back := "‹"
					if ascii {
						back = "<"
					}
					view := ansi.Strip(m.View().Content)
					if !strings.Contains(view, back+" "+cards[1].Name) {
						t.Fatalf("Enter did not open the selected work's stage list:\n%s", view)
					}
					if strings.Contains(view, cards[0].Name) {
						t.Fatal("opened work still shows another card")
					}
					m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
					assertSelectedWorkVisible(t, m, cards[1])
					m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
					if m.conversation.workFocused || m.content.Draft.Text != "keep 초안" {
						t.Fatal("returning to the composer lost the draft or panel focus")
					}
				})
			}
		}
	}
}

func stagePayload(t *testing.T, stages []protocol.ProgressStage) string {
	t.Helper()
	var rows []protocol.ProgressRow
	if err := json.Unmarshal([]byte(stagePanelRows), &rows); err != nil {
		t.Fatal(err)
	}
	rows[0].Line.Stages = &stages
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestWorkPanelStagesFoldAndKeepCurrent(t *testing.T) {
	stages := []protocol.ProgressStage{}
	for i := 1; i <= 22; i++ {
		number := json.Number(fmt.Sprint(i))
		stage := protocol.ProgressStage{Kind: "stage", Number: &number, Label: fmt.Sprintf("Stage-%02d", i), State: "done"}
		if i == 13 {
			stage.State = "working"
			steps := make([]protocol.ProgressStageStep, 25)
			for j := range steps {
				steps[j] = protocol.ProgressStageStep{Name: fmt.Sprintf("Step-%02d", j), State: "working"}
			}
			steps[24].State = "needs_input"
			stage.Steps = &steps
		} else if i > 13 {
			stage.State = "next"
		}
		stages = append(stages, stage)
	}
	for _, height := range []int{10, 29} {
		m := stagePanelShell(t, 120, design.English, false, stagePayload(t, stages))
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		rows := m.workPanelRows(40, height)
		text := strings.Join(rows, "\n")
		for _, keep := range []string{"Stage-12", "Stage-13", "Stage-14", "Step-24"} {
			if !strings.Contains(text, keep) {
				t.Fatalf("height %d lost %q: %q", height, keep, rows)
			}
		}
		if len(rows) != height || strings.Contains(text, "Stage-01") || strings.Contains(text, "Stage-22") {
			t.Fatalf("folding at %d rows: %q", height, rows)
		}
		shown := strings.Count(text, "Step-")
		for _, want := range []string{
			fmt.Sprintf(design.Text(design.English, "work.done"), 11),
			fmt.Sprintf(design.Text(design.English, "work.more"), "…", 8),
			"    " + fmt.Sprintf(design.Text(design.English, "work.more"), "…", 25-shown),
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("incorrect hidden count, want %q: %q", want, rows)
			}
		}
		// Folded entries remain reachable; the current entry stays visible.
		m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		for i := 0; i < 9; i++ {
			m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		}
		rows = m.workPanelRows(40, height)
		if !strings.Contains(strings.Join(rows, "\n"), "Stage-13") || !strings.Contains(strings.Join(rows, "\n"), "› 22") {
			t.Fatalf("current or selected entry folded: %q", rows)
		}
	}
}

func TestWorkPanelStagesSpecialAndHostileText(t *testing.T) {
	for _, kind := range []string{"design", "checking", "done"} {
		m := stagePanelShell(t, 100, design.English, true, stagePayload(t, []protocol.ProgressStage{{Kind: kind, Label: "Check\nresult→│", State: "next"}}))
		rows := m.workPanelRows(40, 10)
		if len(rows) != 2 || rows[1] != "> . Check result" {
			t.Fatalf("special entry invented a number or leaked drawing/control characters: %q", rows)
		}
	}
}

func TestWorkPanelStagesRemovalAndReordering(t *testing.T) {
	m := stagePanelShell(t, 120, design.English, false, stagePanelRows)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	var rows []protocol.ProgressRow
	if err := json.Unmarshal([]byte(stagePanelRows), &rows); err != nil {
		t.Fatal(err)
	}
	// Another active entry can precede the selection; follow Core's stable number.
	stages := *rows[0].Line.Stages
	number := json.Number("7")
	stages = append([]protocol.ProgressStage{{Kind: "stage", Number: &number, Label: "Added entry", State: "working"}}, stages...)
	m.Update(shellData{Generation: 1, Message: workPanelFrame(t, 2, stagePayload(t, stages))})
	if got := strings.Join(m.workPanelRows(40, 29), "\n"); !strings.Contains(got, "› 2 ●") || !strings.Contains(got, "Login screen") {
		t.Fatalf("replacement lost stage selection/expansion: %s", got)
	}
	m.Update(shellData{Generation: 1, Message: workPanelFrame(t, 3, "[]")})
	if got := strings.Join(m.workPanelRows(40, 29), "\n"); strings.Contains(got, "Login screen") || strings.Contains(got, "Login repair") {
		t.Fatalf("removed work retained detail: %s", got)
	}
	if !m.conversation.workFocused || m.content.Draft.Text != "keep 초안" {
		t.Fatal("removal moved focus or lost the draft")
	}
}

// These are display entries supplied by Core, not a client graph fixture.
const stagePanelRows = `[{"ref":"job","line":{"name":"Login repair","kind":"project","stage":"Updating login","just_finished":"unknown","next":"unknown","waiting":0,"depth":0,"state":"in_progress","stages":[
{"kind":"stage","number":1,"label":"Investigate","state":"done","steps":[{"name":"Hidden finished step","state":"done"}]},
{"kind":"stage","number":2,"label":"Updating login and 1 more","state":"working","steps":[{"name":"Login screen","state":"working"},{"name":"Signup screen","state":"waiting"}]},
{"kind":"stage","number":3,"label":"Integrate","state":"next","steps":[{"name":"Hidden future step","state":"next"}]},
{"kind":"checking","label":"Check result","state":"next"}
]}}]`

func stagePanelShell(t *testing.T, width int, language design.Language, ascii bool, rows string) *shellModel {
	t.Helper()
	m := conversationShell()
	m.Update(tea.WindowSizeMsg{Width: width, Height: 34})
	m.styles = NewStyles(Options{Profile: TrueColor, Language: language, ASCII: ascii})
	m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "progress")})
	m.Update(shellData{Generation: 1, Message: workPanelFrame(t, 1, rows)})
	m.Update(tea.PasteMsg{Content: "keep 초안"})
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return m
}

func TestWorkPanelStagesFromCore(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		for _, ascii := range []bool{false, true} {
			for _, width := range []int{120, 100} {
				t.Run(fmt.Sprintf("%s/ascii=%t/%dx34", language, ascii, width), func(t *testing.T) {
					m := stagePanelShell(t, width, language, ascii, stagePanelRows)
					rows := m.workPanelRows(40, 29)
					if len(rows) != 5 {
						t.Fatalf("E6: want title and four Core entries, got %q", rows)
					}
					for i, label := range []string{"Investigate", "Updating login and 1 more", "Integrate", "Check result"} {
						if !strings.HasSuffix(rows[i+1], label) {
							t.Fatalf("Core entry %d changed: %q", i, rows)
						}
					}
					// Opening selects the current stage; only an explicit Enter expands it.
					m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
					view := ansi.Strip(m.View().Content)
					for _, label := range []string{"Login screen", "Signup screen"} {
						if !strings.Contains(view, label) {
							t.Fatalf("open stage step missing %q:\n%s", label, view)
						}
					}
					if strings.Contains(view, "Hidden") {
						t.Fatal("done or next stage exposed steps")
					}
					m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
					if len(m.workPanelRows(40, 29)) != 5 {
						t.Fatal("Esc did not collapse steps")
					}
					for _, key := range []rune{tea.KeyUp, tea.KeyEnter, tea.KeyDown, tea.KeyDown, tea.KeyEnter} {
						m.Update(tea.KeyPressMsg{Code: key})
					}
					if len(m.workPanelRows(40, 29)) != 5 || strings.Contains(ansi.Strip(m.View().Content), "Hidden") {
						t.Fatal("done or future stage expanded")
					}
					for _, row := range m.workPanelRows(40, 29) {
						if (render.Unicode{}).Width(ansi.Strip(row)) > 40 {
							t.Fatalf("stage row wraps: %q", row)
						}
						for _, ch := range row {
							if ch >= 0x2190 && ch <= 0x21ff || ch >= 0x2500 && ch <= 0x257f {
								t.Fatalf("E6 graph character %U", ch)
							}
						}
					}
				})
			}
		}
	}
}

func TestWorkPanelStagesUpdateAndDetails(t *testing.T) {
	for _, width := range []int{120, 100} {
		m := stagePanelShell(t, width, design.English, false, stagePanelRows)
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		anchor := m.conversation.viewport.anchor
		next := strings.ReplaceAll(stagePanelRows, "Login screen", "Updated screen")
		m.Update(shellData{Generation: 1, Message: workPanelFrame(t, 2, next)})
		view := ansi.Strip(m.View().Content)
		if !strings.Contains(view, "Updated screen") || strings.Contains(view, "Login screen") {
			t.Fatalf("expanded entry did not follow Core replacement:\n%s", view)
		}
		if !m.conversation.workFocused || m.content.Draft.Text != "keep 초안" || m.conversation.viewport.anchor != anchor {
			t.Fatal("stage update changed draft, focus or conversation anchor")
		}
		m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
		if !m.conversation.pager.open || !strings.Contains(ansi.Strip(m.View().Content), "Updated screen") {
			t.Fatalf("stage Details not visible at width %d", width)
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if !strings.Contains(ansi.Strip(m.View().Content), "Updated screen") {
			t.Fatal("closing Details lost expanded stage")
		}
		// A completed entry loses expansion on the next Core update.
		next = strings.ReplaceAll(next, `"state":"working"`, `"state":"done"`)
		m.Update(shellData{Generation: 1, Message: workPanelFrame(t, 3, next)})
		if strings.Contains(ansi.Strip(m.View().Content), "Updated screen") {
			t.Fatal("completed stage retained expanded steps")
		}
	}
}

func TestWorkPanelStagesWorkersAndStepIdentity(t *testing.T) {
	var rows []protocol.ProgressRow
	if err := json.Unmarshal([]byte(stagePanelRows), &rows); err != nil {
		t.Fatal(err)
	}
	stages := *rows[0].Line.Stages
	workers := []protocol.ProgressStageWorker{{Name: "Codex", State: protocol.RunningStateInProgress}}
	(*stages[1].Steps)[0].Workers = &workers
	// A malformed display update cannot put a worker under a closed entry.
	(*stages[0].Steps)[0].Workers = &workers
	refs := []protocol.ProgressStageRefs{}
	for _, entry := range stages {
		ref := protocol.ProgressStageRefs{Kind: entry.Kind, Number: entry.Number, Steps: []string{}}
		if entry.Steps != nil {
			for i := range *entry.Steps {
				ref.Steps = append(ref.Steps, fmt.Sprintf("step-%s-%d", entry.Kind, i))
			}
		}
		refs = append(refs, ref)
	}
	rows[0].StageRefs = &refs
	raw, _ := json.Marshal(rows)
	m := stagePanelShell(t, 100, design.English, false, string(raw))
	if strings.Contains(m.View().Content, "[Codex]") {
		t.Fatal("collapsed entry exposed worker")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	view := ansi.Strip(m.View().Content)
	if strings.Count(view, "[Codex]") != 1 {
		t.Fatalf("E7: live worker absent or leaked outside active step:\n%s", view)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if got := m.pagerText(); !strings.Contains(got, "Login screen") || strings.Contains(got, "Signup screen") {
		t.Fatalf("step Details used the wrong selection: %s", got)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	// Reorder and rename, keeping the opaque step references paired with Core's steps.
	steps := *stages[1].Steps
	steps[0].Name = "Renamed login"
	steps[0], steps[1] = steps[1], steps[0]
	refs[1].Steps[0], refs[1].Steps[1] = refs[1].Steps[1], refs[1].Steps[0]
	raw, _ = json.Marshal(rows)
	m.Update(shellData{Generation: 1, Message: workPanelFrame(t, 2, string(raw))})
	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if got := m.pagerText(); !strings.Contains(got, "Renamed login") || strings.Contains(got, "Signup screen") {
		t.Fatalf("step identity did not survive replacement: %s", got)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if strings.Contains(m.View().Content, "[Codex]") {
		t.Fatal("collapsed step retained worker")
	}
}
