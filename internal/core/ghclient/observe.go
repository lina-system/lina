// SPDX-License-Identifier: Apache-2.0
package ghclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (c *Client) observe(ctx context.Context, ref Ref, cache *observation) (s Snapshot, delay time.Duration, err error) {
	s = clone(cache.snapshot)
	s.Ref = ref
	s.CI = "pending"
	s.Cause = ""
	s.Done = false
	s.RetryAt = time.Time{}
	delay = pollInterval
	cache.bytes = 0
	defer func() {
		if data, _ := json.Marshal(s); len(data) > 512<<10 {
			s.Checks = nil
			s.Reviews = nil
			cache.reviewsHead = ""
			err = &readError{cause: "response_too_large"}
		}
		if err != nil {
			s.CI = "pending"
			s.Done = false
			s.Cause = "read_failed"
			var re *readError
			if errors.As(err, &re) {
				s.Cause = re.cause
				if re.retry > 0 {
					delay = re.retry
				}
			}
			if s.Cause == "rate_limited" {
				s.RetryAt = time.Now().Add(delay)
			}
		}
		cache.snapshot = clone(s)
	}()
	body, d, e := c.request(ctx, endpoint(ref), "", cache)
	if e != nil {
		err = e
		return
	}
	delay = d
	var pr struct {
		Number int
		State  string
		Merged bool
		Head   struct{ SHA string }
	}
	if err = decode(body, &pr); err != nil {
		return
	}
	if pr.Number != ref.Number || !shaPattern.MatchString(pr.Head.SHA) || (pr.State != "open" && pr.State != "closed") {
		err = &readError{cause: "invalid_response"}
		return
	}
	if s.Head != pr.Head.SHA {
		prCache := cache.responses[endpoint(ref)]
		cache.responses = map[string]cached{endpoint(ref): prCache}
		s.Checks = nil
		s.Reviews = nil
		cache.reviewsHead = ""
	}
	s.Head = pr.Head.SHA
	s.State = strings.ToUpper(pr.State)
	if pr.Merged {
		s.State = "MERGED"
	}
	if pr.State == "closed" || pr.Merged {
		s.Done = true
		return
	}
	if cache.reviewsHead != s.Head {
		var reviews []Review
		reviews, err = c.reviews(ctx, ref, s.Head, cache)
		if err != nil {
			return
		}
		s.Reviews = reviews
		cache.reviewsHead = s.Head
	}
	s.Checks, d, err = c.checks(ctx, ref, s.Head, cache)
	if d > delay {
		delay = d
	}
	if err != nil {
		return
	}
	if len(s.Checks) == 0 {
		s.Cause = "checks_missing"
		return
	}
	pending, failed := false, false
	for _, check := range s.Checks {
		if check.State != "completed" {
			pending = true
			continue
		}
		switch check.Conclusion {
		case "success", "neutral", "skipped":
		case "failure", "cancelled", "timed_out", "action_required", "stale", "startup_failure", "error":
			failed = true
		default:
			pending = true
		}
	}
	if pending {
		return
	}
	s.Done = true
	s.CI = "success"
	if failed {
		s.CI = "failure"
	}
	return
}
func (c *Client) checks(ctx context.Context, ref Ref, head string, cache *observation) ([]Check, time.Duration, error) {
	var checks []Check
	delay := time.Duration(0)
	for page := 1; page <= maxPages; page++ {
		body, d, err := c.request(ctx, fmt.Sprintf("repos/%s/commits/%s/check-runs?per_page=100&filter=latest&page=%d", ref.Repository, head, page), "", cache)
		if d > delay {
			delay = d
		}
		if err != nil {
			return nil, delay, err
		}
		var result struct {
			Total *int `json:"total_count"`
			Runs  []struct {
				Name, Status string
				Conclusion   *string
			} `json:"check_runs"`
		}
		if err := decode(body, &result); err != nil {
			return nil, delay, err
		}
		if result.Total == nil || result.Runs == nil {
			return nil, delay, &readError{cause: "invalid_response"}
		}
		for _, r := range result.Runs {
			conclusion := ""
			if r.Conclusion != nil {
				conclusion = *r.Conclusion
			}
			checks = append(checks, Check{r.Name, r.Status, conclusion})
		}
		if len(checks) >= *result.Total {
			break
		}
		if len(result.Runs) == 0 || page == maxPages {
			return nil, delay, &readError{cause: "incomplete_checks"}
		}
	}
	count := 0
	seen := map[string]bool{}
	for page := 1; page <= maxPages; page++ {
		body, d, err := c.request(ctx, fmt.Sprintf("repos/%s/commits/%s/status?per_page=100&page=%d", ref.Repository, head, page), "", cache)
		if d > delay {
			delay = d
		}
		if err != nil {
			return nil, delay, err
		}
		var result struct {
			SHA      string
			Total    *int `json:"total_count"`
			Statuses []struct{ Context, State string }
		}
		if err := decode(body, &result); err != nil {
			return nil, delay, err
		}
		if result.SHA != head || result.Total == nil || result.Statuses == nil {
			return nil, delay, &readError{cause: "invalid_response"}
		}
		for _, r := range result.Statuses {
			if !seen[r.Context] {
				seen[r.Context] = true
				state := "completed"
				if r.State != "success" && r.State != "failure" && r.State != "error" {
					state = "pending"
				}
				checks = append(checks, Check{r.Context, state, r.State})
			}
		}
		count += len(result.Statuses)
		if count >= *result.Total {
			break
		}
		if len(result.Statuses) == 0 || page == maxPages {
			return nil, delay, &readError{cause: "incomplete_checks"}
		}
	}
	return checks, delay, nil
}

type pageInfo struct {
	HasNextPage bool
	EndCursor   string
}
type commentsPage struct {
	Nodes    []Comment
	PageInfo pageInfo
}
type thread struct {
	ID                     string
	IsResolved, IsOutdated bool
	Comments               commentsPage
}

func literal(value string) string { b, _ := json.Marshal(value); return string(b) }

const commentFields = `nodes { id body path url } pageInfo { hasNextPage endCursor }`

func (c *Client) reviews(ctx context.Context, ref Ref, head string, cache *observation) ([]Review, error) {
	parts := strings.Split(ref.Repository, "/")
	after := "null"
	seen := map[string]bool{}
	var reviews []Review
	for page := 0; page < maxPages; page++ {
		query := fmt.Sprintf(`query { repository(owner:%s,name:%s) { pullRequest(number:%d) { headRefOid reviewThreads(first:100,after:%s) { nodes { id isResolved isOutdated comments(first:100) { %s } } pageInfo { hasNextPage endCursor } } } } }`, literal(parts[0]), literal(parts[1]), ref.Number, after, commentFields)
		body, _, err := c.request(ctx, "graphql", query, cache)
		if err != nil {
			return nil, err
		}
		var result struct {
			Data struct {
				Repository struct {
					PullRequest *struct {
						HeadRefOid    string
						ReviewThreads struct {
							Nodes    []thread
							PageInfo pageInfo
						}
					}
				}
			}
		}
		if err := decode(body, &result); err != nil {
			return nil, err
		}
		pr := result.Data.Repository.PullRequest
		if pr == nil {
			return nil, &readError{cause: "invalid_response"}
		}
		if pr.HeadRefOid != head {
			return nil, &readError{cause: "head_changed"}
		}
		for _, th := range pr.ReviewThreads.Nodes {
			if th.ID == "" {
				return nil, &readError{cause: "invalid_response"}
			}
			comments, err := c.threadComments(ctx, th, cache)
			if err != nil {
				return nil, err
			}
			reviews = append(reviews, Review{th.ID, th.IsResolved, th.IsOutdated, comments})
		}
		p := pr.ReviewThreads.PageInfo
		if !p.HasNextPage {
			return reviews, nil
		}
		if p.EndCursor == "" || seen[p.EndCursor] {
			break
		}
		seen[p.EndCursor] = true
		after = literal(p.EndCursor)
	}
	return nil, &readError{cause: "incomplete_reviews"}
}
func (c *Client) threadComments(ctx context.Context, th thread, cache *observation) ([]Comment, error) {
	comments := th.Comments.Nodes
	p := th.Comments.PageInfo
	seen := map[string]bool{}
	for page := 0; p.HasNextPage; page++ {
		if page >= maxPages || p.EndCursor == "" || seen[p.EndCursor] {
			return nil, &readError{cause: "incomplete_reviews"}
		}
		seen[p.EndCursor] = true
		query := fmt.Sprintf(`query { node(id:%s) { ... on PullRequestReviewThread { comments(first:100,after:%s) { %s } } } }`, literal(th.ID), literal(p.EndCursor), commentFields)
		body, _, err := c.request(ctx, "graphql", query, cache)
		if err != nil {
			return nil, err
		}
		var result struct {
			Data struct {
				Node *struct{ Comments commentsPage }
			}
		}
		if err := decode(body, &result); err != nil {
			return nil, err
		}
		if result.Data.Node == nil {
			return nil, &readError{cause: "invalid_response"}
		}
		comments = append(comments, result.Data.Node.Comments.Nodes...)
		p = result.Data.Node.Comments.PageInfo
	}
	return comments, nil
}
