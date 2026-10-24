// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/embedding"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

func runEmbedding(args []string) int {
	flags := flag.NewFlagSet("lina embedding", flag.ContinueOnError)
	root := flags.String("state-root", "", "installed state root; stop Core before configuring")
	endpoint := flags.String("endpoint", "", "full OpenAI-compatible embeddings URL; enables embedding of LINA conversations")
	model := flags.String("model", embedding.DefaultModel, "embedding model")
	dimension := flags.Int("dimension", embedding.DefaultDimension, "vector dimension")
	person := flags.String("person", "", "installed person id (required with a key)")
	keyName := flags.String("key-name", "", "secret-store key name")
	keyInput := flags.Bool("key-stdin", false, "read the endpoint key from standard input")
	disable := flags.Bool("disable", false, "use full-text recall only")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	cfg := embedding.Config{Endpoint: *endpoint, Model: *model, Dimension: *dimension, KeyName: *keyName}
	if *root == "" || flags.NArg() != 0 || (*disable && (*endpoint != "" || *keyInput)) || (!*disable && cfg.Validate() != nil) || (*keyInput && (*person == "" || *keyName == "")) {
		return 64
	}
	if *disable {
		cfg = embedding.Config{}
	}
	if err := configureEmbedding(*root, *person, cfg, *keyInput, os.Stdin); err != nil {
		if errors.Is(err, ctlfiles.ErrLocked) {
			fmt.Fprintln(os.Stderr, "Stop Core before configuring embeddings.")
			return 69
		}
		fmt.Fprintln(os.Stderr, "Could not save embedding configuration.")
		return 78
	}
	fmt.Fprintln(os.Stdout, "Embedding configuration saved. Restart Core to apply it.")
	return 0
}
func configureEmbedding(root, person string, cfg embedding.Config, keyInput bool, input io.Reader) error {
	controls, err := ctlfiles.Open(root)
	if err != nil {
		return err
	}
	defer controls.Close()
	if err = controls.Acquire(); err != nil {
		return err
	}
	if keyInput {
		raw, err := io.ReadAll(io.LimitReader(input, 65537))
		if err != nil || len(raw) > 65536 {
			return embedding.ErrInvalid
		}
		defer clear(raw)
		value := []byte(strings.TrimSpace(string(raw)))
		defer clear(value)
		vault, err := secrets.OpenFiles(root)
		if err != nil {
			return err
		}
		defer vault.Close()
		if err = vault.Put(secrets.Key{Person: person, Service: "embedding", Name: cfg.KeyName}, value); err != nil {
			return err
		}
	}
	directory := filepath.Join(root, "system")
	if err = os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".embedding-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err = json.NewEncoder(file).Encode(cfg); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), filepath.Join(directory, "embedding.json")); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
