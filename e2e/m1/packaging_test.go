// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// Drive the real build output through the shared protocol harness. Native
// service-manager startup is a separate packaging acceptance measurement.
func TestPackagedService(t *testing.T) {
	t.Parallel()

	output := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "node", "scripts/build.mjs", "--out", output,
		"--target", runtime.GOOS+"/"+runtime.GOARCH)
	build.Dir = "../.."
	for _, key := range []string{"PATH", "HOME", "GOCACHE", "GOMODCACHE", "TMPDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			build.Env = append(build.Env, key+"="+value)
		}
	}
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("package binaries: %v\n%s", err, data)
	}
	data, err := os.ReadFile("packaged-service.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Steps []string `json:"steps"`
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, filepath.Join(output, "lina"))
	var core *process
	var previous uint64
	welcomes := 0
	for _, step := range scenario.Steps {
		switch step {
		case "start":
			if core != nil {
				t.Fatal("service already running")
			}
			core = h.startCore()
		case "welcome":
			if core == nil {
				t.Fatal("service not running")
			}
			client := h.client(core)
			epoch, err := strconv.ParseUint(client.welcome.Epoch, 10, 64)
			if err != nil || epoch <= previous {
				t.Fatalf("restart must advance epoch: previous=%d current=%s error=%v", previous, client.welcome.Epoch, err)
			}
			previous = epoch
			welcomes++
		case "stop":
			if core == nil {
				t.Fatal("service not running")
			}
			h.stopCore(core)
			core = nil
		default:
			t.Fatalf("unknown packaging step %q", step)
		}
	}
	if welcomes < 2 || core != nil {
		t.Fatal("scenario must cover a restart and finish stopped")
	}
}
