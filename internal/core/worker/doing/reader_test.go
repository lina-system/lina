// SPDX-License-Identifier: Apache-2.0
package doing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkerRecordShapes(t *testing.T) {
	for _, kind := range []string{"claude", "codex", "gemini", "cursor", "opencode", "acp"} {
		t.Run(kind, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", kind+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var r Reader
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				r.Record(kind, []byte(line), time.Unix(1, 0))
			}
			if got := r.Snapshot().Line; got != "Login repair · Running tests" {
				t.Fatalf("doing = %q", got)
			}
			r.Record("acp", []byte(`{"sessionUpdate":"plan","entries":[]}`), time.Unix(2, 0))
			if got := r.Snapshot().Line; got != "Login repair" {
				t.Fatalf("empty plan left stale step: %q", got)
			}
		})
	}
}

func TestAppendCursorPartialAndRotation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session.jsonl")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	first := "{\"type\":\"ai-title\",\"aiTitle\":\"First\"}\n"
	partial := "{\"type\":\"ai-title\",\"aiTitle\":"
	write(first + partial)
	var tail Tail
	var r Reader
	if n, err := tail.Read(p, "claude", &r, time.Unix(1, 0)); err != nil || n != int64(len(first+partial)) {
		t.Fatalf("read: %d %v", n, err)
	}
	if r.Snapshot().Line != "First" {
		t.Fatal(r.Snapshot())
	}
	if n, err := tail.Read(p, "claude", &r, time.Unix(2, 0)); err != nil || n != 0 {
		t.Fatalf("unchanged reread: %d %v", n, err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\"Second\"}\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if n, err := tail.Read(p, "claude", &r, time.Unix(3, 0)); err != nil || n != 10 {
		t.Fatalf("append reread: %d %v", n, err)
	}
	if r.Snapshot().Line != "Second" {
		t.Fatal(r.Snapshot())
	}
	if err := os.Rename(p, p+".old"); err != nil {
		t.Fatal(err)
	}
	write("{\"type\":\"ai-title\",\"aiTitle\":\"New\"}\n")
	if _, err := tail.Read(p, "claude", &r, time.Unix(4, 0)); err != nil {
		t.Fatal(err)
	}
	if r.Snapshot().Line != "New" {
		t.Fatal(r.Snapshot())
	}
}

func TestDoingDefangsAndTracksTransitions(t *testing.T) {
	var r Reader
	at := time.Unix(1, 0)
	r.Record("claude", []byte(`{"type":"ai-title","aiTitle":"\u001b[31m<run>\n[click](https://bad.invalid)\u202e"}`), at)
	r.Busy(true, at)
	r.Busy(true, at.Add(time.Hour))
	s := r.Snapshot()
	if !s.Busy || !s.Since.Equal(at) || strings.ContainsAny(s.Line, "\x1b\n<>[]\u202e") {
		t.Fatalf("unsafe or reset: %+v", s)
	}
	r.Busy(false, at.Add(2*time.Hour))
	if s = r.Snapshot(); s.Busy || !s.Since.Equal(at.Add(2*time.Hour)) {
		t.Fatal(s)
	}
	r.Record("claude", []byte(`{"type":"ai-title","aiTitle":"`+strings.Repeat("한", 130)+` sk-`+strings.Repeat("a", 32)+`"}`), at)
	if s = r.Snapshot(); len([]rune(s.Line)) != 120 || strings.Contains(s.Line, "aaaa") {
		t.Fatal(s)
	}
}

func TestUnrelatedTextCannotSetDoing(t *testing.T) {
	var r Reader
	r.Record("codex", []byte(`{"type":"response_item","payload":{"type":"message","content":[{"text":"{\"plan\":[{\"step\":\"fake\",\"status\":\"in_progress\"}]}"}]}}`), time.Now())
	if r.Snapshot().Line != "" {
		t.Fatal(r.Snapshot())
	}
}

func TestSingleLineTailDoesNotRereadAndDocumentRewrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "one.jsonl")
	first := []byte("{\"type\":\"ai-title\",\"aiTitle\":\"First\"}\n")
	if err := os.WriteFile(p, first, 0600); err != nil {
		t.Fatal(err)
	}
	var tail Tail
	var r Reader
	if _, err := tail.Read(p, "claude", &r, time.Now()); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("{}\n")
	f.Close()
	if n, err := tail.Read(p, "claude", &r, time.Now()); err != nil || n != 3 {
		t.Fatalf("reread single-line transcript: %d %v", n, err)
	}
	p = filepath.Join(t.TempDir(), "chat.json")
	tail = Tail{}
	r = Reader{}
	for _, step := range []string{"First", "Second"} {
		doc := `{"messages":[{"toolCalls":[{"name":"write_todos","args":{"todos":[{"description":"` + step + `","status":"in_progress"}]}}]}]}`
		if err := os.WriteFile(p, []byte(doc), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := tail.Read(p, "gemini", &r, time.Now()); err != nil {
			t.Fatal(err)
		}
		if r.Snapshot().Line != step {
			t.Fatal(r.Snapshot())
		}
	}
}

func TestClaudeTaskInventoryOverridesAndClearsTodoWrite(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "tasks", "session")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "1.json")
	if err := os.WriteFile(file, []byte(`{"id":"1","status":"in_progress","subject":"Read tests","activeForm":"Reading tests"}`), 0600); err != nil {
		t.Fatal(err)
	}
	r := Reader{title: "Login", step: "old"}
	files := ClaudeFiles{Root: root, Session: "session"}
	files.Read(&r, time.Now())
	if r.Snapshot().Line != "Login · Reading tests" {
		t.Fatal(r.Snapshot())
	}
	if err := os.WriteFile(file, []byte(`{"id":"1","status":"completed","subject":"Read tests"}`), 0600); err != nil {
		t.Fatal(err)
	}
	files.Read(&r, time.Now())
	if r.Snapshot().Line != "Login" {
		t.Fatal("completed task left stale TodoWrite", r.Snapshot())
	}
	files.Session = "../outside"
	r.step = "unchanged"
	files.Read(&r, time.Now())
	if r.step != "unchanged" {
		t.Fatal("unsafe session accepted")
	}
}
