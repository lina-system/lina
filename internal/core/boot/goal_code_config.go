// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	goal "github.com/thisisjun786/lina/internal/core/work/goal"
)

// Code-work connections belong to the installed owner, outside repositories and
// model inputs. An empty installation still offers goals but grants no targets.
type codeCommand struct {
	Path string   `json:"path"`
	Args []string `json:"args,omitempty"`
}
type codeService struct {
	Target string      `json:"target"`
	Public bool        `json:"public"`
	Deploy codeCommand `json:"deploy"`
	Probe  codeCommand `json:"probe"`
}
type codeRepository struct {
	Clone      string                 `json:"clone"`
	Base       string                 `json:"base"`
	DesignRefs []string               `json:"design_refs"`
	QA         codeCommand            `json:"qa"`
	Checks     map[string]codeCommand `json:"checks"`
	Service    *codeService           `json:"service,omitempty"`
}
type codeConnections struct {
	EvidenceChecks map[string]codeEvidenceConnection `json:"evidence_checks,omitempty"`
	HostReport     *codeCommand                      `json:"host_report,omitempty"`
	Repositories   map[string]codeRepository         `json:"repositories"`
	Limits         goal.Resources                    `json:"limits"`
}

// Read-only connectors are fixed by the owner, not supplied by a goal. Binding
// the repository preserves the goal's existing scope and authority boundary.
type codeEvidenceConnection struct {
	Repository string            `json:"repository"`
	Target     string            `json:"target"`
	Kind       goal.EvidenceKind `json:"kind"`
	Command    codeCommand       `json:"command"`
}

func readCodeConnections(root string) (codeConnections, error) {
	cfg := codeConnections{Repositories: map[string]codeRepository{}, Limits: goal.Resources{CPU: 1, MemoryMB: 4096, DiskMB: 16384}}
	path := filepath.Join(root, "system", "code-work.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 256<<10 {
		return cfg, errors.New("invalid code-work configuration permissions or size")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&cfg); err != nil {
		return cfg, errors.New("invalid code-work configuration")
	}
	if d.Decode(new(any)) != io.EOF {
		return cfg, errors.New("invalid code-work configuration")
	}
	if cfg.Limits.CPU <= 0 || cfg.Limits.MemoryMB <= 0 || cfg.Limits.DiskMB <= 0 {
		return cfg, errors.New("invalid code-work capacity")
	}
	if cfg.HostReport != nil && !validCodeCommand(*cfg.HostReport) {
		return cfg, errors.New("invalid code-work host report")
	}
	for name, repo := range cfg.Repositories {
		if strings.Count(name, "/") != 1 || !filepath.IsAbs(repo.Clone) || repo.Base == "" || len(repo.DesignRefs) == 0 || !validCodeCommand(repo.QA) {
			return cfg, errors.New("invalid code-work repository")
		}
		canonical, err := filepath.EvalSymlinks(repo.Clone)
		if err != nil || canonical != repo.Clone {
			return cfg, errors.New("invalid code-work clone")
		}
		for method, check := range repo.Checks {
			if method == "" || strings.HasPrefix(method, "code:") || !validCodeCommand(check) {
				return cfg, errors.New("invalid code-work check")
			}
		}
		if s := repo.Service; s != nil && (s.Target == "" || !validCodeCommand(s.Deploy) || !validCodeCommand(s.Probe)) {
			return cfg, errors.New("invalid code-work service")
		}
	}
	for method, c := range cfg.EvidenceChecks {
		_, bound := cfg.Repositories[c.Repository]
		if !bound || method == "" || strings.HasPrefix(method, "code:") || c.Target == "" || !validCodeCommand(c.Command) {
			return cfg, errors.New("invalid evidence connection")
		}
		switch method {
		case "merge.read", "qa.read", "deploy.read", "runtime.probe":
			return cfg, errors.New("reserved evidence method")
		}
		switch c.Kind {
		case goal.ReadEvidence, goal.RunEvidence, goal.CitationEvidence, goal.JudgmentEvidence:
		default:
			return cfg, errors.New("invalid evidence kind")
		}
	}
	return cfg, nil
}
func validCodeCommand(c codeCommand) bool {
	if !filepath.IsAbs(c.Path) || strings.ContainsRune(c.Path, 0) {
		return false
	}
	for _, arg := range c.Args {
		if strings.ContainsRune(arg, 0) {
			return false
		}
	}
	return true
}
