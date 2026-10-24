// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	gotelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/tgtest"
	"github.com/gotd/td/tgtest/cluster"
	"github.com/gotd/td/transport"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/telegram"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// TestM3TelegramSourceInstalledCore proves the Telegram source inside a running
// Core: the person signs in with a phone code and a 2FA password, the session
// lands only in the secret store, Core reads the account's chats into ledger
// records through an MTProto server, a platform edit and deletion reach the
// ledger, a FLOOD_WAIT is honoured, and no read-state or presence request is
// ever sent.
func TestM3TelegramSourceInstalledCore(t *testing.T) {

	server := newFakeTelegram(t)
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

	// The person signs in before Core starts, as the app or the TUI does.
	vault, err := secrets.OpenFiles(h.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := telegram.PutCredentials(vault, person, 12345, "synthetic-api-hash"); err != nil {
		t.Fatal(err)
	}
	signedIn, err := telegram.New(telegram.Config{Person: person, Account: account, Vault: vault, Options: server.options()})
	if err != nil {
		t.Fatal(err)
	}
	if err := signedIn.Authenticate(h.ctx, telegram.PromptFuncs{
		PhoneFunc:    func(context.Context) (string, error) { return "+10000000001", nil },
		CodeFunc:     func(context.Context, telegram.Code) (string, error) { return "12345", nil },
		PasswordFunc: func(context.Context) (string, error) { return "synthetic-2fa", nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := signedIn.Close(); err != nil {
		t.Fatal(err)
	}

	// Connecting a source is the person's act; Core never connects one itself.
	source := messenger.Source{Person: person, Platform: telegram.Platform, Account: account,
		Mode: messenger.ModeOfficial, Adapter: "telegram", State: messenger.StateConnected, ConnectedAt: time.Now()}
	db, closeStore := installedAcceptanceStore(t, h)
	if err := messenger.Open(db, nil, nil).Connect(h.ctx, source); err != nil {
		t.Fatal(err)
	}
	closeStore()

	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- boot.Run(ctx, boot.Config{StateRoot: h.root, Socket: h.socket, Messenger: &boot.MessengerConfig{
			Interval: 25 * time.Millisecond,
			Factory: func(source messenger.Source) (messenger.Adapter, bool, error) {
				if source.Platform != telegram.Platform {
					return nil, false, nil
				}
				adapter, err := telegram.New(telegram.Config{
					Person: source.Person, Account: source.Account, Vault: server.vault(t, h.root), Options: server.options(),
				})
				return adapter, err == nil, err
			},
		}})
	}()

	// The account's messages arrive from the running Core's sync loop.
	waitForRows(t, h, done, 3, "SELECT count(*) FROM conv_event WHERE origin LIKE '%telegram%'")

	// A platform edit and deletion reach the ledger through the live path. The
	// platform may deliver an edit before the source's subscription starts, so
	// the scenario repeats the push until the record appears; admitting the
	// same revision twice adds nothing.
	server.editUntil(t, h, done, 2, "second, edited", "SELECT count(*) FROM conv_event WHERE body LIKE '%second, edited%'")
	server.deleteUntil(t, h, done, 1, "SELECT count(*) FROM removal_applied")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("telegram Core stopped with %v", err)
		}
	case <-time.After(operationTimeout):
		t.Fatal("telegram Core shutdown timed out")
	}

	db, closeStore = installedAcceptanceStore(t, h)
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
	if !server.honouredFloodWait() {
		t.Fatal("the adapter did not honour the platform's FLOOD_WAIT")
	}
	for _, id := range server.invokedTypes() {
		switch id {
		case tg.MessagesReadHistoryRequestTypeID, tg.ChannelsReadHistoryRequestTypeID,
			tg.AccountUpdateStatusRequestTypeID, tg.MessagesSetTypingRequestTypeID,
			tg.MessagesReadMentionsRequestTypeID, tg.MessagesReadReactionsRequestTypeID:
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
	dispatch *tgtest.Dispatcher
	mu       sync.Mutex
	session  tgtest.Session
	server   *tgtest.Server
	invoked  []uint32
	reads    int
	flooded  bool
	deletion bool
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
		return server.SendResult(req, &tg.AuthAuthorization{User: fakeSelf})
	})
	f.dispatch.HandleFunc(tg.UsersGetUsersRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		f.mu.Lock()
		f.session, f.server = req.Session, server
		f.mu.Unlock()
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
		return server.SendResult(req, &tg.UpdatesState{Pts: 1, Date: int(time.Now().Unix())})
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

func (f *fakeTelegram) vault(t *testing.T, root string) *secrets.Store {
	t.Helper()
	store, err := secrets.OpenFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	return store
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

func (f *fakeTelegram) editUntil(t *testing.T, h *harness, done <-chan error, id int, text, query string) {
	t.Helper()
	deadline := time.Now().Add(operationTimeout)
	for {
		f.edit(t, id, text)
		if rowsEnabled(h, query) {
			return
		}
		bootFailed(t, h, done)
		if time.Now().After(deadline) {
			t.Fatalf("the platform edit never reached the ledger: %q", query)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *fakeTelegram) deleteUntil(t *testing.T, h *harness, done <-chan error, id int, query string) {
	t.Helper()
	deadline := time.Now().Add(operationTimeout)
	for {
		f.delete(t, id)
		if rowsEnabled(h, query) {
			return
		}
		bootFailed(t, h, done)
		if time.Now().After(deadline) {
			t.Fatalf("the platform deletion never reached the ledger: %q", query)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// rowsEnabled reports whether the lock-free inspection path sees a row.
func rowsEnabled(h *harness, query string) bool {
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		return false
	}
	defer db.Close(context.Background())
	var n int
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, query).Scan(&n)
	}); err != nil {
		return false
	}
	return n >= 1
}

func (f *fakeTelegram) edit(t *testing.T, id int, text string) {
	t.Helper()
	f.mu.Lock()
	session, server := f.session, f.server
	f.mu.Unlock()
	if server == nil {
		t.Fatal("the source never established a session")
	}
	message := &tg.Message{ID: id, PeerID: &tg.PeerUser{UserID: fakeFriend.ID}, FromID: &tg.PeerUser{UserID: fakeFriend.ID},
		Date: 200, Message: text, EditDate: 500}
	if err := server.SendUpdates(context.Background(), session, &tg.UpdateEditMessage{Message: message, Pts: 2, PtsCount: 1}); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeTelegram) delete(t *testing.T, id int) {
	t.Helper()
	f.mu.Lock()
	session, server := f.session, f.server
	f.deletion = true
	f.mu.Unlock()
	if server == nil {
		t.Fatal("the source never established a session")
	}
	if err := server.SendUpdates(context.Background(), session, &tg.UpdateDeleteMessages{Messages: []int{id}, Pts: 3, PtsCount: 1}); err != nil {
		t.Fatal(err)
	}
}
