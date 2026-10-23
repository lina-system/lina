// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Completed replies are measured once, against their actual speaking agent.
// Restoring history must not turn presentation into another measurement.
func TestReplyBudgetBreachThroughInstalledCore(t *testing.T) {
	t.Parallel()
	full := "Done. " + strings.Repeat("가", 301)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		emitResponse(w, full, true)
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "budget-breach", "Show the complete result.")
	check := func() {
		t.Helper()
		reader, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		var measured, breaches int
		err = reader.QueryRowContext(h.ctx, `SELECT count(*), coalesce(sum(json_extract(e.body,'$.budget_breach')),0)
            FROM conv_event e JOIN conv_event source ON source.id=e.parent_id
            WHERE e.scaffold_kind='reply_check' AND e.agent_id=source.agent_id
                AND source.speaker_kind='agent' AND source.body=?`, full).Scan(&measured, &breaches)
		if err != nil {
			t.Fatal(err)
		}
		if measured != 1 || breaches != 1 {
			t.Errorf("completed reply measurements=%d budget breaches=%d; want one each", measured, breaches)
		}
	}
	check()
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, _ = conversationClient(t, h)
	check()
	c.Close()
	h.stopCore(core)
}
