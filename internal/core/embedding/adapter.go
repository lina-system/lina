// SPDX-License-Identifier: Apache-2.0

// Package embedding owns the non-chat OpenAI-compatible embeddings boundary.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/recall"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

const DefaultModel = "BAAI/bge-m3"
const DefaultDimension = 1024
const BatchSize = recall.EmbeddingBatchSize
const MaxTextRunes = 4096

var ErrInvalid = errors.New("invalid_embedding_configuration")
var ErrUnavailable = errors.New("embedding_unavailable")

// Config contains no key. Endpoint is the full embeddings URL, not a chat route.
type Config struct {
	Endpoint  string `json:"endpoint"`
	Model     string `json:"model"`
	Dimension int    `json:"dimension"`
	KeyName   string `json:"key_name,omitempty"`
}

func (c Config) Defaults() Config {
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.Dimension == 0 {
		c.Dimension = DefaultDimension
	}
	return c
}
func (c Config) Validate() error {
	c = c.Defaults()
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()))) || strings.TrimSpace(c.Model) == "" || len(c.Model) > 256 || c.Dimension < 1 || c.Dimension > 4096 {
		return ErrInvalid
	}
	return nil
}

type Vault interface {
	Get(secrets.Key) (secret.Secret, error)
}
type Adapter struct {
	config Config
	vault  Vault
	person string
	client *http.Client
}

func New(c Config, vault Vault, person string) (*Adapter, error) {
	c = c.Defaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.KeyName != "" && (vault == nil || person == "") {
		return nil, ErrInvalid
	}
	return &Adapter{c, vault, person, &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (a *Adapter) Config() Config { return a.config }
func (a *Adapter) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 || len(texts) > BatchSize {
		return nil, ErrInvalid
	}
	clean := make([]string, len(texts))
	for i, text := range texts {
		masked, err := secrets.Mask(text)
		if err != nil {
			return nil, ErrInvalid
		}
		runes := []rune(masked.DerivedText())
		if len(runes) > MaxTextRunes {
			runes = runes[:MaxTextRunes]
		}
		clean[i] = string(runes)
		if strings.TrimSpace(clean[i]) == "" {
			return nil, ErrInvalid
		}
	}
	body, err := json.Marshal(struct {
		Model    string   `json:"model"`
		Input    []string `json:"input"`
		Encoding string   `json:"encoding_format"`
	}{a.config.Model, clean, "float"})
	if err != nil {
		return nil, ErrInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.config.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("Content-Type", "application/json")
	if a.config.KeyName != "" {
		key, err := a.vault.Get(secrets.Key{Person: a.person, Service: "embedding", Name: a.config.KeyName})
		if err != nil || secret.InjectHeader(req.Header, "Authorization", "Bearer ", key) != nil {
			return nil, ErrUnavailable
		}
	}
	res, err := a.client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return nil, ErrUnavailable
	}
	var out struct {
		Model string `json:"model"`
		Data  []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &out) != nil || len(out.Data) != len(texts) || (out.Model != "" && out.Model != a.config.Model) {
		return nil, ErrUnavailable
	}
	vectors := make([][]float32, len(texts))
	for _, row := range out.Data {
		if row.Index < 0 || row.Index >= len(texts) || vectors[row.Index] != nil || len(row.Embedding) != a.config.Dimension {
			return nil, ErrUnavailable
		}
		var norm float64
		for _, v := range row.Embedding {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return nil, ErrUnavailable
			}
			norm += float64(v) * float64(v)
		}
		if norm == 0 {
			return nil, ErrUnavailable
		}
		vectors[row.Index] = row.Embedding
	}
	return vectors, nil
}

func (a *Adapter) VectorConfig() recall.VectorConfig {
	return recall.VectorConfig{Endpoint: a.config.Endpoint, Model: a.config.Model, Dimension: a.config.Dimension}
}
