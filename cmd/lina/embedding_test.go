// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/embedding"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

func TestEmbeddingConfigurationKeepsKeyInVault(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := embedding.Config{Endpoint: "https://embedding.example/v1/embeddings", Model: embedding.DefaultModel, Dimension: 1024, KeyName: "endpoint"}
	if err := configureEmbedding(root, "owner", cfg, true, strings.NewReader("fixture-embedding-key\n")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "system/embedding.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fixture-embedding-key") {
		t.Fatal("key leaked into config")
	}
	var got embedding.Config
	if json.Unmarshal(raw, &got) != nil || got != cfg {
		t.Fatal("configuration lost")
	}
	vault, err := secrets.OpenFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	key, err := vault.Get(secrets.Key{Person: "owner", Service: "embedding", Name: "endpoint"})
	if err != nil || !key.Ready() {
		t.Fatal("key not stored")
	}
}
