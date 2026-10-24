// SPDX-License-Identifier: Apache-2.0
package storebench

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/embedding"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/recall"
)

var vectorsFlag = flag.Bool("storebench-vectors", false, "measure vector storage and calls using a local fake endpoint; no quality claim")

func measureVectors(ctx context.Context, db *canon.Store, events *ledger.Store, turns int, r *report) error {
	var calls atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var input struct {
			Model string
			Input []string
		}
		if json.NewDecoder(request.Body).Decode(&input) != nil || len(input.Input) > embedding.BatchSize {
			w.WriteHeader(400)
			return
		}
		calls.Add(1)
		rows := make([]map[string]any, len(input.Input))
		for i := range input.Input {
			vector := make([]float32, embedding.DefaultDimension)
			vector[0] = 1
			rows[i] = map[string]any{"index": i, "embedding": vector}
		}
		json.NewEncoder(w).Encode(map[string]any{"model": input.Model, "data": rows})
	}))
	defer endpoint.Close()
	adapter, err := embedding.New(embedding.Config{Endpoint: endpoint.URL}, nil, "")
	if err != nil {
		return err
	}
	vectors, err := recall.NewVectors(ctx, db, func(ctx context.Context, id string) (string, error) {
		event, err := events.Get(ctx, id)
		return event.Body.DerivedText(), err
	}, adapter)
	if err != nil {
		return err
	}
	for {
		if _, err = vectors.DrainBatch(ctx); err != nil {
			return err
		}
		var ready int
		if err = db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT ready FROM search_index_config WHERE name='recall'").Scan(&ready)
		}); err != nil {
			return err
		}
		if ready == 1 {
			break
		}
	}
	r.EmbeddingBatchCalls = calls.Load()
	r.EmbeddingBatchCallsPerTurn = float64(calls.Load()) / float64(turns)
	if err = db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(length(embedding)),0) FROM recall_vec").Scan(&r.VectorDocuments, &r.VectorBytes)
	}); err != nil {
		return err
	}
	if r.VectorDocuments == 0 {
		return errors.New("storebench_vectors_missing")
	}
	r.VectorBytesPerDocument = float64(r.VectorBytes) / float64(r.VectorDocuments)
	for range turns {
		before := calls.Load()
		ids, err := vectors.Query(ctx, recall.Scope{Person: person, Agent: agent}, "the person's remembered question")
		if err != nil {
			return err
		}
		if len(ids) == 0 || calls.Load() != before+1 {
			return errors.New("storebench_vector_query_missing")
		}
	}
	r.EmbeddingQueryCalls = calls.Load() - r.EmbeddingBatchCalls
	r.EmbeddingQueryCallsPerTurn = float64(r.EmbeddingQueryCalls) / float64(turns)
	return nil
}
