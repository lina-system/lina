// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM3LocalModel(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		calls++
		emitResponse(w, `{"title":"from-main","date":"2026-10-24","time":"15:00"}`, true)
	}))
	defer server.Close()
	helper := buildAppleFixture(t)
	h, _ := installedConversationHarness(t, server.URL)
	core := h.command("core", "--state-root", h.root, "--socket", h.socket)
	core.command.Env = append(core.command.Env,
		"LINA_RESPONSES_BASE_URL="+server.URL+"/v1",
		"LINA_RESPONSES_MODEL=fixture-model",
		"LINA_RESPONSES_API_KEY=synthetic-key",
		"LINA_APPLEFM_HELPER="+helper,
	)
	core.start(t)
	t.Cleanup(func() {
		core.killAndWait()
		if t.Failed() {
			t.Logf("core stderr:\n%s", core.stderr.String())
		}
	})
	h.connectWithin(core, 60*time.Second).Close()
	waitCoreReady(t, h)
	conn := h.connectWithin(core, 60*time.Second)
	c := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
	hello := clientHello([]string{"1.0"})
	hello.Capabilities = []protocol.Capability{{Name: "local_model", Version: "1.0"}, {Name: "conversation", Version: "1.0"}}
	answer := c.call(t, protocol.KindHello, hello)
	welcome, ok := answer.Payload.(protocol.Welcome)
	if !ok || answer.Error != nil {
		t.Fatalf("hello: %+v", answer)
	}
	c.welcome = welcome
	if !slices.Contains(welcome.Capabilities, protocol.Capability{Name: "local_model", Version: "1.0"}) {
		t.Fatal("local model was not negotiated")
	}
	schema := `{"type":"object","additionalProperties":false,"properties":{"title":{"type":"string"},"date":{"type":"string"},"time":{"type":"string"}},"required":["title","date","time"]}`
	local := callLocal(t, c, protocol.LocalTask{Task: "intent", Text: "사파리 열어", Schema: &schema})
	if local.Route != "local" || local.Reason != "fit" || local.Provider != "apple" || local.Model != "apple-foundation-on-device" || local.Object == nil || !strings.Contains(*local.Object, `"title":"draft"`) {
		t.Fatalf("local: %+v", local)
	}
	miss := "force-miss the schema"
	missed := callLocal(t, c, protocol.LocalTask{Task: "extract", Text: miss, Schema: &schema})
	if missed.Route != "main" || missed.Reason != "schema_miss" || missed.Provider != "chatgpt" || missed.Model != "fixture-model" || missed.Object == nil || strings.Contains(*missed.Object, "guess") {
		t.Fatalf("miss: %+v", missed)
	}
	long := strings.Repeat("a", 5000)
	over := callLocal(t, c, protocol.LocalTask{Task: "summary", Text: long, Schema: &schema})
	if over.Route != "main" || over.Reason != "over_context" || over.Model != "fixture-model" {
		t.Fatalf("over: %+v", over)
	}
	preference := "main"
	forced := callLocal(t, c, protocol.LocalTask{Task: "intent", Text: "사파리 열어", Schema: &schema, Preference: &preference})
	if forced.Route != "main" || forced.Reason != "forced" || forced.Model != "fixture-model" {
		t.Fatalf("forced: %+v", forced)
	}
	if calls != 3 {
		t.Fatalf("main model calls=%d", calls)
	}
	log, err := os.ReadFile(filepath.Join(filepath.Dir(helper), "calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), "complete") != 2 || strings.Count(string(log), "profile") != 1 {
		t.Fatalf("helper log:\n%s", log)
	}
	db, err := canon.OpenReader(context.Background(), filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var providerName, model, digest string
	err = db.Read(context.Background(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT provider, model, digest FROM model_served WHERE id=?`, local.ServedID).Scan(&providerName, &model, &digest)
	})
	if err != nil || providerName != "apple" || model != "apple-foundation-on-device" || strings.Contains(digest, "사파리") {
		t.Fatalf("ledger provider=%s model=%s digest=%s err=%v", providerName, model, digest, err)
	}
}

func callLocal(t *testing.T, c *client, task protocol.LocalTask) protocol.LocalTaskResult {
	t.Helper()
	answer := c.callWithin(t, protocol.KindLocalTask, task, 60*time.Second)
	if answer.Error != nil || answer.Kind != protocol.KindLocalTaskResult || answer.EffectState != protocol.EffectNone {
		t.Fatalf("local task: kind=%s effect=%s error=%v", answer.Kind, answer.EffectState, answer.Error)
	}
	result, ok := answer.Payload.(protocol.LocalTaskResult)
	if !ok {
		t.Fatalf("payload: %T", answer.Payload)
	}
	return result
}

func buildAppleFixture(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "applefm-fixture")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", out, "./e2e/applefmfixture")
	build.Dir = "../.."
	build.Env = append(os.Environ(), "GOENV=off", "GOTOOLCHAIN=auto")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, output)
	}
	return out
}
