// SPDX-License-Identifier: Apache-2.0
package m4_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/addon"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Only called while this disposable Core is stopped.
func kakaoSQL(t *testing.T, root, query string, args ...any) {
	t.Helper()
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	epoch, err := ctl.Epoch()
	if err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon/canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	if err := db.Write(t.Context(), func(tx *canon.Tx) error { _, err := tx.ExecContext(t.Context(), query, args...); return err }); err != nil {
		t.Fatal(err)
	}
}

func kakaoFixture(t *testing.T, root string, value map[string]any) string {
	t.Helper()
	dir := addon.DataDir(root, "kakaotalk", "kakaotalk")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func kakaoRequest(t *testing.T, client *clientapi.Client, request protocol.KakaotalkRequest) protocol.KakaotalkResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if _, err := client.KakaoTalk(ctx, request); err != nil {
		t.Fatal(err)
	}
	frame, err := client.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Error != nil {
		t.Fatalf("%s: %+v", request.Operation, frame.Error)
	}
	result, ok := frame.Payload.(protocol.KakaotalkResult)
	if !ok {
		t.Fatalf("KakaoTalk reply: %T", frame.Payload)
	}
	return result
}

// The running Core handles the owner relay and reads the real driver inside the
// OS sandbox. Its only platform dependency is the synthetic LOCO-shaped SDK.
func TestM4KakaoTalkSource(t *testing.T) {
	requireAddonSandbox(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	program, err := filepath.Abs("../../adapters/messenger/kakaotalk-fixture-main.ts")
	if err != nil {
		t.Fatal(err)
	}
	root := addonRoot(t, "kakaotalk", "7788", messenger.Source{Platform: "other", Account: "other-account", Adapter: "other", Mode: messenger.ModeOfficial, State: messenger.StateConnected})
	// The owner connects through the socket; a fixture must not pre-connect it.
	kakaoSQL(t, root, `UPDATE messenger_source SET state='disconnected' WHERE platform='kakaotalk'`)
	const big = "9007199254740993123"
	message := func(id, body string, kind int, attachment any) map[string]any {
		return map[string]any{"log_id": id, "type": kind, "author_id": "42", "author_name": "Someone", "message": body, "attachment": attachment, "sent_at": time.Now().Unix()}
	}
	chat := func(last string) []any {
		return []any{map[string]any{"chat_id": "dm-1", "type": 11, "display_name": "Direct", "title": nil, "active_members": 2, "last_log_id": last}}
	}
	script := map[string]any{
		"login":   map[string]any{"attempts": []any{map[string]any{"authenticated": true, "account_id": "7788", "user_id": "7788"}}},
		"profile": map[string]any{"user_id": "7788", "nickname": "Me"},
		"chats":   chat(big),
		"pages": map[string]any{
			"":  map[string]any{"messages": []any{message("1", "first", 1, nil)}, "next_cursor": "1", "complete": false},
			"1": map[string]any{"messages": []any{message(big, "", 18, map[string]any{"k": "file-key", "name": "report.pdf", "mt": "application/pdf", "s": 2048})}, "next_cursor": big, "complete": true},
		},
		"members": []any{map[string]any{"user_id": "42", "nickname": "Someone"}},
		"push": []any{map[string]any{"method": "MSG", "body": map[string]any{"chatId": "dm-1", "chatLog": map[string]any{
			"logId": "9007199254740993125", "type": 1, "authorId": "42", "authorName": "Someone", "message": "live", "sendAt": time.Now().Unix(),
		}}}},
	}
	dir := kakaoFixture(t, root, script)
	defer func() {
		if t.Failed() {
			t.Logf("sources: %s", sourceRows(t, root))
			calls, _ := os.ReadFile(filepath.Join(dir, "calls.json"))
			t.Logf("SDK methods: %s", calls)
		}
	}()

	cfg := addon.Config{Node: node, Program: program, StateRoot: root, Socket: filepath.Join(root, "core.sock"), SingleAccount: []string{"kakaotalk"}, RequestTimeout: 15 * time.Second}
	start := func() func() {
		command := exec.Command(os.Args[0], "-test.run=^TestKakaoTalkCoreProcess$")
		command.Env = append(os.Environ(), "LINA_TEST_KAKAO_ROOT="+root, "LINA_TEST_KAKAO_NODE="+node, "LINA_TEST_KAKAO_PROGRAM="+program)
		command.Stderr = os.Stderr
		stdout, err := command.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		stopped := false
		t.Cleanup(func() {
			if !stopped {
				_ = command.Process.Kill()
				<-done
			}
		})
		ready := make(chan error, 1)
		go func() {
			line, err := bufio.NewReader(stdout).ReadString('\n')
			if err == nil && line != "ready\n" {
				err = fmt.Errorf("unexpected Core startup: %q", line)
			}
			ready <- err
		}()
		select {
		case err := <-ready:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("Core not ready")
		}
		return func() {
			_ = command.Process.Signal(os.Interrupt)
			select {
			case err := <-done:
				stopped = true
				if err != nil {
					t.Errorf("Core stopped: %v", err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("Core did not stop")
			}
		}
	}
	stop := start()
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	waitFor(t, "Core socket", func() bool { _, err := os.Stat(cfg.Socket); return err == nil })
	client, err := clientapi.DialWithCapabilities(t.Context(), cfg.Socket, "kakao-fixture", []protocol.Capability{{Name: "kakaotalk", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	card := kakaoRequest(t, client, protocol.KakaotalkRequest{Operation: "card"})
	if card.Terms == nil || card.Links == nil || len(*card.Links) != 2 {
		t.Fatalf("card: %+v", card)
	}
	step := kakaoRequest(t, client, protocol.KakaotalkRequest{Operation: "connect", Accept: new(true)})
	if step.Step == nil || step.Step.Prompt == nil || step.Step.Prompt.Key != "email" {
		t.Fatalf("connect: %+v", step)
	}
	kakaoRequest(t, client, protocol.KakaotalkRequest{Operation: "answer", Prompt: new("email"), Value: new("person@example.test")})
	step = kakaoRequest(t, client, protocol.KakaotalkRequest{Operation: "answer", Prompt: new("password"), Value: new("synthetic-password")})
	if step.Step == nil || step.Step.State != "signed_in" {
		t.Fatalf("sign-in: %+v", step)
	}
	waitFor(t, "first fill through the sandboxed KakaoTalk driver", func() bool {
		return readCanon(t, root, `SELECT count(*) FROM messenger_record WHERE platform='kakaotalk'`) == 3
	})
	if got := readString(t, root, `SELECT cursor FROM messenger_chat WHERE platform='kakaotalk'`); got != big {
		t.Fatalf("cursor: %s", got)
	}
	if got := readString(t, root, `SELECT message_type FROM messenger_record WHERE message=?`, big); got != "18" {
		t.Fatalf("message type: %s", got)
	}
	if got := readString(t, root, `SELECT e.body FROM conv_event e JOIN messenger_record r ON r.event=e.id WHERE r.message=?`, big); got != "" {
		t.Fatalf("empty body: %q", got)
	}
	if got := readCanon(t, root, `SELECT count(*) FROM messenger_attachment WHERE message=? AND name='report.pdf' AND size=2048`, big); got != 1 {
		t.Fatalf("attachment rows: %d", got)
	}
	client.Close()
	stop()
	stop = nil

	// Age coverage in the stopped fixture, then catch up from the committed id.
	kakaoSQL(t, root, `UPDATE messenger_coverage SET read_at=? WHERE platform='kakaotalk'`, time.Now().Add(-49*time.Hour).UTC().Format(time.RFC3339Nano))
	script["chats"] = chat("9007199254740993124")
	script["pages"] = map[string]any{big: map[string]any{"messages": []any{message("1", "edited upstream without an edit signal", 1, nil), message("9007199254740993124", "new", 1, nil)}, "next_cursor": "9007199254740993124", "complete": true}}
	kakaoFixture(t, root, script)
	stop = start()
	waitFor(t, "incremental read after restart", func() bool {
		return readCanon(t, root, `SELECT count(*) FROM messenger_record WHERE platform='kakaotalk'`) == 4
	})
	waitFor(t, "one gap notice", func() bool {
		return readCanon(t, root, `SELECT count(*) FROM conv_event WHERE json_extract(origin,'$.locator') LIKE 'background-notice:%' AND body LIKE '%kakaotalk%'`) == 1
	})
	if got := readCanon(t, root, `SELECT count(*) FROM messenger_gap WHERE platform='kakaotalk'`); got != 1 {
		t.Fatalf("gap rows: %d", got)
	}
	stop()
	stop = nil

	// A quiet restart covers the chat without fetching any page or retelling it.
	stop = start()
	waitFor(t, "quiet chat coverage", func() bool {
		return readCanon(t, root, `SELECT count(*) FROM messenger_coverage WHERE platform='kakaotalk' AND read_from='9007199254740993124'`) == 1
	})
	stop()
	stop = nil
	calls, err := os.ReadFile(filepath.Join(dir, "calls.json"))
	if err != nil {
		t.Fatal(err)
	}
	var methods []string
	if err := json.Unmarshal(calls, &methods); err != nil {
		t.Fatal(err)
	}
	for _, method := range methods {
		if method == "getMessagePage" {
			t.Fatal("quiet chat fetched a page")
		}
	}
	if got := readCanon(t, root, `SELECT count(*) FROM conv_event WHERE json_extract(origin,'$.locator') LIKE 'background-notice:%' AND body LIKE '%kakaotalk%'`); got != 1 {
		t.Fatalf("repeated gap notice: %d", got)
	}

	// Rejected credentials isolate this source while another source catches up.
	script["expire"] = true
	kakaoFixture(t, root, script)
	kakaoSQL(t, root, `UPDATE messenger_chat SET cursor='' WHERE platform='other'`)
	kakaoSQL(t, root, `DELETE FROM messenger_coverage WHERE platform='other'`)
	stop = start()
	waitFor(t, "expired KakaoTalk session", func() bool {
		return readString(t, root, `SELECT state FROM messenger_source WHERE platform='kakaotalk'`) == "needs_sign_in"
	})
	waitFor(t, "another source continues", func() bool {
		return readCanon(t, root, `SELECT count(*) FROM messenger_coverage WHERE platform='other'`) > 0
	})
	if got := readCanon(t, root, `SELECT count(*) FROM messenger_record WHERE platform='kakaotalk'`); got != 4 {
		t.Fatalf("expired source records: %d", got)
	}
	client, err = clientapi.DialWithCapabilities(t.Context(), cfg.Socket, "kakao-fixture", []protocol.Capability{{Name: "kakaotalk", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	kakaoRequest(t, client, protocol.KakaotalkRequest{Operation: "disconnect", Account: new("7788")})
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("disconnect retained session: %v", err)
	}
	if got := readCanon(t, root, `SELECT count(*) FROM messenger_record WHERE platform='kakaotalk'`); got != 4 {
		t.Fatalf("disconnect lost history: %d", got)
	}
	if got := readString(t, root, `SELECT e.body FROM conv_event e JOIN messenger_record r ON r.event=e.id WHERE r.platform='kakaotalk' AND r.message='1'`); got != "first" {
		t.Fatalf("invented an unsupported edit: %q", got)
	}

}

// Core runs in a child, so the parent client is an independent eligible owner
// process. The test never bypasses the socket's ancestry/decider boundary.
func TestKakaoTalkCoreProcess(t *testing.T) {
	root := os.Getenv("LINA_TEST_KAKAO_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	ctx, cancel := signal.NotifyContext(t.Context(), os.Interrupt)
	defer cancel()
	cfg := addon.Config{Node: os.Getenv("LINA_TEST_KAKAO_NODE"), Program: os.Getenv("LINA_TEST_KAKAO_PROGRAM"), StateRoot: root, Socket: filepath.Join(root, "core.sock"), SingleAccount: []string{"kakaotalk"}, RequestTimeout: 15 * time.Second}
	err := boot.Run(ctx, boot.Config{StateRoot: root, Socket: cfg.Socket,
		Messenger: &boot.MessengerConfig{Addon: addon.NewManager(cfg), Adapters: map[string]messenger.Adapter{"other": &otherSource{}}, Interval: 25 * time.Millisecond},
		AfterStep: func(step boot.Step, _ boot.Snapshot) error {
			if step == boot.Ready {
				_, err := fmt.Fprintln(os.Stdout, "ready")
				return err
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}
