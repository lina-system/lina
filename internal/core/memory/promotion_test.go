// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"context"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"testing"
)

func TestPromotionRequiresConfirmationAndPreservesSessionPrecision(t *testing.T) {
	db := database(t)
	in := proposal("A", "prefers synthetic tea")
	in.Item.Basis = Imported
	in.Item.Evidence = []Evidence{{Kind: External, ID: "observation", Precision: Session}}
	resolver := func(context.Context, *canon.Tx, Evidence) (Origin, error) {
		return Origin{Person: "person", Agent: "A", Speaker: guard.Forwarded, Scope: Local, Quote: "original sentence", Verified: true}, nil
	}
	err := db.Write(t.Context(), func(tx *canon.Tx) error { _, err := Insert(t.Context(), tx, in, resolver); return err })
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("ordinary write allowed import: %v", err)
	}
	err = db.Write(t.Context(), func(tx *canon.Tx) error { _, err := Promote(t.Context(), tx, in, "", resolver); return err })
	if err == nil {
		t.Fatal("missing confirmation accepted")
	}
	var item Item
	err = db.Write(t.Context(), func(tx *canon.Tx) error {
		var err error
		item, err = Promote(t.Context(), tx, in, "confirmed-batch", resolver)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.Basis != Imported || item.Evidence[0].Precision != Session || item.Evidence[0].Quote != "original sentence" || item.SourcePerson != "" {
		t.Fatalf("invented evidence precision/provenance: %+v", item)
	}
}
