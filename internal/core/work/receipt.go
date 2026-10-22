// SPDX-License-Identifier: Apache-2.0
package work

import (
	"crypto/sha256"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
)

func digest(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
func ManifestHash(manifest []Artifact) string {
	entries := slices.Clone(manifest)
	slices.SortFunc(entries, func(a, b Artifact) int { return strings.Compare(a.Path, b.Path) })
	lines := make([]string, len(entries))
	for i, a := range entries {
		lines[i] = a.Path + ":" + a.SHA256
	}
	return digest(strings.Join(lines, "\n"))
}
func ReceiptID(r Receipt) string {
	fields := []string{r.RelationshipID, strconv.Itoa(r.ExecutionGeneration)}
	if r.Outcome == "ready_for_review" {
		fields = append(fields, r.RevisionHash, r.Outcome)
	} else {
		fields = append(fields, r.Outcome, r.TurnRef.TurnID, strconv.Itoa(r.Attempt))
	}
	return digest(strings.Join(fields, "|"))[:32]
}
func validHash(s string) bool { return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == "" }
func absolute(p string) bool {
	return strings.HasPrefix(p, "/") && path.Clean(p) == p && !strings.ContainsAny(p, "\x00\n\r")
}
func inside(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

func validateReceipt(c Card, r Receipt) error {
	if c.Run == nil || r.RelationshipID != c.Task.ID || r.ExecutionGeneration != c.Run.Generation || r.TurnRef.ThreadID != c.Run.ThreadID || r.TurnRef.TurnID != c.Run.TurnID {
		return ErrState
	}
	if r.SchemaVersion != 1 || r.Attempt < 1 || r.Producer != "child" || r.EmittedAt.IsZero() || r.EventID != ReceiptID(r) || !validHash(r.RevisionHash) {
		return ErrInvalid
	}
	switch r.Outcome {
	case "ready_for_review":
		if len(r.Manifest) == 0 || r.RevisionHash != ManifestHash(r.Manifest) || r.TurnRef.TurnStatus != "completed" {
			return ErrInvalid
		}
	case "failed":
		if r.TurnRef.TurnStatus != "failed" {
			return ErrInvalid
		}
	case "interrupted":
		if r.TurnRef.TurnStatus != "interrupted" {
			return ErrInvalid
		}
	case "blocked_needs_input":
		if r.TurnRef.TurnStatus != "completed" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if len(r.Manifest) == 0 && r.RevisionHash != strings.Repeat("0", 64) {
		return ErrInvalid
	}
	if len(r.Manifest) > 0 && r.RevisionHash != ManifestHash(r.Manifest) {
		return ErrInvalid
	}
	if len(r.Provenance) != len(r.Manifest) {
		return ErrInvalid
	}
	seenPaths := map[string]bool{}
	seenArtifacts := map[string]bool{}
	for _, a := range r.Manifest {
		if !absolute(a.Path) || !validHash(a.SHA256) || (a.Bytes != nil && *a.Bytes < 0) || seenPaths[a.Path] {
			return ErrInvalid
		}
		seenPaths[a.Path] = true
		allowed := false
		for _, root := range c.Task.ArtifactRoots {
			allowed = allowed || inside(a.Path, root)
		}
		if !allowed {
			return ErrInvalid
		}
		found := false
		for _, p := range r.Provenance {
			if p.Path != a.Path {
				continue
			}
			if found || p.ArtifactID == "" || seenArtifacts[p.ArtifactID] || p.TaskID != c.Task.ID || p.NodeID != c.Run.NodeID || p.SHA256 != a.SHA256 || p.ObservedAt.IsZero() || p.ObservedAt.After(r.EmittedAt) {
				return ErrInvalid
			}
			seenArtifacts[p.ArtifactID] = true
			found = true
		}
		if !found {
			return ErrInvalid
		}
	}
	if c.TurnStatus != "" && c.TurnStatus != r.TurnRef.TurnStatus {
		return ErrState
	}
	return nil
}
