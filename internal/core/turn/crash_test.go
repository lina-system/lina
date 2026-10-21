// SPDX-License-Identifier: Apache-2.0
package turn_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

func crashStore(t *testing.T, root string, crash bool) (*canon.Store, loop.Tool) {
	t.Helper()
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	epoch := uint64(2)
	if crash {
		epoch = 1
	}
	migrations := canon.Migrations()
	migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: turn.Schema})
	db := open(t, canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: epoch, Controls: controls, Migrations: migrations})
	auth := grant.Request{GrantID: "grant", MainID: "main", Epoch: epoch, TaskID: "turn", Host: "host", OSUser: "user", Person: "person", Agent: "agent", Revision: "r1"}
	svc, err := authority.Open(t.Context(), db, authority.Config{Epoch: epoch, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
		return grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: "edit", Target: "document", Write: true}}, Revisions: map[string]string{"document": "r1"}}, grant.Environment{Epoch: epoch, Now: time.Now(), OSPermitted: true, Revision: "r1"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	spec := tools.Spec{Name: "edit", Conc: 1, Deadline: time.Second, Placement: tools.Core, Schema: func(raw json.RawMessage) (tools.Input, error) {
		return tools.Input{Arguments: raw, Target: "document", Write: true, What: "edit", Where: "document", ScopeKey: "edit", Undoable: true}, nil
	}}
	registry := tools.NewRegistry(svc, leaser{})
	if err = registry.Register(spec, func(ctx context.Context, _ lease.Lease, _ tools.Input) (tools.Result, error) {
		var calls, intents int
		err := db.Read(ctx, func(tx *canon.Tx) error {
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM turn_calls WHERE turn_id='turn'").Scan(&calls); err != nil {
				return err
			}
			return tx.QueryRowContext(ctx, "SELECT count(*) FROM authority_effects WHERE running=1").Scan(&intents)
		})
		if err != nil || calls != 1 || intents != 1 {
			return tools.Result{State: tools.Failed}, errors.New("dispatch preceded commits")
		}
		path := filepath.Join(root, "effect")
		previous, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return tools.Result{State: tools.Failed}, err
		}
		if err = os.WriteFile(path, append(previous, []byte("applied\n")...), 0600); err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		if crash {
			os.Exit(73)
		} // real process death after effect, before receipt
		return tools.Result{State: tools.Applied, Revision: "r2"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	return db, turn.Tool(registry, spec, tools.Call{Auth: auth}, "turn")
}
func TestProcessCrashAfterEffectBeforeReceipt(t *testing.T) {
	root := os.Getenv("LINA_TURN_CRASH_ROOT")
	if root != "" {
		db, tool := crashStore(t, root, true)
		id := admit(t, db)
		c := provider.ToolCall{ID: "a", Name: "edit", Arguments: `{}`}
		cfg := loop.Config{Request: provider.Request{Tools: []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"edit"}`)}}, Tools: map[string]loop.Tool{"edit": tool}, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
			return &stream{[]provider.Event{{Kind: provider.ItemDone, Item: json.RawMessage(`{"type":"function_call","call_id":"a","name":"edit","arguments":"{}"}`)}, {Kind: provider.ToolCallReady, Call: &c}, {Kind: provider.Terminal}}}, nil
		}}
		_, err := turn.New(db).Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: id, Loop: cfg})
		t.Fatalf("effect did not terminate process: %v", err)
	}
	root = filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, "-test.run=^TestProcessCrashAfterEffectBeforeReceipt$")
	child.Env = []string{"LINA_TURN_CRASH_ROOT=" + root}
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("child: %v %s", err, output)
	}
	db, tool := crashStore(t, root, false)
	s := turn.New(db)
	if err = s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	result := tool.Execute(t.Context(), provider.ToolCall{ID: "a", Name: "edit", Arguments: `{}`})
	if result.Code != loop.Unknown {
		t.Fatal(result)
	}
	receipt, err := inbox.New(db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: "conversation", Source: "client", NativeID: "message"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Claim(t.Context(), "replacement", "conversation", receipt.ID); !errors.Is(err, turn.ErrAccepted) {
		t.Fatal(err)
	}
	row, err := s.Read(t.Context(), "turn")
	if err != nil || len(row.Results) != 1 || row.Results[0].Code != loop.Aborted || row.State != turn.Aborted {
		t.Fatalf("%+v %v", row, err)
	}
	effect, err := os.ReadFile(filepath.Join(root, "effect"))
	if err != nil || string(effect) != "applied\n" {
		t.Fatalf("effect=%q err=%v", effect, err)
	}
}
