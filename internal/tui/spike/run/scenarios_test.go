// SPDX-License-Identifier: Apache-2.0

package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/term"
)

func runScenario(ctx context.Context, id string, q *observations) (map[string]any, error) {
	switch id {
	case "S1":
		return runIME(ctx, q)
	case "S2":
		return runStatus(ctx)
	case "S3":
		return runStreams(ctx, q)
	case "S4":
		return runResize(ctx, q)
	case "S5":
		return runProbes(ctx, q)
	case "S6":
		return runKeys(ctx, q)
	case "S7":
		return runLostPaste(ctx)
	case "S8":
		return runHarness(ctx)
	case "S9":
		return runIdle(ctx)
	case "S10":
		return runTables(ctx, q)
	case "S11":
		return runPane(ctx, q)
	default:
		return nil, errors.New("unknown scenario")
	}
}

func editor(width, height int, value string) textarea.Model {
	m := textarea.New()
	m.SetVirtualCursor(false)
	m.ShowLineNumbers = false
	m.Prompt = ""
	m.SetWidth(width)
	m.SetHeight(height)
	m.SetValue(value)
	m.Focus()
	return m
}

func runIME(ctx context.Context, q *observations) (map[string]any, error) {
	names := []string{"composed", "midline", "wrapped"}
	phase := 0
	input := editor(20, 3, "한")
	values := map[string]any{}
	model := &screen{}
	capture := func() {
		lines := strings.Split(input.Value(), "\n")
		offset := 0
		for i := 0; i < input.Line(); i++ {
			offset += len(lines[i]) + 1
		}
		runes := []rune(lines[input.Line()])
		offset += len(string(runes[:min(input.Column(), len(runes))]))
		expected := render.Cursor(input.Value(), offset, 20, render.Unicode{})
		cursor := input.Cursor()
		name := names[phase]
		values[name+"_expected_column_cells"], values[name+"_expected_row_cells"] = expected.Column, expected.Row
		values[name+"_application_column_cells"], values[name+"_application_row_cells"] = cursor.X, cursor.Y
		values[name+"_cursor_matches"] = cursor.X == expected.Column && cursor.Y == expected.Row
	}
	model.onMessage = func(m *screen, message tea.Msg) tea.Cmd {
		if key, ok := message.(tea.KeyPressMsg); ok && key.Mod&tea.ModCtrl != 0 && key.Code == 'n' {
			capture()
			if phase < 2 {
				phase++
				if phase == 1 {
					input = editor(20, 3, "한AB글")
					input.SetCursorColumn(2)
				} else {
					input = editor(20, 3, strings.Repeat("한", 9)+"A")
				}
			}
			return nil
		}
		var cmd tea.Cmd
		input, cmd = input.Update(message)
		return cmd
	}
	model.onView = func(m *screen) string {
		m.cursor = input.Cursor()
		return input.View() + "\n\n" + names[phase] + ": compose 글; inspect uncommitted IME placement.\nCtrl+N: next case. Ctrl+D: finish after wrapped case."
	}
	s, err := startSession(ctx, model, os.Stdin)
	if err != nil {
		return nil, err
	}
	defer s.close()
	if err := s.wait(); err != nil {
		return nil, err
	}
	if phase != 2 {
		return nil, errors.New("all three IME cases must be exercised")
	}
	capture()
	for _, name := range names {
		ok, err := q.yes(fmt.Sprintf("%s: physical cursor matched the expected cell (%v,%v)", name, values[name+"_expected_column_cells"], values[name+"_expected_row_cells"]))
		if err != nil {
			return nil, err
		}
		values[name+"_cursor_matches"] = values[name+"_cursor_matches"].(bool) && ok
		values[name+"_composition_in_place"], err = q.yes(name + ": uncommitted Korean composition appeared at the insertion point")
		if err != nil {
			return nil, err
		}
	}
	values["width_mode"] = "wcwidth"
	values["method"] = "Real Bubbles textarea and hardware cursor; shared measurer plus owner observations of physical cursor and uncommitted composition."
	return values, nil
}

func runStatus(ctx context.Context) (map[string]any, error) {
	content := strings.Repeat(strings.Repeat("한", 70)+"\n", 44)
	model := &screen{text: content}
	s, err := startSession(ctx, model, os.Stdin)
	if err != nil {
		return nil, err
	}
	defer s.close()
	if s.width != 140 || s.height != 45 {
		return nil, errors.New("S2 requires a real 140-column by 45-row terminal; resize it and rerun")
	}
	if err := s.output.waitFrame(ctx, 0); err != nil {
		return nil, err
	}
	values := map[string]any{"columns": 140, "rows": 45, "status_ticks": 10}
	largest := 0
	for i := 1; i <= 10; i++ {
		if err := delay(ctx, time.Second); err != nil {
			return nil, err
		}
		before := s.output.count()
		s.program.Send(frameMsg{text: content, sequence: i})
		if err := s.output.waitFrame(ctx, i); err != nil {
			return nil, err
		}
		bytes := s.output.count() - before
		values[fmt.Sprintf("tick_%d_bytes", i)] = bytes
		largest = max(largest, bytes)
	}
	if err := s.quit(); err != nil {
		return nil, err
	}
	values["max_tick_bytes"] = largest
	return values, nil
}

func runStreams(ctx context.Context, q *observations) (map[string]any, error) {
	values := map[string]any{"method": "A separate producer queues input at 250 Hz. Incremental Stream build and real terminal writes consume it; F20 key latency starts at queue arrival, including backlog. Flush time includes the recorder and terminal write. Heap is sampled after every input event and final sealed Glamour rendering; raw timing samples are retained."}
	for _, fixture := range streamFixtures() {
		model := &screen{}
		model.onMessage = func(m *screen, message tea.Msg) tea.Cmd {
			if key, ok := message.(tea.KeyPressMsg); ok && key.Code == tea.KeyF20 {
				m.key++
			}
			return nil
		}
		s, err := startSession(ctx, model, os.Stdin)
		if err != nil {
			return nil, err
		}
		err = func() error {
			defer s.close()
			if err := s.output.waitFrame(ctx, 0); err != nil {
				return err
			}
			width, height := s.width, s.height
			stream := render.NewStream("synthetic", render.Config{Width: width}, nil)
			chunk := max(1, min(256, len(fixture.text)/500))
			var build []float64
			var heap uint64
			keyID, sequence := 0, 0
			s.output.mu.Lock()
			s.output.active = true
			s.output.mu.Unlock()
			started := time.Now()
			type arrival struct {
				offset int
				at     time.Time
			}
			events := make(chan arrival, (len(fixture.text)+chunk-1)/chunk)
			producerCtx, stop := context.WithCancel(ctx)
			producerDone := make(chan struct{})
			go func() {
				defer close(producerDone)
				defer close(events)
				for offset := 0; offset < len(fixture.text); offset += chunk {
					if delay(producerCtx, time.Until(started.Add(time.Duration(offset/chunk)*4*time.Millisecond))) != nil {
						return
					}
					events <- arrival{offset, time.Now()}
				}
			}()
			defer func() { stop(); <-producerDone }()
			var arrivalTimes []float64
			for event := range events {
				offset := event.offset
				arrivalTimes = append(arrivalTimes, float64(event.at.Sub(started))/float64(time.Millisecond))
				sequence++
				at := time.Now()
				stream.Append(uint64(sequence), fixture.text[offset:min(len(fixture.text), offset+chunk)])
				var rows []string
				for i := max(0, stream.Len()-height+1); i < stream.Len(); i++ {
					rows = append(rows, stream.At(i).Text)
				}
				content := strings.Join(rows, "\n")
				build = append(build, float64(time.Since(at))/float64(time.Millisecond))
				s.program.Send(frameMsg{text: content, sequence: sequence, key: -1})
				if sequence%10 == 0 {
					keyID++
					s.output.beginKey(keyID, event.at)
					s.program.Send(tea.KeyPressMsg{Code: tea.KeyF20})
				}
				var stats runtime.MemStats
				runtime.ReadMemStats(&stats)
				heap = max(heap, stats.HeapAlloc)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			at := time.Now()
			stream.Seal(uint64(sequence+1), fixture.text)
			painter, err := glamour.NewTermRenderer(glamour.WithStandardStyle("notty"), glamour.WithWordWrap(0))
			if err != nil {
				return err
			}
			sealed, err := painter.Render(fixture.text)
			if err != nil {
				return err
			}
			lines := strings.Split(sealed, "\n")
			lines = lines[max(0, len(lines)-height+1):]
			build = append(build, float64(time.Since(at))/float64(time.Millisecond))
			sequence++
			s.program.Send(frameMsg{text: strings.Join(lines, "\n"), sequence: sequence, key: -1})
			if err := s.output.waitFrame(ctx, sequence); err != nil {
				return err
			}
			var stats runtime.MemStats
			runtime.ReadMemStats(&stats)
			heap = max(heap, stats.HeapAlloc)
			s.output.mu.Lock()
			s.output.active = false
			flush := append([]float64(nil), s.output.flush...)
			keys := append([]float64(nil), s.output.keyLatency...)
			pending := len(s.output.keyStart)
			s.output.mu.Unlock()
			if err := s.quit(); err != nil {
				return err
			}
			if pending != 0 || len(flush) < 3 || len(keys) == 0 {
				return errors.New("streaming output did not acknowledge the measured events")
			}
			p := fixture.id + "_"
			values[p+"input_bytes"] = len(fixture.text)
			values[p+"target_events_per_second"] = 250
			values[p+"elapsed_seconds"] = time.Since(started).Seconds()
			values[p+"input_events"] = sequence
			values[p+"build_p50_ms"], values[p+"build_p95_ms"] = percentile(build, .5), percentile(build, .95)
			values[p+"flush_p50_ms"], values[p+"flush_p95_ms"] = percentile(flush, .5), percentile(flush, .95)
			values[p+"peak_heap_mib"] = float64(heap) / (1024 * 1024)
			values[p+"key_to_byte_p95_ms"] = percentile(keys, .95)
			values[p+"key_samples"] = len(keys)
			values[p+"flush_samples"] = len(flush)
			for name, samples := range map[string][]float64{"build_samples_ms_json": build, "flush_samples_ms_json": flush, "key_to_byte_samples_ms_json": keys, "arrival_times_ms_json": arrivalTimes} {
				data, err := json.Marshal(samples)
				if err != nil {
					return err
				}
				values[p+name] = string(data)
			}
			third := len(flush) / 3
			values[p+"early_flush_p95_ms"], values[p+"middle_flush_p95_ms"], values[p+"late_flush_p95_ms"] = percentile(flush[:third], .95), percentile(flush[third:2*third], .95), percentile(flush[2*third:], .95)
			fmt.Printf("%s flush p95 ms: early %.3f, middle %.3f, late %.3f; build p95 %.3f\n", fixture.id, values[p+"early_flush_p95_ms"], values[p+"middle_flush_p95_ms"], values[p+"late_flush_p95_ms"], values[p+"build_p95_ms"])
			values[p+"flat_per_flush_cost"], err = q.yes("Per-flush cost stayed flat rather than growing with accumulated history")
			return err
		}()
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

type resizeReady struct {
	generation int
	at         time.Time
}

func historyItems() []*render.Stream {
	items := make([]*render.Stream, 200)
	for i := range items {
		items[i] = render.NewStream(fmt.Sprint(i), render.Config{Width: 140}, nil)
		items[i].Seal(1, fmt.Sprintf("Message %d 한글 synthetic history. %s", i, strings.Repeat("text ", 30)))
	}
	return items
}
func historyWindow(items []*render.Stream, width, rows int) string {
	var lines []string
	for i := len(items) - 1; i >= 0 && len(lines) < rows; i-- {
		for _, row := range items[i].Window(0, rows-len(lines), render.Config{Width: width}, nil) {
			lines = append(lines, row.Text)
		}
	}
	return strings.Join(lines, "\n")
}
func runResize(ctx context.Context, q *observations) (map[string]any, error) {
	fmt.Print("Tab reference before startup:\nA\tB\tC\n")
	items := historyItems()
	generation := 0
	model := &screen{text: "Resize the terminal at least three times. Compare tab stops. Ctrl+D finishes."}
	model.onMessage = func(m *screen, message tea.Msg) tea.Cmd {
		switch msg := message.(type) {
		case tea.WindowSizeMsg:
			generation++
			gen := generation
			return tea.Tick(32*time.Millisecond, func(at time.Time) tea.Msg { return resizeReady{gen, at} })
		case resizeReady:
			if msg.generation == generation {
				m.sequence++
				m.writer.beginFrame(m.sequence, msg.at)
				m.text = "A\tB\tC\n" + historyWindow(items, max(1, m.width), max(0, m.height-3))
			}
		}
		return nil
	}
	s, err := startSession(ctx, model, os.Stdin)
	if err != nil {
		return nil, err
	}
	defer s.close()
	if err := s.wait(); err != nil {
		return nil, err
	}
	fmt.Print("Tab reference after quit:\nA\tB\tC\n")
	latency := s.output.frameLatency
	if len(latency) < 3 {
		return nil, errors.New("need at least three observed resize/reflow frames")
	}
	aligned, err := q.yes("Tab columns matched the reference before, during and after the program")
	if err != nil {
		return nil, err
	}
	return map[string]any{"reflow_max_ms": percentile(latency, 1), "resize_samples": len(latency), "history_messages": 200, "tab_alignment": aligned, "method": "32 ms coalescing followed by viewport reflow; duration ends at the recorder's acknowledgement of the final terminal frame."}, nil
}

type harnessModel struct {
	input  textarea.Model
	sealed string
}

func (m *harnessModel) Init() tea.Cmd { return nil }
func (m *harnessModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(message)
	return m, cmd
}
func (m *harnessModel) View() tea.View {
	v := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, m.input.View(), m.sealed))
	v.AltScreen = true
	v.Cursor = m.input.Cursor()
	return v
}

func runHarness(ctx context.Context) (map[string]any, error) {
	painter, err := glamour.NewTermRenderer(glamour.WithStandardStyle("notty"), glamour.WithWordWrap(0))
	if err != nil {
		return nil, err
	}
	sealed, err := painter.Render("**7**\n\n")
	if err != nil {
		return nil, err
	}
	model := &harnessModel{input: editor(20, 2, "한A"), sealed: sealed}
	h, err := term.Start(ctx, model, 20, 6)
	if err != nil {
		return nil, err
	}
	defer h.Close()
	first, err := h.Output.Wait(ctx, func(s term.Snapshot) bool { return s.AltScreen && s.Cells[0][0].Content == "한" && s.CursorX == 3 })
	if err != nil {
		return nil, fmt.Errorf("initial S8 frame: %w", err)
	}
	h.Send(tea.PasteMsg{Content: "B"})
	pasted, err := h.Output.Wait(ctx, func(s term.Snapshot) bool { return s.Cells[0][3].Content == "B" && s.CursorX == 4 })
	if err != nil {
		return nil, fmt.Errorf("S8 paste frame: %w", err)
	}
	h.Send(tea.Quit())
	if _, err := h.Wait(); err != nil {
		return nil, err
	}
	last := h.Output.Snapshot()
	found := false
	for _, row := range pasted.Cells[2:] {
		for _, cell := range row {
			found = found || cell.Content == "7"
		}
	}
	return map[string]any{"runs": true, "quits": !last.AltScreen && !last.CursorHidden, "parses": first.Cells[0][0].Width == 2 && model.input.Value() == "한AB" && found, "sleep_calls": 0, "output_bytes": last.Bytes, "output_writes": last.Writes, "hangul_cells": first.Cells[0][0].Width, "cursor_after_paste_cells": 4}, nil
}

func resourceUsage() (cpu float64, rss float64, err error) {
	var r syscall.Rusage
	if err = syscall.Getrusage(syscall.RUSAGE_SELF, &r); err != nil {
		return
	}
	cpu = float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
	rss = float64(r.Maxrss)
	if runtime.GOOS != "darwin" {
		rss *= 1024
	}
	rss /= 1024 * 1024
	return
}
func runIdle(ctx context.Context) (map[string]any, error) {
	items := historyItems()
	model := &screen{}
	s, err := startSession(ctx, model, os.Stdin)
	if err != nil {
		return nil, err
	}
	defer s.close()
	s.program.Send(frameMsg{text: historyWindow(items, s.width, s.height-1), sequence: 1})
	if err := s.output.waitFrame(ctx, 1); err != nil {
		return nil, err
	}
	before := s.output.count()
	cpu0, rss, err := resourceUsage()
	if err != nil {
		return nil, err
	}
	started := time.Now()
	for i := 0; i < 15; i++ {
		if err := delay(ctx, time.Second); err != nil {
			return nil, err
		}
		_, sample, err := resourceUsage()
		if err != nil {
			return nil, err
		}
		rss = max(rss, sample)
	}
	cpu1, _, err := resourceUsage()
	if err != nil {
		return nil, err
	}
	elapsed := time.Since(started).Seconds()
	bytes := s.output.count() - before
	if err := s.quit(); err != nil {
		return nil, err
	}
	runtime.KeepAlive(items)
	return map[string]any{"cpu_percent_one_core": 100 * (cpu1 - cpu0) / elapsed, "idle_bytes": bytes, "peak_resident_mib": rss, "window_items": 200, "observation_seconds": elapsed, "method": "15-second idle interval after observed initial paint. Renderer ticker remains enabled; resident memory uses the process high-water mark, conservatively including initialization."}, nil
}
