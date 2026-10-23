// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"golang.org/x/sys/unix"
)

// The installed Core dispatches a real process, consumes its existing plan,
// publishes progress and produces the result receipt. A real TUI draws both
// observations while the worker is held at a barrier, with no progress prompt.
func TestWorkerDoingThroughRunningCore(t *testing.T) {
	script := strings.Replace(acpAgentScript, "progress('Working on result.txt');", `
  if(/report your progress/i.test(packet))throw Error('progress prompt');
  const update=u=>send({method:'session/update',params:{sessionId:'acp-fixture',update:u}});
  update({sessionUpdate:'session_info_update',title:'Login'});
  update({sessionUpdate:'plan',entries:[{content:'Reading tests',status:'in_progress',priority:'medium'}]});
  progress('Working on result.txt');
  const next=setInterval(()=>{
   if(fs.existsSync(path.join(root,'next-step'))){
    clearInterval(next);
    update({sessionUpdate:'plan',entries:[{content:'Running tests',status:'in_progress',priority:'medium'}]});
   }
  },5);
 `, 1)
	runACPWorkersThroughRunningCore(t, []acpScenario{{name: "doing", worker: "gemini", binary: "gemini", mode: "success", script: script,
		beforeFinish: func(t *testing.T, h *harness, fixture string, record dispatch.Record) {
			db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close(context.Background())
			read := func(want string) work.Card {
				t.Helper()
				for {
					card, err := work.New(db).Get(h.ctx, record.ID)
					if err == nil && card.Doing != nil && card.Doing.Line == want {
						return card
					}
					select {
					case <-h.ctx.Done():
						t.Fatalf("doing never reached work card: %+v %v", card, err)
					case <-time.After(5 * time.Millisecond):
					}
				}
			}
			first := read("Login · Reading tests")
			if !first.Doing.Busy || first.Doing.Since.IsZero() || first.Receipt != nil {
				t.Fatalf("invalid live observation: %+v", first)
			}
			capture := startDoingTUI(t, h)
			capture("Login · Reading tests")
			// One new adapter event must update the durable card and live sidebar.
			if err := os.WriteFile(filepath.Join(fixture, "next-step"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			second := read("Login · Running tests")
			if !second.Doing.Since.Equal(first.Doing.Since) || second.Receipt != nil {
				t.Fatal("step change reset busy time or ended work")
			}
			capture("Login · Running tests")
		}, afterFinish: func(t *testing.T, h *harness, _ string, record dispatch.Record) {
			db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close(context.Background())
			card, err := work.New(db).Get(h.ctx, record.ID)
			if err != nil || card.Doing == nil || card.Doing.Busy || card.Receipt == nil {
				t.Fatalf("finish lost observed idle or actual receipt: %+v %v", card, err)
			}
		}}})
}

func startDoingTUI(t *testing.T, h *harness) func(string) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 34, Col: 120}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(h.ctx, filepath.Join(h.root, "system/bin/lina"), "tui", "--socket", h.socket)
	cmd.Env = []string{"HOME=" + filepath.Dir(h.root), "TERM=xterm-256color", "LANG=en_US.UTF-8"}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	screen, err := tuiterm.NewRecorder(120, 34)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = io.Copy(screen, master) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); master.Close(); <-done; screen.Close() })
	return func(want string) {
		t.Helper()
		for {
			snapshot := screen.Snapshot()
			var rows []string
			for _, row := range snapshot.Cells {
				var b strings.Builder
				for _, cell := range row {
					b.WriteString(cell.Content)
				}
				rows = append(rows, b.String())
			}
			text := strings.Join(rows, "\n")
			if strings.Contains(text, want) {
				t.Logf("sidebar capture (%s):\n%s", want, text)
				return
			}
			select {
			case <-h.ctx.Done():
				t.Fatalf("sidebar missing %q:\n%s", want, text)
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
}
