// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/tui/term"
)

func TestApprovalViewWithoutOutputDoesNotReadTargets(t *testing.T) {
	m := longApprovalModel(t)
	m.resetCardReading()
	for range 100 {
		m.View()
		m.cardKey(term.Key{Name: "pgdown", Events: true})
	}
	m.cardKey(term.Key{Name: "1", Events: true})
	if cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true}); cmd != nil {
		t.Fatal("views never written to the terminal allowed approval")
	}
}

type rejectedFrame struct{}

func (rejectedFrame) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type frameOutputFunc func([]byte) (int, error)

func (f frameOutputFunc) Write(p []byte) (int, error) { return f(p) }

func TestApprovalFailedOutputDoesNotReadTargets(t *testing.T) {
	for _, profile := range []Profile{Monochrome, TrueColor} {
		m := cardModel(t)
		m.resetCardReading()
		d := &display{interactive: true, output: rejectedFrame{}, styles: NewStyles(Options{Profile: profile})}
		if err := d.showShell(m); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("output error = %v", err)
		}
		m.cardKey(term.Key{Name: "1", Events: true})
		if cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true}); cmd != nil {
			t.Fatal("failed output allowed approval")
		}
	}
}

func TestApprovalPlainDisplayWritesTargetsBeforeReading(t *testing.T) {
	m := cardModel(t)
	m.resetCardReading()
	var output bytes.Buffer
	d := &display{interactive: true, output: &output, styles: NewStyles(Options{Profile: Monochrome})}
	if err := d.showShell(m); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), m.content.Cards[0].Targets[0].Value) {
		t.Fatal("plain display recorded reading without writing the target")
	}
	m.cardKey(term.Key{Name: "1", Events: true})
	if cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true}); cmd == nil {
		t.Fatal("plain displayed target did not permit confirmation")
	}
}

func TestApprovalRapidPagesReachTerminalBeforeReading(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	output, err := term.NewRecorder(120, 34)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	m := longApprovalModel(t)
	d := &display{interactive: true, renderer: startTerminalRenderer(ctx, rawTerminalWriter{output}, 120, 34, TrueColor)}
	defer d.renderer.close()
	for range 100 {
		want := ansi.Strip(strings.Split(m.View().Content, "\n")[0])
		if err := d.showShell(m); err != nil {
			t.Fatal(err)
		}
		// No idle sleeps or wait-for-text: returning from display must mean the
		// approval frame is already on the output, before the next navigation.
		snapshot := output.Snapshot()
		var row strings.Builder
		for _, cell := range snapshot.Cells[0] {
			row.WriteString(cell.Content)
		}
		if strings.TrimRight(row.String(), " ") != strings.TrimRight(want, " ") {
			t.Fatalf("display returned before target page reached terminal: got %q, want %q", row.String(), want)
		}
		before := m.cardScroll
		m.cardKey(term.Key{Name: "pgdown", Events: true})
		if m.cardScroll == before {
			break
		}
	}
	m.cardKey(term.Key{Name: "1", Events: true})
	if cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true}); cmd == nil {
		t.Fatal("displayed target pages did not permit separate confirmation")
	}
	d.renderer.close()
	if strings.Contains(string(output.Bytes()), frameTitlePrefix) {
		t.Fatal("private frame acknowledgement reached the terminal")
	}
}

func TestApprovalRendererWaitsForSuccessfulWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	m := cardModel(t)
	m.resetCardReading()
	entered, release := make(chan struct{}), make(chan struct{})
	blocked := false
	output := frameOutputFunc(func(p []byte) (int, error) {
		if !blocked && strings.Contains(string(p), "Agent") {
			blocked = true
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		return len(p), nil
	})
	d := &display{interactive: true, renderer: startTerminalRenderer(ctx, output, 100, 30, TrueColor)}
	defer d.renderer.close()
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- d.showShell(m) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("approval frame never reached output")
	}
	for _, read := range m.cardRead.rows {
		if read {
			t.Error("target counted before the frame write completed")
		}
	}
	select {
	case err := <-result:
		t.Fatalf("display returned while output was blocked: %v", err)
	default:
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	m.cardKey(term.Key{Name: "1", Events: true})
	if cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true}); cmd == nil {
		t.Fatal("successful frame write did not permit confirmation")
	}
}

func TestApprovalRendererRejectsIncompleteWrites(t *testing.T) {
	for _, short := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		m := cardModel(t)
		m.resetCardReading()
		output := frameOutputFunc(func(p []byte) (int, error) {
			if strings.Contains(string(p), "Agent") {
				if short {
					return len(p) - 1, nil
				}
				return 0, io.ErrClosedPipe
			}
			return len(p), nil
		})
		d := &display{interactive: true, renderer: startTerminalRenderer(ctx, rawTerminalWriter{output}, 100, 30, TrueColor)}
		err := d.showShell(m)
		cancel()
		d.renderer.close()
		want := io.ErrClosedPipe
		if short {
			want = io.ErrShortWrite
		}
		if !errors.Is(err, want) {
			t.Fatalf("short=%t: display error=%v, want %v", short, err, want)
		}
		m.cardKey(term.Key{Name: "1", Events: true})
		if cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true}); cmd != nil {
			t.Fatal("incomplete renderer output allowed approval")
		}
	}
}

func TestApprovalStoppedRendererCannotReadTargets(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	m := cardModel(t)
	m.resetCardReading()
	d := &display{interactive: true, renderer: startTerminalRenderer(ctx, io.Discard, 100, 30, TrueColor)}
	cancel()
	d.renderer.close()
	if err := d.showShell(m); err == nil {
		t.Fatal("stopped renderer accepted an approval frame")
	}
	m.cardKey(term.Key{Name: "1", Events: true})
	if cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true}); cmd != nil {
		t.Fatal("stopped renderer allowed approval")
	}
}
