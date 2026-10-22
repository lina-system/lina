// SPDX-License-Identifier: Apache-2.0

// Package crwpin defines the build provenance of CRW contract inputs.
package crwpin

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
)

const Repository = "https://github.com/thisisjun786/codex-relay-workflow"

// Record is the complete provenance included in a build's crwPin metadata.
// Paths are relative to the local corpus; Source paths belong to Commit.
type Record struct {
	Version    int         `json:"version"`
	Repository string      `json:"repository"`
	Commit     string      `json:"commit"`
	Domains    []string    `json:"domains"`
	Schemas    []File      `json:"schemas"`
	Fixtures   []File      `json:"fixtures"`
	Replay     *ReplayFile `json:"replay,omitempty"`
}

// ReplayFile pins LINA's input-only materialization of upstream placeholders.
// Its bytes are local test data, not an unchanged upstream file.
type ReplayFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type File struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	SHA256 string `json:"sha256"`
	Domain string `json:"domain,omitempty"`
}

var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var commit = regexp.MustCompile(`^[0-9a-f]{40}$`)
var domain = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Validate checks metadata without requiring a source checkout on an installed
// host. It does not equate a well-formed pin with verified corpus bytes.
func (r Record) Validate() error {
	if r.Version != 1 || r.Repository != Repository || !commit.MatchString(r.Commit) {
		return fmt.Errorf("invalid CRW pin version, repository or commit")
	}
	if len(r.Schemas) == 0 || len(r.Fixtures) == 0 || len(r.Domains) == 0 {
		return fmt.Errorf("CRW pin requires schemas, fixtures and domains")
	}
	domains := make(map[string]int)
	for _, name := range r.Domains {
		if _, exists := domains[name]; exists || !domain.MatchString(name) {
			return fmt.Errorf("invalid or duplicate fixture domain %q", name)
		}
		domains[name] = 0
	}
	paths, sources := make(map[string]bool), make(map[string]bool)
	for _, group := range []struct {
		files   []File
		fixture bool
	}{{r.Schemas, false}, {r.Fixtures, true}} {
		for _, f := range group.files {
			if !validPath(f.Path) || !validPath(f.Source) || !digest.MatchString(f.SHA256) || paths[f.Path] || sources[f.Source] {
				return fmt.Errorf("invalid or duplicate CRW file %q (source %q)", f.Path, f.Source)
			}
			paths[f.Path], sources[f.Source] = true, true
			if group.fixture {
				if _, ok := domains[f.Domain]; !ok {
					return fmt.Errorf("fixture %q has unadopted domain %q", f.Path, f.Domain)
				}
				domains[f.Domain]++
			} else if f.Domain != "" {
				return fmt.Errorf("schema %q must not name a fixture domain", f.Path)
			}
		}
	}
	for name, n := range domains {
		if n == 0 {
			return fmt.Errorf("fixture domain %q is empty", name)
		}
	}
	if r.Replay != nil && (!validPath(r.Replay.Path) || !digest.MatchString(r.Replay.SHA256) || paths[r.Replay.Path]) {
		return fmt.Errorf("invalid or duplicate replay input file")
	}
	return nil
}

func validPath(path string) bool {
	return fs.ValidPath(path) && path != "." && !strings.ContainsAny(path, "\\:\x00")
}

// Verify checks every adopted file before any fixture can execute. A missing
// file or even a whitespace-only change fails with the affected path.
func (r Record) Verify(corpus fs.FS) error {
	if err := r.Validate(); err != nil {
		return err
	}
	groups := [][]File{r.Schemas, r.Fixtures}
	if r.Replay != nil {
		groups = append(groups, []File{{Path: r.Replay.Path, SHA256: r.Replay.SHA256}})
	}
	for _, files := range groups {
		for _, f := range files {
			raw, err := fs.ReadFile(corpus, f.Path)
			if err != nil {
				return fmt.Errorf("CRW pin %s: %w", f.Path, err)
			}
			actual := fmt.Sprintf("%x", sha256.Sum256(raw))
			if actual != f.SHA256 {
				return fmt.Errorf("CRW pin %s: SHA-256 %s, want %s", f.Path, actual, f.SHA256)
			}
		}
	}
	return nil
}
