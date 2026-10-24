// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"testing"

	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestStartedLookupNeverAdmitsOrReleases(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t, func(context.Context, provider.Request) (provider.Stream, error) {
		t.Error("lookup started model")
		return nil, nil
	})
	call := startCall(t, brief())
	if _, found, err := s.Started(t.Context(), origin(), call.ID); err != nil || found {
		t.Fatalf("missing lookup: %t %v", found, err)
	}
	receipt := startReceipt(t, s, origin(), call)
	o := origin()
	o.Epoch++
	r, found, err := s.Started(t.Context(), o, call.ID)
	if err != nil || !found || r.ID != receipt.ID || r.Released {
		t.Fatalf("lost receipt: %+v %t %v", r, found, err)
	}
	for _, change := range []func(*runs.Origin){func(o *runs.Origin) { o.Person = "other" }, func(o *runs.Origin) { o.Agent = "other" }, func(o *runs.Origin) { o.Conversation = "other" }, func(o *runs.Origin) { o.Turn = "other" }} {
		o := origin()
		change(&o)
		if _, found, err := s.Started(t.Context(), o, call.ID); err != nil || found {
			t.Fatalf("cross-origin receipt: %t %v", found, err)
		}
	}
}
