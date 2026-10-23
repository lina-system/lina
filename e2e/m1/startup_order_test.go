// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Startup order is observed through the installed CLI and its client socket:
// the welcome carries the installed main id and the durable epoch, the lock
// refuses a second Core with exit 69, and a restart mints a higher epoch.
func TestM1StartupOrder(t *testing.T) {
	t.Parallel()

	binary := buildLina(t)
	bundle := makeInstallBundle(t, t.TempDir(), binary, buildCommand(t, "lina-node"))
	h, start := installConversationBundle(t, newHarness(t, binary), bundle, "")
	core := start(false)

	mainID := installedMainID(t, h.root)
	epoch := durableEpoch(t, h.root)
	c := startupClient(t, h, core)
	if c.welcome.MainID != mainID || c.welcome.Epoch != epoch {
		t.Fatalf("welcome main=%q epoch=%q, installed main=%q durable epoch=%q", c.welcome.MainID, c.welcome.Epoch, mainID, epoch)
	}
	h.inputRefused(c) // READY, rather than merely a bound socket.

	// A contender must be refused by the stable lock, without creating or
	// replacing control state for the running Core.
	second := h.command("core", "--state-root", h.root, "--socket", h.socket)
	second.start(t)
	second.wait(t, 69)
	h.inputRefused(c)

	h.stopCore(core)
	next := start(false)
	after := startupClient(t, h, next)
	previous, err := strconv.ParseUint(epoch, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	minted, err := strconv.ParseUint(after.welcome.Epoch, 10, 64)
	if err != nil || minted <= previous {
		t.Fatalf("restart epoch=%q previous=%q: %v", after.welcome.Epoch, epoch, err)
	}
	if after.welcome.MainID != mainID {
		t.Fatal("restart changed the installed main id")
	}
	if after.welcome.Epoch != durableEpoch(t, h.root) {
		t.Fatal("welcome epoch differs from the durable epoch")
	}
	h.stopCore(next)
}

// A Core that serves its socket before reconciliation refuses mutations with
// unavailable, still admits reads, and admits mutations once READY.
func TestM1RecoveringRefusesMutations(t *testing.T) {
	t.Parallel()

	binary := buildLina(t)
	bundle := makeInstallBundle(t, t.TempDir(), binary, buildCommand(t, "lina-node"))
	h, _ := installConversationBundle(t, newHarness(t, binary), bundle, "")

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	installed := h.binary
	h.binary = executable
	child := h.command("-test.run=^TestM1StartupOrderChild$")
	h.binary = installed
	child.command.Env = append(child.command.Env,
		"LINA_M1_ORDER_ROOT="+h.root, "LINA_M1_ORDER_SOCKET="+h.socket)
	child.command.Stdout = nil
	out, err := child.command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := child.command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	child.start(t)
	decoder := json.NewDecoder(out)
	var snapshot boot.Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		child.killAndWait()
		t.Fatalf("recovering boundary: %v; %s", err, &child.stderr)
	}
	if snapshot.State != boot.Recovering {
		child.killAndWait()
		t.Fatalf("served state = %q", snapshot.State)
	}
	// The minted epoch is durable before the socket serves.
	epoch := durableEpoch(t, h.root)
	if snapshot.Epoch == 0 || strconv.FormatUint(snapshot.Epoch, 10) != epoch {
		child.killAndWait()
		t.Fatalf("snapshot epoch=%d durable=%q", snapshot.Epoch, epoch)
	}
	c := startupClient(t, h, child)
	if c.welcome.MainID != installedMainID(t, h.root) || c.welcome.Epoch != epoch {
		child.killAndWait()
		t.Fatalf("welcome main=%q epoch=%q", c.welcome.MainID, c.welcome.Epoch)
	}
	input := c.call(t, protocol.KindInput, protocol.Input{})
	if input.Kind != protocol.KindError || input.EffectState != protocol.EffectRefused ||
		input.Error == nil || input.Error.Code != protocol.ErrorUnavailable {
		child.killAndWait()
		t.Fatalf("recovering input: kind=%s effect=%s error=%v", input.Kind, input.EffectState, input.Error)
	}
	// A read passes the recovery gate; this Core registers no handlers.
	history := c.call(t, protocol.KindHistoryGet, protocol.HistoryGet{})
	if history.Error == nil || history.Error.Code != protocol.ErrorCapabilityNotNegotiated {
		child.killAndWait()
		t.Fatalf("recovering read: %+v", history)
	}

	if _, err := fmt.Fprintln(in, "release"); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&snapshot); err != nil {
		child.killAndWait()
		t.Fatalf("ready boundary: %v; %s", err, &child.stderr)
	}
	if snapshot.State != boot.Running {
		child.killAndWait()
		t.Fatalf("served state = %q", snapshot.State)
	}
	ready := c.call(t, protocol.KindInput, protocol.Input{})
	if ready.Error == nil || ready.Error.Code != protocol.ErrorCapabilityNotNegotiated {
		child.killAndWait()
		t.Fatalf("ready input: %+v", ready)
	}
	if _, err := fmt.Fprintln(in, "stop"); err != nil {
		t.Fatal(err)
	}
	child.wait(t, 0)
	if _, err := os.Lstat(filepath.Join(h.root, "system/core/run.sentinel")); !os.IsNotExist(err) {
		t.Fatalf("orderly shutdown left the sentinel: %v", err)
	}
	if durableEpoch(t, h.root) != epoch {
		t.Fatal("orderly shutdown changed the epoch")
	}
}

// TestM1StartupOrderChild runs the real startup coordinator in this process so
// the parent can hold one acknowledged boundary. Only the boundary seam is
// injected; the state root, socket and services are the installed ones.
func TestM1StartupOrderChild(t *testing.T) {
	root := os.Getenv("LINA_M1_ORDER_ROOT")
	if root == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdin := bufio.NewReader(os.Stdin)
	err := boot.Run(ctx, boot.Config{
		StateRoot: root, Socket: os.Getenv("LINA_M1_ORDER_SOCKET"),
		AfterStep: func(step boot.Step, snapshot boot.Snapshot) error {
			if step != boot.Reconciled && step != boot.Ready {
				return nil
			}
			if err := json.NewEncoder(os.Stdout).Encode(snapshot); err != nil {
				return err
			}
			if step == boot.Ready {
				return nil
			}
			// Hold the acknowledged boundary until the observer releases it,
			// then stop on the next line so shutdown stays orderly.
			if _, err := stdin.ReadString('\n'); err != nil {
				return err
			}
			go func() {
				if _, err := stdin.ReadString('\n'); err == nil {
					cancel()
				}
			}()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// startupClient performs the real handshake and keeps the reply identity for
// later requests. It does not require a particular installed main id.
func startupClient(t *testing.T, h *harness, core *process) *client {
	t.Helper()
	conn := h.connect(core)
	c := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
	answer := c.call(t, protocol.KindHello, clientHello([]string{"1.0"}))
	welcome, ok := answer.Payload.(protocol.Welcome)
	if !ok || answer.Kind != protocol.KindWelcome || answer.Error != nil {
		t.Fatalf("handshake: kind=%s error=%v", answer.Kind, answer.Error)
	}
	c.welcome = welcome
	return c
}

func installedMainID(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "system", "core", "main.json"))
	if err != nil {
		t.Fatal(err)
	}
	var main struct {
		ID string `json:"main_id"`
	}
	if err := json.Unmarshal(data, &main); err != nil || strings.TrimSpace(main.ID) == "" {
		t.Fatalf("installed main id: %v", err)
	}
	return main.ID
}

func durableEpoch(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "system", "core", "epoch"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
