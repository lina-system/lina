// SPDX-License-Identifier: Apache-2.0

package run

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	xterm "github.com/charmbracelet/x/term"
	"github.com/muesli/cancelreader"
	"github.com/thisisjun786/lina/internal/tui/spike"
	"github.com/thisisjun786/lina/internal/tui/term"
)

var scenarioOption = flag.String("scenario", "", "opt-in terminal scenario S1 through S11")
var outputOption = flag.String("evidence", "", "new evidence JSON path; never overwritten")
var terminalOption = flag.String("terminal", "", "observed terminal product name")
var versionOption = flag.String("terminal-version", "", "observed terminal product version")
var transportOption = flag.String("transport", "", "local or ssh")
var revisionOption = flag.String("revision", "", "40-character source commit used to build this runner")
var confirmedOption = flag.Bool("confirmed", false, "owner has confirmed the terminal matrix for this run")
var checkOption = flag.String("check-evidence", "", "validate a scenario JSON file or a directory of scenario JSON files")

func TestMain(m *testing.M) {
	flag.Parse()
	if *checkOption != "" {
		count, err := checkEvidence(*checkOption)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("Validated %d scenario record(s); observations and scenario coverage still require inspection.\n", count)
		return
	}
	if *scenarioOption == "" {
		os.Exit(m.Run())
	}
	if err := terminalExperiment(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func terminalExperiment() error {
	id := *scenarioOption
	if boundsFor(id) == nil || *outputOption == "" {
		return errors.New("supply -scenario S1..S11 and a new -evidence path")
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(*revisionOption) {
		return errors.New("supply the source commit with -revision")
	}
	if _, err := os.Stat(*outputOption); err == nil {
		return errors.New("evidence already exists; choose a new filename")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	local := false
	terminal := spike.Terminal{Name: "github.com/charmbracelet/x/vt (headless)", Version: "v0.1.0", TERM: "xterm-256color", SSH: &local}
	if id != "S8" {
		if !*confirmedOption {
			return errors.New("terminal matrix is pending confirmation; the owner must confirm it before using -confirmed")
		}
		if *terminalOption == "" || *versionOption == "" || os.Getenv("TERM") == "" || (*transportOption != "local" && *transportOption != "ssh") {
			return errors.New("terminal name, actual version, TERM and explicit local/ssh transport are required")
		}
		if strings.Contains(strings.ToUpper(*versionOption), "REPLACE") || strings.EqualFold(*versionOption, "unknown") || *terminalOption == "installed default terminal" {
			return errors.New("replace proposed metadata with the observed terminal product and version")
		}
		if !xterm.IsTerminal(os.Stdin.Fd()) || !xterm.IsTerminal(os.Stdout.Fd()) {
			return errors.New("this scenario needs a real input/output terminal; pipes cannot establish terminal evidence")
		}
		ssh := *transportOption == "ssh"
		terminal = spike.Terminal{Name: *terminalOption, Version: *versionOption, TERM: os.Getenv("TERM"), SSH: &ssh}
	}
	if err := checkPins(); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 10*time.Minute)
	defer timeout()
	questions := observations{input: bufio.NewReader(os.Stdin), output: os.Stdout}
	if id != "S8" {
		reader, err := cancelreader.NewReader(os.Stdin)
		if err != nil {
			return err
		}
		questions.input = bufio.NewReader(reader)
		canceled := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { reader.Cancel(); close(canceled) })
		defer func() {
			if !stop() {
				<-canceled
			}
			reader.Close()
		}()
	}
	fmt.Printf("%s: synthetic test data only. Ctrl+D finishes interactive screens; Ctrl+C cancels without evidence.\n", id)
	values, err := runScenario(ctx, id, &questions)
	if err != nil {
		return err
	}
	// Normalize numbers just as the JSON reader does before evaluating bounds.
	data, err := json.Marshal(values)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	result, err := verdict(id, values)
	if err != nil {
		return err
	}
	values["source_revision"] = *revisionOption
	values["platform"] = runtime.GOOS + "/" + runtime.GOARCH
	values["measured_at_utc"] = time.Now().UTC().Format(time.RFC3339Nano)
	values["bounds_reference"] = "https://github.com/thisisjun786/lina/issues/87#issuecomment-6307893344"
	if id != "S8" {
		values["owner_confirmed_matrix"] = true
	}
	record := spike.Record{Format: 1, Scenario: id, Terminal: terminal, Libraries: fixturePins(), Bounds: rawMetrics(boundsFor(id)), Measured: rawMetrics(values), Result: result}
	if err := saveRecord(*outputOption, record); err != nil {
		return err
	}
	fmt.Printf("%s: %s; wrote %s\n", id, result, *outputOption)
	if result != "pass" {
		fmt.Println("Keep this record. A miss selects a fallback; do not increase the bound.")
	}
	return nil
}

func checkPins() error {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		return errors.New("run from the repository root so the compiled pins can be checked against go.mod")
	}
	for library, version := range fixturePins() {
		pattern := `(?m)^\s*` + regexp.QuoteMeta(library) + `\s+` + regexp.QuoteMeta(version) + `(?:\s|$)`
		if !regexp.MustCompile(pattern).Match(data) {
			return fmt.Errorf("runner pin differs from go.mod: %s; update and rebuild the runner", library)
		}
	}
	return nil
}

type observations struct {
	input  *bufio.Reader
	output io.Writer
}

func (q *observations) line(prompt string) (string, error) {
	fmt.Fprint(q.output, prompt+": ")
	line, err := q.input.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("observation incomplete: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("empty observation; no evidence written")
	}
	return line, nil
}
func (q *observations) yes(prompt string) (bool, error) {
	line, err := q.line(prompt + " [yes/no]")
	if err != nil {
		return false, err
	}
	switch strings.ToLower(line) {
	case "yes", "y":
		return true, nil
	case "no", "n":
		return false, nil
	default:
		return false, errors.New("answer yes or no; unknown is not a passing observation")
	}
}
func (q *observations) number(prompt string) (float64, error) {
	line, err := q.line(prompt)
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(line, 64)
}

type frameMsg struct {
	text          string
	sequence, key int
}
type screen struct {
	width, height, sequence, key int
	text                         string
	cursor                       *tea.Cursor
	keyboard                     tea.KeyboardEnhancements
	onMessage                    func(*screen, tea.Msg) tea.Cmd
	onView                       func(*screen) string
	writer                       *recordingOutput
}

func (m *screen) Init() tea.Cmd { return nil }
func (m *screen) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok && key.Mod&tea.ModCtrl != 0 {
		if key.Code == 'd' {
			return m, tea.Quit
		}
		if key.Code == 'c' {
			return m, tea.Interrupt
		}
	}
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.writer != nil {
			if err := m.writer.resize(max(1, msg.Width), max(1, msg.Height)); err != nil {
				return m, tea.Interrupt
			}
		}
	case frameMsg:
		m.text, m.sequence = msg.text, msg.sequence
		if msg.key >= 0 {
			m.key = msg.key
		}
	}
	if m.onMessage != nil {
		return m, m.onMessage(m, message)
	}
	return m, nil
}
func (m *screen) View() tea.View {
	text := m.text
	if m.onView != nil {
		text = m.onView(m)
	}
	lines := strings.Split(text, "\n")
	rows := max(0, m.height-1)
	if len(lines) > rows {
		lines = lines[:rows]
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], max(1, m.width), "")
	}
	lines = append(lines, fmt.Sprintf("F%08d K%08d", m.sequence, m.key))
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	v.Cursor = m.cursor
	v.KeyboardEnhancements = m.keyboard
	return v
}

// recordingOutput reuses LN-86's recorder while also writing the actual terminal.
// Its snapshots are synchronization signals; timer expiry never proves a frame.
type recordingOutput struct {
	mu                   sync.Mutex
	recorder             *term.Recorder
	sink                 io.Writer
	width, height, bytes int
	changed              chan struct{}
	active               bool
	flush                []float64
	keyStart             map[int]time.Time
	keyLatency           []float64
	frameStart           map[int]time.Time
	frameLatency         []float64
	lastFrame, lastKey   int
	normalizeNewline     bool
}

func newOutput(sink io.Writer, width, height int) (*recordingOutput, error) {
	r, err := term.NewRecorder(width, height)
	if err != nil {
		return nil, err
	}
	return &recordingOutput{recorder: r, sink: sink, width: width, height: height, changed: make(chan struct{}), keyStart: map[int]time.Time{}, frameStart: map[int]time.Time{}, lastFrame: -1}, nil
}
func (w *recordingOutput) Fd() uintptr {
	if f, ok := w.sink.(interface{ Fd() uintptr }); ok {
		return f.Fd()
	}
	return ^uintptr(0)
}

// Preserve the terminal File interface so Bubble Tea subscribes to real resize
// signals. The wrapper owns the recorder, never the caller's standard output.
func (w *recordingOutput) Name() string {
	if file, ok := w.sink.(interface{ Name() string }); ok {
		return file.Name()
	}
	return "spike recorder"
}
func (w *recordingOutput) Read(data []byte) (int, error) {
	if reader, ok := w.sink.(io.Reader); ok {
		return reader.Read(data)
	}
	return 0, io.EOF
}
func (w *recordingOutput) Close() error { w.close(); return nil }

var _ xterm.File = (*recordingOutput)(nil)

func (w *recordingOutput) resize(width, height int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if width == w.width && height == w.height {
		return nil
	}
	r, err := term.NewRecorder(width, height)
	if err != nil {
		return err
	}
	w.recorder.Close()
	w.recorder = r
	w.width, w.height = width, height
	return nil
}
func (w *recordingOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	started := time.Now()
	originalLength := len(data)
	if w.normalizeNewline {
		data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
	}
	n, err := w.sink.Write(data)
	if n > 0 {
		if _, parseErr := w.recorder.Write(data[:n]); err == nil {
			err = parseErr
		}
	}
	w.bytes += n
	finished := time.Now()
	if w.active && n > 0 {
		w.flush = append(w.flush, float64(finished.Sub(started))/float64(time.Millisecond))
	}
	s := w.recorder.Snapshot()
	if len(s.Cells) > 0 {
		var line strings.Builder
		for _, cell := range s.Cells[len(s.Cells)-1] {
			line.WriteString(cell.Content)
		}
		var frame, key int
		if count, _ := fmt.Sscanf(line.String(), "F%08d K%08d", &frame, &key); count == 2 {
			w.lastFrame, w.lastKey = frame, key
			for id, at := range w.keyStart {
				if id <= key {
					w.keyLatency = append(w.keyLatency, float64(finished.Sub(at))/float64(time.Millisecond))
					delete(w.keyStart, id)
				}
			}
			for id, at := range w.frameStart {
				if id <= frame {
					w.frameLatency = append(w.frameLatency, float64(finished.Sub(at))/float64(time.Millisecond))
					delete(w.frameStart, id)
				}
			}
		}
	}
	close(w.changed)
	w.changed = make(chan struct{})
	if n != len(data) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return 0, err
	}
	return originalLength, err
}
func (w *recordingOutput) waitFrame(ctx context.Context, frame int) error {
	for {
		w.mu.Lock()
		seen, changed := w.lastFrame, w.changed
		w.mu.Unlock()
		if seen >= frame {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
func (w *recordingOutput) count() int { w.mu.Lock(); defer w.mu.Unlock(); return w.bytes }
func (w *recordingOutput) beginKey(id int, at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.keyStart[id] = at
}
func (w *recordingOutput) beginFrame(id int, at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frameStart[id] = at
}
func (w *recordingOutput) close() { w.mu.Lock(); defer w.mu.Unlock(); w.recorder.Close() }

type session struct {
	program       *tea.Program
	output        *recordingOutput
	done          chan struct{}
	cancel        context.CancelFunc
	err           error
	width, height int
}

func startSession(ctx context.Context, model *screen, input io.Reader) (*session, error) {
	width, height, err := xterm.GetSize(os.Stdout.Fd())
	if err != nil {
		return nil, err
	}
	if width < 40 || height < 10 {
		return nil, errors.New("use at least 40 columns and 10 rows")
	}
	output, err := newOutput(os.Stdout, width, height)
	if err != nil {
		return nil, err
	}
	model.width, model.height, model.writer = width, height, output
	if inputFile, ok := input.(interface{ Fd() uintptr }); !ok || !xterm.IsTerminal(inputFile.Fd()) {
		output.normalizeNewline = true
	}
	ctx, cancel := context.WithCancel(ctx)
	environment := []string{"TERM=" + os.Getenv("TERM"), "TERM_PROGRAM=" + os.Getenv("TERM_PROGRAM")}
	if *transportOption == "ssh" {
		environment = append(environment, "SSH_TTY=spike")
	}
	s := &session{output: output, done: make(chan struct{}), cancel: cancel, width: width, height: height}
	s.program = tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithEnvironment(environment), tea.WithWindowSize(width, height), tea.WithoutSignalHandler())
	go func() { defer close(s.done); _, s.err = s.program.Run() }()
	return s, nil
}
func (s *session) close()      { s.cancel(); <-s.done; s.output.close() }
func (s *session) quit() error { s.program.Send(tea.Quit()); <-s.done; return s.err }
func (s *session) wait() error { <-s.done; return s.err }
func delay(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(max(0, duration))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func TestObservationsRefuseUnknownAndEOF(t *testing.T) {
	for _, input := range []string{"", "\n", "maybe\n"} {
		q := observations{bufio.NewReader(strings.NewReader(input)), io.Discard}
		if _, err := q.yes("observed"); err == nil {
			t.Fatal("accepted missing observation")
		}
	}
}
func TestRunnerPins(t *testing.T) {
	data, err := os.ReadFile("../../../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for library, pin := range fixturePins() {
		if !regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(library) + `\s+` + regexp.QuoteMeta(pin) + `(?:\s|$)`).Match(data) {
			t.Fatalf("rebuild runner pins for %s", library)
		}
	}
}

func TestHarnessScenarioUsesRealRecorder(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	values, err := runHarness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(values)
	if err := json.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	if result, err := verdict("S8", values); err != nil || result != "pass" {
		t.Fatalf("%s: %v; observations=%v", result, err, values)
	}
}

func TestFrameAcknowledgementUsesParsedCells(t *testing.T) {
	w, err := newOutput(io.Discard, 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	w.beginKey(3, time.Now())
	w.beginFrame(7, time.Now())
	if _, err := w.Write([]byte("\x1b[?1049h\x1b[10;1HF00000007 K00000003")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := w.waitFrame(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if w.lastKey != 3 || len(w.keyStart) != 0 || len(w.frameStart) != 0 || len(w.keyLatency) != 1 || len(w.frameLatency) != 1 {
		t.Fatal("frame/key was not acknowledged")
	}
	if err := w.waitFrame(ctx, 8); !errors.Is(err, context.Canceled) {
		t.Fatal("unpainted frame was accepted")
	}
}
