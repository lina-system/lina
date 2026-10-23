// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestImportedToolCallsReachExpansionAndSummary(t *testing.T) {
	for _, source := range []SQLiteSource{HermesLCM, HermesState} {
		for _, content := range []any{"Inspecting the archive.", nil} {
			name := string(source) + "/content"
			if content == nil {
				name = string(source) + "/null"
			}
			t.Run(name, func(t *testing.T) {
				req := importRequest(t)
				path, keyColumn := req.Profiles[0].LCMPath, "store_id"
				if source == HermesState {
					path, keyColumn = req.Profiles[0].StatePath, "id"
					req.Profiles[0].LCMPath = ""
				}
				src, err := canon.OpenSQLite(t.Context(), path)
				if err != nil {
					t.Fatal(err)
				}
				calls := `[{"id":"read-1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"/work/기록.txt\"}"}},{"id":"run-2","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"printf archive\"}"}}]`
				_, err = src.Exec("UPDATE messages SET content=?,tool_calls=? WHERE "+keyColumn+"=2", content, calls)
				src.Close()
				if err != nil {
					t.Fatal(err)
				}
				p, err := Prepare(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
				defer p.Close()
				db := importStore(t)
				if _, err = p.Import(t.Context(), db); err != nil {
					t.Fatal(err)
				}
				if _, err = p.Seal(t.Context(), db, func(context.Context) (string, error) { return "backup", nil }); err != nil {
					t.Fatal(err)
				}
				ref, err := Resolve(t.Context(), db, LegacyKey{string(source), "profile", "messages", "2"})
				if err != nil {
					t.Fatal(err)
				}
				journal := ledger.New(db, nil)
				e, err := journal.Get(t.Context(), ref.RecordID)
				if err != nil {
					t.Fatal(err)
				}
				check := func(body string) {
					t.Helper()
					var got struct {
						Content   string
						ToolCalls []struct {
							Type            string
							CallID          string `json:"call_id"`
							Name, Arguments string
						} `json:"tool_calls"`
					}
					if err := json.Unmarshal([]byte(body), &got); err != nil {
						t.Errorf("tool-call event body is not structured: %q: %v", body, err)
						return
					}
					expected := ""
					if content != nil {
						expected = content.(string)
					}
					if got.Content != expected || len(got.ToolCalls) != 2 {
						t.Errorf("lost content or calls: %s", body)
						return
					}
					for i, want := range []struct{ id, name, args string }{{"read-1", "read_file", `{"path":"/work/기록.txt"}`}, {"run-2", "exec_command", `{"cmd":"printf archive"}`}} {
						call := got.ToolCalls[i]
						if call.Type != "function_call" || call.CallID != want.id || call.Name != want.name || call.Arguments != want.args {
							t.Errorf("call %d: %+v", i, call)
						}
					}
				}
				page, err := (compaction.Expansion{DB: db, Events: journal}).Read(t.Context(), "agent", "event:"+e.ID, 0, 50)
				if err != nil {
					t.Fatal(err)
				}
				var expanded compaction.ExpansionPage
				if err = json.Unmarshal(page, &expanded); err != nil || len(expanded.Events) != 1 {
					t.Fatalf("expand: %s %v", page, err)
				}
				check(expanded.Events[0].Body)
				var segment compaction.PassSegment
				for _, c := range p.conversations {
					if c.record.id == e.ConversationID {
						for _, r := range c.records {
							if r.event != nil {
								segment.Records = append(segment.Records, compaction.Record{ID: r.id})
							}
						}
					}
				}
				found := false
				builder := compaction.Builder{Store: compaction.New(db), Events: journal, Agent: "agent", Conversation: e.ConversationID, Model: "fixture", Now: time.Now, NextID: func() string { return "summary" }, Generate: func(_ context.Context, s compaction.PassSegment) (compaction.Text, error) {
					for _, r := range s.Records {
						if r.ID == e.ID {
							var item struct{ Body string }
							if err := json.Unmarshal(r.Item, &item); err != nil {
								t.Fatal(err)
							}
							check(item.Body)
							found = true
						}
					}
					return compaction.Text{Requests: "none", Topics: "archive", Actions: "read", Values: "path", Omitted: "none"}, nil
				}}
				if _, err = builder.Summarize(t.Context(), segment); err != nil {
					t.Fatal(err)
				}
				if !found {
					t.Error("summary omitted assistant event")
				}
				row, err := Reverse(t.Context(), db, ref.Key)
				if err != nil || row.text("tool_calls") != calls || RowHash(row) != ref.SourceHash {
					t.Fatalf("source round trip: %v", err)
				}
				if content == nil && row["content"].Kind != "null" {
					t.Error("NULL content changed")
				}
				if _, err = p.Verify(t.Context(), db); err != nil {
					t.Fatal(err)
				}
				if _, err = p.Seal(t.Context(), db, func(context.Context) (string, error) { t.Error("repeat seal backed up again"); return "", nil }); err != nil {
					t.Fatal(err)
				}
				if _, err = e.Message(); err == nil {
					t.Error("import became native request history")
				}
			})
		}
	}
}

func TestImportedToolCallSecretsStayPrivate(t *testing.T) {
	req := importRequest(t)
	src, err := canon.OpenSQLite(t.Context(), req.Profiles[0].LCMPath)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "sk-proj-abcdefghijklmnopqrstuv"
	calls := `[{"id":"call","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"curl\",\"token\":\"` + secret + `\"}"}}]`
	_, err = src.Exec("UPDATE messages SET tool_calls=? WHERE store_id=2", calls)
	src.Close()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	db := importStore(t)
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Seal(t.Context(), db, func(context.Context) (string, error) { return "backup", nil }); err != nil {
		t.Fatal(err)
	}
	ref, err := Resolve(t.Context(), db, LegacyKey{string(HermesLCM), "profile", "messages", "2"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := ledger.New(db, nil).Get(t.Context(), ref.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Body.Text(), "exec_command") || strings.Contains(e.Body.Text(), secret) || strings.Contains(e.Body.DerivedText(), secret) {
		t.Fatalf("unsafe or missing tool-call projection: %q", e.Body.Text())
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}

func TestImportedToolCallUnknownPayloadsStayReadable(t *testing.T) {
	for _, tc := range []struct {
		name, calls string
		valid       bool
	}{
		{"empty", "", true}, {"null", "null", true}, {"no calls", "[]", true},
		{"broken JSON", "  [\n", false}, {"wrong container", `{}`, false},
		{"missing function", `[{"id":"c","type":"function"}]`, false},
		{"missing arguments", `[{"id":"c","type":"function","function":{"name":"read"}}]`, false},
		{"unknown type", `[{"id":"c","type":"custom","function":{"name":"read","arguments":"{}"}}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := importRequest(t)
			src, err := canon.OpenSQLite(t.Context(), req.Profiles[0].LCMPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = src.Exec("UPDATE messages SET tool_calls=? WHERE store_id=2", tc.calls)
			src.Close()
			if err != nil {
				t.Fatal(err)
			}
			p, err := Prepare(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			db := importStore(t)
			if _, err = p.Import(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			_, err = p.Seal(t.Context(), db, func(context.Context) (string, error) {
				return "backup", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !tc.valid {
				ref, err := Resolve(t.Context(), db, LegacyKey{string(HermesLCM), "profile", "messages", "2"})
				if err != nil {
					t.Fatal(err)
				}
				e, err := ledger.New(db, nil).Get(t.Context(), ref.RecordID)
				if err != nil {
					t.Fatal(err)
				}
				var body struct {
					Content string
					Raw     string `json:"unparsed_tool_calls"`
				}
				if err = json.Unmarshal([]byte(e.Body.Text()), &body); err != nil || body.Raw != tc.calls || body.Content != "Synthetic response" {
					t.Fatalf("unknown payload lost: %+v %v", body, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				ref, err := Resolve(t.Context(), db, LegacyKey{string(HermesLCM), "profile", "messages", "2"})
				if err != nil {
					t.Fatal(err)
				}
				e, err := ledger.New(db, nil).Get(t.Context(), ref.RecordID)
				if err != nil || e.Body.Text() != "Synthetic response" {
					t.Fatalf("plain body changed: %q %v", e.Body.Text(), err)
				}
			}
			if _, err = p.Verify(t.Context(), db); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestImportedToolCallQuotedSecrets(t *testing.T) {
	req := importRequest(t)
	src, err := canon.OpenSQLite(t.Context(), req.Profiles[0].LCMPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := `[{"id":"call","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"echo password=\\\"argument-private\\\"\"}"}}]`
	_, err = src.Exec("UPDATE messages SET content=?,tool_calls=? WHERE store_id=2", `The password="content-private" is set.`, calls)
	src.Close()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	db := importStore(t)
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Seal(t.Context(), db, func(context.Context) (string, error) { return "backup", nil }); err != nil {
		t.Fatal(err)
	}
	ref, err := Resolve(t.Context(), db, LegacyKey{string(HermesLCM), "profile", "messages", "2"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := ledger.New(db, nil).Get(t.Context(), ref.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(e.Body.Text())) || !strings.Contains(e.Body.Text(), "exec_command") || strings.Contains(e.Body.Text(), "content-private") || strings.Contains(e.Body.Text(), "argument-private") {
		t.Fatalf("invalid masking: %q", e.Body.Text())
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}

func TestImportedToolCallNumericSecrets(t *testing.T) {
	req := importRequest(t)
	src, err := canon.OpenSQLite(t.Context(), req.Profiles[0].LCMPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := `[{"id":"call","type":"function","function":{"name":"exec_command","arguments":"{\"password\":123456,\"port\":8080}"}}]`
	_, err = src.Exec("UPDATE messages SET tool_calls=? WHERE store_id=2", calls)
	src.Close()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	db := importStore(t)
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Seal(t.Context(), db, func(context.Context) (string, error) { return "backup", nil }); err != nil {
		t.Fatal(err)
	}
	ref, err := Resolve(t.Context(), db, LegacyKey{string(HermesLCM), "profile", "messages", "2"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := ledger.New(db, nil).Get(t.Context(), ref.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(e.Body.Text())) || !strings.Contains(e.Body.Text(), "8080") || strings.Contains(e.Body.Text(), "123456") || strings.Contains(e.Body.DerivedText(), "123456") {
		t.Fatalf("numeric credential leaked or public number lost: %q", e.Body.Text())
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}

func TestImportedToolCallMaskingFailureReported(t *testing.T) {
	req := importRequest(t)
	src, err := canon.OpenSQLite(t.Context(), req.Profiles[0].LCMPath)
	if err != nil {
		t.Fatal(err)
	}
	arguments := strings.Repeat("[", 66) + `"synthetic-private-value"` + strings.Repeat("]", 66)
	calls, _ := json.Marshal([]any{map[string]any{"id": "deep", "type": "function", "function": map[string]string{"name": "read_file", "arguments": arguments}}})
	_, err = src.Exec("UPDATE messages SET tool_calls=? WHERE store_id=2", string(calls))
	src.Close()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	db := importStore(t)
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	ref, err := Resolve(t.Context(), db, LegacyKey{string(HermesLCM), "profile", "messages", "2"})
	if err != nil {
		t.Fatal(err)
	}
	backups := 0
	for range 2 {
		report, err := p.Seal(t.Context(), db, func(context.Context) (string, error) { backups++; return "backup", nil })
		if err != nil {
			t.Fatal(err)
		}
		if report.UnavailableBodies != 1 || len(report.BodyFailures) != 1 || report.BodyFailures[0].EventID != ref.RecordID || report.BodyFailures[0].Reason != "invalid_secret_span" {
			t.Fatalf("missing masking failure report: %+v", report)
		}
	}
	if backups != 1 {
		t.Fatalf("backups=%d", backups)
	}
	e, err := ledger.New(db, nil).Get(t.Context(), ref.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Body.Text(), "invalid_secret_span") || strings.Contains(e.Body.Text(), "synthetic-private-value") || len(e.Body.Spans()) == 0 {
		t.Fatalf("unsafe fallback: %q", e.Body.Text())
	}
	row, err := Reverse(t.Context(), db, ref.Key)
	if err != nil || row.text("tool_calls") != string(calls) || RowHash(row) != ref.SourceHash {
		t.Fatalf("original calls changed: %v", err)
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}

// A sidecar material supplies the output text of its event. An assistant event
// with tool calls still needs the live JSON projection, so the seal publishes
// that projection inline and leaves the original sidecar on the import record.
func TestImportedMaterialToolCallsKeepLiveProjection(t *testing.T) {
	req := importRequest(t)
	db := importStore(t)
	writer, store := importMaterials(t, db)
	req.Materials = writer
	const secret = "sk-proj-abcdefghijklmnopqrstuv"
	raw := []byte("synthetic large result password=" + secret)
	path := filepath.Join(t.TempDir(), "output")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	arguments := `{"path":"/work/기록.txt"}`
	calls := `[{"id":"read-1","type":"function","function":{"name":"read_file","arguments":` + strconv.Quote(arguments) + `}}]`
	src, err := canon.OpenSQLite(t.Context(), req.Profiles[0].LCMPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = src.Exec("UPDATE messages SET content='Externalized output',tool_calls=? WHERE store_id=2", calls)
	src.Close()
	if err != nil {
		t.Fatal(err)
	}
	key := LegacyKey{string(HermesLCM), "profile", "messages", "2"}
	req.Sidecars = []Sidecar{{Key: LegacyKey{string(HermesLCM), "profile", "sidecars", "output"}, Event: key, SnapshotPath: path, SHA256: bytesHash(raw)}}
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Seal(t.Context(), db, func(context.Context) (string, error) { return "backup", nil }); err != nil {
		t.Fatal(err)
	}
	ref, err := Resolve(t.Context(), db, key)
	if err != nil {
		t.Fatal(err)
	}
	events := ledger.New(db, importedMaterials{store})
	e, err := events.Get(t.Context(), ref.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	check := func(body string) {
		t.Helper()
		var got struct {
			Content   string
			ToolCalls []struct {
				Type      string
				CallID    string `json:"call_id"`
				Name      string
				Arguments string
			} `json:"tool_calls"`
		}
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("material tool-call event body is not structured: %q: %v", body, err)
		}
		if !strings.Contains(got.Content, "synthetic large result") || strings.Contains(got.Content, secret) {
			t.Fatalf("material content lost or unmasked: %q", got.Content)
		}
		if len(got.ToolCalls) != 1 {
			t.Fatalf("lost calls: %q", body)
		}
		if call := got.ToolCalls[0]; call.Type != "function_call" || call.CallID != "read-1" || call.Name != "read_file" || call.Arguments != arguments {
			t.Fatalf("call: %+v", call)
		}
	}
	check(e.Body.Text())
	if e.Material.ID != "" || strings.Contains(e.Body.DerivedText(), secret) {
		t.Fatalf("material projection: %+v %q", e.Material, e.Body.DerivedText())
	}
	page, err := (compaction.Expansion{DB: db, Events: events}).Read(t.Context(), "agent", "event:"+e.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var expanded compaction.ExpansionPage
	if err = json.Unmarshal(page, &expanded); err != nil || len(expanded.Events) != 1 {
		t.Fatalf("expand: %s %v", page, err)
	}
	check(expanded.Events[0].Body)
	// The original sidecar stays referenced by the import record for reverse mapping.
	var id string
	var revision int64
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT material_id,material_revision FROM import_event WHERE id=?", ref.RecordID).Scan(&id, &revision)
	}); err != nil {
		t.Fatal(err)
	}
	original, err := store.Read(t.Context(), id, revision)
	if err != nil || !bytes.Equal(original, raw) {
		t.Fatal("original sidecar changed", err)
	}
	row, err := Reverse(t.Context(), db, key)
	if err != nil || RowHash(row) != ref.SourceHash {
		t.Fatalf("source round trip: %v", err)
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}
