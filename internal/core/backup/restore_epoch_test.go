// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func TestRestoreEpochNeverFallsAndLeavesControlsAlone(t *testing.T) {
	for _, tc := range []struct{ control, current, captured, want uint64 }{
		{90, 80, 7, 90}, {2, 85, 7, 85}, {0, 0, 70, 70}, {9007199254740993, 1, 8, 9007199254740993}, {0, 0, math.MaxUint64, 0},
	} {
		root := t.TempDir()
		if err := os.Chmod(root, 0700); err != nil {
			t.Fatal(err)
		}
		ctl, err := ctlfiles.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := ctl.Acquire(); err != nil {
			t.Fatal(err)
		}
		if err := ctl.Write("epoch", []byte(strconv.FormatUint(tc.control, 10))); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "canon.sqlite")
		if tc.current != 0 {
			db, err := canon.Open(t.Context(), canon.Config{Path: path, Epoch: tc.current, Controls: ctl, Migrations: canon.Migrations()})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		got, err := chooseRestoreEpoch(t.Context(), ctl, path, tc.captured)
		if tc.want == 0 {
			if err == nil {
				t.Fatal("exhausted epoch accepted")
			}
		} else if err != nil || got != tc.want {
			t.Fatalf("%+v: %d %v", tc, got, err)
		}
		after, err := ctl.Epoch()
		if err != nil || after != tc.control {
			t.Fatalf("control changed: %d %v", after, err)
		}
		ctl.Close()
	}
}
