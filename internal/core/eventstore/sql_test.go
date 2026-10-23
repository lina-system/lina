// SPDX-License-Identifier: Apache-2.0
package eventstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/diag"
)

// This scripted driver verifies database/sql calls and error handling only. It
// deliberately does not emulate SQLite or claim persistence/durability evidence.
type sqlStep struct {
	op, statement string
	args          []driver.Value
	columns       []string
	rows          [][]driver.Value
	err           error
}
type sqlScript struct {
	t     *testing.T
	steps []sqlStep
}

func (s *sqlScript) take(op, statement string, args []driver.NamedValue) sqlStep {
	s.t.Helper()
	if len(s.steps) == 0 {
		s.t.Fatalf("unexpected SQL operation %s %s", op, statement)
	}
	step := s.steps[0]
	s.steps = s.steps[1:]
	if step.op != op || (step.statement != "" && !strings.Contains(statement, step.statement)) {
		s.t.Fatalf("SQL operation %s %s, want %s %s", op, statement, step.op, step.statement)
	}
	values := []driver.Value{}
	for _, arg := range args {
		values = append(values, arg.Value)
	}
	if step.args != nil && !reflect.DeepEqual(values, step.args) {
		s.t.Fatalf("SQL bindings = %v, want %v", values, step.args)
	}
	return step
}

type scriptedConnector struct{ script *sqlScript }

func (c scriptedConnector) Connect(context.Context) (driver.Conn, error) {
	return &scriptedConn{c.script}, nil
}
func (c scriptedConnector) Driver() driver.Driver { return scriptedDriver(c) }

type scriptedDriver struct{ script *sqlScript }

func (d scriptedDriver) Open(string) (driver.Conn, error) { return &scriptedConn{d.script}, nil }

type scriptedConn struct{ script *sqlScript }

func (*scriptedConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*scriptedConn) Close() error { return nil }
func (c *scriptedConn) Begin() (driver.Tx, error) {
	step := c.script.take("begin", "", nil)
	if step.err != nil {
		return nil, step.err
	}
	return scriptedTx{c.script}, nil
}
func (c *scriptedConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	step := c.script.take("exec", q, args)
	return driver.RowsAffected(1), step.err
}
func (c *scriptedConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	step := c.script.take("query", q, args)
	if step.err != nil {
		return nil, step.err
	}
	return &scriptedRows{step.columns, step.rows}, nil
}

type scriptedTx struct{ script *sqlScript }

func (tx scriptedTx) Commit() error   { return tx.script.take("commit", "", nil).err }
func (tx scriptedTx) Rollback() error { return tx.script.take("rollback", "", nil).err }

type scriptedRows struct {
	columns []string
	rows    [][]driver.Value
}

func (r *scriptedRows) Columns() []string { return r.columns }
func (*scriptedRows) Close() error        { return nil }
func (r *scriptedRows) Next(values []driver.Value) error {
	if len(r.rows) == 0 {
		return io.EOF
	}
	copy(values, r.rows[0])
	r.rows = r.rows[1:]
	return nil
}
func scriptedDB(t *testing.T, steps []sqlStep) *sql.DB {
	t.Helper()
	s := &sqlScript{t, steps}
	db := sql.OpenDB(scriptedConnector{s})
	t.Cleanup(func() {
		db.Close()
		if len(s.steps) != 0 {
			t.Errorf("%d SQL operations unexecuted", len(s.steps))
		}
	})
	return db
}
func sqlFixture(t *testing.T) diag.Event {
	s, _ := memoryStore()
	return root(t, s, diag.References{TaskID: newID(t)})
}

func TestSQLInitializationUsesDedicatedFullSynchronousConnection(t *testing.T) {
	db := scriptedDB(t, []sqlStep{
		{op: "query", statement: "PRAGMA journal_mode=WAL", columns: []string{"journal_mode"}, rows: [][]driver.Value{{"wal"}}},
		{op: "exec", statement: "PRAGMA synchronous=FULL"},
		{op: "query", statement: "PRAGMA synchronous", columns: []string{"synchronous"}, rows: [][]driver.Value{{int64(2)}}},
		{op: "exec", statement: "CREATE TABLE IF NOT EXISTS events"},
		{op: "query", statement: "SELECT event_id, record FROM events", columns: []string{"event_id", "record"}},
	})
	reserve, err := PrepareReserve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer reserve.Close()
	s, err := Open(t.Context(), db, reserve, func(error) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if db.Stats().InUse != 1 {
		t.Fatal("writer connection not reserved")
	}
}

func TestSQLCommitAndRollback(t *testing.T) {
	e := sqlFixture(t)
	record, _ := json.Marshal(e)
	failure := errors.New("synthetic disk fault")
	for _, stage := range []string{"success", "insert", "commit", "already committed", "id collision"} {
		t.Run(stage, func(t *testing.T) {
			steps := []sqlStep{{op: "begin"}, {op: "query", statement: "SELECT record FROM events WHERE event_id = ?", args: []driver.Value{e.EventID.String()}, columns: []string{"record"}}}
			if stage == "already committed" || stage == "id collision" {
				existing := string(record)
				if stage == "id collision" {
					existing = "{}"
				}
				steps[1].rows = [][]driver.Value{{existing}}
				if stage == "id collision" {
					steps = append(steps, sqlStep{op: "rollback"})
				} else {
					steps = append(steps, sqlStep{op: "commit"})
				}
			} else {
				insert := sqlStep{op: "exec", statement: "INSERT INTO events (event_id, record) VALUES (?, ?)", args: []driver.Value{e.EventID.String(), string(record)}}
				if stage == "insert" {
					insert.err = failure
				}
				steps = append(steps, insert)
				if stage == "insert" {
					steps = append(steps, sqlStep{op: "rollback"})
				} else {
					commit := sqlStep{op: "commit"}
					if stage == "commit" {
						commit.err = failure
					}
					steps = append(steps, commit)
				}
			}
			db := scriptedDB(t, steps)
			conn, err := db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			b := sqlBackend{conn}
			err = b.append(t.Context(), e)
			wantErr := stage == "insert" || stage == "commit" || stage == "id collision"
			if (err != nil) != wantErr {
				t.Fatalf("append error: %v", err)
			}
		})
	}
}

func TestSQLReadRejectsUnredactedOrMismatchedRows(t *testing.T) {
	e := sqlFixture(t)
	record, _ := json.Marshal(e)
	for _, item := range []struct {
		name, id, record string
		wantErr          bool
	}{
		{"valid", e.EventID.String(), string(record), false},
		{"wrong index", newID(t).String(), string(record), true},
		{"raw payload", e.EventID.String(), strings.TrimSuffix(string(record), "}") + `,"provider_body":"private"}`, true},
		{"bad id", e.EventID.String(), strings.Replace(string(record), `"agent_id":null`, `"agent_id":"/home/example"`, 1), true},
	} {
		t.Run(item.name, func(t *testing.T) {
			db := scriptedDB(t, []sqlStep{{op: "query", statement: "SELECT event_id, record FROM events", columns: []string{"event_id", "record"}, rows: [][]driver.Value{{item.id, item.record}}}})
			conn, err := db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			b := sqlBackend{conn}
			events, err := b.all(t.Context())
			if (err != nil) != item.wantErr || (!item.wantErr && len(events) != 1) {
				t.Fatalf("read: %v %v", events, err)
			}
		})
	}
}
