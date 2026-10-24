// SPDX-License-Identifier: Apache-2.0
package messenger_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

func comparisonFixture(t *testing.T) (*fixture, messenger.ComparisonRequest, string) {
	t.Helper()
	f := newFixture(t)
	source := messenger.Source{Person: "person", Platform: "kakaotalk", Account: "account", Mode: messenger.ModeUnofficial, Adapter: "fixture", State: messenger.StateConnected, AcceptedTerms: &messenger.Terms{Person: "person", At: f.now, Text: "synthetic"}}
	if err := f.store.Connect(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpsertChats(t.Context(), source, []messenger.Chat{{ID: "123", Kind: "direct"}, {ID: "456", Kind: "group"}}); err != nil {
		t.Fatal(err)
	}
	for chat, ids := range map[string][]string{"123": {"9223372036854775807", "200"}, "456": {"300"}} {
		var messages []messenger.Message
		for _, id := range ids {
			messages = append(messages, message(id, messenger.RouteParticipant, f.now, "PRIVATE BODY"))
		}
		if _, err := f.store.AdmitPage(t.Context(), source, chat, "", messenger.Page{Messages: messages}); err != nil {
			t.Fatal(err)
		}
	}
	request := messenger.ComparisonRequest{Person: "person", Account: "account", Since: f.now, Until: f.now.Add(7 * 24 * time.Hour)}
	return f, request, t.TempDir()
}

func inboxLine(id string, at time.Time) string {
	raw, _ := json.Marshal(map[string]any{"type": "message", "source": "agent-messenger", "message_id": id, "timestamp": at.Format(time.RFC3339Nano), "message_type": "kakao-1", "sender": map[string]string{"id": "999", "name": "PRIVATE NAME"}, "text": "PRIVATE BODY", "attachment": nil})
	return string(raw) + "\n"
}
func writeInbox(t *testing.T, dir, chat, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, chat+".jsonl"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMurmurComparison(t *testing.T) {
	f, req, dir := comparisonFixture(t)
	writeInbox(t, dir, "123", inboxLine("123-9223372036854775807", f.now)+inboxLine("200", f.now)+inboxLine("200", f.now)+inboxLine("999", req.Until)+inboxLine("998", req.Since.Add(-time.Nanosecond)))
	writeInbox(t, dir, "456", inboxLine("300", f.now))
	report, err := f.store.CompareMurmur(t.Context(), req, dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.Murmur != 3 || report.LINA != 3 || report.MissingLINA != 0 || report.MissingMurmur != 0 {
		t.Fatalf("identical: %+v", report)
	}
	writeInbox(t, dir, "123", inboxLine("9223372036854775807", f.now)+inboxLine("201", f.now))
	writeInbox(t, dir, "789", inboxLine("400", f.now))
	report, err = f.store.CompareMurmur(t.Context(), req, dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.MissingLINA != 2 || report.MissingMurmur != 1 || len(report.Chats) != 3 {
		t.Fatalf("differences: %+v", report)
	}
	if strings.Join(report.Chats[0].MissingLINA, ",") != "201" || strings.Join(report.Chats[0].MissingMurmur, ",") != "200" || report.Chats[2].Chat != "789" {
		t.Fatalf("ids: %+v", report.Chats)
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "sender") {
		t.Fatal("report contains content")
	}
	req.Person = "another"
	if _, err = f.store.CompareMurmur(t.Context(), req, dir); err == nil {
		t.Fatal("foreign source accepted")
	}
}

func TestMurmurComparisonRefusesInvalidInbox(t *testing.T) {
	f, req, dir := comparisonFixture(t)
	for name, body := range map[string]string{
		"torn":         strings.TrimSuffix(inboxLine("1", f.now), "\n"),
		"malformed":    "{PRIVATE BODY\n",
		"wrong source": strings.Replace(inboxLine("1", f.now), "agent-messenger", "line", 1),
		"bad id":       inboxLine("PRIVATE BODY", f.now),
		"bad time":     strings.Replace(inboxLine("1", f.now), f.now.Format(time.RFC3339Nano), "PRIVATE BODY", 1),
		"overflow":     inboxLine("9223372036854775808", f.now),
	} {
		t.Run(name, func(t *testing.T) {
			writeInbox(t, dir, "123", body)
			_, err := f.store.CompareMurmur(t.Context(), req, dir)
			if err == nil || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
	writeInbox(t, dir, "123", inboxLine("1", f.now))
	if err := os.Symlink(filepath.Join(dir, "123.jsonl"), filepath.Join(dir, "456.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CompareMurmur(t.Context(), req, dir); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestMurmurComparisonKeepsDeletedIDsAndLINAOnlyChats(t *testing.T) {
	f, req, dir := comparisonFixture(t)
	source := messenger.Source{Person: req.Person, Platform: "kakaotalk", Account: req.Account}
	if _, err := f.store.AdmitPage(t.Context(), source, "123", "", messenger.Page{Deletions: []messenger.Deletion{{Message: "200", At: f.now.Add(time.Hour)}}}); err != nil {
		t.Fatal(err)
	}
	writeInbox(t, dir, "123", inboxLine("9223372036854775807", f.now)+inboxLine("200", f.now))
	report, err := f.store.CompareMurmur(t.Context(), req, dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.MissingLINA != 0 || report.MissingMurmur != 1 || len(report.Chats) != 2 || report.Chats[1].Chat != "456" || report.Chats[1].MissingMurmur[0] != "300" {
		t.Fatalf("tombstone/extra chat: %+v", report)
	}
}

func TestMurmurComparisonExactNumericEnvelope(t *testing.T) {
	f, req, dir := comparisonFixture(t)
	raw := inboxLine("9223372036854775807", f.now)
	raw = strings.Replace(raw, `"message_id":"9223372036854775807"`, `"message_id":9223372036854775807`, 1)
	raw = strings.Replace(raw, `"timestamp":"`+f.now.Format(time.RFC3339Nano)+`"`, `"timestamp":`+strconv.FormatInt(f.now.Unix(), 10), 1)
	writeInbox(t, dir, "123", raw)
	report, err := f.store.CompareMurmur(t.Context(), req, dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.Murmur != 1 || report.MissingLINA != 0 {
		t.Fatalf("numeric envelope: %+v", report)
	}
}
