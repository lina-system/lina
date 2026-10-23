// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIConversationResize(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("tui-resize.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Sizes               []struct{ Width, Height int }
		Input, Reply, Draft string
		NextInput           string `json:"next_input"`
		NextReply           string `json:"next_reply"`
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		input, reply := scenario.Input, scenario.Reply
		if calls.Add(1) == 2 {
			input, reply = scenario.NextInput, scenario.NextReply
		}
		var body struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(string(mustJSON(t, body.Input)), input) {
			t.Errorf("provider did not receive %q", input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}}\n\n", reply)
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	observer, conversation := conversationClient(t, h)
	defer observer.Close()
	waitTurn := func() {
		for {
			frame, err := observer.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if turn, ok := frame.Payload.(protocol.TurnState); ok && turn.Active != nil && !*turn.Active {
				return
			}
		}
	}
	environment := []string{"TERM=xterm-256color", "COLORTERM=truecolor"}
	size := scenario.Sizes[0]
	socket := h.socket
	tap := newAccountTap(t, h, socket)
	h.socket = tap.socket
	session := h.startTerminalSize(environment, false, true, size.Width, size.Height)
	h.socket = socket
	waitScreen(t, h, session, design.Text(design.English, "live"))
	// The connection screen can precede conversation discovery. As in the
	// history-order scenario, wait for the TUI's subscription before typing.
	for {
		frame := tap.next(t, h)
		if snapshot, ok := frame.Payload.(protocol.Snapshot); ok && snapshot.Topic == "conversation:"+conversation {
			break
		}
	}
	h.keys(session, scenario.Input+"\r")
	waitTurn()
	waitScreen(t, h, session, "REPLY-340")
	// Compare clients with the same restored display state. A live turn's
	// completion duration belongs only to that client, not persisted history.
	h.keys(session, "\x04")
	session.process.wait(t, 0)
	h.expectTerminalRestored(session)
	session = h.startTerminalSize(environment, false, true, size.Width, size.Height)
	waitScreen(t, h, session, "REPLY-340")
	h.keys(session, scenario.Draft)
	initial := waitResizeChrome(t, h, session, size.Height, scenario.Draft)
	header := screenRows(initial)[0]
	if header == "" || !strings.Contains(screenText(initial), "ROW-01") {
		t.Fatal("initial header or transcript missing")
	}
	for _, size := range scenario.Sizes[1:] {
		h.resizeTerminal(session, size.Width, size.Height)
		resized := waitResizeChrome(t, h, session, size.Height, scenario.Draft)
		if screenRows(resized)[0] != header {
			t.Fatal("resize lost the header")
		}
		// A fresh client at the same dimensions is an independent oracle for
		// every row, including blank rows and transcript wrapping. Do not clear
		// the resized emulator: stale cells must be erased by the application.
		fresh := h.startTerminalSize(environment, false, true, size.Width, size.Height)
		waitScreen(t, h, fresh, "REPLY-340")
		h.keys(fresh, scenario.Draft)
		expected := waitResizeChrome(t, h, fresh, size.Height, scenario.Draft)
		waitMatchingScreen(t, h, session, expected)
		h.keys(fresh, "\x04")
		fresh.process.wait(t, 0)
		h.expectTerminalRestored(fresh)
		t.Logf("%dx%d: header, status, draft and cursor visible; all rows match a fresh client", size.Width, size.Height)
	}
	h.keys(session, "\x03"+scenario.NextInput+"\r")
	waitTurn()
	waitScreen(t, h, session, scenario.NextReply)
	size = scenario.Sizes[len(scenario.Sizes)-1]
	// Sending after the resizes must still complete through Core and restore
	// the empty composer. Activity completion metadata is checked separately.
	waitResizeChrome(t, h, session, size.Height, "")
	fresh := h.startTerminalSize(environment, false, true, size.Width, size.Height)
	waitScreen(t, h, fresh, scenario.NextReply)
	expected := waitResizeChrome(t, h, fresh, size.Height, "")
	waitMatchingScreen(t, h, session, expected)
	if calls.Load() != 2 {
		t.Fatalf("resize sent the draft or repeated a request: calls=%d", calls.Load())
	}
	for _, term := range []*terminal{session, fresh} {
		h.keys(term, "\x04")
		term.process.wait(t, 0)
		h.expectTerminalRestored(term)
	}
	observer.Close()
	h.stopCore(core)
}

func screenRows(snapshot tuiterm.Snapshot) []string {
	rows := strings.Split(strings.TrimSuffix(screenText(snapshot), "\n"), "\n")
	for i := range rows {
		rows[i] = strings.TrimRight(rows[i], " ")
	}
	return rows
}

func waitResizeChrome(t *testing.T, h *harness, term *terminal, height int, draft string) tuiterm.Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
	defer cancel()
	content := draft
	if content == "" {
		content = fmt.Sprintf(design.Text(design.English, "composer.placeholder"), "Lina")
	}
	snapshot, err := term.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
		rows := screenRows(s)
		return len(rows) == height && s.AltScreen && !s.CursorHidden &&
			s.CursorY == height-2 && strings.TrimSpace(rows[height-2]) == "› "+content &&
			s.CursorX == (render.Unicode{}).Width("› "+draft) &&
			strings.Contains(rows[height-1], design.Text(design.English, "live"))
	})
	if err != nil {
		t.Fatalf("composer/status/cursor missing at height %d: %v\n%s", height, err, strings.Join(screenRows(snapshot), "\n"))
	}
	return snapshot
}

func waitMatchingScreen(t *testing.T, h *harness, term *terminal, expected tuiterm.Snapshot) {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
	defer cancel()
	want := strings.Join(screenRows(expected), "\n")
	actual, err := term.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
		reserve := completionRowCount(s)
		return strings.Join(resizeComparableRows(s, reserve), "\n") == strings.Join(resizeComparableRows(expected, reserve), "\n") && s.CursorX == expected.CursorX &&
			s.CursorY == expected.CursorY && s.CursorHidden == expected.CursorHidden && s.AltScreen
	})
	if err != nil {
		t.Fatalf("resized screen differs from fresh client: %v\nactual:\n%s\nexpected:\n%s", err, strings.Join(screenRows(actual), "\n"), want)
	}
}

// Completion durations are local display metadata, so a fresh client has none.
// Reserve their viewport rows in both clients before comparing the visible tail.
// The header, composer rule, composer, status, wrapping and cursor still compare
// exactly. These fixtures have a one-row composer and a visible rule.
func completionRowCount(s tuiterm.Snapshot) int {
	prefix := strings.TrimSuffix(design.Text(design.English, "activity.worked_for"), "%s")
	count := 0
	for _, row := range screenRows(s) {
		if strings.HasPrefix(row, prefix) {
			count++
		}
	}
	return count
}

func resizeComparableRows(s tuiterm.Snapshot, reserve int) []string {
	rows := screenRows(s)
	prefix := strings.TrimSuffix(design.Text(design.English, "activity.worked_for"), "%s")
	var transcript []string
	for _, row := range rows[1 : len(rows)-3] {
		if !strings.HasPrefix(row, prefix) {
			transcript = append(transcript, row)
		}
	}
	for len(transcript) > 0 && transcript[len(transcript)-1] == "" {
		transcript = transcript[:len(transcript)-1]
	}
	capacity := len(rows) - 4
	if len(transcript) > capacity-reserve {
		transcript = transcript[len(transcript)-(capacity-reserve):]
	}
	for len(transcript) < capacity {
		transcript = append(transcript, "")
	}
	result := append([]string{rows[0]}, transcript...)
	return append(result, rows[len(rows)-3:]...)
}

// Exercise both a clipped transcript and a padded transcript. A local duration
// may change the visible tail, but must never move or mask composer chrome.
func TestResizeComparisonPreservesEverySharedRow(t *testing.T) {
	t.Parallel()

	for _, height := range []int{12, 35} {
		t.Run(fmt.Sprint(height), func(t *testing.T) {
			const width = 40
			makeRows := func(completed bool) []string {
				rows := make([]string, height)
				rows[0] = "header"
				transcript := []string{"input label", "input", "agent label", "reply 1", "reply 2", "reply 3", "reply 4", "reply 5", "reply 6"}
				if completed {
					transcript = append(transcript, fmt.Sprintf(design.Text(design.English, "activity.worked_for"), "2s"))
				}
				if len(transcript) > height-4 {
					transcript = transcript[len(transcript)-(height-4):]
				}
				copy(rows[1:], transcript)
				rows[height-3] = strings.Repeat(design.Dark().GlyphUnicodeRule, width)
				rows[height-2], rows[height-1] = "draft", "status"
				return rows
			}
			snapshot := func(rows []string) tuiterm.Snapshot {
				s := tuiterm.Snapshot{Cells: make([][]tuiterm.Cell, height)}
				for y, row := range rows {
					s.Cells[y] = make([]tuiterm.Cell, width)
					for x, r := range []rune(row) {
						s.Cells[y][x] = tuiterm.Cell{Content: string(r), Width: 1}
					}
				}
				return s
			}
			actualRows := makeRows(true)
			expected := snapshot(makeRows(false))
			equal := func(rows []string) bool {
				actual := snapshot(rows)
				reserve := completionRowCount(actual)
				return strings.Join(resizeComparableRows(actual, reserve), "\n") == strings.Join(resizeComparableRows(expected, reserve), "\n")
			}
			if !equal(actualRows) {
				t.Fatal("local duration changed the comparison of shared rows")
			}
			// Include every blank row and all four fixed regions. No ignored
			// row, relaxed substring match or delayed retry may hide corruption.
			for y := range actualRows {
				changed := append([]string(nil), actualRows...)
				changed[y] = "corrupt row"
				if equal(changed) {
					t.Errorf("corruption at row %d escaped comparison", y)
				}
			}
		})
	}
}

// Exercise the production Core and TUI while a provider response is held by an
// explicit gate. Resize and rendered cells, not sleeps, establish each frame.
func TestM1TUINarrowActivityResize(t *testing.T) {
	t.Parallel()

	for _, locale := range []struct {
		name     string
		language design.Language
	}{{"en_US.UTF-8", design.English}, {"ko_KR.UTF-8", design.Korean}} {
		t.Run(locale.name, func(t *testing.T) {
			release, received := make(chan struct{}), make(chan struct{}, 2)
			stop := make(chan struct{})
			defer close(stop)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				received <- struct{}{}
				select {
				case <-release:
					emitResponse(w, "NARROW-RESULT", true)
				case <-stop:
				case <-r.Context().Done():
				}
			}))
			t.Cleanup(provider.Close)
			h, start := installedConversationHarness(t, provider.URL)
			core := start(true)
			term := startReadyScreenTUI(t, h, []string{"TERM=xterm-256color", "COLORTERM=truecolor", "LC_ALL=" + locale.name}, 79, 12, nil)
			waitScreen(t, h, term, design.Text(locale.language, "live"))
			h.keys(term, "\x1b[200~NARROW-01\nNARROW-02\nNARROW-03\nNARROW-04\x1b[201~\r")
			select {
			case <-received:
			case <-h.ctx.Done():
				t.Fatal("input did not reach Core's provider")
			}
			// A full composer forces the planner to preserve the transcript floor.
			h.keys(term, "\x1b[200~draft-1\ndraft-2\ndraft-3\ndraft-4\ndraft-5\ndraft-6\x1b[201~")
			for _, size := range []struct{ width, height int }{{79, 12}, {50, 10}, {40, 10}, {79, 12}} {
				hint := design.Text(locale.language, "activity.stop")
				if size.width < 50 {
					hint = "Esc"
				}
				activity := regexp.MustCompile(`\([0-9]+s · ` + regexp.QuoteMeta(hint) + `\)`)
				h.resizeTerminal(term, size.width, size.height)
				ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
				snapshot, err := term.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
					text := screenText(s)
					rows := screenRows(s)
					if len(rows) != size.height || !activity.MatchString(text) {
						return false
					}
					// The emulator can resize before SIGWINCH reaches the TUI.
					// Wait for complete chrome, not the clipped previous frame.
					if !wholeNarrowFooter(rows[size.height-1], locale.language) {
						return false
					}
					return strings.Contains(rows[size.height-1], design.Text(locale.language, "live")) &&
						strings.Contains(rows[size.height-2], "draft-6") &&
						strings.Contains(text, design.Text(locale.language, "activity.thinking")) &&
						strings.Contains(text, "NARROW-01") && strings.Contains(text, "NARROW-04")
				})
				cancel()
				t.Logf("held turn %dx%d:\n%s", size.width, size.height, strings.Join(screenRows(snapshot), "\n"))
				if err != nil {
					t.Fatalf("activity/composer/transcript floor: %v", err)
				}
				rows := screenRows(snapshot)
				for _, marker := range []string{"NARROW-01", "NARROW-02", "NARROW-03", "NARROW-04"} {
					if !strings.Contains(screenText(snapshot), marker) {
						t.Errorf("four-row transcript floor lost %s", marker)
					}
				}

				if !activity.MatchString(screenText(snapshot)) {
					t.Errorf("complete elapsed and interrupt hint missing (want %s)", activity)
				}
				if !wholeNarrowFooter(rows[size.height-1], locale.language) {
					t.Errorf("footer cut or reordered a hint: %q", rows[size.height-1])
				}
			}
			close(release)
			waitScreen(t, h, term, "NARROW-RESULT")
			select {
			case <-received:
				t.Error("resize repeated provider request")
			default:
			}
			h.keys(term, "\x04")
			term.process.wait(t, 0)
			h.expectTerminalRestored(term)
			h.stopCore(core)
		})
	}
}

func wholeNarrowFooter(footer string, language design.Language) bool {
	parts := strings.Split(footer, "  ")
	hints := [][2]string{{"?", "shortcuts.title"}, {"Esc", "shortcuts.stop"}, {"Enter", "shortcuts.send"}, {"Ctrl+D", "action.quit"}, {"Alt+Enter", "shortcuts.newline"}, {"PgUp/PgDn", "shortcuts.scroll"}, {"Ctrl+O", "shortcuts.open_hint"}, {"F6", "shortcuts.decisions"}, {"Ctrl+T", "shortcuts.transcript"}}
	if len(parts) < 2 || len(parts)-1 > len(hints) {
		return false
	}
	for i, part := range parts[1:] {
		if part != hints[i][0]+" "+design.Text(language, hints[i][1]) {
			return false
		}
	}
	return true
}
