// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	gotelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/tgtest"
	"github.com/gotd/td/tgtest/cluster"
	"github.com/gotd/td/transport"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/telegram"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/protocol"
)

// TestM3TelegramSourceInstalledCore proves the Telegram source inside a running
// Core: the person signs in with a phone code and a 2FA password, the session
// lands only in the secret store, Core reads the account's chats into ledger
// records through an MTProto server, a platform edit and deletion reach the
// ledger, a FLOOD_WAIT is honoured, and no read-state or presence request is
// ever sent.
func TestM3TelegramSourceInstalledCore(t *testing.T) {

	const (
		person  = "person:telegram-host"
		account = "1001"
	)
	h := newHarness(t, "")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bundle := makeInstallBundle(t, t.TempDir(), executable, executable)
	provenance, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"canon", "system/bin", "system/core"} {
		if err := os.MkdirAll(filepath.Join(h.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.root, "system/bin/manifest.json"), provenance, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root, "system/core/main.json"), []byte(`{"main_id":"telegram-host"}`), 0600); err != nil {
		t.Fatal(err)
	}

	prior := h.binary
	h.binary = executable
	child := h.command("-test.run=^TestTelegramBootHelper$")
	h.binary = prior
	child.command.Env = append(child.command.Env, "LINA_TEST_TELEGRAM_ROOT="+h.root, "LINA_TEST_TELEGRAM_SOCKET="+h.socket)
	child.start(t)
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { h.stopCore(child) }) }
	t.Cleanup(stop)
	done := make(chan error)

	client := telegramClient(t, h)
	defer client.Close()
	exchange := func(p protocol.TelegramRequest) protocol.TelegramResult {
		t.Helper()
		id, err := client.Telegram(h.ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		for {
			e, err := client.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if e.ReplyTo == nil || *e.ReplyTo != id {
				continue
			}
			if e.Error != nil {
				t.Fatalf("Telegram settings: %s", e.Error.Code)
			}
			return e.Payload.(protocol.TelegramResult)
		}
	}
	apiID, hash, phone := "12345", "synthetic-api-hash", "+10000000001"
	if result := exchange(protocol.TelegramRequest{Operation: "start", APIID: &apiID, APIHash: &hash, Phone: &phone}); result.Stage != "code" {
		t.Fatalf("sign-in stage %s", result.Stage)
	}
	code := "12345"
	if result := exchange(protocol.TelegramRequest{Operation: "answer", Answer: &code}); result.Stage != "password" {
		t.Fatalf("sign-in stage %s", result.Stage)
	}
	password := "synthetic-2fa"
	if result := exchange(protocol.TelegramRequest{Operation: "answer", Answer: &password}); result.Stage != "connected" || len(result.Sources) != 1 || result.Sources[0].Account != account {
		t.Fatalf("signed-in source %+v", result)
	}
	url, err := os.ReadFile(filepath.Join(h.root, "telegram-fixture-url"))
	if err != nil {
		t.Fatal(err)
	}
	call := func(path string) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(h.ctx, "GET", string(url)+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("fixture request: %d %v", resp.StatusCode, err)
		}
		return body
	}
	call("/ready")

	// The account's messages arrive from the running Core's sync loop.
	waitForRows(t, h, done, 3, "SELECT count(*) FROM conv_event WHERE origin LIKE '%telegram%'")

	// The subscription barrier makes each update a single delivery.
	call("/edit")
	waitForRows(t, h, done, 1, "SELECT count(*) FROM conv_event WHERE body LIKE '%second, edited%'")
	call("/delete")
	waitForRows(t, h, done, 1, "SELECT count(*) FROM removal_applied")
	call("/missed-edit")
	waitForRows(t, h, done, 1, "SELECT count(*) FROM conv_event WHERE body='second, recovered'")
	call("/missed-delete")
	waitForRows(t, h, done, 2, "SELECT count(*) FROM removal_applied")
	// Signing in again must replace the cached reader and resume its checkpoint.
	if result := exchange(protocol.TelegramRequest{Operation: "start", APIID: &apiID, APIHash: &hash, Phone: &phone}); result.Stage != "connected" {
		t.Fatalf("reauthentication stage %s", result.Stage)
	}
	call("/ready")
	client.Close()

	var stats telegramStats
	if err := json.Unmarshal(call("/stats"), &stats); err != nil {
		t.Fatal(err)
	}
	stop()

	db, closeStore := installedAcceptanceStore(t, h)
	defer closeStore()
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, query, args...).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("SELECT count(*) FROM messenger_chat WHERE platform='telegram'"); n != 2 {
		t.Fatalf("chats = %d, want the account's two dialogs", n)
	}
	if n := count("SELECT count(*) FROM conv_event WHERE origin LIKE '%telegram%' AND speaker_kind='person'"); n != 1 {
		t.Fatalf("the account's own message records = %d", n)
	}
	if n := count("SELECT count(*) FROM conv_event WHERE origin LIKE '%telegram%' AND body='first' AND speaker_kind='external_participant'"); n != 1 {
		t.Fatal("the other person's message is not an external participant record")
	}
	if n := count("SELECT count(*) FROM messenger_record WHERE message='1' AND state='deleted'"); n != 1 {
		t.Fatalf("the platform deletion left no tombstone (%d)", n)
	}
	if n := count("SELECT count(*) FROM messenger_coverage WHERE platform='telegram'"); n < 2 {
		t.Fatalf("coverage rows = %d", n)
	}
	if !stats.Waited {
		t.Fatal("the adapter did not honour the platform's FLOOD_WAIT")
	}
	for _, id := range stats.Invoked {
		switch id {
		case tg.MessagesReadHistoryRequestTypeID, tg.ChannelsReadHistoryRequestTypeID,
			tg.AccountUpdateStatusRequestTypeID, tg.MessagesSetTypingRequestTypeID,
			tg.MessagesReadMentionsRequestTypeID, tg.MessagesReadReactionsRequestTypeID, tg.MessagesReadMessageContentsRequestTypeID, tg.ChannelsReadMessageContentsRequestTypeID:
			t.Fatalf("the source sent a read-state or presence request: %#x", id)
		}
	}

	// The session stays in the secret store, never in a plaintext file.
	stored, err := secrets.OpenFiles(h.root)
	if err != nil {
		t.Fatal(err)
	}
	defer stored.Close()
	blob, err := stored.GetBlob(secrets.Key{Person: person, Service: "telegram", Name: "session:" + account})
	if err != nil || !blob.Ready() {
		t.Fatalf("the session is not in the secret store: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(h.root, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("a secret file has mode %v", info.Mode().Perm())
		}
	}
}

// waitForRows polls canon until the query reports at least want rows.
func waitForRows(t *testing.T, h *harness, done <-chan error, want int, query string) {
	t.Helper()
	deadline := time.Now().Add(operationTimeout)
	for {
		bootFailed(t, h, done)
		db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		var n int
		err = db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, query).Scan(&n)
		})
		db.Close(context.Background())
		if err == nil && n >= want {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q reported %d rows, want %d", query, n, want)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// bootFailed reports a Core that stopped before the scenario's first record.
func bootFailed(t *testing.T, h *harness, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Core stopped before reading the source: %v", err)
		}
	default:
	}
}

// fakeTelegram is an in-process MTProto server for the scenario.
type fakeTelegram struct {
	*cluster.Cluster
	dispatch   *tgtest.Dispatcher
	mu         sync.Mutex
	session    tgtest.Session
	server     *tgtest.Server
	invoked    []uint32
	reads      int
	flooded    bool
	deletion   bool
	authorized bool
	updates    []tg.UpdateClass
}

var fakePrime = []byte{
	199, 28, 174, 185, 198, 177, 201, 4, 142, 108, 82, 47, 112, 241, 63, 115,
	152, 13, 64, 35, 142, 62, 33, 193, 73, 52, 208, 55, 86, 61, 147, 15,
	72, 25, 138, 10, 167, 193, 64, 88, 34, 148, 147, 210, 37, 48, 244, 219,
	250, 51, 111, 110, 10, 201, 37, 19, 149, 67, 174, 212, 76, 206, 124, 55,
	32, 253, 81, 246, 148, 88, 112, 90, 198, 140, 212, 254, 107, 107, 19, 171,
	220, 151, 70, 81, 41, 105, 50, 132, 84, 241, 143, 175, 140, 89, 95, 100,
	36, 119, 254, 150, 187, 42, 148, 29, 91, 205, 29, 74, 200, 204, 73, 136,
	7, 8, 250, 155, 55, 142, 60, 79, 58, 144, 96, 190, 230, 124, 249, 164,
	164, 166, 149, 129, 16, 81, 144, 126, 22, 39, 83, 181, 107, 15, 107, 65,
	13, 186, 116, 216, 168, 75, 42, 20, 179, 20, 78, 14, 241, 40, 71, 84,
	253, 23, 237, 149, 13, 89, 101, 180, 185, 221, 70, 88, 45, 177, 23, 141,
	22, 156, 107, 196, 101, 176, 214, 255, 156, 163, 146, 143, 239, 91, 154, 228,
	228, 24, 252, 21, 232, 62, 190, 160, 248, 127, 169, 255, 94, 237, 112, 5,
	13, 237, 40, 73, 244, 123, 249, 89, 217, 86, 133, 12, 233, 41, 133, 31,
	13, 129, 21, 246, 53, 177, 5, 238, 46, 78, 21, 208, 75, 36, 84, 191,
	111, 79, 173, 240, 52, 177, 4, 3, 17, 156, 216, 227, 185, 47, 204, 91,
}

var fakeSelf = &tg.User{ID: 1001, AccessHash: 11, Self: true, FirstName: "Owner"}
var fakeFriend = &tg.User{ID: 2002, AccessHash: 22, FirstName: "Friend"}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f := &fakeTelegram{Cluster: cluster.NewCluster(cluster.Options{Protocol: transport.Intermediate})}
	f.dispatch = f.Dispatch(2, "telegram")
	f.dispatch.HandleFunc(tg.AuthSendCodeRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		return server.SendResult(req, &tg.AuthSentCode{Type: &tg.AuthSentCodeTypeApp{Length: 5}, PhoneCodeHash: "code-hash"})
	})
	f.dispatch.HandleFunc(tg.AuthSignInRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		var request tg.AuthSignInRequest
		if err := request.Decode(req.Buf); err != nil {
			return err
		}
		if request.PhoneCode != "12345" {
			return server.SendErr(req, tgerr.New(400, "PHONE_CODE_INVALID"))
		}
		return server.SendErr(req, tgerr.New(401, "SESSION_PASSWORD_NEEDED"))
	})
	f.dispatch.HandleFunc(tg.AccountGetPasswordRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		algo := &tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow{
			Salt1: []byte("salt-one"), Salt2: []byte("salt-two"), G: 3, P: fakePrime,
		}
		return server.SendResult(req, &tg.AccountPassword{
			SRPID: 7, SRPB: []byte{2}, CurrentAlgo: algo, NewAlgo: algo,
			NewSecureAlgo: &tg.SecurePasswordKdfAlgoPBKDF2HMACSHA512iter100000{Salt: []byte("salt")},
			SecureRandom:  []byte("0123456789abcdef"),
		})
	})
	f.dispatch.HandleFunc(tg.AuthCheckPasswordRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		f.mu.Lock()
		f.authorized = true
		f.mu.Unlock()
		return server.SendResult(req, &tg.AuthAuthorization{User: fakeSelf})
	})
	f.dispatch.HandleFunc(tg.UsersGetUsersRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		f.mu.Lock()
		f.session, f.server = req.Session, server
		authorized := f.authorized
		f.mu.Unlock()
		if !authorized {
			return server.SendErr(req, tgerr.New(401, "AUTH_KEY_UNREGISTERED"))
		}
		return server.SendVector(req, fakeSelf)
	})
	f.dispatch.HandleFunc(tg.MessagesGetDialogsRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		return server.SendResult(req, &tg.MessagesDialogs{
			Dialogs: []tg.DialogClass{
				&tg.Dialog{Peer: &tg.PeerUser{UserID: fakeFriend.ID}, TopMessage: 2},
				&tg.Dialog{Peer: &tg.PeerChat{ChatID: 3003}, TopMessage: 9},
			},
			Users: []tg.UserClass{fakeSelf, fakeFriend},
			Chats: []tg.ChatClass{&tg.Chat{ID: 3003, Title: "Team", Photo: &tg.ChatPhotoEmpty{}}},
		})
	})
	f.dispatch.HandleFunc(tg.MessagesGetHistoryRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		var request tg.MessagesGetHistoryRequest
		if err := request.Decode(req.Buf); err != nil {
			return err
		}
		if _, friend := request.Peer.(*tg.InputPeerUser); friend {
			f.mu.Lock()
			limited := f.reads == 0
			f.reads++
			if limited {
				f.flooded = true
			}
			f.mu.Unlock()
			if limited {
				return server.SendErr(req, tgerr.New(420, "FLOOD_WAIT_1"))
			}
			return server.SendResult(req, &tg.MessagesMessages{
				Messages: []tg.MessageClass{
					&tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: fakeFriend.ID}, FromID: &tg.PeerUser{UserID: fakeFriend.ID}, Date: 200, Message: "second"},
					&tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: fakeFriend.ID}, FromID: &tg.PeerUser{UserID: fakeFriend.ID}, Date: 100, Message: "first"},
				},
				Users: []tg.UserClass{fakeSelf, fakeFriend},
			})
		}
		return server.SendResult(req, &tg.MessagesMessages{
			Messages: []tg.MessageClass{
				&tg.Message{ID: 9, PeerID: &tg.PeerChat{ChatID: 3003}, FromID: &tg.PeerUser{UserID: fakeSelf.ID}, Date: 100, Message: "team one"},
			},
			Users: []tg.UserClass{fakeSelf},
		})
	})
	f.dispatch.HandleFunc(tg.UpdatesGetStateRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		return server.SendResult(req, &tg.UpdatesState{Pts: 1, Date: 100})
	})
	f.dispatch.HandleFunc(tg.UpdatesGetDifferenceRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		var request tg.UpdatesGetDifferenceRequest
		if err := request.Decode(req.Buf); err != nil {
			return err
		}
		f.mu.Lock()
		var pending []tg.UpdateClass
		for _, update := range f.updates {
			if update.(interface{ GetPts() int }).GetPts() > request.Pts {
				pending = append(pending, update)
			}
		}
		pts := 1 + len(f.updates)
		f.mu.Unlock()
		return server.SendResult(req, &tg.UpdatesDifference{OtherUpdates: pending, Users: []tg.UserClass{fakeSelf, fakeFriend}, State: tg.UpdatesState{Pts: pts, Date: 100}})
	})
	go f.Up(ctx)
	<-f.Ready()
	return f
}

func (f *fakeTelegram) options() gotelegram.Options {
	return gotelegram.Options{
		PublicKeys:  f.Keys(),
		DCList:      f.List(),
		Resolver:    f.Resolver(),
		Middlewares: []gotelegram.Middleware{f.recorder()},
	}
}

func (f *fakeTelegram) recorder() gotelegram.Middleware {
	return recorder{f}
}

type recorder struct{ fake *fakeTelegram }

func (r recorder) Handle(next tg.Invoker) gotelegram.InvokeFunc {
	return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		if typed, ok := input.(interface{ TypeID() uint32 }); ok {
			r.fake.mu.Lock()
			r.fake.invoked = append(r.fake.invoked, typed.TypeID())
			r.fake.mu.Unlock()
		}
		return next.Invoke(ctx, input, output)
	}
}

func (f *fakeTelegram) invokedTypes() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint32(nil), f.invoked...)
}

// honouredFloodWait reports whether the platform rate limited a read. The
// record it produced proves the adapter retried afterwards.
func (f *fakeTelegram) honouredFloodWait() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flooded
}

func (f *fakeTelegram) edit(t *testing.T, id int, text string, push bool) {
	t.Helper()
	f.mu.Lock()
	session, server := f.session, f.server
	f.mu.Unlock()
	if server == nil {
		t.Fatal("the source never established a session")
	}
	message := &tg.Message{ID: id, PeerID: &tg.PeerUser{UserID: fakeFriend.ID}, FromID: &tg.PeerUser{UserID: fakeFriend.ID},
		Date: 200, Message: text, EditDate: 500}
	f.mu.Lock()
	pts := 2 + len(f.updates)
	message.EditDate = 500 + pts
	update := &tg.UpdateEditMessage{Message: message, Pts: pts, PtsCount: 1}
	f.updates = append(f.updates, update)
	f.mu.Unlock()
	if !push {
		return
	}
	if err := server.SendUpdates(context.Background(), session, update); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeTelegram) delete(t *testing.T, id int, push bool) {
	t.Helper()
	f.mu.Lock()
	session, server := f.session, f.server
	f.deletion = true
	f.mu.Unlock()
	if server == nil {
		t.Fatal("the source never established a session")
	}
	f.mu.Lock()
	update := &tg.UpdateDeleteMessages{Messages: []int{id}, Pts: 2 + len(f.updates), PtsCount: 1}
	f.updates = append(f.updates, update)
	f.mu.Unlock()
	if !push {
		return
	}
	if err := server.SendUpdates(context.Background(), session, update); err != nil {
		t.Fatal(err)
	}
}

func telegramVault(t *testing.T, root string) *secrets.Store {
	t.Helper()
	v, err := secrets.OpenFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}
func telegramClient(t *testing.T, h *harness) *clientapi.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
	defer cancel()
	for {
		c, err := clientapi.DialWithCapabilities(ctx, h.socket, "test", []protocol.Capability{{Name: "telegram_source", Version: "1.0"}})
		if err == nil {
			for {
				id, err := c.Telegram(ctx, protocol.TelegramRequest{Operation: "list"})
				if err != nil {
					t.Fatal(err)
				}
				e, err := c.Receive(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if e.ReplyTo == nil || *e.ReplyTo != id {
					t.Fatal("uncorrelated response")
				}
				if e.Kind == protocol.KindTelegramResult {
					return c
				}
				if e.Error == nil || e.Error.Code != protocol.ErrorUnavailable {
					t.Fatalf("settings readiness: %+v", e.Error)
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal(err)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type telegramStats struct {
	Waited  bool
	Invoked []uint32
}

func TestTelegramBootHelper(t *testing.T) {
	root := os.Getenv("LINA_TEST_TELEGRAM_ROOT")
	if root == "" {
		return
	}
	fixture := newFakeTelegram(t)
	live := make(chan *telegram.Adapter, 4)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ready":
			select {
			case a := <-live:
				if err := a.WaitLive(r.Context()); err != nil {
					http.Error(w, "not ready", 503)
				}
			case <-r.Context().Done():
				http.Error(w, "not ready", 503)
			}
		case "/edit":
			fixture.edit(t, 2, "second, edited", true)
		case "/delete":
			fixture.delete(t, 1, true)
		case "/missed-edit":
			fixture.edit(t, 2, "second, recovered", false)
		case "/missed-delete":
			fixture.delete(t, 2, false)
		case "/stats":
			json.NewEncoder(w).Encode(telegramStats{fixture.honouredFloodWait(), fixture.invokedTypes()})
		default:
			http.NotFound(w, r)
		}
	}))
	defer httpServer.Close()
	if err := os.WriteFile(filepath.Join(root, "telegram-fixture-url"), []byte(httpServer.URL), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(t.Context(), syscall.SIGTERM)
	defer cancel()
	err := boot.Run(ctx, boot.Config{StateRoot: root, Socket: os.Getenv("LINA_TEST_TELEGRAM_SOCKET"), Messenger: &boot.MessengerConfig{Interval: 25 * time.Millisecond, Factory: func(source messenger.Source) (messenger.Adapter, bool, error) {
		if source.Platform != telegram.Platform {
			return nil, false, nil
		}
		a, err := telegram.New(telegram.Config{Person: source.Person, Account: source.Account, Vault: telegramVault(t, root), Options: fixture.options()})
		if err == nil && !strings.HasPrefix(source.Account, "pending:") {
			live <- a
			return telegramPollingAdapter{a}, true, nil
		}
		return a, err == nil, err
	}}})
	if err != nil {
		t.Fatal(err)
	}
}

// Use the production sync path with a short fixture interval, without waiting
// a minute to prove that an update absent from the live stream is recovered.
type telegramPollingAdapter struct{ *telegram.Adapter }

func (a telegramPollingAdapter) Capabilities() messenger.Capabilities {
	caps := a.Adapter.Capabilities()
	caps.Interval = 25 * time.Millisecond
	return caps
}
