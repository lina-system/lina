// SPDX-License-Identifier: Apache-2.0
package ghclient

import (
	"context"
	"strings"
	"testing"
)

func TestClosePinsIdentityAndConfirmsOnce(t *testing.T) {
	open := mergeBundle(sha)[0]
	closed := fakeResponse{200, "", `{"number":140,"state":"closed","merged":false,"head":{"sha":"` + sha + `"},"base":{"ref":"dev","repo":{"full_name":"o/r"}}}`}
	f := &fakeRunner{replies: []fakeResponse{open, {200, "", ""}, closed, {200, "", `[{"body":"P1 major: Missing behavior."}]`}}}
	c := testClient(t, f)
	claims := 0
	r, err := c.ClosePR(t.Context(), Ref{"o/r", 140}, "dev", sha, "P1 major: Missing behavior.", func(context.Context) error { claims++; return nil })
	if err != nil || r.State != "closed" || r.Head != sha || claims != 1 || len(f.replies) != 0 {
		t.Fatalf("close=%+v error=%v claims=%d unread=%d", r, err, claims, len(f.replies))
	}
	if got := strings.Join(f.args[1], " "); got != "pr close 140 --repo github.com/o/r --comment P1 major: Missing behavior." {
		t.Fatal(got)
	}
}

func TestCloseRefusesChangedHeadBeforeIntent(t *testing.T) {
	f := &fakeRunner{replies: []fakeResponse{mergeBundle(nextSHA)[0]}}
	c := testClient(t, f)
	r, err := c.ClosePR(t.Context(), Ref{"o/r", 140}, "dev", sha, "P1 major: Missing behavior.", func(context.Context) error { t.Fatal("stale close admitted"); return nil })
	if err != nil || r.Cause != "head_changed" || len(f.args) != 1 {
		t.Fatalf("close=%+v error=%v writes=%d", r, err, len(f.args))
	}
}

func TestCloseRequiresObservedReason(t *testing.T) {
	closed := fakeResponse{200, "", `{"number":140,"state":"closed","merged":false,"head":{"sha":"` + sha + `"},"base":{"ref":"dev","repo":{"full_name":"o/r"}}}`}
	f := &fakeRunner{replies: []fakeResponse{closed, {200, "", `[]`}}}
	c := testClient(t, f)
	r, err := c.ClosePR(t.Context(), Ref{"o/r", 140}, "dev", sha, "P1 gap: Missing result.", func(context.Context) error { t.Fatal("already closed PR written"); return nil })
	if err != nil || r.State == "closed" || r.Cause != "close_reason_missing" {
		t.Fatalf("closed state invented the reason: %+v %v", r, err)
	}
}
