// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"html"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// Resolve only new admissions. The caller has checked authority; a replay
// retains its persisted base and manager decision without rereading Git.
func (s *Service) resolveBase(ctx context.Context, d Definition) (string, error) {
	if s.cfg.Activities == nil || s.cfg.Git == "" {
		return "", work.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	l, err := s.cfg.Activities.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "validate work definition"})
	if err != nil {
		return "", err
	}
	defer l.Release()
	owner := proc.NewOwner()
	defer owner.Close(context.Background())
	git := func(args ...string) (string, error) { return s.command(ctx, l, owner, d.Repository, args...) }
	for _, ref := range []string{d.Base, d.Branch} {
		if _, err = git("check-ref-format", "--branch", ref); err != nil {
			return "", work.ErrInvalid
		}
	}
	base, err := git("rev-parse", "--verify", d.Base+"^{commit}")
	if err != nil {
		return "", work.ErrInvalid
	}
	for _, ref := range d.DesignRefs {
		file, anchor, _ := strings.Cut(ref, "#")
		body, err := git("show", base+":"+file)
		if err != nil || !hasHeading(body, anchor) {
			return "", work.ErrInvalid
		}
	}
	return base, nil
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
