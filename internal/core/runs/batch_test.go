// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"io"
	"testing"

	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/provider"
)

type batchStream struct {
	events []provider.Event
	batch  string
}

func (s *batchStream) Next(context.Context) (provider.Event, error) {
	if len(s.events) == 0 {
		return provider.Event{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func (s *batchStream) Meta() provider.Meta { return provider.Meta{Batch: s.batch} }
func (s *batchStream) Close() error        { return nil }

// A deferred submission names itself in the run's request evidence, which is
// what the ledger and the provider replay read.
func TestBackgroundJobRecordsBatchIdentity(t *testing.T) {
	t.Parallel()
	s, _, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		complete := answer(runs.Artifact{Body: "report", Done: true}).(*stream)
		return &batchStream{events: complete.events, batch: "batch-1"}, nil
	}, func(c *runs.Config) { c.ProviderClass = provider.ModelAPI })
	id, _, err := s.Start(t.Context(), origin(), brief())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Release(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err = s.Wait(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	record, err := s.Get(t.Context(), id)
	if err != nil || len(record.Requests) != 1 || record.Requests[0].Batch != "batch-1" {
		t.Fatalf("batch evidence: %+v %v", record.Requests, err)
	}
	if record.Requests[0].Class != provider.ModelAPI {
		t.Fatalf("class %v", record.Requests[0].Class)
	}
}
