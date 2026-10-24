// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Fifty separate conversation turns launch fifty installed protocol peers. Core
// owns every session, question, result and card; only the provider and Codex are
// synthetic. Each question stays pending until its PTY frame has been recorded.
func TestM3WorkPanelLiveMatrix(t *testing.T) {
	dirs := make([]string, 50)
	commands := make([]chan string, 50)
	for i := range dirs {
		dirs[i] = filepath.Join(t.TempDir(), fmt.Sprintf("panel-%02d", i))
		if err := os.Mkdir(dirs[i], 0700); err != nil {
			t.Fatal(err)
		}
		commands[i] = make(chan string, 1)
	}
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id int
		if _, err := fmt.Sscanf(r.URL.Path, "/panel-%d", &id); err != nil || id < 0 || id >= len(commands) {
			http.Error(w, "invalid worker", 400)
			return
		}
		select {
		case command := <-commands[id]:
			fmt.Fprint(w, command)
		case <-r.Context().Done():
		}
	}))
	defer gate.Close()
	var summaries atomic.Int32
	server := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input, Tools []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		var last struct{ Type, Role, Content, Output string }
		if len(req.Input) > 0 {
			_ = json.Unmarshal(req.Input[len(req.Input)-1], &last)
		}
		if last.Role == "developer" && strings.Contains(last.Content, "unanswered_requests") && strings.Contains(last.Content, "omitted_detail") {
			summaries.Add(1)
			text := mustJSON(t, compaction.Text{Requests: "Keep the running panel jobs active.", Topics: "Independent panel jobs.", Actions: "Earlier jobs were started; their workers remain active.", Values: "Read current cards and worker results.", Omitted: "Earlier tool details remain in the ledger."})
			emitResponse(w, string(text), true)
			return
		}
		if scriptedCodingManager(t, w, req) || respondWorkerCompletion(w, req.Input, "PANEL_RESULT") {
			return
		}
		if last.Type == "function_call_output" {
			var result struct{ Code string }
			if json.Unmarshal([]byte(last.Output), &result) != nil || result.Code != "ok" {
				t.Errorf("panel worker start: %s", last.Output)
			}
			emitResponse(w, "Started.", true)
			return
		}
		var id int
		// The input is followed by Core's ContextPacket; find this fixture's
		// latest explicit launch request without interpreting packet evidence.
		for _, raw := range req.Input {
			var item struct{ Role, Content string }
			if json.Unmarshal(raw, &item) == nil && item.Role == "user" && strings.HasPrefix(item.Content, "Panel item ") {
				if _, err := fmt.Sscanf(item.Content, "Panel item %d", &id); err != nil {
					t.Error(err)
					return
				}
			}
		}
		emitPersonaCall(w, fmt.Sprintf("start-%02d", id), "worker_start", map[string]any{"workdir": dirs[id], "task_packet": "Create result.txt with PANEL_RESULT."})
	})))
	defer server.Close()
	h, bootstrap := installedConversationHarness(t, server.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	h.ctx = ctx
	seedCodingFixture(t, h, bootstrap)
	bin := t.TempDir()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(node, filepath.Join(bin, "node")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bin, "codex"), []byte(panelWorkerScript), 0700); err != nil {
		t.Fatal(err)
	}
	core := h.command("core", "--state-root", h.root, "--socket", h.socket)
	core.command.Env = append(core.command.Env, "PATH="+bin, "LINA_PANEL_GATE="+gate.URL, "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
	core.start(t)
	h.connect(core).Close()
	defer h.stopCore(core)
	waitCoreReady(t, h)
	client, conversation := conversationClient(t, h, protocol.Capability{Name: "waiting", Version: "1.0"})
	defer client.Close()
	topic := "waiting"
	if _, err = client.Subscribe(ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Receive(ctx); err != nil {
		t.Fatal(err)
	}
	var terminal *terminal
	var pending protocol.Question
	answer := func() {
		t.Helper()
		value := "Continue"
		values := protocol.FormValues{"answer": protocol.FormValue{String: &value}}
		if _, err := client.AnswerQuestion(ctx, protocol.QuestionAnswer{CardID: &pending.Card.ID, Revision: &pending.Card.Revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values}); err != nil {
			t.Fatal(err)
		}
	}
	question := func(id int, label string) protocol.Question {
		t.Helper()
		commands[id] <- "ask:" + label
		for {
			frame, err := client.Receive(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if frame.Error != nil {
				t.Fatalf("panel protocol: %+v", frame.Error)
			}
			if q, ok := frame.Payload.(protocol.Question); ok && q.Card != nil && q.Card.Summary == label {
				return q
			}
		}
	}
	for i := 0; i < 50; i++ {
		if pending.Card != nil {
			answer()
		}
		frames := admittedTurn(t, h, client, conversation, fmt.Sprintf("panel-%02d", i), fmt.Sprintf("Panel item %02d", i))
		if rows := panelOverview(t, h); len(rows) != i+1 {
			t.Fatalf("Core launched %d of %d jobs; summary requests=%d; turn events=%s", len(rows), i+1, summaries.Load(), mustJSON(t, frames))
		}
		if !slices.Contains([]int{1, 5, 20, 50}, i+1) {
			pending = protocol.Question{}
			continue
		}
		pending = question(i, fmt.Sprintf("Check item %02d", i))
		for _, language := range []design.Language{design.English, design.Korean} {
			for _, ascii := range []bool{false, true} {
				env := []string{"TERM=xterm-256color", "LANG=" + string(language) + "_US.UTF-8", "LC_ALL=" + string(language) + "_US.UTF-8"}
				if ascii {
					env = append(env, "LINA_GLYPHS=ascii")
				}
				terminal = h.startTerminalSize(env, false, true, 120, 34)
				snap, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
					rows := panelCellRows(s, 80, 40)
					return !s.CursorHidden && strings.Contains(panelCellRows(s, 0, 120)[33], design.Text(language, "live")) && strings.Contains(rows[2], fmt.Sprintf("Panel item %02d", i)) && strings.Contains(rows[3], pending.Card.Summary)
				})
				if err != nil {
					t.Fatalf("live matrix N=%d: %v\n%s", i+1, err, screenText(snap))
				}
				rows := panelOverview(t, h)
				running, waiting := 0, 0
				for _, row := range rows {
					if row.Line.State != nil && *row.Line.State == protocol.RunningStateInProgress {
						running++
					}
					if row.Line.Waiting != "0" {
						waiting++
					}
				}
				if len(rows) != i+1 || running != i || waiting != 1 {
					t.Fatalf("N=%d running=%d waiting=%d rows=%d", i+1, running, waiting, len(rows))
				}
				panel := panelCellRows(snap, 80, 40)
				shown := 0
				for y := 2; y+1 < len(panel) && strings.HasPrefix(panel[y+1], "  "); y += 2 {
					if !strings.Contains(panel[y], "Panel item ") || !strings.HasSuffix(panel[y], "m") || strings.TrimSpace(panel[y+1]) == "" {
						t.Fatalf("invalid two-row card: %q / %q", panel[y], panel[y+1])
					}
					shown++
				}
				if shown != min(i+1, 14) {
					t.Fatalf("visible=%d want=%d", shown, min(i+1, 14))
				}
				if i+1 > shown {
					ellipsis := "…"
					if ascii {
						ellipsis = "..."
					}
					want := fmt.Sprintf(design.Text(language, "work.more"), ellipsis, i+1-shown)
					if !strings.Contains(strings.Join(panel, "\n"), want) {
						t.Fatalf("missing overflow %q", want)
					}
				}
				for _, row := range panel[1:31] {
					for _, r := range row {
						if r >= 0x2190 && r <= 0x21ff || r >= 0x2500 && r <= 0x257f {
							t.Fatalf("graph character in card: %U", r)
						}
					}
				}
				t.Logf("live matrix N=%d running=%d waiting=%d shown=%d hidden=%d %s ascii=%t 120x34\n%s", i+1, running, waiting, shown, i+1-shown, language, ascii, screenText(snap))
				h.keys(terminal, "\x04")
				terminal.process.wait(t, 0)
				h.expectTerminalRestored(terminal)
			}
		}
	}
	firstQuestion := pending
	extraQuestions := []protocol.Question{question(48, "Check item 48"), question(47, "Check item 47")}
	terminal = h.startTerminalSize([]string{"TERM=xterm-256color", "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8"}, false, true, 120, 34)
	waitResizeChrome(t, h, terminal, 34, "")
	// The connection and waiting cards can render before the conversation
	// snapshot. PgUp must act on known history, not an initially empty view.
	if snap, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
		return strings.Contains(strings.Join(panelCellRows(s, 0, 79), "\n"), "Panel item 49")
	}); err != nil {
		t.Fatalf("conversation history missing before scroll: %v\n%s", err, screenText(snap))
	}
	three, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
		rows := panelCellRows(s, 80, 40)
		firstCards := strings.Join(rows[2:8], "\n")
		return strings.Contains(firstCards, "Panel item 47") && strings.Contains(firstCards, "Panel item 48") && strings.Contains(firstCards, "Panel item 49") && strings.Contains(firstCards, "Check item 47") && strings.Contains(firstCards, "Check item 48") && strings.Contains(firstCards, "Check item 49")
	})
	if err != nil {
		t.Fatalf("three person-needed cards not visible: %v\n%s", err, screenText(three))
	}
	t.Logf("live matrix N=50 needs_person=3 visible_without_scroll=true\n%s", screenText(three))
	for _, q := range extraQuestions {
		pending = q
		answer()
	}
	pending = firstQuestion
	if snap, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
		return strings.Contains(panelCellRows(s, 80, 40)[2], "Panel item 49") && strings.Contains(panelCellRows(s, 80, 40)[3], pending.Card.Summary)
	}); err != nil {
		t.Fatalf("single question order not restored: %v\n%s", err, screenText(snap))
	}
	h.keys(terminal, "Keep this draft")
	beforeScroll := waitResizeChrome(t, h, terminal, 34, "Keep this draft")
	h.keys(terminal, "\x1b[5~")
	anchor, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
		return strings.Join(panelCellRows(s, 0, 79)[1:11], "\n") != strings.Join(panelCellRows(beforeScroll, 0, 79)[1:11], "\n")
	})
	if err != nil {
		t.Fatal("could not establish conversation reading anchor", err)
	}
	anchorText := strings.Join(panelCellRows(anchor, 0, 79)[1:11], "\n")
	db, err := canon.OpenReader(ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var times []time.Duration
	for i := 0; i < 50; i++ {
		answer()
		pending = question(49, fmt.Sprintf("Check update %02d", i))
		snap, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
			return strings.Contains(panelCellRows(s, 80, 40)[3], pending.Card.Summary)
		})
		observed := time.Now()
		if err != nil {
			t.Fatalf("update %d: %v\n%s", i, err, screenText(snap))
		}
		var recorded string
		if err = db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT recorded_at FROM conv_event WHERE id=?", pending.Card.ID).Scan(&recorded)
		}); err != nil {
			t.Fatal(err)
		}
		at, err := time.Parse(time.RFC3339Nano, recorded)
		if err != nil {
			t.Fatal(err)
		}
		elapsed := observed.Sub(at)
		if elapsed < 0 {
			t.Fatal("negative event latency")
		}
		times = append(times, elapsed)
		if snap.CursorHidden || !strings.Contains(screenText(snap), "Keep this draft") || strings.Join(panelCellRows(snap, 0, 79)[1:11], "\n") != anchorText {
			t.Fatalf("update changed focus, draft or conversation reading anchor: hidden=%t draft=%t\nbefore:\n%s\nafter:\n%s\nframe:\n%s", snap.CursorHidden, strings.Contains(screenText(snap), "Keep this draft"), anchorText, strings.Join(panelCellRows(snap, 0, 79)[1:11], "\n"), screenText(snap))
		}
		t.Logf("latency update=%02d recorded=%s observed=%s elapsed_ms=%.3f", i, at.UTC().Format(time.RFC3339Nano), observed.UTC().Format(time.RFC3339Nano), float64(elapsed)/float64(time.Millisecond))
	}
	slices.Sort(times)
	t.Logf("latency N=50 samples=50 p95_ms=%.3f max_ms=%.3f\n%s", float64(times[47])/float64(time.Millisecond), float64(times[49])/float64(time.Millisecond), screenText(terminal.screen.Snapshot()))
	if times[47] > 500*time.Millisecond || times[49] > time.Second {
		t.Errorf("T-16 latency exceeded: p95=%s max=%s", times[47], times[49])
	}
	answer()
	for i := range commands {
		commands[i] <- "finish"
	}
	// The final result is checked from actual worker writes, independently of
	// the synthetic provider's description. Core must publish every finish too.
	waitWorkerCanon(t, h, `SELECT count(*) FROM conv_event WHERE scaffold_kind='worker_completion'`, 50)
	for _, dir := range dirs {
		raw, err := os.ReadFile(filepath.Join(dir, "result.txt"))
		if err != nil || string(raw) != "PANEL_RESULT" {
			t.Fatalf("worker artifact: %q %v", raw, err)
		}
	}
	t.Logf("completed workers=50 artifacts=50 summary_requests=%d", summaries.Load())
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
}

const panelWorkerScript = `#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
const fs=require('node:fs'),path=require('node:path'),readline=require('node:readline');
if(process.argv[2]==='--version'){console.log('codex-cli 99.0.0');process.exit(0)}
if(process.env.LINA_RESPONSES_API_KEY)process.exit(91);
const send=x=>process.stdout.write(JSON.stringify(x)+'\n');
const notify=(method,params)=>send({method,params});
let cwd,thread,turn='panel-turn',finished=false;
async function commands(){
 for(;;){
  const response=await fetch(process.env.LINA_PANEL_GATE+'/'+thread);
  const command=await response.text();
  if(command.startsWith('ask:')){
   const label=command.slice(4);
   send({id:label,method:'item/tool/call',params:{threadId:thread,turnId:turn,callId:label,namespace:'lina',tool:'ask',arguments:{question:label}}});
  }else if(command==='finish'){
   fs.writeFileSync(path.join(cwd,'result.txt'),'PANEL_RESULT');
   notify('item/completed',{threadId:thread,turnId:turn,item:{type:'agentMessage',id:'result',text:'PANEL_RESULT'}});
   finished=true;
   notify('turn/completed',{threadId:thread,turn:{id:turn,status:'completed'}});
   return;
  }else process.exit(92);
 }
}
readline.createInterface({input:process.stdin}).on('line',line=>{
 const m=JSON.parse(line),reply=result=>send({id:m.id,result});
 switch(m.method){
 case 'initialize':reply({userAgent:'panel-fixture/99.0.0'});break;
 case 'initialized':break;
 case 'thread/start':cwd=m.params.cwd;thread=path.basename(cwd);reply({thread:{id:thread},cwd,model:'fixture'});break;
 case 'turn/start':notify('turn/started',{threadId:thread,turn:{id:turn,status:'inProgress'}});reply({turn:{id:turn,status:'inProgress'}});commands().catch(e=>{console.error(e);process.exit(93)});break;
 case 'thread/read':reply({thread:{id:thread,cwd,status:{type:finished?'idle':'active',activeFlags:[]},turns:[{id:turn,status:finished?'completed':'inProgress',items:[]}]}});break;
 case 'account/rateLimits/read':reply({rateLimits:{primary:{usedPercent:0,windowDurationMins:300},secondary:{usedPercent:0,windowDurationMins:10080}}});break;
 case undefined:if(m.error)process.exit(94);break;
 default:console.error(m.method);process.exit(95);
 }
});
`
