// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
)

func TestM1TUIEpochNoticeInstalledCore(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("tui-epoch-notice.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Width, Height       int
		Input, Reply, Draft string
		Restarts            []string
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var body struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		input := scenario.Input
		if call == 2 {
			input = scenario.Draft + strings.Repeat("x", len(scenario.Restarts))
		}
		if call > 2 || !strings.Contains(string(mustJSON(t, body.Input)), input) {
			t.Error("unexpected model input or automatic replay")
		}
		reply := fmt.Sprintf("%s-%d", scenario.Reply, call)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%q}\n\n", reply)
		fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}}\n\n", reply)
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer provider.Close()

	// Exercise the release builder and install its binaries with isolated service
	// management, HOME/XDG paths and a loopback-only Responses fixture.
	output := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "node", "scripts/build.mjs", "--out", output, "--target", runtime.GOOS+"/"+runtime.GOARCH)
	build.Dir = "../.."
	for _, key := range []string{"PATH", "HOME", "GOCACHE", "GOMODCACHE", "TMPDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			build.Env = append(build.Env, key+"="+value)
		}
	}
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build release: %v\n%s", err, data)
	}
	h := newHarness(t, filepath.Join(output, "lina"))
	// Dirty development builds omit provenance; the fixture supplies only a
	// manifest with exact hashes, as in the other installed-Core scenarios.
	bundle := output
	if _, err := os.Stat(filepath.Join(bundle, "manifest.json")); os.IsNotExist(err) {
		bundle = makeInstallBundle(t, h.root, h.binary, filepath.Join(output, "lina-node"))
	}
	h, start := installConversationBundle(t, h, bundle, provider.URL)
	core := start(true)
	welcome := func() (string, uint64) {
		t.Helper()
		client, err := clientapi.Dial(h.ctx, h.socket, "epoch-test")
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		epoch, err := strconv.ParseUint(client.Welcome.Epoch, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return client.Welcome.MainID, epoch
	}
	mainID, previous := welcome()
	environment := []string{"TERM=xterm-256color", "COLORTERM=truecolor", "LC_ALL=C"}
	terminal, _ := h.startConversationTUI(scenario.Width, scenario.Height, environment...)
	live := design.Text(design.English, "live")
	notice := design.Text(design.English, "epoch_changed")
	initial := waitScreen(t, h, terminal, live)
	if strings.Contains(screenText(initial), notice) {
		t.Fatal("initial connection announced a restart")
	}
	h.keys(terminal, scenario.Input+"\r")
	waitScreen(t, h, terminal, scenario.Reply+"-1")
	h.keys(terminal, scenario.Draft)
	waitScreen(t, h, terminal, scenario.Draft)
	for index, restart := range scenario.Restarts {
		switch restart {
		case "graceful":
			h.stopCore(core)
		case "forced":
			core.killAndWait()
		default:
			t.Fatalf("unknown restart %q", restart)
		}
		waitScreen(t, h, terminal, design.Text(design.English, "retrying"))
		core = start(true)
		id, epoch := welcome()
		if id != mainID || epoch <= previous {
			t.Fatalf("restart identity/epoch: same main=%t, previous=%d current=%d", id == mainID, previous, epoch)
		}
		waitScreen(t, h, terminal, live)
		// A composer edit is an observable redraw barrier after reconnect. Do not
		// infer success or the absence of duplicate notices from a timed sleep.
		h.keys(terminal, "x")
		draft := scenario.Draft + strings.Repeat("x", index+1)
		snapshot, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
			text := screenText(s)
			return strings.Contains(text, live) && strings.Contains(text, draft)
		})
		if err != nil {
			t.Fatalf("reconnected draft not usable: %v", err)
		}
		text := screenText(snapshot)
		count := strings.Count(text, notice)
		t.Logf("%s restart %d: epoch %d -> %d; visible local notices=%d; history=%t; draft=%t", restart, index+1, previous, epoch, count, strings.Contains(text, scenario.Reply+"-1"), strings.Contains(text, draft))
		if count != 1 {
			t.Fatalf("epoch notices = %d, want 1; screen:\n%s", count, text)
		}
		if !strings.Contains(text, scenario.Input) || !strings.Contains(text, scenario.Reply+"-1") || calls.Load() != 1 {
			t.Fatal("reconnect lost history or submitted the draft")
		}
		previous = epoch
	}
	h.keys(terminal, "\r")
	final := waitScreen(t, h, terminal, scenario.Reply+"-2")
	if strings.Count(screenText(final), notice) != 1 || calls.Load() != 2 {
		t.Fatal("post-reconnect send duplicated the notice or model call")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	// A new client restores durable history, but never the old client's notice.
	fresh := h.startTerminalSize(environment, false, true, scenario.Width, scenario.Height)
	restored := waitScreen(t, h, fresh, scenario.Reply+"-2")
	if strings.Contains(screenText(restored), notice) || !strings.Contains(screenText(restored), scenario.Input) {
		t.Fatal("local notice entered durable history or history was lost")
	}
	h.keys(fresh, "\x04")
	fresh.process.wait(t, 0)
	h.expectTerminalRestored(fresh)
	h.stopCore(core)
}
