// SPDX-License-Identifier: Apache-2.0

package applefm

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "applefm-fixture" {
		os.Exit(fixture())
	}
	os.Exit(m.Run())
}

func fixture() int {
	var req struct {
		ID, Op, Model, Input string
		Schema               json.RawMessage
	}
	if json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&req) != nil || req.ID == "" {
		return 1
	}
	profile := Profile{ContextTokens: 4096, ToolCalling: true, GuidedGeneration: true, ImageInput: false, Model: req.Model, PrivateCloud: false}
	switch os.Getenv("LINA_APPLEFM_MODE") {
	case "down":
		return write(map[string]any{"id": req.ID, "ok": false, "unavailable": true})
	case "cloud":
		profile.PrivateCloud = true
	}
	if req.Op == "profile" {
		return write(map[string]any{"id": req.ID, "ok": true, "profile": profile})
	}
	if req.Op != "complete" {
		return 1
	}
	if os.Getenv("LINA_APPLEFM_MODE") == "miss" {
		return write(map[string]any{"id": req.ID, "ok": false, "schema_ok": false, "object": json.RawMessage(`{"title":"guess"}`)})
	}
	if len(req.Schema) > 0 {
		return write(map[string]any{"id": req.ID, "ok": true, "schema_ok": true, "object": json.RawMessage(`{"title":"draft"}`)})
	}
	return write(map[string]any{"id": req.ID, "ok": true, "text": "Safari"})
}

func write(body any) int {
	if json.NewEncoder(os.Stdout).Encode(body) != nil {
		return 1
	}
	return 0
}

func helper(t *testing.T, model string, cloud bool, mode string) *Adapter {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Settings{Helper: exe, Model: model, PrivateCloud: cloud, Args: []string{"applefm-fixture"}, Env: []string{"LINA_APPLEFM_MODE=" + mode}})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestProbeAndGuidedCompletion(t *testing.T) {
	hold := lease.New(func() {})
	adapter := helper(t, OnDeviceModel, false, "ok")
	profile, err := adapter.Probe(t.Context(), hold)
	if err != nil || profile.Model != OnDeviceModel || profile.ContextTokens != 4096 || !profile.GuidedGeneration || profile.ImageInput || profile.PrivateCloud {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"title":{"type":"string"}},"required":["title"]}`)
	got, err := adapter.Complete(t.Context(), hold, Request{Instructions: "Name it.", Input: "draft", Schema: schema})
	if err != nil || string(got.Object) != `{"title":"draft"}` {
		t.Fatalf("object=%s err=%v", got.Object, err)
	}
	plain, err := adapter.Complete(t.Context(), hold, Request{Input: "open safari"})
	if err != nil || plain.Text != "Safari" || len(plain.Object) != 0 {
		t.Fatalf("text=%q err=%v", plain.Text, err)
	}
}

func TestSchemaMissDiscardsTheObject(t *testing.T) {
	hold := lease.New(func() {})
	adapter := helper(t, OnDeviceModel, false, "miss")
	_, err := adapter.Complete(t.Context(), hold, Request{Input: "draft", Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"title":{"type":"string"}},"required":["title"]}`)})
	if !errors.Is(err, ErrSchemaMiss) {
		t.Fatalf("err=%v", err)
	}
}

func TestUnavailableAndPrivateCloudMismatch(t *testing.T) {
	hold := lease.New(func() {})
	down := helper(t, OnDeviceModel, false, "down")
	_, err := down.Probe(t.Context(), hold)
	var failure *provider.Failure
	if !errors.As(err, &failure) || failure.Kind != provider.Unavailable {
		t.Fatalf("err=%v", err)
	}
	cloud := helper(t, PrivateCloudModel, true, "ok")
	if _, err = cloud.Probe(t.Context(), hold); !errors.As(err, &failure) {
		t.Fatalf("private cloud accepted: %v", err)
	}
}

func TestRejectsARelativeHelperAndAnInvalidSchema(t *testing.T) {
	if _, err := New(Settings{Helper: "helper", Model: OnDeviceModel}); err == nil {
		t.Fatal("relative helper accepted")
	}
	hold := lease.New(func() {})
	adapter := helper(t, OnDeviceModel, false, "ok")
	_, err := adapter.Complete(t.Context(), hold, Request{Input: "draft", Schema: json.RawMessage(`{"type":"object"}`)})
	var failure *provider.Failure
	if !errors.As(err, &failure) || failure.Kind != provider.NotAccepted {
		t.Fatalf("err=%v", err)
	}
}
