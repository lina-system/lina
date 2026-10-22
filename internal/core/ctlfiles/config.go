// SPDX-License-Identifier: Apache-2.0

package ctlfiles

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Config is local installation configuration, separate from the main identity.
type Config struct {
	// PersonaSource is retained for legacy configuration compatibility only.
	PersonaSource string `json:"persona_source,omitempty"`
}

func (c Config) Validate() error {
	if c.PersonaSource != "" && (!filepath.IsAbs(c.PersonaSource) || strings.ContainsAny(c.PersonaSource, "\x00\r\n")) {
		return errors.New("persona_source must be an absolute local directory path")
	}
	return nil
}

// ReadConfig rereads the file so changes take effect at the next preparation.
// Existing installations without a config file retain the empty defaults.
func (s *Store) ReadConfig() (Config, error) {
	var cfg Config
	data, err := s.Read("config.json")
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, errors.New("invalid Core config")
	}
	return cfg, cfg.Validate()
}
