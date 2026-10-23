// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"github.com/thisisjun786/lina/kit/provider"
	"testing"
)

func TestQuotaReadAndUpdateWake(t *testing.T) {
	s, ctx, _ := openFixture(t, "quota")
	rows, err := s.Quota(ctx)
	if err != nil || len(rows) != 1 || rows[0].State != provider.QuotaBlocked || rows[0].ResetsAt == nil {
		t.Fatalf("quota %v %v", rows, err)
	}
	select {
	case <-s.QuotaUpdates():
	case <-ctx.Done():
		t.Fatal("quota update did not wake scheduler")
	}
}
