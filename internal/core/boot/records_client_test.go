// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/kit/protocol"
)

func recordClientFixture(t *testing.T) (*conversationRuntime, clientsrv.Session) {
	t.Helper()
	r, s, a, _ := managementFixture(t)
	r.agent = a
	s.Epoch = r.epoch
	s.CanDecide = true
	s.ConnectionID = "record-client"
	repo, err := records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	r.records = repo
	return r, s
}
func putClientRecord(t *testing.T, r *conversationRuntime, id, person, note string) {
	t.Helper()
	data := fmt.Sprintf("---\nid: %q\nkind: memo\nstate: active\nperson_id: %q\nagent_id: %q\n---\n\n# %s\n\n%s\n", id, person, r.agent, id, note)
	_, err := r.records.Commit(t.Context(), records.Change{ID: id, Path: "inbox/" + id + ".md", Data: []byte(data)}, records.Actor{Person: person, Agent: r.agent})
	if err != nil {
		t.Fatal(err)
	}
}
func TestRecordClientQueriesOwnerPagesAndReadsBoundedBodies(t *testing.T) {
	r, s := recordClientFixture(t)
	for i := range 53 {
		putClientRecord(t, r, fmt.Sprintf("memo-%02d", i), r.person, "연필을 챙긴다")
	}
	putClientRecord(t, r, "foreign", "another-person", "private")
	long := strings.Repeat("한글\n", 10000)
	putClientRecord(t, r, "long", r.person, long)
	p := protocol.RecordRequest{Operation: "query", View: textptr("memos"), Search: textptr("연필")}
	first, err := r.Record(t.Context(), s, p)
	if err != nil || first.Code != nil || len(first.Rows) != 50 || first.Next == nil {
		t.Fatalf("first page: %+v %v", first, err)
	}
	p.After = first.Next
	second, err := r.Record(t.Context(), s, p)
	if err != nil || len(second.Rows) != 3 || second.Next != nil {
		t.Fatalf("second page: %+v %v", second, err)
	}
	for _, row := range append(first.Rows, second.Rows...) {
		if row.Note != "" || row.Ref == "foreign" {
			t.Fatal("list leaked private/body content")
		}
	}
	read := protocol.RecordRequest{Operation: "read", Ref: textptr("long")}
	body := ""
	for {
		result, err := r.Record(t.Context(), s, read)
		if err != nil || result.Code != nil || len(result.Rows) != 1 {
			t.Fatal(result, err)
		}
		raw, _ := json.Marshal(result)
		if len(raw) > 128*1024 {
			t.Fatal("unbounded record body")
		}
		row := result.Rows[0]
		body += row.Note
		if result.Next == nil {
			break
		}
		read.After = result.Next
		read.Revision = &row.Revision
	}
	if body != strings.TrimSpace(long) {
		t.Fatal("paged read changed body")
	}
	denied, err := r.Record(t.Context(), s, protocol.RecordRequest{Operation: "read", Ref: textptr("foreign")})
	if err != nil || denied.Code == nil || *denied.Code != "record_not_found" {
		t.Fatal("foreign record exposed", denied, err)
	}
	s.PersonID = "another-person"
	_, err = r.Record(t.Context(), s, p)
	var refusal *clientsrv.OperationError
	if !errors.As(err, &refusal) || refusal.Code != protocol.ErrorNotAuthorized {
		t.Fatal("caller supplied owner", err)
	}
}
func TestRecordClientRefusesOutsideChangesAndStaleBodyPages(t *testing.T) {
	r, s := recordClientFixture(t)
	putClientRecord(t, r, "memo", r.person, "old")
	first, err := r.Record(t.Context(), s, protocol.RecordRequest{Operation: "read", Ref: textptr("memo")})
	if err != nil {
		t.Fatal(err)
	}
	record, revision, err := r.records.Read(t.Context(), "memo")
	if err != nil {
		t.Fatal(err)
	}
	record.Data = append(record.Data, []byte("new")...)
	if _, err := r.records.Commit(t.Context(), records.Change{ID: record.ID, Path: record.Path, Base: revision, Data: record.Data}, records.Actor{Person: r.person, Agent: r.agent}); err != nil {
		t.Fatal(err)
	}
	result, err := r.Record(t.Context(), s, protocol.RecordRequest{Operation: "read", Ref: textptr("memo"), Revision: &first.Rows[0].Revision, After: textptr("0")})
	if err != nil || result.Code == nil || *result.Code != "stale_revision" {
		t.Fatal("mixed body revisions", result, err)
	}
	if err := os.WriteFile(filepath.Join(r.root, "versions/records", record.Path), append(record.Data, []byte("outside")...), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = r.Record(t.Context(), s, protocol.RecordRequest{Operation: "query", View: textptr("memos")})
	if err != nil || result.Code == nil || *result.Code != "records_changed" {
		t.Fatal("outside edit adopted", result, err)
	}
}

func TestRecordCapturePreviewDoesNotWriteAndConfirmIsOnce(t *testing.T) {
	r, s := recordClientFixture(t)
	text := "책 반납 2026-10-25 %심부름 !"
	preview, err := r.Record(t.Context(), s, protocol.RecordRequest{Operation: "preview", Text: &text})
	if err != nil || preview.Code != nil || preview.Token == nil || len(preview.Rows) != 1 {
		t.Fatal("preview missing", preview, err)
	}
	rows, err := r.records.Query(t.Context(), records.Params{Person: r.person, View: "list"})
	if err != nil || len(rows) != 0 {
		t.Fatal("preview saved early", rows, err)
	}
	chips := map[string]string{}
	for _, chip := range preview.Rows[0].Chips {
		chips[chip.Kind] = chip.Value
	}
	if preview.Rows[0].Title != "책 반납" || chips["when"] != "2026-10-25" || chips["tag"] != "심부름" || chips["important"] != "true" {
		t.Fatal("wrong interpreted chips", preview.Rows[0])
	}
	other := s
	other.ConnectionID = "another-client"
	denied, err := r.Record(t.Context(), other, protocol.RecordRequest{Operation: "save", Token: preview.Token})
	if err == nil && denied.Code == nil {
		t.Fatal("preview token crossed connections")
	}
	save, err := r.Record(t.Context(), s, protocol.RecordRequest{Operation: "save", Token: preview.Token})
	if err != nil || save.Code != nil || len(save.Rows) != 1 {
		t.Fatal("save failed", save, err)
	}
	again, err := r.Record(t.Context(), s, protocol.RecordRequest{Operation: "save", Token: preview.Token})
	if err != nil || again.Code != nil || len(again.Rows) != 1 || again.Rows[0].Ref != save.Rows[0].Ref {
		t.Fatal("repeat confirm did not return original receipt", again, err)
	}
	rows, err = r.records.Query(t.Context(), records.Params{Person: r.person, View: "list"})
	if err != nil || len(rows) != 1 || rows[0].Todo.When != "2026-10-25" {
		t.Fatal("preview changed on save or duplicated", rows, err)
	}
}
func TestRecordCaptureRejectsAmbiguousDatesBeforePreview(t *testing.T) {
	r, s := recordClientFixture(t)
	for _, text := range []string{"Return book 03/04", "Return book 2026-02-30", "Return book {2026-02-30}"} {
		result, err := r.Record(t.Context(), s, protocol.RecordRequest{Operation: "preview", Text: &text})
		if err != nil || result.Code == nil || *result.Code != "ambiguous_date" || result.Token != nil {
			t.Fatal("ambiguous date can save", text, result, err)
		}
	}
	rows, err := r.records.Query(t.Context(), records.Params{Person: r.person})
	if err != nil || len(rows) != 0 {
		t.Fatal("refusal changed records", rows, err)
	}
}

func TestRecordDetailPagesBoundLargeMetadataWithoutLosingChips(t *testing.T) {
	row := protocol.RecordRow{Title: "fixture", Note: strings.Repeat("한", 20000), Chips: []protocol.RecordChip{}}
	for range 300 {
		row.Chips = append(row.Chips, protocol.RecordChip{Kind: "tag", Value: strings.Repeat("\\", 1000)})
	}
	var cursor *string
	note := ""
	chips := 0
	for {
		page, next, err := recordPage(row, cursor)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(page)
		if err != nil || len(raw) > 128*1024 {
			t.Fatal("unbounded detail page", len(raw), err)
		}
		note += page.Note
		chips += len(page.Chips)
		if next == nil {
			break
		}
		cursor = next
	}
	if note != row.Note || chips != len(row.Chips) {
		t.Fatal("pagination dropped metadata or note")
	}
}
