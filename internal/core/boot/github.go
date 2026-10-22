// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ghclient"
	"github.com/thisisjun786/lina/kit/protocol"
	"strconv"
	"time"
)

type githubService struct {
	client      *ghclient.Client
	merges      *authority.Merges
	agentExists func(context.Context, string) bool
}

func (g githubService) ObserveGitHub(ctx context.Context, session clientsrv.Session, p protocol.WorkRequest) (protocol.WorkResult, error) {
	if *p.Operation != "github_read" && *p.Operation != "github_watch" {
		return g.mergeRequest(ctx, session, p)
	}
	number, err := strconv.Atoi(*p.PrNumber)
	if err != nil {
		return protocol.WorkResult{}, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
	}
	ref := ghclient.Ref{Repository: *p.Repository, Number: number}
	var s ghclient.Snapshot
	switch *p.Operation {
	case "github_read":
		s, err = g.client.Read(ctx, ref)
	case "github_watch":
		var w *ghclient.Watch
		w, err = g.client.Watch(ref)
		if err == nil {
			s, err = w.Next(ctx, 0)
		}
	default:
		return protocol.WorkResult{}, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
	}
	if err != nil && s.Cause == "" {
		return protocol.WorkResult{}, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
	}
	checks := []protocol.GithubCheck{}
	reviews := []protocol.GithubReview{}
	for _, c := range s.Checks {
		checks = append(checks, protocol.GithubCheck{Name: c.Name, State: c.State, Conclusion: c.Conclusion})
	}
	for _, r := range s.Reviews {
		comments := []protocol.GithubComment{}
		for _, v := range r.Comments {
			comments = append(comments, protocol.GithubComment{ID: v.ID, Body: v.Body, Path: v.Path, URL: v.URL})
		}
		reviews = append(reviews, protocol.GithubReview{ID: r.ID, Resolved: r.IsResolved, Outdated: r.IsOutdated, Comments: comments})
	}
	revision := strconv.FormatUint(s.Revision, 10)
	n := strconv.Itoa(s.Ref.Number)
	result := protocol.WorkResult{Repository: &s.Ref.Repository, PrNumber: &n, Head: &s.Head, State: &s.State, Ci: &s.CI, Cause: &s.Cause, Done: &s.Done, Revision: &revision, Checks: &checks, Reviews: &reviews}
	if !s.RetryAt.IsZero() {
		retry := s.RetryAt.UTC().Format(time.RFC3339Nano)
		result.RetryAt = &retry
	}
	return result, nil
}
