// SPDX-License-Identifier: Apache-2.0
package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Check struct {
	Type       string `json:"__typename"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}
type Review struct {
	State  string `json:"state"`
	Body   string `json:"body"`
	Commit struct {
		OID string `json:"oid"`
	} `json:"commit"`
}
type ReviewComment struct {
	CommitID string `json:"commit_id"`
	Body     string `json:"body"`
}
type PullRequest struct {
	Number   int             `json:"number"`
	URL      string          `json:"url"`
	State    string          `json:"state"`
	Draft    bool            `json:"isDraft"`
	Branch   string          `json:"headRefName"`
	Base     string          `json:"baseRefName"`
	Head     string          `json:"headRefOid"`
	Checks   []Check         `json:"statusCheckRollup"`
	Reviews  []Review        `json:"reviews"`
	Comments []ReviewComment `json:"inline_comments,omitempty"`
}

var blocking = regexp.MustCompile(`(?im)(\[\s*P[01](\s+Badge)?\s*\]|^[#* \t-]*P[01]\s*[:—-])`)
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Verify establishes only delivery readiness for this exact branch/head. Merge
// and design acceptance remain Core's separate judgment, never an adapter act.
func (p PullRequest) Verify(repository, branch, base, head string) error {
	if !repositoryName.MatchString(repository) || p.Number <= 0 || p.URL != fmt.Sprintf("https://github.com/%s/pull/%d", repository, p.Number) || p.State != "OPEN" || p.Draft || p.Branch != branch || p.Base != base || p.Head != head || len(head) != 40 || strings.Trim(head, "0123456789abcdef") != "" {
		return errors.New("pull request identity or head mismatch")
	}
	if len(p.Checks) == 0 {
		return errors.New("pull request checks missing")
	}
	for _, c := range p.Checks {
		if c.Type == "CheckRun" {
			if c.Status != "COMPLETED" || c.Conclusion != "SUCCESS" {
				return errors.New("pull request checks not green")
			}
		} else if c.Type == "StatusContext" {
			if c.State != "SUCCESS" {
				return errors.New("pull request status not green")
			}
		} else {
			return errors.New("unknown pull request check")
		}
	}
	reviewed := false
	for _, r := range p.Reviews {
		if r.Commit.OID != head || r.State == "DISMISSED" {
			continue
		}
		if r.State == "CHANGES_REQUESTED" || blocking.MatchString(r.Body) {
			return errors.New("blocking pull request review")
		}
		reviewed = reviewed || r.State == "APPROVED" || r.State == "COMMENTED"
	}
	if !reviewed {
		return errors.New("current head review missing")
	}
	for _, c := range p.Comments {
		if c.CommitID == head && blocking.MatchString(c.Body) {
			return errors.New("blocking inline review")
		}
	}
	return nil
}

// ReadPullRequest reads only fixed gh operations. The selector is the task
// branch, never a URL or shell command parsed from worker output.
type PullRequestReader interface {
	ReadBranch(context.Context, string, string) ([]byte, error)
	ReadComments(context.Context, string, int) ([]byte, error)
}

func ReadPullRequest(ctx context.Context, reader PullRequestReader, repository, branch, base, head string) (PullRequest, error) {
	if !repositoryName.MatchString(repository) || branch == "" || strings.HasPrefix(branch, "-") {
		return PullRequest{}, errors.New("invalid PR lookup")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	read := func() (PullRequest, error) {
		raw, err := reader.ReadBranch(ctx, repository, branch)
		var p PullRequest
		if err == nil {
			err = json.Unmarshal(raw, &p)
		}
		return p, err
	}
	p, err := read()
	if err != nil {
		return p, err
	}
	raw, err := reader.ReadComments(ctx, repository, p.Number)
	if err != nil {
		return p, err
	}
	var pages [][]ReviewComment
	if err = json.Unmarshal(raw, &pages); err != nil {
		return p, err
	}
	if len(pages) == 0 {
		return p, errors.New("missing PR comment observation")
	}
	for _, page := range pages {
		p.Comments = append(p.Comments, page...)
	}
	if err = p.Verify(repository, branch, base, head); err != nil {
		return p, err
	}
	// Re-read after the paginated review read so a moved head cannot inherit
	// checks/comments from the earlier head.
	current, err := read()
	if err != nil {
		return p, err
	}
	current.Comments = p.Comments
	return current, current.Verify(repository, branch, base, head)
}
