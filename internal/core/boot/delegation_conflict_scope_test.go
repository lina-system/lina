// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/codex"
	"github.com/thisisjun786/lina/kit/lease"
)

func TestConflictChannelPermission(t *testing.T) {
	for _, change := range []string{"unchanged", "tools-disabled", "disconnected"} {
		t.Run(change, func(t *testing.T) {
			r := controlRuntime(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			now := time.Now()
			store := r.channels.store
			bot := channels.Bot{Platform: "telegram", Account: "fixture"}
			if err := store.Connect(ctx, bot, r.person); err != nil {
				t.Fatal(err)
			}
			code, err := store.PairingCode(ctx, bot, r.person, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Pair(ctx, bot, code, "42", "100", now); err != nil {
				t.Fatal(err)
			}
			binding := channels.Binding{Address: channels.Address{Bot: bot, Chat: "100"}, Person: r.person, Agent: r.agent, Conversation: r.conversation, Scope: channels.Scope{Reads: channels.DMOnly, Writes: channels.ReplyOnly, Tools: true}}
			if err := store.Bind(ctx, binding); err != nil {
				t.Fatal(err)
			}
			var delivery gateway.Delivery
			if err := gateway.New(r.db, func(_ context.Context, d gateway.Delivery) error { delivery = d; return nil }).Admit(ctx, gateway.Inbound{Address: binding.Address, NativeID: "source", Sender: "42", Cursor: "1", Direct: true, Body: []byte(`{"Text":"do the task"}`)}); err != nil {
				t.Fatal(err)
			}
			input, err := r.journal().Admit(ctx, guard.EntryOwner, ledger.Input{ID: "channel-input:" + strconv.FormatInt(delivery.InboxID, 10), PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Surface: ledger.Surface{Kind: "channel", Platform: bot.Platform, ChatID: binding.Chat}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "do the task"})
			if err != nil {
				t.Fatal(err)
			}
			task := work.Task{ID: "channel-task", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Schedule: &work.Schedule{InputID: input.ID, Repository: "fixture/repo", Worker: "codex", Host: "local"}}
			// Use the actual adapter and count turn/start RPCs. The first turn
			// completes before the channel permission changes.
			dir := t.TempDir()
			node, err := exec.LookPath("node")
			if err != nil {
				t.Fatal(err)
			}
			program := filepath.Join(dir, "codex")
			if err := os.WriteFile(program, []byte("#!"+node+"\n"+conflictScopeWorker), 0700); err != nil {
				t.Fatal(err)
			}
			process := lease.New(func() {})
			defer process.Release()
			adapter, err := codex.Open(ctx, process, codex.Config{Executable: program})
			if err != nil {
				t.Fatal(err)
			}
			defer adapter.Close()
			thread, err := adapter.Start(ctx, process, worker.StartSpec{TaskID: task.ID, AttemptID: "attempt", Workdir: dir, TaskPacket: "Do the task."})
			if err != nil {
				t.Fatal(err)
			}
			for {
				select {
				case event := <-adapter.Events():
					if event.Kind != worker.FinishEvent {
						continue
					}
					if event.Finish.Status != worker.Done {
						t.Fatalf("first turn failed: %+v", event.Finish)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				break
			}
			switch change {
			case "tools-disabled":
				binding.Scope.Tools = false
				if err := store.Bind(ctx, binding); err != nil {
					t.Fatal(err)
				}
			case "disconnected":
				if err := store.Disconnect(ctx, bot, r.person); err != nil {
					t.Fatal(err)
				}
			}
			d := &delegationRuntime{root: r, threads: map[string]worker.Thread{task.ID: thread}, adapters: map[string]*codex.Session{task.ID: adapter}, leases: map[string]lease.Lease{}}
			defer func() {
				for _, l := range d.leases {
					l.Release()
				}
			}()
			run, err := d.returnConflict(ctx, work.Card{Task: task, Run: &work.Run{ID: "channel-task:1", TaskID: task.ID, Generation: 1, Worker: "codex", ThreadID: thread.ID, TurnID: thread.TurnID, NodeID: "local"}})
			want := "1"
			if change == "unchanged" {
				want = "2"
				if err != nil || run.Generation != 2 {
					t.Fatalf("authorized correction refused: %+v %v", run, err)
				}
			} else if !errors.Is(err, channels.ErrDenied) || run.ID != "" || len(d.leases) != 0 {
				t.Errorf("revoked origin reached conflict correction: run=%+v leases=%d err=%v", run, len(d.leases), err)
			}
			starts, readErr := os.ReadFile(filepath.Join(dir, "turn-count"))
			if readErr != nil || string(starts) != want {
				t.Fatalf("correction input sent after %s: turn starts=%q want=%s err=%v", change, starts, want, readErr)
			}
		})
	}
}

const conflictScopeWorker = `// SPDX-License-Identifier: Apache-2.0
const fs = require('node:fs');
const path = require('node:path');
if(process.argv.includes('--version')) { console.log('codex-cli 99.0.0'); } else {
 let count=0, thread={id:'fixture',status:{type:'idle'},turns:[]}, cwd;
 const send = value => console.log(JSON.stringify(value));
 require('node:readline').createInterface({input:process.stdin}).on('line', line => {
  const {id,method,params:p}=JSON.parse(line);
  let result={};
  switch(method) {
   case 'initialize': result={userAgent:'fixture'}; break;
   case 'initialized': return;
   case 'account/rateLimits/read': result={rateLimits:{}}; break;
   case 'thread/start': cwd=p.cwd; result={thread,cwd,model:'fixture'}; break;
   case 'thread/read': result={thread}; break;
   case 'turn/start': {
    count++; fs.writeFileSync(path.join(cwd,'turn-count'),String(count));
    const turn={id:'turn-'+count,status:'completed',items:[]}; thread.turns=[turn];
    send({id,result:{turn}});
    send({method:'turn/completed',params:{threadId:thread.id,turn}});
    return;
   }
   default: send({id,error:{code:-32601,message:'unexpected fixture method'}}); return;
  }
  send({id,result});
 });
}
`
