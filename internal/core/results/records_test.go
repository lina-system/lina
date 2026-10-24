// SPDX-License-Identifier: Apache-2.0
package results

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/result"
)

func TestSaveCreatesOneVersionedRecord(t *testing.T) {
	t.Parallel()
	_, db, _, _ := setup(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	w, err := OpenRecords(root, db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	s, err := New(db, Config{Journal: LedgerJournal{Surface: ledger.Surface{Kind: "tui"}}, Writer: w})
	if err != nil {
		t.Fatal(err)
	}
	c := present(t, s)
	req := result.SaveRequest{ID: c.ID, Revision: 1, Destination: result.Destination{Kind: "resource"}}
	first, err := s.Save(t.Context(), scope, req)
	if err != nil || first.State != "applied" {
		t.Fatal(first, err)
	}
	second, err := s.Save(t.Context(), scope, req)
	if err != nil || second.SavedAs.ID != first.SavedAs.ID {
		t.Fatal(second, err)
	}
	repo := filepath.Join(root, "versions", "records")
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	if got := strings.TrimSpace(run("rev-list", "--count", "HEAD")); got != "1" {
		t.Fatal(got)
	}
	files := strings.Fields(run("ls-tree", "-r", "--name-only", "HEAD"))
	if len(files) != 1 || !strings.HasPrefix(files[0], "resources/") || strings.Contains(files[0], first.SavedAs.ID) {
		t.Fatal(files)
	}
	body := run("show", "HEAD:"+files[0])
	for _, part := range []string{first.SavedAs.ID, scope.AgentID, scope.TurnID, c.ID, "A complete document"} {
		if !strings.Contains(body, part) {
			t.Errorf("record missing %q", part)
		}
	}
	if got := run("status", "--porcelain"); got != "" {
		t.Fatal("unclean records repo:", got)
	}
	run("fsck", "--strict")
	if err := w.Save(context.Background(), scope, result.Result{ID: c.ID}, *first.SavedAs); err == nil {
		t.Fatal("changed record identity was accepted")
	}
}

func TestRecordsDetectExternalChangeAndRejectPathEscape(t *testing.T) {
	t.Parallel()
	_, db, _, _ := setup(t)
	root := t.TempDir()
	os.Chmod(root, 0700)
	w, err := OpenRecords(root, db, nil, func(context.Context, result.Scope, result.Destination) (string, error) { return "../outside", nil })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ref := result.SavedRef{ID: "record", Version: 1, Destination: result.Destination{Kind: "project", Container: "project"}}
	if err := w.Save(t.Context(), scope, result.Result{Title: "Title"}, ref); err == nil {
		t.Fatal("escaped destination")
	}
	ref.Destination = result.Destination{Kind: "resource"}
	r := result.Result{ID: "result", Revision: 1, Title: "Title", Type: result.DraftType, Text: "Text", Body: []byte(`{"blocks":[{"kind":"paragraph","text":"Text"}],"from_summary":null}`)}
	if err := w.Save(t.Context(), scope, r, ref); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "versions", "records", "resources", "title.md")
	if err := os.WriteFile(path, []byte("outside edit"), 0600); err != nil {
		t.Fatal(err)
	}
	ref.ID = "another"
	if err := w.Save(t.Context(), scope, r, ref); err == nil {
		t.Fatal("outside edit silently committed")
	}
	bytes, err := os.ReadFile(path)
	if err != nil || string(bytes) != "outside edit" {
		t.Fatal(string(bytes), err)
	}
}

func TestSavedFileOwnsBytesAfterPreviewAssetIsTrashed(t *testing.T) {
	t.Parallel()
	_, db, _, _ := setup(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	a, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	actor := assets.Actor{PersonID: scope.PersonID, AgentID: scope.AgentID}
	token, err := a.StageImport(t.Context(), strings.NewReader("synthetic PDF bytes"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := a.Handover(t.Context(), token, "report.pdf", assets.Lina, actor)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenRecords(root, db, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	s, err := New(db, Config{Journal: LedgerJournal{Surface: ledger.Surface{Kind: "tui"}}, Writer: w})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(result.File{Asset: result.AssetRef{ID: original.ID, Revision: 1}, Name: "Report.pdf", MediaType: "application/pdf", Size: original.Size})
	c, err := s.Present(t.Context(), scope, result.PresentRequest{Type: result.FileType, Title: "Report", Summary: "Saved report.", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := s.Save(t.Context(), scope, result.SaveRequest{ID: c.ID, Revision: 1, Destination: result.Destination{Kind: "resource"}})
	if err != nil || receipt.State != "applied" {
		t.Fatal(receipt, err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "versions", "records", "resources", "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	var saved result.Result
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "result: ") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "result: ")), &saved); err != nil {
				t.Fatal(err)
			}
		}
	}
	var f result.File
	if err := json.Unmarshal(saved.Body, &f); err != nil {
		t.Fatal(err)
	}
	if f.Asset.ID == original.ID {
		t.Fatal("saved file reused preview bytes")
	}
	if err := a.Trash(t.Context(), original.ID, actor); err != nil {
		t.Fatal(err)
	}
	content, err := a.Read(t.Context(), f.Asset.ID, int64(f.Asset.Revision))
	if err != nil || string(content) != "synthetic PDF bytes" {
		t.Fatal(string(content), err)
	}
}

func TestRecordsRefuseCommittedOutsideChange(t *testing.T) {
	t.Parallel()
	_, db, _, _ := setup(t)
	root := t.TempDir()
	os.Chmod(root, 0700)
	w, err := OpenRecords(root, db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	r := result.Result{ID: "result", Revision: 1, Title: "Title", Type: result.DraftType, Text: "Text", Body: []byte(`{"blocks":[{"kind":"paragraph","text":"Text"}],"from_summary":null}`)}
	ref := result.SavedRef{ID: "record", Version: 1, Destination: result.Destination{Kind: "resource"}}
	if err := w.Save(t.Context(), scope, r, ref); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "versions", "records")
	if err := os.WriteFile(filepath.Join(repo, "resources", "title.md"), []byte("outside version"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "resources/title.md"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "Fixture outside change"}} {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
	}
	ref.ID = "another"
	if err := w.Save(t.Context(), scope, r, ref); !errors.Is(err, ErrRecordsChanged) {
		t.Fatalf("outside commit: %v", err)
	}
}
