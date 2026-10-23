// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// A committed admission owns its base, just as it owns its method snapshot.
// Only new admissions resolve a moving branch. Git reads happen after the
// caller's authority check and before the manager, queue or workspace exists.
func (s *Service) baseForAdmission(ctx context.Context, origin Origin, callID string, d Definition) (string, string, error) {
	var raw []byte
	err := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT record FROM worker_dispatch WHERE id=?", admissionID(origin, callID)).Scan(&raw)
	})
	if err == nil {
		var saved Record
		if err = json.Unmarshal(raw, &saved); err != nil {
			return "", "", err
		}
		// recordForAdmission already matched the complete definition and origin.
		if saved.BaseCommit != "" {
			return saved.BaseCommit, saved.RepositoryKey, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	if s.cfg.Activities == nil || s.cfg.Git == "" {
		return "", "", work.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	l, err := s.cfg.Activities.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "validate work definition"})
	if err != nil {
		return "", "", err
	}
	defer l.Release()
	owner := proc.NewOwner()
	defer owner.Close(context.Background())
	git := func(args ...string) (string, error) { return s.command(ctx, l, owner, d.Repository, args...) }
	for _, ref := range []string{d.Base, taskBranch(d.Branch, admissionID(origin, callID))} {
		if _, err = git("check-ref-format", "--branch", ref); err != nil {
			return "", "", work.ErrInvalid
		}
	}
	base, err := git("rev-parse", "--verify", d.Base+"^{commit}")
	if err != nil {
		return "", "", work.ErrInvalid
	}
	for _, ref := range d.DesignRefs {
		file, anchor, _ := strings.Cut(ref, "#")
		body, err := git("show", base+":"+file)
		if err != nil || !hasHeading(body, anchor) {
			return "", "", work.ErrInvalid
		}
	}
	key := d.Repository
	if remote, remoteErr := git("remote", "get-url", "origin"); remoteErr == nil {
		if strings.HasPrefix(remote, "https://github.com/") || strings.HasPrefix(remote, "git@github.com:") {
			key = "github.com/" + strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(remote, "https://github.com/"), "git@github.com:"), ".git"))
		}
	}
	return base, key, nil
}

// Parse Markdown, so code examples and plain text cannot stand in for a
// contract section. Use rendered heading text and GitHub-style unique anchors.
func hasHeading(body, anchor string) bool {
	if anchor == "" {
		return false
	}
	source := []byte(body)
	document := goldmark.New().Parser().Parse(text.NewReader(source))
	seen := map[string]bool{}
	found := false
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || node.Kind() != ast.KindHeading {
			return ast.WalkContinue, nil
		}
		var slug strings.Builder
		var title strings.Builder
		_ = ast.Walk(node, func(part ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			switch part.(type) {
			case *ast.Text, *ast.String, *ast.AutoLink:
				title.Write(part.Text(source))
			}
			return ast.WalkContinue, nil
		})
		for _, r := range strings.ToLower(html.UnescapeString(title.String())) {
			switch {
			case unicode.IsLetter(r), unicode.IsNumber(r), unicode.IsMark(r), r == '-', r == '_':
				slug.WriteRune(r)
			case unicode.IsSpace(r):
				slug.WriteByte('-')
			}
		}
		base := slug.String()
		id := base
		for n := 1; seen[id]; n++ {
			id = base + "-" + strconv.Itoa(n)
		}
		seen[id] = true
		if id == anchor {
			found = true
			return ast.WalkStop, nil
		}
		return ast.WalkSkipChildren, nil
	})
	return found
}

// Other tasks may add their own branch/base refs in the shared parent clone.
// The installed ref guard protects their ownership; comparing their moving
// heads would turn legitimate disjoint work into a false scope violation.
func protectedTaskRef(ref, task string) bool {
	name, _, _ := strings.Cut(ref, " ")
	if strings.HasPrefix(name, "refs/heads/") {
		return name == "refs/heads/dev" || name == "refs/heads/main" || name == "refs/heads/master"
	}
	if strings.HasPrefix(name, "refs/lina/base/") {
		return name == "refs/lina/base/"+task
	}
	return name != ""
}
