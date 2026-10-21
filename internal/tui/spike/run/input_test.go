// SPDX-License-Identifier: Apache-2.0

package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xterm "github.com/charmbracelet/x/term"
	"github.com/muesli/cancelreader"
)

type terminalInput struct {
	chunks   chan []byte
	done     chan struct{}
	stop     context.CancelFunc
	reader   cancelreader.CancelReader
	previous *xterm.State
}

func rawInput(ctx context.Context) (*terminalInput, error) {
	previous, err := xterm.MakeRaw(os.Stdin.Fd())
	if err != nil {
		return nil, err
	}
	reader, err := cancelreader.NewReader(os.Stdin)
	if err != nil {
		xterm.Restore(os.Stdin.Fd(), previous)
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	p := &terminalInput{chunks: make(chan []byte, 8), done: make(chan struct{}), stop: cancel, reader: reader, previous: previous}
	go func() {
		defer close(p.done)
		defer close(p.chunks)
		buffer := make([]byte, 4096)
		for {
			n, err := reader.Read(buffer)
			if n > 0 {
				copy := append([]byte(nil), buffer[:n]...)
				select {
				case p.chunks <- copy:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return p, nil
}
func (p *terminalInput) close() {
	p.stop()
	p.reader.Cancel()
	<-p.done
	p.reader.Close()
	xterm.Restore(os.Stdin.Fd(), p.previous)
}

var modeReport = regexp.MustCompile("\x1b\\[\\?(2026|2027);([0-4])\\$y")

func readMode(data string, mode int) (int, bool) {
	for _, match := range modeReport.FindAllStringSubmatch(data, -1) {
		number, _ := strconv.Atoi(match[1])
		if number == mode {
			setting, _ := strconv.Atoi(match[2])
			return setting, true
		}
	}
	return 0, false
}

type probeResult struct {
	elapsed   float64
	supported bool
	setting   int
	timedOut  bool
}

func probeMode(ctx context.Context, p *terminalInput, w io.Writer, mode int) (probeResult, error) {
	started := time.Now()
	if _, err := fmt.Fprintf(w, "\x1b[?%d$p", mode); err != nil {
		return probeResult{}, err
	}
	// Leave scheduling margin inside the fixed 200 ms bound. A later response is
	// quarantined by the caller, rather than extending this 180 ms deadline.
	timer := time.NewTimer(180 * time.Millisecond)
	defer timer.Stop()
	var received strings.Builder
	for {
		select {
		case <-ctx.Done():
			return probeResult{}, ctx.Err()
		case <-timer.C:
			return probeResult{elapsed: float64(time.Since(started)) / float64(time.Millisecond), timedOut: true}, nil
		case data, ok := <-p.chunks:
			if !ok {
				return probeResult{}, io.EOF
			}
			received.Write(data)
			if setting, ok := readMode(received.String(), mode); ok {
				return probeResult{elapsed: float64(time.Since(started)) / float64(time.Millisecond), supported: setting != 0, setting: setting}, nil
			}
		}
	}
}
func runProbes(ctx context.Context, q *observations) (map[string]any, error) {
	width, height, err := xterm.GetSize(os.Stdout.Fd())
	if err != nil {
		return nil, err
	}
	output, err := newOutput(os.Stdout, width, height)
	if err != nil {
		return nil, err
	}
	defer output.close()
	p, err := rawInput(ctx)
	if err != nil {
		return nil, err
	}
	values := map[string]any{"probe_deadline_ms": 180, "method": "Own DECRQM queries with a 180 ms deadline inside the 200 ms bound. OSC protocols have no write acknowledgement: their times measure completed output writes, and the owner separately observes support."}
	err = func() error {
		defer p.close()
		for _, item := range []struct {
			name string
			mode int
		}{{"sync", 2026}, {"grapheme", 2027}} {
			r, err := probeMode(ctx, p, output, item.mode)
			if err != nil {
				return err
			}
			values[item.name+"_resolve_ms"], values[item.name+"_supported"], values[item.name+"_timed_out"] = r.elapsed, r.supported, r.timedOut
		}
		for _, item := range []struct{ name, sequence string }{{"osc52", "\x1b]52;c;TElOQS1TUElLRQ==\x07"}, {"osc8", "\x1b]8;;https://example.invalid/lina-spike\x1b\\synthetic link\x1b]8;;\x1b\\\r\n"}} {
			at := time.Now()
			if _, err := io.WriteString(output, item.sequence); err != nil {
				return err
			}
			values[item.name+"_resolve_ms"] = float64(time.Since(at)) / float64(time.Millisecond)
		}
		// Keep the reader alive after logical deadlines, including over SSH.
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		late := 0
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case data, ok := <-p.chunks:
				if !ok {
					return io.EOF
				}
				late += len(modeReport.FindAll(data, -1))
			case <-timer.C:
				values["late_reports_consumed"] = late
				return nil
			}
		}
	}()
	if err != nil {
		return nil, err
	}
	values["osc52_supported"], err = q.yes("Explicitly paste into a disposable scratch area: did OSC 52 write LINA-SPIKE")
	if err != nil {
		return nil, err
	}
	values["osc8_supported"], err = q.yes("Did the synthetic link expose https://example.invalid/lina-spike (without opening it)")
	if err != nil {
		return nil, err
	}
	values["reply_leaked"], err = q.yes("Did any raw probe reply appear after terminal restoration")
	if err != nil {
		return nil, err
	}
	values["late_reply_quarantined"] = !values["reply_leaked"].(bool)
	values["late_reply_limit"] = "Observed for 500 ms after probe/OSC writes. Replies arriving after process exit are not established by this run; retain a failure if the shell later receives one."
	return values, nil
}

func admitKey(class string, release, repeat bool) bool {
	return !release && (!repeat || class == "edit")
}

type timedInput struct {
	*os.File
	mu     sync.Mutex
	escape time.Time
}

func (r *timedInput) Read(data []byte) (int, error) {
	n, err := r.File.Read(data)
	if n > 0 {
		chunk := string(data[:n])
		if chunk == "\x1b" || strings.Contains(chunk, "\x1b[27;") || strings.Contains(chunk, "\x1b[27u") {
			r.mu.Lock()
			r.escape = time.Now()
			r.mu.Unlock()
		}
	}
	return n, err
}
func (r *timedInput) escAt() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	at := r.escape
	r.escape = time.Time{}
	return at
}
func runKeys(ctx context.Context, q *observations) (map[string]any, error) {
	input := &timedInput{File: os.Stdin}
	model := &screen{keyboard: tea.KeyboardEnhancements{ReportEventTypes: true}}
	counts := map[string]int{"toggle": 0, "edit": 0, "approve": 0}
	var latency []float64
	var trace strings.Builder
	supported, plain := false, false
	model.onMessage = func(m *screen, message tea.Msg) tea.Cmd {
		if caps, ok := message.(tea.KeyboardEnhancementsMsg); ok {
			supported = supported || caps.SupportsEventTypes()
		}
		if key, ok := message.(tea.KeyPressMsg); ok && key.Mod&tea.ModCtrl != 0 && key.Code == 'n' {
			plain = true
			m.keyboard = tea.KeyboardEnhancements{}
			return nil
		}
		if key, ok := message.(tea.KeyPressMsg); ok && key.Code == tea.KeyEscape {
			if at := input.escAt(); !at.IsZero() {
				latency = append(latency, float64(time.Since(at))/float64(time.Millisecond))
			}
		}
		if message, ok := message.(tea.KeyMsg); ok {
			key := message.Key()
			class := ""
			switch key.Code {
			case 't':
				class = "toggle"
			case 'e':
				class = "edit"
			case 'a':
				class = "approve"
			}
			if class != "" {
				_, release := message.(tea.KeyReleaseMsg)
				accepted := admitKey(class, release, key.IsRepeat)
				if accepted {
					counts[class]++
				}
				fmt.Fprintf(&trace, "%s release=%t repeat=%t admitted=%t; ", class, release, key.IsRepeat, accepted)
			}
		}
		return nil
	}
	model.onView = func(m *screen) string {
		return fmt.Sprintf("Press and hold t (Toggle), e (Edit), a (Approve).\nReleases: ignored. Repeats: Edit only.\nCtrl+N switches to plain-key mode; repeat the key checks.\nThen press bare Esc five times. Ctrl+D finishes.\nEvent types supported: %t; plain mode: %t\nActions: toggle=%d edit=%d approve=%d; Esc samples=%d\n%s", supported, plain, counts["toggle"], counts["edit"], counts["approve"], len(latency), trace.String())
	}
	s, err := startSession(ctx, model, input)
	if err != nil {
		return nil, err
	}
	defer s.close()
	if err := s.wait(); err != nil {
		return nil, err
	}
	if !plain || len(latency) < 5 {
		return nil, errors.New("exercise plain-key mode and at least five bare Esc events")
	}
	correct, err := q.yes("Observed key actions followed the displayed press/repeat/release gate, including plain-key mode")
	if err != nil {
		return nil, err
	}
	return map[string]any{"press_gate_correct": correct, "event_types_supported": supported, "plain_key_mode_exercised": plain, "esc_samples": len(latency), "esc_to_action_max_ms": percentile(latency, 1), "key_events": trace.String(), "method": "Esc latency starts at the terminal Read boundary and ends at the decoded action; includes library escape-sequence waiting."}, nil
}

func runLostPaste(ctx context.Context) (map[string]any, error) {
	pump, err := rawInput(ctx)
	if err != nil {
		return nil, err
	}
	defer pump.close()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	forwardDone := make(chan struct{})
	forwardCtx, stopForward := context.WithCancel(ctx)
	defer stopForward()
	go func() {
		defer close(forwardDone)
		for {
			select {
			case <-forwardCtx.Done():
				return
			case data, ok := <-pump.chunks:
				if !ok {
					return
				}
				if _, err := writer.Write(data); err != nil {
					return
				}
			}
		}
	}()
	keyWorked := make(chan time.Time, 1)
	model := &screen{text: "Injecting a paste start without its end marker, followed by x. Observing for one second."}
	model.onMessage = func(m *screen, message tea.Msg) tea.Cmd {
		if key, ok := message.(tea.KeyPressMsg); ok && key.Code == 'x' {
			select {
			case keyWorked <- time.Now():
			default:
			}
		}
		return nil
	}
	s, err := startSession(ctx, model, reader)
	if err != nil {
		stopForward()
		writer.Close()
		<-forwardDone
		return nil, err
	}
	defer func() { stopForward(); writer.Close(); reader.Close(); <-forwardDone; s.close() }()
	if err := s.output.waitFrame(ctx, 0); err != nil {
		return nil, err
	}
	if _, err := io.WriteString(writer, "\x1b[200~synthetic truncated paste"); err != nil {
		return nil, err
	}
	started := time.Now()
	if _, err := io.WriteString(writer, "x"); err != nil {
		return nil, err
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	values := map[string]any{}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case at := <-keyWorked:
		values["later_key_worked"] = true
		values["observed_ms"] = float64(at.Sub(started)) / float64(time.Millisecond)
	case <-timer.C:
		values["later_key_worked"] = false
		values["observed_ms"] = float64(time.Since(started)) / float64(time.Millisecond)
		values["mitigation"] = "Add a bounded paste buffer and deadline/reset at the input decoder, then rerun the lost-marker case. Cancelling this spike restores the terminal but does not implement that product mitigation."
	}
	values["method"] = "Injected bytes pass through Bubble Tea's input decoder, not Program.Send(PasteMsg). The real input reader drains terminal replies throughout the experiment."
	return values, nil
}

func TestPressOnlyGate(t *testing.T) {
	for _, class := range []string{"toggle", "edit", "approve"} {
		for _, release := range []bool{false, true} {
			for _, repeat := range []bool{false, true} {
				want := !release && (!repeat || class == "edit")
				if got := admitKey(class, release, repeat); got != want {
					t.Fatalf("%s release=%t repeat=%t", class, release, repeat)
				}
			}
		}
	}
}
func TestSplitModeReports(t *testing.T) {
	data := "\x1b[?2027;2$y"
	for split := 0; split < len(data); split++ {
		if _, ok := readMode(data[:split], 2027); ok {
			t.Fatal("accepted a partial response")
		}
		if setting, ok := readMode(data[:split]+data[split:], 2027); !ok || setting != 2 {
			t.Fatal("lost a split response")
		}
	}
	if _, ok := readMode("\x1b[?2026;2$y", 2027); ok {
		t.Fatal("accepted the other probe's response")
	}
}
