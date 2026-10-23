// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/addons/importer/hermes"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/protocol"
)

// The installed command must carry confirmed gateway/local input through seal,
// promotion and a restarted Core's memory API, while excluding other speakers.
func TestM1ImportOwnerChannels(t *testing.T) {
	t.Parallel()
	h, start := installedConversationHarness(t, "")
	ctx, cancel := context.WithTimeout(t.Context(), 9*operationTimeout)
	defer cancel()
	h.ctx = ctx
	core := start(false)
	h.stopCore(core)
	db, err := canon.OpenSQLiteReader(ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var agent, person string
	err = db.QueryRowContext(ctx, `SELECT f.agent_id,r.person_id FROM agent_first_run f JOIN agents a ON a.id=f.agent_id JOIN agent_revisions r ON r.agent_id=a.id AND r.revision=a.revision`).Scan(&agent, &person)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	path := filepath.Join(source, "state.db")
	src, err := canon.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := os.ReadFile("../../testdata/hermes/state.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, err = src.ExecContext(ctx, string(ddl)+`;
 DELETE FROM messages; DELETE FROM sessions;
 INSERT INTO sessions(id,source,user_id,chat_id,chat_type,started_at) VALUES
 ('tg-one','telegram','owner','chat','private',100),
 ('tg-two','telegram','owner','chat','dm',100),
 ('other','telegram','owner','other-chat','private',100),
 ('group','telegram','owner','chat','group',100),
 ('desk','desktop','basic:owner',NULL,NULL,100),
 ('web','webui',NULL,NULL,NULL,100);
 INSERT INTO messages(id,session_id,role,content,timestamp,display_kind,display_metadata) VALUES
 (1,'tg-one','user','Synthetic citrus preference',101,NULL,'{"gateway_input_owner":"delivery-one"}'),
 (2,'tg-two','user','Synthetic cycling preference',102,'steer',NULL),
 (3,'other','user','Synthetic external preference',103,NULL,'{"gateway_input_owner":"delivery-other"}'),
 (4,'tg-one','user','Synthetic notification preference',104,'internal_notification','{"gateway_input_owner":"delivery-notice"}'),
 (5,'group','user','Synthetic group preference',105,NULL,'{"gateway_input_owner":"delivery-group"}'),
 (6,'desk','user','Synthetic desktop preference',106,NULL,NULL),
 (7,'web','user','Synthetic web preference',107,NULL,NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	if err = src.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"import-hermes", "--state-root", h.root, "--source", source, "--profile", "profile", "--agent", agent, "--person", person, "--lcm", ""}
	run := func(extra ...string) *process {
		t.Helper()
		p := h.command(append(append([]string{}, args...), extra...)...)
		p.start(t)
		p.wait(t, 0)
		return p
	}
	rehearsal := run("--rehearse")
	var report struct {
		Rehearsal struct {
			Mappings []struct {
				CandidateOwnerChannels []struct{ Source, SessionID, UserID, ChatID, ChatType string }
			}
		}
	}
	if err = json.Unmarshal(rehearsal.stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Rehearsal.Mappings) != 1 || len(report.Rehearsal.Mappings[0].CandidateOwnerChannels) != 6 {
		t.Fatalf("missing candidates: %s", rehearsal.stdout.String())
	}
	args = append(args, "--owner-channel", "telegram:owner:chat", "--owner-session", "desk,web")
	confirmed := run("--rehearse")
	var classified struct {
		Rehearsal struct {
			Speakers []struct {
				Events []struct {
					Kind string `json:"speaker_kind"`
				}
			}
		}
	}
	if err = json.Unmarshal(confirmed.stdout.Bytes(), &classified); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, s := range classified.Rehearsal.Speakers {
		for _, e := range s.Events {
			kinds[e.Kind]++
		}
	}
	if !reflect.DeepEqual(kinds, map[string]int{"person": 4, "external_participant": 2, "system": 1}) {
		t.Fatalf("rehearsal speakers: %v", kinds)
	}
	run("--confirm-import", "--promote", "all")
	// Prove sealed kinds from the live ledger, resolving original row identities.
	db, err = canon.OpenSQLiteReader(ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT e.speaker_kind, count(*) FROM conv_event e JOIN legacy_ref r ON r.record_id=e.id WHERE r.system=? AND r.kind='messages' GROUP BY e.speaker_kind`, string(hermes.HermesState))
	if err != nil {
		t.Fatal(err)
	}
	sealed := map[string]int{}
	for rows.Next() {
		var kind string
		var n int
		if err = rows.Scan(&kind, &n); err != nil {
			t.Fatal(err)
		}
		sealed[kind] = n
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	db.Close()
	if !reflect.DeepEqual(kinds, sealed) {
		t.Fatalf("sealed=%v, rehearsal=%v", sealed, kinds)
	}
	core = start(false)
	defer h.stopCore(core)
	client, err := clientapi.DialWithCapabilities(ctx, h.socket, "test", []protocol.Capability{{Name: "memory", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, tc := range []struct {
		text string
		want int
	}{
		{"Synthetic citrus preference", 1}, {"Synthetic cycling preference", 1}, {"Synthetic desktop preference", 1}, {"Synthetic web preference", 1},
		{"Synthetic external preference", 0}, {"Synthetic notification preference", 0}, {"Synthetic group preference", 0},
	} {
		op := "list"
		if _, err = client.Memory(ctx, protocol.MemoryRequest{Operation: &op, AgentID: &agent, Text: &tc.text}); err != nil {
			t.Fatal(err)
		}
		reply, err := client.Receive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		result, ok := reply.Payload.(protocol.MemoryResult)
		if !ok || result.Items == nil || len(*result.Items) != tc.want {
			t.Fatalf("recall %q: %+v", tc.text, reply)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("source changed", err)
	}
}
