// SPDX-License-Identifier: Apache-2.0
package ledger_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestProviderItemMasksDecodedQuotedTextAcrossReopen(t *testing.T) {
	t.Parallel()
	db, cfg := database(t)
	in := input("provider-item")
	in.PersonID = ""
	in.ScaffoldKind = "provider_item"
	body, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": `The password="synthetic-private" is set.`}}})
	in.Body = string(body)
	e := admit(t, ledger.New(db, nil), guard.EntryAgent, in)
	for _, text := range []string{e.Body.Text(), e.Body.DerivedText()} {
		if !json.Valid([]byte(text)) || strings.Contains(text, "synthetic-private") {
			t.Errorf("masked provider item is invalid or retains the synthetic credential: %q", text)
		}
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	e, err := ledger.New(open(t, cfg), nil).Get(t.Context(), in.ID)
	if err != nil || !json.Valid([]byte(e.Body.DerivedText())) || strings.Contains(e.Body.Text(), "synthetic-private") {
		t.Fatalf("reopened provider item: %+v %v", e, err)
	}
}
