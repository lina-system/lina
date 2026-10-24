// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestSecretsDoNotReachRunRecordsTranscriptsMaterialsOrNextRequest(t *testing.T) {
	toolSecret := "sk-proj-" + strings.Repeat("A7", 24)
	resultSecret := "sk-proj-" + strings.Repeat("B8", 24)
	followSecret := "sk-proj-" + strings.Repeat("C9", 24)
	fieldSecret := "synthetic-unshaped-value"
	secrets := []string{toolSecret, resultSecret, followSecret, fieldSecret}
	check := func(where string, data []byte) {
		t.Helper()
		for _, secret := range secrets {
			if bytes.Contains(data, []byte(secret)) {
				t.Errorf("raw credential in %s", where)
			}
		}
	}
	var calls atomic.Int64
	var work string
	s, _, materials := setupWith(t, func(_ context.Context, req provider.Request) (provider.Stream, error) {
		if calls.Add(1) == 1 {
			return call("read-secret"), nil
		}
		data, _ := json.Marshal(req.Items)
		check("next model request", data)
		if !bytes.Contains(data, []byte("9007199254740993")) {
			t.Error("masking changed exact numeric evidence")
		}
		return answer(runs.Artifact{Body: "Result " + resultSecret, Done: true}), nil
	}, func(cfg *runs.Config) {
		work = cfg.WorkDir
		tool := cfg.Tools["read"]
		tool.Handler.Execute = func(_ context.Context, c provider.ToolCall) loop.Result {
			data, _ := json.Marshal(map[string]any{toolSecret: "secret-key", "api_key": fieldSecret, "note": toolSecret, "count": json.Number("9007199254740993")})
			return loop.Result{ID: c.ID, Code: loop.OK, Output: data}
		}
		cfg.Tools["read"] = tool
	})
	id := start(t, s)
	if err := s.Follow(t.Context(), id, "Use "+followSecret); err != nil {
		t.Fatal(err)
	}
	before, err := s.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(before)
	check("queued follow-up", data)
	if err := s.Release(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(t.Context(), id)
	if err != nil || r.Outcome != runs.Complete {
		t.Fatalf("run failed: %s %v", r.Outcome, err)
	}
	data, _ = json.Marshal(r)
	check("canon record and checkpoint", data)
	data, err = materials.Read(t.Context(), r.Material.ID, r.Material.Revision)
	if err != nil {
		t.Fatal(err)
	}
	check("result material", data)
	if !json.Valid(data) {
		t.Error("masking corrupted material JSON")
	}
	data, err = os.ReadFile(filepath.Join(work, "runs", id, "transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	check("transcript", data)
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		if !json.Valid(line) {
			t.Error("masking corrupted transcript JSON")
		}
	}
}
