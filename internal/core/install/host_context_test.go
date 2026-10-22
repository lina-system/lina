// SPDX-License-Identifier: Apache-2.0
package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessHostRunWithContext(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "synthetic-secret")
	t.Setenv("LN541_TEST_SETUP", "expected")
	host := NewProcessHost()
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	path := filepath.Join(t.TempDir(), "tool")
	write(t, path, []byte("#!/bin/sh\n[ -z \"$OPENAI_API_KEY\" ] || exit 99\n[ \"$LN541_TEST_SETUP\" = expected ] || exit 98\nprintf '%s' \"$1\"\n"), 0700)

	t.Run("environment and literal arguments", func(t *testing.T) {
		const arg = "a value; $(not-a-command)"
		out, err := host.RunWithContext(t.Context(), path, arg)
		if err != nil || string(out) != arg {
			t.Fatalf("command: %q %v", out, err)
		}
	})
	t.Run("bounded output", func(t *testing.T) {
		out, err := host.RunWithContext(t.Context(), path, strings.Repeat("x", 5000))
		if err == nil || !strings.Contains(err.Error(), "output exceeds") || len(out) != 4096 {
			t.Fatalf("output length=%d err=%v", len(out), err)
		}
	})
	for _, reason := range []string{"canceled", "deadline"} {
		t.Run(reason, func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			want := context.Canceled
			if reason == "deadline" {
				ctx, cancel = context.WithDeadline(t.Context(), time.Unix(1, 0))
				want = context.DeadlineExceeded
			} else {
				ctx, cancel = context.WithCancel(t.Context())
			}
			cancel()
			marker := filepath.Join(t.TempDir(), "started")
			command := filepath.Join(t.TempDir(), "command")
			write(t, command, []byte("#!/bin/sh\nprintf started > \"$1\"\n"), 0700)
			if _, err := host.RunWithContext(ctx, command, marker); !errors.Is(err, want) {
				t.Fatalf("command error=%v, want %v", err, want)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("expired command ran: %v", err)
			}
		})
	}
}
