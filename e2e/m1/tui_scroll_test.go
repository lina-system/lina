// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/layout"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
)

func TestM1TUIWheelStickyConversation(t *testing.T) {
	t.Parallel()

	var lines []string
	for i := 1; i <= 80; i++ {
		lines = append(lines, fmt.Sprintf("SCROLL-%03d 합성 대화 행.", i))
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := strings.Join(lines, "\n")
		if calls.Add(1) > 1 {
			reply = "SCROLL-NEW 새 답변입니다."
		}
		emitResponse(w, reply, true)
	}))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	defer h.stopCore(core)
	client, conversation := conversationClient(t, h)
	defer client.Close()
	terminal := h.startTerminalSize([]string{"TERM=xterm-256color"}, false, true, 100, 34)
	waitScreen(t, h, terminal, design.Text(design.English, "live"))
	admittedTurn(t, h, client, conversation, "scroll-first", "Show the synthetic rows.")
	plan := layout.Plan(layout.Size{Width: 100, Height: 34}, layout.Caps{SidePane: true}, layout.Shape{ComposerRows: 1})
	activePlan := layout.Plan(layout.Size{Width: 100, Height: 34}, layout.Caps{SidePane: true}, layout.Shape{ComposerRows: 1, ActivityRows: 1})
	conversationReady := func(s tuiterm.Snapshot) bool {
		// PTY reads can split a redraw between transcript rows. The renderer
		// returns its hardware cursor to the composer after painting the view.
		return s.AltScreen && !s.CursorHidden && s.CursorY == plan.Composer.Y && s.CursorX == 2
	}
	idle := func(s tuiterm.Snapshot) bool {
		// Turn activity temporarily borrows one transcript row. Compare the
		// full idle viewport only after the TUI consumes the completed turn.
		return conversationReady(s) && !strings.Contains(screenRows(s)[activePlan.Activity.Y], design.Text(design.English, "activity.stop"))
	}
	region := func(s tuiterm.Snapshot) []string {
		return screenRows(s)[plan.Conversation.Y : plan.Conversation.Y+plan.Conversation.Height]
	}
	wait := func(label string, predicate func(tuiterm.Snapshot) bool) tuiterm.Snapshot {
		t.Helper()
		ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
		defer cancel()
		s, err := terminal.screen.Wait(ctx, func(frame tuiterm.Snapshot) bool {
			return renderedFrame(terminal, frame) && predicate(frame)
		})
		if err != nil {
			t.Fatalf("%s: %v\n%s", label, err, screenText(s))
		}
		return s
	}
	latest := wait("latest reply", func(s tuiterm.Snapshot) bool {
		return idle(s) && strings.Contains(strings.Join(region(s), "\n"), lines[79]) && strings.Contains(screenText(s), "Ctrl+O")
	})
	first := region(latest)[0]
	var index int
	if _, err := fmt.Sscanf(strings.TrimSpace(first), "SCROLL-%03d", &index); err != nil {
		t.Fatalf("unexpected first row %q: %v", first, err)
	}
	webWheel := func(wheel string) tuiterm.Snapshot {
		t.Helper()
		h.keys(terminal, "/web\r")
		wait("web settings from Core", func(s tuiterm.Snapshot) bool {
			return s.CursorHidden && strings.Contains(screenText(s), design.Text(design.English, "web.unconfigured"))
		})
		h.keys(terminal, wheel+"\x1b")
		return wait("conversation after closing web settings", func(s tuiterm.Snapshot) bool {
			return conversationReady(s) && strings.Contains(screenText(s), "SCROLL-")
		})
	}
	if got := strings.Join(region(webWheel("\x1b[<64;10;10M")), "\n"); got != strings.Join(region(latest), "\n") {
		t.Fatalf("wheel under web settings displaced latest viewport:\n%s", got)
	}
	h.keys(terminal, "\x1b[<64;10;10M")
	scrolled := wait("wheel up by three rows", func(s tuiterm.Snapshot) bool {
		return conversationReady(s) && wheelViewportReached(s, plan.Conversation.Y, index-3)
	})
	frozen := strings.Join(region(scrolled), "\n")
	t.Logf("wheel-up viewport:\n%s", frozen)
	if got := strings.Join(region(webWheel("\x1b[<65;10;10M")), "\n"); got != frozen {
		t.Fatalf("wheel under web settings displaced retained viewport:\nBEFORE\n%s\nAFTER\n%s", frozen, got)
	}
	marker := design.Text(design.English, "conversation.new_messages")
	admittedTurn(t, h, client, conversation, "scroll-next", "Send another reply.")
	arrived := wait("new messages marker", func(s tuiterm.Snapshot) bool {
		return idle(s) && strings.Contains(screenRows(s)[plan.Status.Y], marker)
	})
	if got := strings.Join(region(arrived), "\n"); got != frozen {
		t.Fatalf("new output moved viewport:\nBEFORE\n%s\nAFTER\n%s", frozen, got)
	}
	h.keys(terminal, "\x1b[F")
	end := wait("End returns to latest", func(s tuiterm.Snapshot) bool {
		return conversationReady(s) && strings.Contains(strings.Join(region(s), "\n"), "SCROLL-NEW") && !strings.Contains(screenRows(s)[plan.Status.Y], marker)
	})
	t.Logf("End viewport:\n%s", screenText(end))
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	// Process exit closes the slave, but the recorder may still be draining
	// its final output. Join that reader before inspecting mode restoration.
	select {
	case <-terminal.done:
		if terminal.err != nil {
			t.Fatal(terminal.err)
		}
	case <-h.ctx.Done():
		t.Fatal("terminal output did not finish", h.ctx.Err())
	}
	raw := string(terminal.screen.Bytes())
	for _, mode := range []string{"1000", "1006"} {
		if !strings.Contains(raw, "\x1b[?"+mode+"h") || !strings.Contains(raw, "\x1b[?"+mode+"l") {
			t.Errorf("mouse mode %s not enabled and restored", mode)
		}
	}
	for _, mode := range []string{"1002", "1003"} {
		if strings.Contains(raw, "\x1b[?"+mode+"h") {
			t.Errorf("requested mouse motion mode %s", mode)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("provider calls = %d", calls.Load())
	}
}

func wheelViewportReached(s tuiterm.Snapshot, top, index int) bool {
	// This PTY does not negotiate synchronized output. Bubble Tea hides the
	// cursor around each conversation diff; a matching first row alone can
	// still be a partial frame when the PTY splits the renderer's write.
	return !s.CursorHidden && strings.Contains(screenRows(s)[top], fmt.Sprintf("SCROLL-%03d", index))
}

func TestTUIWheelViewportWaitsForCompleteFrame(t *testing.T) {
	screen, err := tuiterm.NewRecorder(40, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer screen.Close()
	write := func(data string) {
		t.Helper()
		if _, err := screen.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	// The PTY may split one renderer write after the scroll optimization has
	// moved the first row, but before it fills a cleared row. Bubble Tea hides
	// the cursor during the diff and shows it after completing the frame.
	write("\x1b[?1049h\x1b[?25l\x1b[HSCROLL-050\x1b[3;1HSCROLL-052")
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // Bound a rejected snapshot without any sleep or scheduler race.
	ready := func(s tuiterm.Snapshot) bool { return wheelViewportReached(s, 0, 50) }
	assertPending := func() {
		t.Helper()
		if s, err := screen.Wait(ctx, ready); !errors.Is(err, context.Canceled) {
			t.Fatalf("accepted an incomplete wheel frame: hidden=%v, err=%v\n%s", s.CursorHidden, err, screenText(s))
		}
	}
	assertPending()
	write("\x1b[2;1HSCROLL-051\x1b[4;3H")
	// Also split the cursor-show sequence itself across PTY reads.
	for _, b := range []byte("\x1b[?25") {
		write(string(b))
		assertPending()
	}
	write("h")
	s, err := screen.Wait(ctx, ready)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(screenRows(s)[:3], "\n"); got != "SCROLL-050\nSCROLL-051\nSCROLL-052" {
		t.Fatalf("completed viewport: %q", got)
	}
}
