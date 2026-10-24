// SPDX-License-Identifier: Apache-2.0
package messenger_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

func TestCompareMurmur(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	const large = "9223372036854775807"
	_, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Messages: []messenger.Message{
		message(large, messenger.RoutePerson, f.now, "private body"),
		message("lina-only", messenger.RouteParticipant, f.now.Add(time.Minute), "private body"),
		message("until", messenger.RouteParticipant, f.now.Add(time.Hour), "private body"),
		message("before", messenger.RouteParticipant, f.now.Add(-time.Nanosecond), "private body"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	request := messenger.ComparisonRequest{Person: source.Person, Platform: source.Platform, Account: source.Account, Since: f.now, Until: f.now.Add(time.Hour)}
	input := []messenger.ComparisonRecord{
		{Chat: "dm-1", Message: large, At: f.now},
		{Chat: "dm-1", Message: large, At: f.now},
		{Chat: "dm-1", Message: "missing", At: f.now.Add(time.Minute)},
		{Chat: "other-chat", Message: large, At: f.now},
		{Chat: "dm-1", Message: "outside", At: request.Until},
	}
	got, err := f.store.Compare(t.Context(), request, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Chats) != 2 || got.MurmurCount != 3 || got.LinaCount != 2 {
		t.Fatalf("counts: %+v", got)
	}
	chat := got.Chats[0]
	if chat.Chat != "dm-1" || !reflect.DeepEqual(chat.MissingInLina, []string{"missing"}) || !reflect.DeepEqual(chat.MissingInMurmur, []string{"lina-only"}) {
		t.Fatalf("diff: %+v", chat)
	}
	if !reflect.DeepEqual(got.Chats[1].MissingInLina, []string{large}) {
		t.Fatalf("chat isolation: %+v", got.Chats[1])
	}
	encoded, _ := json.Marshal(got)
	for _, private := range []string{"private body", "someone", "Direct"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("comparison leaked content")
		}
	}
	identical := []messenger.ComparisonRecord{{Chat: "dm-1", Message: large, At: f.now}, {Chat: "dm-1", Message: "lina-only", At: f.now}}
	got, err = f.store.Compare(t.Context(), request, identical)
	if err != nil || len(got.Chats) != 1 || len(got.Chats[0].MissingInLina)+len(got.Chats[0].MissingInMurmur) != 0 {
		t.Fatalf("identical: %+v, %v", got, err)
	}
	request.Person = "other-person"
	if _, err = f.store.Compare(t.Context(), request, input); !errors.Is(err, messenger.ErrNotFound) {
		t.Fatalf("person isolation: %v", err)
	}
	request.Person, request.Account = source.Person, "other-account"
	if _, err = f.store.Compare(t.Context(), request, input); !errors.Is(err, messenger.ErrNotFound) {
		t.Fatalf("account isolation: %v", err)
	}
	request.Account, request.Until = source.Account, request.Since
	if _, err = f.store.Compare(t.Context(), request, input); !errors.Is(err, messenger.ErrInvalid) {
		t.Fatalf("window: %v", err)
	}
}

func TestCompareRetainsDeletedKeysAndLinaOnlyChats(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	page := messenger.Page{Messages: []messenger.Message{message("deleted", messenger.RoutePerson, f.now, "gone")}}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", page); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Deletions: []messenger.Deletion{{Message: "deleted", At: f.now.Add(time.Minute)}}}); err != nil {
		t.Fatal(err)
	}
	q := messenger.ComparisonRequest{Person: source.Person, Platform: source.Platform, Account: source.Account, Since: f.now, Until: f.now.Add(time.Hour)}
	got, err := f.store.Compare(t.Context(), q, nil)
	if err != nil || len(got.Chats) != 1 || !reflect.DeepEqual(got.Chats[0].MissingInMurmur, []string{"deleted"}) {
		t.Fatalf("deleted key: %+v, %v", got, err)
	}
}
