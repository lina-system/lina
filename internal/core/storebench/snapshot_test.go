// SPDX-License-Identifier: Apache-2.0
package storebench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

var errSnapshot = errors.New("storebench_snapshot_invalid")

func digest(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, errSnapshot
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, errSnapshot
	}
	return [32]byte(h.Sum(nil)), nil
}

// The flag accepts a checkpointed offline LCM database, never a live Hermes
// state directory. There is no source copy, export, network call or log of its
// path/text. Reject active WAL rather than silently reading an older snapshot.
func readSnapshot(ctx context.Context, path string) (_ corpus, err error) {
	defer func() {
		if err != nil {
			err = errSnapshot
		}
	}()
	stable := func() error {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errSnapshot
		}
		for _, suffix := range []string{"-wal", "-journal"} {
			info, err := os.Lstat(path + suffix)
			if err == nil && info.Size() != 0 || err != nil && !errors.Is(err, os.ErrNotExist) {
				return errSnapshot
			}
		}
		return nil
	}
	if err := stable(); err != nil {
		return corpus{}, err
	}
	before, err := digest(path)
	if err != nil {
		return corpus{}, err
	}
	db, closeDB, err := canon.OpenSQLiteInspection(ctx, path)
	if err != nil {
		return corpus{}, err
	}
	defer closeDB()
	// Force any SQLite working data into memory. Source text is persisted only
	// by the benchmark's real write paths inside its private scratch root.
	if _, err := db.ExecContext(ctx, "PRAGMA temp_store=MEMORY"); err != nil {
		return corpus{}, err
	}
	rows, err := db.QueryContext(ctx, "SELECT store_id,session_id,role,coalesce(content,'') FROM messages ORDER BY store_id")
	if err != nil {
		return corpus{}, err
	}
	c := corpus{}
	sessions := map[string]string{}
	turnBySession := map[string]int{}
	bySource := map[int64]message{}
	position := map[string]int{}
	for rows.Next() {
		var source int64
		var session, role, body string
		if err := rows.Scan(&source, &session, &role, &body); err != nil {
			rows.Close()
			return corpus{}, err
		}
		entry := guard.EntryOwner
		switch role {
		case "user":
		case "assistant":
			entry = guard.EntryAgent
		case "tool":
			entry = guard.EntryTool
		case "system":
			entry = guard.EntrySystem
		default:
			rows.Close()
			return corpus{}, errSnapshot
		}
		masked, err := secrets.Mask(body)
		if err != nil {
			rows.Close()
			return corpus{}, err
		}
		if strings.TrimSpace(masked.DerivedText()) == "" {
			c.Skipped++
			continue
		}
		if sessions[session] == "" {
			sessions[session] = fmt.Sprintf("snapshot-session-%08d", len(sessions))
		}
		turn, started := turnBySession[session]
		if entry == guard.EntryOwner {
			turn = len(c.Turns)
			c.Turns = append(c.Turns, nil)
			turnBySession[session] = turn
		} else if !started {
			c.Skipped++
			continue
		}
		m := message{fmt.Sprintf("snapshot-event-%08d", len(bySource)), sessions[session], masked.DerivedText(), entry}
		position[m.ID] = len(bySource)
		bySource[source] = m
		c.Turns[turn] = append(c.Turns[turn], m)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return corpus{}, err
	}
	type rawNode struct {
		id                  int64
		session, text, kind string
		sources             []int64
		end                 string
	}
	nodes := map[int64]*rawNode{}
	rows, err = db.QueryContext(ctx, "SELECT node_id,session_id,summary,source_type,source_ids FROM summary_nodes ORDER BY node_id")
	if err != nil {
		return corpus{}, err
	}
	for rows.Next() {
		n := &rawNode{}
		var raw string
		if err := rows.Scan(&n.id, &n.session, &n.text, &n.kind, &raw); err != nil {
			rows.Close()
			return corpus{}, err
		}
		if err := json.Unmarshal([]byte(raw), &n.sources); err != nil {
			rows.Close()
			return corpus{}, err
		}
		nodes[n.id] = n
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return corpus{}, err
	}
	visiting := map[int64]bool{}
	var resolve func(int64) (string, error)
	resolve = func(id int64) (string, error) {
		n := nodes[id]
		if n == nil || visiting[id] {
			return "", errSnapshot
		}
		if n.end != "" {
			return n.end, nil
		}
		visiting[id] = true
		defer delete(visiting, id)
		for _, source := range n.sources {
			var end string
			switch n.kind {
			case "messages":
				m, ok := bySource[source]
				if !ok || m.Session != sessions[n.session] {
					return "", errSnapshot
				}
				end = m.ID
			case "nodes":
				if nodes[source] == nil || nodes[source].session != n.session {
					return "", errSnapshot
				}
				var err error
				end, err = resolve(source)
				if err != nil {
					return "", err
				}
			default:
				return "", errSnapshot
			}
			if n.end == "" || position[end] > position[n.end] {
				n.end = end
			}
		}
		if n.end == "" {
			return "", errSnapshot
		}
		return n.end, nil
	}
	var order []int64
	for id := range nodes {
		order = append(order, id)
	}
	slices.Sort(order)
	for _, id := range order {
		n := nodes[id]
		end, err := resolve(id)
		if err != nil {
			return corpus{}, err
		}
		masked, err := secrets.Mask(n.text)
		if err != nil || strings.TrimSpace(masked.DerivedText()) == "" {
			return corpus{}, errSnapshot
		}
		c.Summaries = append(c.Summaries, summary{fmt.Sprintf("snapshot-summary-%08d", len(c.Summaries)), sessions[n.session], masked.DerivedText(), end})
	}
	// LINA publishes a growing prefix. Equal endpoints still retain a node's
	// text as a parent of the preceding root; this is a workload, not an import.
	slices.SortStableFunc(c.Summaries, func(a, b summary) int { return position[a.End] - position[b.End] })
	after, err := digest(path)
	if err != nil || before != after || stable() != nil || len(c.Turns) == 0 {
		return corpus{}, errSnapshot
	}
	return c, nil
}

func TestSnapshotReadOnlyAndNumericReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.sqlite")
	db, err := canon.OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`CREATE TABLE messages(store_id INTEGER PRIMARY KEY,session_id TEXT,role TEXT,content TEXT)`,
		`CREATE TABLE summary_nodes(node_id INTEGER PRIMARY KEY,session_id TEXT,summary TEXT,source_type TEXT,source_ids TEXT)`,
	} {
		if _, err := db.ExecContext(t.Context(), sql); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	// Text is generated from the same public word list, including a material.
	fixture := generate(4)
	n := 0
	for _, turn := range fixture.Turns {
		for _, msg := range turn {
			role := "assistant"
			if msg.Entry == guard.EntryOwner {
				role = "user"
			} else if msg.Entry == guard.EntryTool {
				role = "tool"
			}
			n++
			if _, err := db.ExecContext(t.Context(), "INSERT INTO messages VALUES(?,?,?,?)", n, "session", role, msg.Text); err != nil {
				db.Close()
				t.Fatal(err)
			}
		}
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO summary_nodes VALUES(1,'session',?,'messages','[1,2]'),(2,'session',?,'nodes','[1]')", english[0], korean[0]); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := digest(path)
	c, err := readSnapshot(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := digest(path)
	if before != after || len(c.Turns) != 4 || len(c.Summaries) != 2 {
		t.Fatal("snapshot changed or corpus omitted")
	}
	again, err := readSnapshot(t.Context(), path)
	if err != nil || !reflect.DeepEqual(c, again) {
		t.Fatal("snapshot corpus not reproducible")
	}
	r, err := measure(t.Context(), t.TempDir(), c)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(r)
	var shape any
	if err := json.Unmarshal(data, &shape); err != nil {
		t.Fatal(err)
	}
	checkNumeric(t, shape)
	if r.MaterialBytes == 0 || r.Latency["publish_summary"].Count != 2 {
		t.Fatal("private path omitted materials or summaries")
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 1 || files[0].Name() != filepath.Base(path) {
		t.Fatal("snapshot reader created source files")
	}
	// A valid synthetic source is varied one contract at a time. Diagnostics
	// remain fixed even if the rejected source holds arbitrary private text.
	for _, change := range []struct{ corrupt, restore string }{
		{"UPDATE messages SET role='unknown' WHERE store_id=1", "UPDATE messages SET role='user' WHERE store_id=1"},
		{"UPDATE summary_nodes SET source_ids='[999999]' WHERE node_id=2", "UPDATE summary_nodes SET source_ids='[1]' WHERE node_id=2"},
		{"UPDATE summary_nodes SET source_ids='[2]' WHERE node_id=2", "UPDATE summary_nodes SET source_ids='[1]' WHERE node_id=2"},
	} {
		updateSnapshot(t, path, change.corrupt)
		if _, err := readSnapshot(t.Context(), path); !errors.Is(err, errSnapshot) {
			t.Fatal("malformed snapshot accepted")
		}
		updateSnapshot(t, path, change.restore)
	}
	if err := os.WriteFile(path+"-wal", []byte{1}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSnapshot(t.Context(), path); !errors.Is(err, errSnapshot) {
		t.Fatal("live WAL accepted")
	}
}

func updateSnapshot(t *testing.T, path, statement string) {
	t.Helper()
	db, err := canon.OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(t.Context(), statement)
	if err := errors.Join(err, db.Close()); err != nil {
		t.Fatal(err)
	}
}
