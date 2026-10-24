// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/thisisjun786/lina/internal/core/embedding"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/recall"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

func (r *conversationRuntime) initEmbeddings(ctx context.Context) error {
	file, err := os.Open(filepath.Join(r.root, "system", "embedding.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return embedding.ErrInvalid
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(raw) > 8192 {
		return embedding.ErrInvalid
	}
	var cfg embedding.Config
	if json.Unmarshal(raw, &cfg) != nil {
		return embedding.ErrInvalid
	}
	if cfg.Endpoint == "" {
		return nil
	}
	adapter, err := embedding.New(cfg, r.vault, r.person)
	if err != nil {
		return err
	}
	r.vectors, err = recall.NewVectors(ctx, r.db, func(ctx context.Context, id string) (string, error) {
		event, err := r.journal().Get(ctx, id)
		if err != nil {
			return "", err
		}
		if event.BytesUnavailable {
			return "", ledger.ErrBytesUnavailable
		}
		return event.Body.DerivedText(), nil
	}, adapter)
	return err
}
func (r *conversationRuntime) vectorQuery() recall.VectorQuery {
	root := r
	if r.router != nil {
		root = r.router.root
	}
	if root.vectors == nil {
		return nil
	}
	return root.vectors.Query
}
func (api growthJobAPI) Embed(ctx context.Context) (jobapi.EmbeddingResult, error) {
	n, err := api.root.vectors.DrainBatch(ctx)
	return jobapi.EmbeddingResult{Completed: n}, err
}
