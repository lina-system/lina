// SPDX-License-Identifier: Apache-2.0
package ghclient

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const mergedSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const nextSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func mergeBundle(head string) []fakeResponse {
	r := bundle("OPEN", "COMPLETED", "SUCCESS", head)
	r[0].body = strings.TrimSuffix(r[0].body, "}") + `,"base":{"ref":"dev","repo":{"full_name":"o/r"}}}`
	return r
}
func mergedBundle(target string) []fakeResponse {
	return []fakeResponse{
		{200, "", `{"number":140,"state":"closed","merged":true,"head":{"sha":"` + sha + `"},"base":{"ref":"dev","repo":{"full_name":"o/r"}},"merge_commit_sha":"` + mergedSHA + `"}`},
		{200, "", `{"sha":"` + mergedSHA + `"}`},
		{200, "", `{"ref":"refs/heads/dev","object":{"type":"commit","sha":"` + target + `"}}`},
		{200, "", `{"total_count":1,"check_runs":[{"name":"foundation","status":"completed","conclusion":"success"}]}`},
		{200, "", `{"sha":"` + mergedSHA + `","total_count":0,"statuses":[]}`},
	}
}
func TestMergeMovedHeadIsReadAndNeverWritten(t *testing.T) {
	f := &fakeRunner{replies: mergeBundle(nextSHA)}
	c := testClient(t, f)
	permitted := false
	r, err := c.Merge(t.Context(), Ref{"o/r", 140}, "dev", sha, func(context.Context) error { permitted = true; return nil })
	if err != nil || r.Cause != "head_changed" || r.Head != nextSHA || permitted {
		t.Fatalf("result=%+v err=%v permitted=%v", r, err, permitted)
	}
	for _, args := range f.args {
		if args[0] != "api" {
			t.Fatalf("wrote after moved head: %v", args)
		}
	}
}
func TestMergeHeadMovesDuringReviewRead(t *testing.T) {
	for _, kind := range []string{"new_head", "original_head", "moves_again", "read_failure"} {
		t.Run(kind, func(t *testing.T) {
			// REST saw the accepted head, but the review read saw a newer one.
			replies := []fakeResponse{mergeBundle(sha)[0], mergeBundle(nextSHA)[1]}
			wantHead, wantCI, wantError := nextSHA, "success", false
			fresh := mergeBundle(nextSHA)
			switch kind {
			case "original_head":
				wantHead = sha
				fresh = mergeBundle(sha)
			case "moves_again":
				wantCI, wantError = "pending", true
				fresh = []fakeResponse{fresh[0], mergeBundle(mergedSHA)[1]}
			case "read_failure":
				wantCI, wantError = "pending", true
				fresh = []fakeResponse{fresh[0], {503, "", `{}`}}
			}
			f := &fakeRunner{replies: append(replies, fresh...)}
			c := testClient(t, f)
			r, err := c.Merge(t.Context(), Ref{"o/r", 140}, "dev", sha, func(context.Context) error {
				t.Fatal("changed head admitted for merge")
				return nil
			})
			if (err != nil) != wantError || r.State != "blocked" || r.Cause != "head_changed" || r.Head != wantHead || r.CI != wantCI {
				t.Fatalf("result=%+v err=%v", r, err)
			}
			if len(f.replies) != 0 || len(f.args) != 2+len(fresh) {
				t.Fatalf("refresh calls=%d unread=%d", len(f.args), len(f.replies))
			}
			for _, args := range f.args {
				if args[0] != "api" {
					t.Fatalf("wrote after moved head: %v", args)
				}
			}
		})
	}
}
func TestMergeProtectedMismatchPreventsWrite(t *testing.T) {
	replies := mergeBundle(sha)
	replies = append(replies, replies[0])
	f := &fakeRunner{replies: replies}
	c := testClient(t, f)
	_, err := c.Merge(t.Context(), Ref{"o/r", 140}, "dev", sha, func(context.Context) error { return errors.New("protected_ref_changed") })
	if err == nil || !strings.Contains(err.Error(), "protected_ref_changed") {
		t.Fatalf("err=%v", err)
	}
	for _, args := range f.args {
		if args[0] != "api" {
			t.Fatalf("write: %v", args)
		}
	}
}
func TestMergePinsLastHeadAndConfirmsResult(t *testing.T) {
	replies := mergeBundle(sha)
	replies = append(replies, replies[0], fakeResponse{200, "", ""})
	replies = append(replies, mergedBundle(mergedSHA)...)
	f := &fakeRunner{replies: replies}
	c := testClient(t, f)
	c.cfg.Env = append(c.cfg.Env, "GH_HOST=enterprise.example.invalid")
	permits := 0
	r, err := c.Merge(t.Context(), Ref{"o/r", 140}, "dev", sha, func(context.Context) error { permits++; return nil })
	if err != nil || r.State != "merged" || r.Commit != mergedSHA || r.Source != "receipt" || permits != 1 {
		t.Fatalf("%+v %v permits=%d", r, err, permits)
	}
	count := 0
	for _, args := range f.args {
		if args[0] == "pr" {
			count++
			got := strings.Join(args, " ")
			if got != "pr merge 140 --repo github.com/o/r --merge --match-head-commit "+sha {
				t.Fatal(got)
			}
		}
	}
	if count != 1 {
		t.Fatalf("merges=%d", count)
	}
}
func TestObserveMergeRequiresCommitAndExactTarget(t *testing.T) {
	for _, target := range []string{mergedSHA, nextSHA} {
		t.Run(target, func(t *testing.T) {
			f := &fakeRunner{replies: mergedBundle(target)}
			c := testClient(t, f)
			r, err := c.ObserveMerge(t.Context(), Ref{"o/r", 140}, "dev")
			if target == mergedSHA {
				if err != nil || r.Source != "observed" || r.CI != "success" || r.Commit != mergedSHA {
					t.Fatalf("%+v %v", r, err)
				}
			} else if err == nil || r.State == "merged" {
				t.Fatalf("unverified result: %+v %v", r, err)
			}
		})
	}
}

func TestMergeRefusalAfterPinRereadsWithoutRetry(t *testing.T) {
	replies := mergeBundle(sha)
	replies = append(replies, replies[0], fakeResponse{409, "", `{"message":"head moved"}`}, mergeBundle(nextSHA)[0])
	replies = append(replies, mergeBundle(nextSHA)...)
	f := &fakeRunner{replies: replies}
	c := testClient(t, f)
	r, err := c.Merge(t.Context(), Ref{"o/r", 140}, "dev", sha, func(context.Context) error { return nil })
	if err != nil || r.Cause != "head_changed" || r.Head != nextSHA || r.State == "merged" {
		t.Fatalf("%+v %v", r, err)
	}
	writes := 0
	for _, args := range f.args {
		if args[0] == "pr" {
			writes++
		}
	}
	if writes != 1 || len(f.replies) != 0 {
		t.Fatalf("writes=%d unread=%d", writes, len(f.replies))
	}
}
func TestMergeFreshReadFindsExternalMerge(t *testing.T) {
	f := &fakeRunner{replies: append([]fakeResponse{mergedBundle(mergedSHA)[0]}, mergedBundle(mergedSHA)...)}
	c := testClient(t, f)
	r, err := c.Merge(t.Context(), Ref{"o/r", 140}, "dev", sha, func(context.Context) error { t.Fatal("attempted own write"); return nil })
	if err != nil || r.Source != "observed" || r.State != "merged" {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestMergeBlocksNonGreenAndOpenPriorityFindings(t *testing.T) {
	for _, kind := range []string{"pending", "failure", "P0", "P1"} {
		t.Run(kind, func(t *testing.T) {
			replies := mergeBundle(sha)
			if kind == "pending" {
				replies[2].body = `{"total_count":0,"check_runs":[]}`
			} else if kind == "failure" {
				replies[2].body = strings.ReplaceAll(replies[2].body, "success", "failure")
			} else {
				replies[1].body = strings.ReplaceAll(replies[1].body, "review data", "["+kind+"] unsafe")
			}
			f := &fakeRunner{replies: replies}
			c := testClient(t, f)
			r, err := c.Merge(t.Context(), Ref{"o/r", 140}, "dev", sha, func(context.Context) error { t.Fatal("write admitted"); return nil })
			if err != nil || r.State != "blocked" || r.Cause == "" {
				t.Fatalf("%+v %v", r, err)
			}
		})
	}
}
func TestObserveMergeMissingCommitAndFailedChecks(t *testing.T) {
	for _, kind := range []string{"missing_commit", "wrong_commit", "failed_checks", "missing_checks"} {
		t.Run(kind, func(t *testing.T) {
			replies := mergedBundle(mergedSHA)
			switch kind {
			case "missing_commit":
				replies[1] = fakeResponse{404, "", `{"message":"missing"}`}
			case "wrong_commit":
				replies[1].body = `{"sha":"` + nextSHA + `"}`
			case "failed_checks":
				replies[3].body = strings.ReplaceAll(replies[3].body, "success", "failure")
			case "missing_checks":
				replies[3].body = `{"total_count":0,"check_runs":[]}`
			}
			c := testClient(t, &fakeRunner{replies: replies})
			r, err := c.ObserveMerge(t.Context(), Ref{"o/r", 140}, "dev")
			if strings.Contains(kind, "commit") {
				if err == nil || r.State == "merged" {
					t.Fatalf("unverified %+v %v", r, err)
				}
			} else if err != nil || r.State != "merged" || r.Cause != "merged_without_green_checks" {
				t.Fatalf("%+v %v", r, err)
			}
		})
	}
}

func TestHistoricalMergeRemainsVerifiedAfterBaseAdvances(t *testing.T) {
	replies := mergedBundle(nextSHA)
	replies = append(replies, fakeResponse{200, "", `{"status":"ahead","merge_base_commit":{"sha":"` + mergedSHA + `"}}`})
	f := &fakeRunner{replies: replies}
	c := testClient(t, f)
	r, err := c.ReadLanded(t.Context(), Ref{"o/r", 140}, "dev", sha, mergedSHA)
	if err != nil || r.State != "merged" || r.Commit != mergedSHA {
		t.Fatalf("historical merge lost: %+v %v", r, err)
	}
	for _, args := range f.args {
		if args[0] != "api" {
			t.Fatal("observation wrote to GitHub")
		}
	}
}
func TestHistoricalMergeRejectsRemovedCommit(t *testing.T) {
	replies := mergedBundle(nextSHA)
	replies = append(replies, fakeResponse{200, "", `{"status":"diverged","merge_base_commit":{"sha":"` + nextSHA + `"}}`})
	c := testClient(t, &fakeRunner{replies: replies})
	if r, err := c.ReadLanded(t.Context(), Ref{"o/r", 140}, "dev", sha, mergedSHA); err == nil || r.State == "merged" {
		t.Fatalf("removed merge accepted: %+v %v", r, err)
	}
}

func TestHistoricalMergeReconcilesAnUnreceiptedWrite(t *testing.T) {
	replies := mergedBundle(nextSHA)
	replies = append(replies, fakeResponse{200, "", `{"status":"ahead","merge_base_commit":{"sha":"` + mergedSHA + `"}}`})
	c := testClient(t, &fakeRunner{replies: replies})
	r, err := c.ReadLanded(t.Context(), Ref{"o/r", 140}, "dev", sha, "")
	if err != nil || r.Commit != mergedSHA || r.State != "merged" {
		t.Fatalf("uncertain merge did not settle from ancestry: %+v %v", r, err)
	}
}
