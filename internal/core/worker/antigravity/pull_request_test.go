// SPDX-License-Identifier: Apache-2.0
package antigravity

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/ghclient"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPullRequestIsOnlyDeliveryEvidence(t *testing.T) {
	raw, err := os.ReadFile("testdata/pull-request.json")
	if err != nil {
		t.Fatal(err)
	}
	var good PullRequest
	if err = json.Unmarshal(raw, &good); err != nil {
		t.Fatal(err)
	}
	good.Reviews[0].Body = "No P0/P1 findings."
	if err = good.Verify("owner/repo", "task-branch", "dev", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "pending", "red", "stale", "wrong_branch", "wrong_base", "closed", "draft", "unreviewed", "p1", "inline_p0"} {
		t.Run(name, func(t *testing.T) {
			var p PullRequest
			_ = json.Unmarshal(raw, &p)
			switch name {
			case "missing":
				p.Checks = nil
			case "pending":
				p.Checks[0].Status = "IN_PROGRESS"
			case "red":
				p.Checks[0].Conclusion = "FAILURE"
			case "stale":
				p.Head = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			case "wrong_branch":
				p.Branch = "unrelated"
			case "wrong_base":
				p.Base = "main"
			case "closed":
				p.State = "CLOSED"
			case "draft":
				p.Draft = true
			case "unreviewed":
				p.Reviews = nil
			case "p1":
				p.Reviews[0].Body = "[P1] Broken completion handling"
			case "inline_p0":
				p.Comments = []ReviewComment{{CommitID: p.Head, Body: "[P0] Data loss"}}
			}
			if err = p.Verify("owner/repo", "task-branch", "dev", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
				t.Fatal("accepted unverifiable delivery")
			}
		})
	}
}

func TestPRHeadMovementDuringCommentReadRefusesDelivery(t *testing.T) {
	a, ctx, _, dir := fixture(t, `case "$1" in
 pr)
   if [ -f "$fixture/read" ]; then cat "$fixture/moved.json"; else touch "$fixture/read"; cat "$fixture/first.json"; fi ;;
 api) echo '[[]]' ;;
 *) exit 2 ;;
esac
`)
	raw, err := os.ReadFile("testdata/pull-request.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "first.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	var moved PullRequest
	if err = json.Unmarshal(raw, &moved); err != nil {
		t.Fatal(err)
	}
	moved.Head = strings.Repeat("b", 40)
	data, _ := json.Marshal(moved)
	if err = os.WriteFile(filepath.Join(dir, "moved.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := ghclient.New(ctx, ghclient.Config{Path: a.cfg.Binary, Env: append(a.cfg.Environment, "fixture="+dir), Leaser: leaser{}})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	_, err = ReadPullRequest(ctx, reader, "owner/repo", "task-branch", "dev", strings.Repeat("a", 40))
	if err == nil {
		t.Fatal("moving PR head inherited earlier checks and reviews")
	}
}
