// SPDX-License-Identifier: Apache-2.0
package secrets_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

func TestMaskBeforeStorageAndDerivation(t *testing.T) {
	token := "sk-proj-" + strings.Repeat("A7", 24)
	input := "앞 before " + token + " after tail"
	masked, err := secrets.Mask(input)
	if err != nil {
		t.Fatal(err)
	}
	if masked.Text() != "앞 before "+secret.Placeholder+" after tail" {
		t.Fatalf("unexpected masked text: %q", masked.Text())
	}
	spans := masked.Spans()
	if len(spans) != 1 || spans[0].Start != len("앞 before ") || spans[0].End != len("앞 before "+secret.Placeholder) {
		t.Fatalf("unexpected spans: %+v", spans)
	}
	// The only stored body is already masked. Round-tripping never needs raw bytes.
	record, err := json.Marshal(masked)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(record), token) || !strings.Contains(string(record), `"secret"`) {
		t.Fatal("unsafe persisted record")
	}
	var restored secrets.Masked
	if err := json.Unmarshal(record, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Text() != masked.Text() || !reflect.DeepEqual(restored.Spans(), spans) {
		t.Fatal("record did not round trip")
	}
	derived := restored.DerivedText()
	if strings.Contains(derived, token) || strings.Contains(derived, secret.Placeholder) || !strings.Contains(derived, "before") || !strings.Contains(derived, "after") {
		t.Fatal("unsafe derived input")
	}
	// Index, summary, ContextPacket and export consumers share this projection.
	exported, err := json.Marshal(map[string]string{"text": derived})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(exported), token) {
		t.Fatal("export exposed credential")
	}
	spans[0].Start = 0
	if restored.Spans()[0].Start == 0 || masked.Spans()[0].Start == 0 {
		t.Fatal("span mutation escaped")
	}
}

func TestMaskShapesAndExplicitSpans(t *testing.T) {
	cases := []string{
		"ghp_" + strings.Repeat("a", 36),
		"github_pat_" + strings.Repeat("b", 82),
		"sk-ant-api03-" + strings.Repeat("c", 40),
		"AIza" + strings.Repeat("d", 35),
		"xoxb-123456789-123456789-abcdefghijklmnopqrstuvwx",
		"AKIA1234567890ABCDEF",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJleGFtcGxlIn0.signature123",
		"-----BEGIN PRIVATE KEY-----\nsynthetickeybody\n-----END PRIVATE KEY-----",
		"-----BEGIN RSA PRIVATE KEY-----\r\nsynthetickeybody\r\n-----END RSA PRIVATE KEY-----",
		"-----BEGIN OPENSSH PRIVATE KEY-----\ntruncatedkeybody",
	}
	for i, token := range cases {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			m, err := secrets.Mask("prefix " + token)
			if err != nil {
				t.Fatal(err)
			}
			if m.Text() != "prefix "+secret.Placeholder || m.DerivedText() != "prefix " {
				t.Fatalf("shape not masked: %q", m.Text())
			}
			assertMaskedRoundTrip(t, m)
		})
	}
	for _, text := range []string{"token=syntheticcredential", `{"api_key":"syntheticcredential"}`, "Authorization: Bearer syntheticcredential", "password='synthetic pass phrase'"} {
		m, err := secrets.Mask(text)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(m.Text(), "synthetic") || len(m.Spans()) != 1 {
			t.Fatalf("assignment not masked: %q", m.Text())
		}
		assertMaskedRoundTrip(t, m)
	}
	m, err := secrets.Mask("a private z", secrets.Span{Start: 2, End: 9}, secrets.Span{Start: 3, End: 8})
	if err != nil || m.Text() != "a "+secret.Placeholder+" z" || len(m.Spans()) != 1 {
		t.Fatal("overlapping explicit spans not merged")
	}
	for _, span := range []secrets.Span{{Start: -1, End: 1}, {Start: 0, End: 99}, {Start: 2, End: 1}, {Start: 0, End: 0}, {Start: 1, End: 3}} {
		if _, err := secrets.Mask("한글", span); err == nil {
			t.Fatalf("invalid span accepted: %+v", span)
		}
	}
	safe := "ordinary prose and -----BEGIN PUBLIC KEY-----\npublic\n-----END PUBLIC KEY-----"
	m, err = secrets.Mask(safe)
	if err != nil || m.Text() != safe || m.DerivedText() != safe || len(m.Spans()) != 0 {
		t.Fatal("safe text changed")
	}
}

func TestMaskedRecordCannotImportUnmaskedSecrets(t *testing.T) {
	for _, record := range []string{
		`{"text":"token=syntheticcredential","spans":[]}`,
		`{"text":"raw","spans":[{"start":0,"end":3,"sensitivity":"secret"}]}`,
		`{"text":"safe","spans":[{"start":-1,"end":2,"sensitivity":"secret"}]}`,
		`{"text":"safe","unknown":"raw"}`,
	} {
		var m secrets.Masked
		if err := json.Unmarshal([]byte(record), &m); err == nil {
			t.Fatal("unsafe stored record accepted")
		}
	}
}

func TestDerivedSpansCannotBecomeSearchableOrExported(t *testing.T) {
	token := "sk-proj-sensitivecanary0123456789abcdef"
	masked, err := secrets.Mask("publicbefore " + token + " publicafter")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "projection.sqlite")
	db, err := canon.OpenSQLite(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE events(record TEXT); CREATE VIRTUAL TABLE search USING fts5(body)"); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(masked)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO events VALUES (?)", string(record)); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := db.QueryRow("SELECT record FROM events").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var restored secrets.Masked
	if err := json.Unmarshal(stored, &restored); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO search VALUES (?)", restored.DerivedText()); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]int{`"` + token + `"`: 0, "REDACTED": 0, "publicbefore": 1, "publicafter": 1} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM search WHERE search MATCH ?", query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("search count=%d, want %d", count, want)
		}
	}
	// Exercise the shared input projection, without a model call or export service.
	summaryInput := restored.DerivedText()
	contextPacket := struct{ Text string }{restored.DerivedText()}
	var export bytes.Buffer
	if err := json.NewEncoder(&export).Encode(contextPacket); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{summaryInput, contextPacket.Text, export.String()} {
		if strings.Contains(output, token) || strings.Contains(output, secret.Placeholder) {
			t.Fatal("secret span reached a derived consumer")
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{dbPath, dbPath + "-wal"} {
		data, err := os.ReadFile(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(token)) {
			t.Fatal("raw token reached database bytes")
		}
	}
}
