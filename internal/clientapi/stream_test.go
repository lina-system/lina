// SPDX-License-Identifier: Apache-2.0

package clientapi_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/protocol"
)

func streamServer(t *testing.T) (string, *clientsrv.Topics) {
	t.Helper()
	root, err := os.MkdirTemp("", "client-stream-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	socket := filepath.Join(root, "client.sock")
	listener, err := clientsrv.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	hub, err := clientsrv.NewTopics("42", clientsrv.TopicOptions{RingSize: 2, SnapshotSize: 2, QueueSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	cap := protocol.Capability{Name: "conversation", Version: "1.0"}
	rows := clientsrv.LocalCombinations()
	for i := range rows {
		rows[i].Capabilities = []protocol.Capability{cap}
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- clientsrv.Serve(ctx, listener, clientsrv.Identity{MainID: "main-stream", Epoch: "42", Supported: rows, Topics: hub, TopicCapabilities: map[string]protocol.Capability{"conversation:": cap, "core": cap}, Recovering: func() bool { return true }})
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
		hub.Close()
	})
	return socket, hub
}

func TestClientStreamsAgainstListener(t *testing.T) {
	socket, hub := streamServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	topic, other := "conversation:example", "core"
	if err := hub.PublishBatch(topic, []clientsrv.TopicEvent{{Kind: protocol.KindItemDone, Key: "one", Data: []byte(`{"text":"first"}`)}, {Kind: protocol.KindItemDone, Key: "two", Data: []byte(`{"text":"second"}`)}, {Kind: protocol.KindItemDone, Key: "three", Data: []byte(`{"text":"third"}`)}}); err != nil {
		t.Fatal(err)
	}
	client, err := clientapi.DialWithCapabilities(ctx, socket, "test", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if len(client.Welcome.Capabilities) != 1 {
		t.Fatal("capability not negotiated")
	}
	id, err := client.Subscribe(ctx, protocol.Subscribe{Topic: &topic, After: &protocol.StreamCursor{Epoch: "42", Seq: "0"}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.Receive(ctx)
	if err != nil || first.Kind != protocol.KindResync || first.ReplyTo == nil || *first.ReplyTo != id {
		t.Fatalf("resync: %+v %v", first, err)
	}
	next, err := client.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := next.Payload.(protocol.Snapshot)
	if !ok || len(snapshot.Items) != 2 || snapshot.Cursor.Seq != "3" {
		t.Fatalf("snapshot: %+v", next)
	}
	if _, err := client.Subscribe(ctx, protocol.Subscribe{Topic: &other}); err != nil {
		t.Fatal(err)
	}
	if next, err := client.Receive(ctx); err != nil || next.Kind != protocol.KindSnapshot {
		t.Fatalf("other snapshot: %s %v", next.Kind, err)
	}
	batch := []clientsrv.TopicEvent{{Kind: protocol.KindItemDone, Key: "four", Data: []byte(`{"text":"fourth"}`)}, {Kind: protocol.KindItemDone, Key: "five", Data: []byte(`{"text":"fifth"}`)}, {Kind: protocol.KindItemDone, Key: "six", Data: []byte(`{"text":"sixth"}`)}}
	if err := hub.PublishBatch(topic, batch); err != nil {
		t.Fatal(err)
	}
	if err := hub.Publish(other, clientsrv.TopicEvent{Kind: protocol.KindConnected, Key: "state", Data: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	seen := map[protocol.Kind]bool{}
	for range 2 {
		event, err := client.Receive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		seen[event.Kind] = true
	}
	if !seen[protocol.KindResyncRequired] || !seen[protocol.KindConnected] {
		t.Fatalf("overflow affected other subscription: %v", seen)
	}
	if _, err := client.Subscribe(ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	if next, err := client.Receive(ctx); err != nil || next.Kind != protocol.KindSnapshot {
		t.Fatalf("recover subscription: %s %v", next.Kind, err)
	}
	active := false
	if _, err := client.Subscribe(ctx, protocol.Subscribe{Topic: &topic, Active: &active}); err != nil {
		t.Fatal(err)
	}
	if next, err := client.Receive(ctx); err != nil || next.Kind != protocol.KindSubscribe {
		t.Fatalf("unsubscribe: %s %v", next.Kind, err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := client.Receive(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
