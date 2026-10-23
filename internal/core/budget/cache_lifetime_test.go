// SPDX-License-Identifier: Apache-2.0
package budget

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/provider"
	"reflect"
	"testing"
)

func TestCacheLifetimeUsageSurvivesRestartAndLegacyRows(t *testing.T) {
	s, cfg := fixture(t)
	zero, write, read, total, out := int64(0), int64(23), int64(41), int64(100), int64(2)
	usage := provider.Usage{Input: &total, Output: &out, CacheByLifetime: map[provider.CacheLifetime]provider.CacheUsage{"1h": {Write: &write, Read: &read}, "5m": {Write: &zero}}}
	err := s.Write(t.Context(), func(tx *canon.Tx) error {
		for _, id := range []string{"new", "old"} {
			if err := Reserve(t.Context(), tx, Reservation{ID: id, Agent: "a", Account: "account", Provider: provider.Claude, Kind: provider.Turn, Run: "turn", Tokens: 10}, 1000); err != nil {
				return err
			}
		}
		if err := Settle(t.Context(), tx, "new", usage); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), `UPDATE budget_usage SET usage='{"input":10,"cached":4,"cache_write":2,"output":1,"reasoning":null,"cost_estimate":null}',settled=1,charged=11 WHERE id='old'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err = canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	err = s.Read(t.Context(), func(tx *canon.Tx) error {
		row, err := Read(t.Context(), tx, "new")
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(row.Usage, usage) || row.Charged != 102 {
			t.Fatalf("lost or double-counted usage: %+v", row)
		}
		old, err := Read(t.Context(), tx, "old")
		if err != nil {
			return err
		}
		if old.Usage.CacheByLifetime != nil || old.Usage.Cached == nil || *old.Usage.Cached != 4 {
			t.Fatal("legacy usage changed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	negative := int64(-1)
	usage.CacheByLifetime["1h"] = provider.CacheUsage{Read: &negative}
	if usage.Valid() {
		t.Fatal("negative lifetime count accepted")
	}
	b, _ := json.Marshal(provider.Usage{})
	var decoded map[string]any
	_ = json.Unmarshal(b, &decoded)
	if _, ok := decoded["cache_by_lifetime"]; ok {
		t.Fatal("unknown lifetime reported")
	}
}
