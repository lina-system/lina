// SPDX-License-Identifier: Apache-2.0
package telegram_test

import (
	"context"
	"os"
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

	"github.com/thisisjun786/lina/internal/core/messenger/telegram"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

const (
	personPhone = "+10000000001"
	phoneCode   = "12345"
	password    = "synthetic-2fa"
)

// self is the connected account.
var self = &tg.User{ID: 1001, AccessHash: 11, Self: true, FirstName: "Owner"}

// other is the person the account talks with.
var other = &tg.User{ID: 2002, AccessHash: 22, FirstName: "Friend"}

// server is an in-process Telegram server. It records every request type it was
// asked for so a test can prove which methods the adapter never sends.
type server struct {
	*cluster.Cluster
	dispatcher *tgtest.Dispatcher

	mu        sync.Mutex
	sent      []uint32
	invoked   []uint32
	gets      []historyCall
	session   tgtest.Session
	tgServer  *tgtest.Server
	authorize bool
}

type historyCall struct {
	peer   string
	offset int
	limit  int
}

var passAlgo = []byte{
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

func newServer(t *testing.T) *server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &server{Cluster: cluster.NewCluster(cluster.Options{Protocol: transport.Intermediate})}
	s.dispatcher = s.Dispatch(2, "telegram")
	s.dispatcher.HandleFunc(tg.AuthSendCodeRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		return server.SendResult(req, &tg.AuthSentCode{
			Type:          &tg.AuthSentCodeTypeApp{Length: 5},
			PhoneCodeHash: "code-hash",
			NextType:      &tg.AuthCodeTypeSMS{},
			Timeout:       60,
		})
	})
	s.dispatcher.HandleFunc(tg.AuthSignInRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		var request tg.AuthSignInRequest
		if err := request.Decode(req.Buf); err != nil {
			return err
		}
		if request.PhoneNumber != personPhone || request.PhoneCode != phoneCode || request.PhoneCodeHash != "code-hash" {
			return server.SendErr(req, tgerr.New(400, "PHONE_CODE_INVALID"))
		}
		return server.SendErr(req, tgerr.New(401, "SESSION_PASSWORD_NEEDED"))
	})
	s.dispatcher.HandleFunc(tg.AccountGetPasswordRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		algo := &tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow{
			Salt1: []byte("salt-one"),
			Salt2: []byte("salt-two"),
			G:     3,
			P:     passAlgo,
		}
		return server.SendResult(req, &tg.AccountPassword{
			SRPID: 7, SRPB: []byte{2}, CurrentAlgo: algo, NewAlgo: algo,
			NewSecureAlgo: &tg.SecurePasswordKdfAlgoPBKDF2HMACSHA512iter100000{Salt: []byte("salt")},
			SecureRandom:  []byte("0123456789abcdef"),
		})
	})
	s.dispatcher.HandleFunc(tg.AuthCheckPasswordRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		s.mu.Lock()
		s.authorize = true
		s.mu.Unlock()
		return server.SendResult(req, &tg.AuthAuthorization{User: self})
	})
	s.dispatcher.HandleFunc(tg.UsersGetUsersRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		s.mu.Lock()
		s.session, s.tgServer = req.Session, server
		authorized := s.authorize
		s.mu.Unlock()
		if !authorized {
			return server.SendErr(req, tgerr.New(401, "AUTH_KEY_UNREGISTERED"))
		}
		return server.SendVector(req, self)
	})
	s.dispatcher.HandleFunc(tg.MessagesGetDialogsRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		chat := &tg.Chat{ID: 3003, Title: "Team", ParticipantsCount: 2, Photo: &tg.ChatPhotoEmpty{}}
		return server.SendResult(req, &tg.MessagesDialogs{
			Dialogs: []tg.DialogClass{
				&tg.Dialog{Peer: &tg.PeerUser{UserID: other.ID}, TopMessage: 4},
				&tg.Dialog{Peer: &tg.PeerChat{ChatID: chat.ID}, TopMessage: 2},
			},
			Users: []tg.UserClass{self, other},
			Chats: []tg.ChatClass{chat},
		})
	})
	s.dispatcher.HandleFunc(tg.MessagesGetHistoryRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		var request tg.MessagesGetHistoryRequest
		if err := request.Decode(req.Buf); err != nil {
			return err
		}
		peer := "other"
		if _, ok := request.Peer.(*tg.InputPeerChat); ok {
			peer = "team"
		}
		s.mu.Lock()
		s.gets = append(s.gets, historyCall{peer: peer, offset: request.OffsetID, limit: request.Limit})
		first := len(s.gets) == 1
		s.mu.Unlock()
		if first {
			// The first read is rate limited; the adapter must wait and repeat it.
			return server.SendErr(req, tgerr.New(420, "FLOOD_WAIT_1"))
		}
		var messages []tg.MessageClass
		if peer == "other" {
			messages = []tg.MessageClass{
				&tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: other.ID}, FromID: &tg.PeerUser{UserID: other.ID}, Date: 500, Message: "with a file",
					Media: &tg.MessageMediaDocument{Document: document()}},
				&tg.Message{ID: 4, PeerID: &tg.PeerUser{UserID: other.ID}, FromID: &tg.PeerUser{UserID: other.ID}, Date: 400, Message: "fourth"},
				&tg.Message{ID: 3, PeerID: &tg.PeerUser{UserID: other.ID}, FromID: &tg.PeerUser{UserID: self.ID}, Date: 300, Message: "third"},
				&tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: other.ID}, FromID: &tg.PeerUser{UserID: other.ID}, Date: 200, Message: "second",
					ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 1}},
				&tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: other.ID}, FromID: &tg.PeerUser{UserID: other.ID}, Date: 100, Message: "first"},
			}
		} else {
			messages = []tg.MessageClass{
				&tg.Message{ID: 2, PeerID: &tg.PeerChat{ChatID: 3003}, FromID: &tg.PeerUser{UserID: other.ID}, Date: 200, Message: "team two"},
				&tg.Message{ID: 1, PeerID: &tg.PeerChat{ChatID: 3003}, FromID: &tg.PeerUser{UserID: other.ID}, Date: 100, Message: "team one"},
			}
		}
		// The platform returns newest first, below the requested offset.
		selected := make([]tg.MessageClass, 0, len(messages))
		for _, item := range messages {
			message, ok := item.(*tg.Message)
			if !ok || request.OffsetID > 0 && message.ID >= request.OffsetID {
				continue
			}
			selected = append(selected, item)
		}
		if request.Limit > 0 && len(selected) > request.Limit {
			selected = selected[:request.Limit]
		}
		if len(selected) == 0 && request.OffsetID == 0 {
			selected = messages[:0]
		}
		return server.SendResult(req, &tg.MessagesMessages{Messages: selected, Users: []tg.UserClass{self, other}})
	})
	s.dispatcher.HandleFunc(tg.MessagesGetFullChatRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		return server.SendResult(req, &tg.MessagesChatFull{
			FullChat: &tg.ChatFull{ID: 3003, About: "Team chat", NotifySettings: tg.PeerNotifySettings{}, Participants: &tg.ChatParticipants{
				Participants: []tg.ChatParticipantClass{
					&tg.ChatParticipant{UserID: self.ID, InviterID: self.ID, Date: 100},
					&tg.ChatParticipant{UserID: other.ID, InviterID: self.ID, Date: 100},
				},
				Version: 1,
			}},
			Users: []tg.UserClass{self, other},
		})
	})
	s.dispatcher.HandleFunc(tg.UploadGetFileRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		var request tg.UploadGetFileRequest
		if err := request.Decode(req.Buf); err != nil {
			return err
		}
		if request.Offset != 0 {
			return server.SendResult(req, &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Mtime: 1, Bytes: nil})
		}
		return server.SendResult(req, &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Mtime: 1, Bytes: []byte("file")})
	})
	s.dispatcher.HandleFunc(tg.UpdatesGetStateRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		return server.SendResult(req, &tg.UpdatesState{Pts: 1, Date: int(time.Now().Unix())})
	})
	go s.Up(ctx)
	<-s.Ready()
	return s
}

// document is the file a message carries. Its bytes stay on the platform until
// the person asks for them.
func document() *tg.Document {
	return &tg.Document{
		ID: 99, AccessHash: 5, FileReference: []byte("ref"), Date: 500,
		MimeType: "text/plain", Size: 4, DCID: 2,
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: "notes.txt"},
			&tg.DocumentAttributeVideo{Duration: 1, W: 2, H: 2},
		},
	}
}

// middleware records every request the client invokes, handled or not.
type middleware struct{ server *server }

func (m middleware) Handle(next tg.Invoker) gotelegram.InvokeFunc {
	return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		if id, ok := input.(interface{ TypeID() uint32 }); ok {
			m.server.mu.Lock()
			m.server.invoked = append(m.server.invoked, id.TypeID())
			m.server.mu.Unlock()
		}
		return next.Invoke(ctx, input, output)
	}
}

// invokedRequests lists every request type the client sent.
func (s *server) invokedRequests() []uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint32(nil), s.invoked...)
}

func newVault(t *testing.T) *secrets.Store {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := secrets.OpenFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func newAdapter(t *testing.T, s *server, vault *secrets.Store, waits *[]time.Duration) *telegram.Adapter {
	t.Helper()
	return newSizedAdapter(t, s, vault, waits, 0)
}

func newSizedAdapter(t *testing.T, s *server, vault *secrets.Store, waits *[]time.Duration, size int) *telegram.Adapter {
	t.Helper()
	if err := telegram.PutCredentials(vault, "person:owner", 12345, "synthetic-hash"); err != nil {
		t.Fatal(err)
	}
	adapter, err := telegram.New(telegram.Config{
		Person: "person:owner", Account: "1001", Vault: vault,
		Options: gotelegram.Options{
			PublicKeys: s.Keys(), DCList: s.List(), Resolver: s.Resolver(),
			Middlewares: []gotelegram.Middleware{middleware{server: s}},
			OnDead:      func(err error) { t.Logf("connection dead: %v", err) },
		},
		Wait: func(ctx context.Context, d time.Duration) error {
			if waits != nil {
				*waits = append(*waits, d)
			}
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
		PageSize: size,
		Now:      func() time.Time { return time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { adapter.Close() })
	return adapter
}
