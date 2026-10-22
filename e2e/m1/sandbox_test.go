// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/design"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Real boot composition and a real TUI stay usable when confinement cannot run.
func TestM1SandboxUnavailable(t *testing.T) {
	data, err := os.ReadFile("sandbox.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Cases []struct {
			Name, Cause, OS string
			LauncherExit    int `json:"launcher_exit"`
		}
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	binary := buildLina(t)
	for _, tc := range scenario.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.OS != "" && tc.OS != runtime.GOOS {
				t.Skip("platform scenario")
			}
			h := newHarness(t, binary)
			control := filepath.Join(h.root, "system/core")
			if err := os.MkdirAll(control, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(control, "main.json"), []byte(`{"main_id":"main-e2e"}`), 0600); err != nil {
				t.Fatal(err)
			}
			launcher := filepath.Join(h.root, "launcher")
			if tc.LauncherExit != 0 {
				if err := os.WriteFile(launcher, []byte("#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\nexit "+strconv.Itoa(tc.LauncherExit)+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(h.ctx)
			ready := make(chan boot.Snapshot, 1)
			done := make(chan error, 1)
			go func() {
				done <- boot.Run(ctx, boot.Config{StateRoot: h.root, Socket: h.socket, SandboxLauncher: launcher, AfterStep: func(step boot.Step, s boot.Snapshot) error {
					if step == boot.Ready {
						ready <- s
					}
					return nil
				}})
			}()
			defer func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			select {
			case s := <-ready:
				if s.Sandbox.Available || s.Sandbox.Cause != tc.Cause {
					t.Fatalf("sandbox status: %+v", s.Sandbox)
				}
			case err := <-done:
				done <- err
				t.Fatalf("Core stopped before READY: %v", err)
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
			for _, locale := range []string{"en_US.UTF-8", "ko_KR.UTF-8"} {
				term := h.startScreenTUI("TERM=xterm-256color", "LC_ALL="+locale)
				notice := design.Text(design.LanguageFor(locale), "sandbox.unavailable")
				prefix := string([]rune(notice)[:min(18, len([]rune(notice)))])
				screen := screenText(waitScreen(t, h, term, prefix))
				if strings.Count(screen, prefix) != 1 {
					t.Errorf("setup notice is duplicated: %s", screen)
				}
				if strings.Contains(screen, "launcher_missing") || strings.Contains(screen, "probe_failed") {
					t.Error("private diagnostic leaked")
				}
				h.keys(term, "\x04")
				term.process.wait(t, 0)
				h.expectTerminalRestored(term)
			}
		})
	}
}

func TestM1SandboxEmptyPATH(t *testing.T) {
	h := newHarness(t, buildLina(t))
	dir := filepath.Join(h.root, "system/core")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.json"), []byte(`{"main_id":"main-e2e"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	err := boot.Run(ctx, boot.Config{StateRoot: h.root, Socket: h.socket, AfterStep: func(step boot.Step, s boot.Snapshot) error {
		if step != boot.Ready {
			return nil
		}
		defer cancel()
		if !s.Sandbox.Available {
			return fmt.Errorf("empty PATH lost installed launcher: %s", s.Sandbox.Cause)
		}
		term := h.startScreenTUI("TERM=xterm-256color")
		waitScreen(t, h, term, "Connected")
		h.keys(term, "\x04")
		term.process.wait(t, 0)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
}
